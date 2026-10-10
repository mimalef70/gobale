package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
)

type adminAuthClient struct {
	*fakeClient
	starts      atomic.Int32
	submissions atomic.Int32
	response    domains.Challenge
	startErr    error
	codeErr     error
	password    string
}

func (f *adminAuthClient) StartAuth(context.Context, string) (domains.Challenge, error) {
	n := f.starts.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		f.status.Auth = "auth_required"
		return domains.Challenge{}, f.startErr
	}
	f.status.Auth = "awaiting_code"
	ch := f.response
	ch.ID = fmt.Sprintf("local-challenge-%d", n)
	ch.State = "awaiting_code"
	return ch, nil
}
func (f *adminAuthClient) SubmitCode(context.Context, string, string) (*domains.Session, error) {
	f.submissions.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.codeErr != nil {
		return nil, f.codeErr
	}
	f.status.Auth = "awaiting_password"
	return nil, nil
}
func (f *adminAuthClient) SubmitPassword(_ context.Context, _ string, password string) (*domains.Session, error) {
	f.submissions.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.password = password
	f.status.Auth = "authenticated"
	return &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: "1001", Token: "synthetic-session"}, nil
}

func TestOverviewFiftyAccountsDoesNotConstructOrCallProvider(t *testing.T) {
	var factories atomic.Int32
	f := &fakeClient{status: readyStatus()}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { factories.Add(1); return f })
	ctx := context.Background()
	for n := range 50 {
		mustDevice(t, s, fmt.Sprintf("account-%02d", n))
	}
	for range 2 {
		overview, err := s.DevicesOverview(ctx)
		if err != nil || len(overview.Devices) != 50 {
			t.Fatalf("overview: %+v %v", overview, err)
		}
		for _, d := range overview.Devices {
			if d.InstanceID == "" || d.InstanceID != d.InstanceToken() || d.InstanceID == d.ConnectionID {
				t.Fatal("missing immutable opaque identity")
			}
			if d.Status.Auth != "auth_required" || d.Status.Recovery != "degraded" || d.Deliveries != (domains.DeliveryCounts{}) {
				t.Fatalf("unexpected status: %+v", d)
			}
		}
	}
	if factories.Load() != 0 || f.callCalls != 0 {
		t.Fatal("overview invoked provider factory or RPC")
	}
	d, err := s.GetDevice(ctx, "account-00")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := s.entry(d)
	if err != nil {
		t.Fatal(err)
	}
	entry.mu.Lock() // local snapshot cannot wait for a blocked provider operation
	done := make(chan error, 1)
	go func() { _, err := s.DevicesOverview(ctx); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("overview blocked on provider operation")
	}
	entry.mu.Unlock()
	if f.callCalls != 0 {
		t.Fatal("overview called RPC")
	}
	_, err = st.AppendEvent(ctx, d.ConnectionID, domains.Event{ID: "synthetic", Type: "message", Payload: json.RawMessage(`{"text":"not in overview"}`)}, []storage.WebhookTarget{{URL: "https://example.test/hook", Secret: "not in overview"}})
	if err != nil {
		t.Fatal(err)
	}
	overview, err := s.DevicesOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Devices[0].Deliveries.Pending != 1 {
		t.Fatalf("pending count: %+v", overview.Devices[0])
	}
	raw, _ := json.Marshal(overview)
	if strings.Contains(string(raw), "not in overview") {
		t.Fatal("overview leaked payload or secret")
	}
}

func TestLoginMetadataRefreshExpiryAndServerCooldown(t *testing.T) {
	wait := int64(60)
	f := &adminAuthClient{fakeClient: &fakeClient{}, response: domains.Challenge{ExpiresAt: time.Now().Add(120 * time.Second), ResendAfterSeconds: &wait, SentCodeType: 1, NextSendCodeType: 2, AvailableSendCodeTypes: []int32{1, 2}}}
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	ctx := context.Background()
	d := mustDevice(t, s, "auth")
	ch, err := s.StartAuth(ctx, d.ID, "+15550000123")
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.LoginState(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "awaiting_code" || state.Challenge == nil || state.Challenge.ID != ch.ID || state.Challenge.MaskedPhone != "••••••••23" {
		t.Fatalf("state: %+v", state)
	}
	if !state.Challenge.ExpiresAt.After(state.Challenge.ResendAvailableAt) {
		t.Fatal("expiry conflated with resend cooldown")
	}
	state.Challenge.AvailableSendCodeTypes[0] = 99
	again, _ := s.LoginState(ctx, d.ID)
	if again.Challenge.AvailableSendCodeTypes[0] != 1 {
		t.Fatal("metadata was not copied")
	}
	_, err = s.StartAuth(ctx, d.ID, "+15550000456")
	codeIs(t, err, "AUTH_RESEND_TOO_SOON")
	var de *domains.Error
	if !errors.As(err, &de) || de.RetryAfterSeconds < 1 || de.HTTP != 429 {
		t.Fatalf("cooldown error: %v", err)
	}
	if f.starts.Load() != 1 {
		t.Fatal("cooldown reached provider")
	}
	e, _ := s.entry(d)
	e.authMetaMu.Lock()
	e.challenge.ExpiresAt = time.Now().Add(-time.Second)
	e.authMetaMu.Unlock()
	state, err = s.LoginState(ctx, d.ID)
	if err != nil || state.State != "auth_required" || state.Challenge != nil {
		t.Fatalf("expired state: %+v %v", state, err)
	}
	_, err = s.SubmitCode(ctx, d.ID, ch.ID, "12345")
	codeIs(t, err, "CHALLENGE_EXPIRED")
	if f.submissions.Load() != 0 {
		t.Fatal("expired code reached provider")
	}
	_, err = s.StartAuth(ctx, d.ID, "+15550000456")
	codeIs(t, err, "AUTH_RESEND_TOO_SOON")
}

func TestConcurrentLoginTabsStartOnlyOneProviderChallenge(t *testing.T) {
	wait := int64(60)
	f := &adminAuthClient{fakeClient: &fakeClient{}, response: domains.Challenge{ResendAfterSeconds: &wait}}
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "tabs")
	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.StartAuth(context.Background(), d.ID, "+15550000123")
			if err == nil {
				successes.Add(1)
			} else {
				var de *domains.Error
				if !errors.As(err, &de) || de.Code != "AUTH_RESEND_TOO_SOON" {
					t.Errorf("concurrent login: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || f.starts.Load() != 1 {
		t.Fatalf("successes=%d starts=%d", successes.Load(), f.starts.Load())
	}
}

func TestFailedChallengeReplacementInvalidatesOldCode(t *testing.T) {
	f := &adminAuthClient{fakeClient: &fakeClient{}}
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "replacement")
	ctx := context.Background()
	ch, err := s.StartAuth(ctx, d.ID, "+15550000123")
	if err != nil {
		t.Fatal(err)
	}
	f.startErr = errors.New("private diagnostic")
	_, err = s.StartAuth(ctx, d.ID, "+15550000123")
	if err == nil {
		t.Fatal("expected replacement failure")
	}
	state, err := s.LoginState(ctx, d.ID)
	if err != nil || state.Challenge != nil || state.State != "auth_required" {
		t.Fatalf("old challenge resurrected: %+v %v", state, err)
	}
	_, err = s.SubmitCode(ctx, d.ID, ch.ID, "12345")
	codeIs(t, err, "CHALLENGE_EXPIRED")
	if f.submissions.Load() != 0 {
		t.Fatal("invalidated challenge reached provider")
	}
}

func TestLoginStatePasswordCompletionAndLifecycleCleanup(t *testing.T) {
	f := &adminAuthClient{fakeClient: &fakeClient{}}
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	d := mustDevice(t, s, "lifecycle")
	ctx := context.Background()
	ch, err := s.StartAuth(ctx, d.ID, "+15550000123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitCode(ctx, d.ID, ch.ID, "12345"); err != nil {
		t.Fatal(err)
	}
	state, _ := s.LoginState(ctx, d.ID)
	if state.State != "awaiting_password" || state.Challenge == nil {
		t.Fatalf("2FA state: %+v", state)
	}
	password := "  synthetic password\u2003 "
	if _, err = s.SubmitPassword(ctx, d.ID, ch.ID, password); err != nil {
		t.Fatal(err)
	}
	state, _ = s.LoginState(ctx, d.ID)
	if state.State != "authenticated" || state.Challenge != nil || f.password != password {
		t.Fatalf("successful state/password: %+v", state)
	}
	if err = s.Logout(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	e, _ := s.entry(d)
	if e.publicChallenge(time.Now()) != nil {
		t.Fatal("logout kept challenge")
	}
	if _, err = s.StartAuth(ctx, d.ID, "+15550000123"); err != nil {
		t.Fatal(err)
	}
	e.setClient(&fakeClient{})
	if e.publicChallenge(time.Now()) != nil {
		t.Fatal("client replacement kept challenge")
	}
	// A new usecase (as after restart) has no in-memory challenge even though the
	// persisted device and immutable account binding are retained.
	fresh := New(s.store, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	state, err = fresh.LoginState(ctx, d.ID)
	if err != nil || state.State != "auth_required" || state.Challenge != nil {
		t.Fatalf("restart revived challenge: %+v %v", state, err)
	}
	if err = s.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.LoginState(ctx, d.ID)
	codeIs(t, err, "NOT_FOUND")
	replacement := mustDevice(t, s, d.ID)
	if replacement.InstanceID == d.InstanceID {
		t.Fatal("reused alias inherited instance token")
	}
}

func TestWebhookDetailsRoutingAndSecretRedaction(t *testing.T) {
	ctx := context.Background()
	for _, merge := range []bool{false, true} {
		t.Run(fmt.Sprint(merge), func(t *testing.T) {
			options := Options{MergeGlobal: merge, GlobalWebhooks: []storage.WebhookTarget{{URL: "https://global.test/hook", Secret: "never-expose-global"}}, GlobalWebhookEvents: []string{"message"}}
			s, _ := testService(t, options, func(domains.Device) domains.Client { return &fakeClient{} })
			d := mustDevice(t, s, "webhook")
			got, err := s.WebhookDetails(ctx, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.RoutingMode != "global" || len(got.RoutingRules) != 1 || !got.RoutingRules[0].SecretConfigured {
				t.Fatalf("global details: %+v", got)
			}
			url, secret := "https://device.test/hook", "never-expose-device"
			if _, err = s.PatchWebhook(ctx, d.ID, domains.WebhookPatch{URL: &url, Secret: &secret}); err != nil {
				t.Fatal(err)
			}
			got, err = s.WebhookDetails(ctx, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantMode, wantRules := "device", 1
			if merge {
				wantMode, wantRules = "merged", 2
			}
			if got.RoutingMode != wantMode || len(got.RoutingRules) != wantRules || !got.SecretConfigured {
				t.Fatalf("device details: %+v", got)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), "never-expose") {
				t.Fatal("secret was serialized")
			}
			empty := ""
			if _, err = s.PatchWebhook(ctx, d.ID, domains.WebhookPatch{URL: &empty}); err != nil {
				t.Fatal(err)
			}
			got, _ = s.WebhookDetails(ctx, d.ID)
			if got.RoutingMode != "global" || !got.SecretConfigured {
				t.Fatal("clearing URL must preserve secret and fallback")
			}
		})
	}
	s, _ := testService(t, Options{}, func(domains.Device) domains.Client { return &fakeClient{} })
	d := mustDevice(t, s, "none")
	got, err := s.WebhookDetails(ctx, d.ID)
	if err != nil || got.RoutingMode != "none" || len(got.RoutingRules) != 0 || got.SecretConfigured {
		t.Fatalf("none details: %+v %v", got, err)
	}
}
