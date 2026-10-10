//go:build uismoke

// Command uitestserver serves the real embedded UI/REST stack against a wholly
// synthetic local provider. It is excluded from ordinary builds and releases.
// No native Bale client, service worker, webhook HTTP dispatcher or private
// database is opened. Use only for isolated browser acceptance tests.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/goomni/src/config"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"github.com/mimalef70/goomni/src/ui/rest"
	"github.com/mimalef70/goomni/src/ui/web"
	"github.com/mimalef70/goomni/src/usecase"
)

type syntheticClient struct {
	mu        sync.Mutex
	account   string
	status    domains.ConnectionStatus
	sequence  int
	challenge string
}

func newSyntheticClient(d domains.Device) domains.Client {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(d.ConnectionID))
	return &syntheticClient{account: strconv.FormatUint((hash.Sum64()%((1<<32)-1))+1, 10), status: domains.ConnectionStatus{Auth: "auth_required", Transport: "disconnected", Recovery: "degraded"}}
}
func (c *syntheticClient) StartAuth(context.Context, string) (domains.Challenge, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence++
	c.challenge = fmt.Sprintf("synthetic-%d", c.sequence)
	c.status.Auth = "awaiting_code"
	cooldown := int64(30)
	return domains.Challenge{ID: c.challenge, State: "awaiting_code", ExpiresAt: time.Now().Add(2 * time.Minute), ResendAfterSeconds: &cooldown, SentCodeType: 1, NextSendCodeType: 1, AvailableSendCodeTypes: []int32{1}}, nil
}
func (c *syntheticClient) SubmitCode(_ context.Context, challenge, code string) (*domains.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if challenge != c.challenge || challenge == "" {
		return nil, domains.E("CHALLENGE_EXPIRED", "synthetic challenge expired", 400)
	}
	if code == "00000" {
		return nil, domains.E("INVALID_CODE", "synthetic invalid code", 400)
	}
	if code == "11111" {
		c.status.Auth = "awaiting_password"
		return nil, nil
	}
	return c.accept(), nil
}
func (c *syntheticClient) SubmitPassword(_ context.Context, challenge, password string) (*domains.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if challenge != c.challenge || challenge == "" {
		return nil, domains.E("CHALLENGE_EXPIRED", "synthetic challenge expired", 400)
	}
	if password == "" {
		return nil, domains.E("INVALID_PASSWORD", "synthetic empty password", 400)
	}
	// Preserve input exactly, including intentional leading/trailing spaces. It
	// is deliberately never logged or persisted by the synthetic provider.
	return c.accept(), nil
}
func (c *syntheticClient) accept() *domains.Session {
	c.challenge = ""
	c.status.Auth = "authenticated"
	return &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: c.account, Token: "synthetic-session", DeviceHash: "synthetic-device"}
}
func (c *syntheticClient) Connect(context.Context, *domains.Session, domains.Sink) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = domains.ConnectionStatus{Auth: "authenticated", Transport: "connected", Recovery: "gap_detected", LastError: "RECOVERY_GAP"}
	return nil
}
func (c *syntheticClient) Disconnect(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Transport = "disconnected"
	return nil
}
func (c *syntheticClient) Logout(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.challenge = ""
	c.status = domains.ConnectionStatus{Auth: "auth_required", Transport: "disconnected", Recovery: "degraded"}
	return nil
}
func (c *syntheticClient) Status() domains.ConnectionStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}
func (c *syntheticClient) Send(context.Context, domains.SendRequest) (domains.SendResult, error) {
	return domains.SendResult{}, domains.Unsupported("synthetic provider does not send messages")
}
func (c *syntheticClient) Call(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, domains.Unsupported("synthetic provider does not perform RPCs")
}

func seed(ctx context.Context, svc *usecase.Service, st *storage.Store) error {
	online, err := svc.CreateDevice(ctx, "online-demo", domains.ProviderBale)
	if err != nil {
		return err
	}
	if _, err = svc.CreateDevice(ctx, "login-demo", domains.ProviderBale); err != nil {
		return err
	}
	challenge, err := svc.StartAuth(ctx, online.ID, "+15550000123")
	if err != nil {
		return err
	}
	if _, err = svc.SubmitCode(ctx, online.ID, challenge.ID, "12345"); err != nil {
		return err
	}
	oldURL, secret := "https://previous.example.test/webhook", "synthetic-webhook-secret"
	if _, err = svc.PatchWebhook(ctx, online.ID, domains.WebhookPatch{URL: &oldURL, Secret: &secret}); err != nil {
		return err
	}
	appendFixture := func(name string) error {
		d, err := svc.GetDevice(ctx, online.ID)
		if err != nil {
			return err
		}
		_, err = st.AppendEvent(ctx, d.ConnectionID, domains.Event{ID: name, Type: "message", Peer: domains.Peer{Type: "user", ID: "42"}, Time: time.Now().UTC(), Payload: json.RawMessage(`{"text":"Synthetic local test event — رویداد آزمایشی"}`)}, []storage.WebhookTarget{{URL: d.Webhook.URL, Secret: d.Webhook.Secret, Revision: d.Webhook.Revision, Device: true}})
		return err
	}
	if err = appendFixture("paused-fixture"); err != nil {
		return err
	}
	nextURL := "https://current.example.test/webhook"
	if _, err = svc.PatchWebhook(ctx, online.ID, domains.WebhookPatch{URL: &nextURL}); err != nil {
		return err
	}
	if err = appendFixture("failed-fixture"); err != nil {
		return err
	}
	claimed, err := st.ClaimDeliveries(ctx, 1, time.Now().Add(time.Second))
	if err != nil {
		return err
	}
	if len(claimed) != 1 {
		return fmt.Errorf("synthetic delivery fixture missing")
	}
	if err = st.UpdateDelivery(ctx, online.ConnectionID, claimed[0].ID, "failed", time.Now(), "synthetic destination unavailable"); err != nil {
		return err
	}
	return appendFixture("pending-fixture")
}

func run() error {
	port := flag.Int("port", 3017, "loopback listening port (0 chooses a free port)")
	basePath := flag.String("base-path", "", "optional API/UI path prefix")
	flag.Parse()
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("invalid port")
	}
	bundle, err := web.Validate(config.AppVersion, config.ContractSHA256)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "goomni-ui-synthetic-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	st, err := storage.Open(filepath.Join(dir, "test.db"), bytes.Repeat([]byte{91}, 32))
	if err != nil {
		return err
	}
	defer st.Close()
	svc := usecase.New(st, usecase.Options{}, newSyntheticClient)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	}()
	// Do not call svc.Start: all browser actions use synchronous lifecycle methods
	// and local storage. This fixture must never dispatch even a configured URL.
	if err = seed(context.Background(), svc, st); err != nil {
		return err
	}
	srv, err := rest.New(svc, st, rest.Options{UIEnabled: true, UIAssets: bundle, Version: config.AppVersion, BasicAuth: "admin:synthetic-ui-password", BasePath: *basePath, MediaRoot: filepath.Join(dir, "media")})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.App.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()
	fmt.Printf("synthetic local test server: http://%s%s/ui/\n", listener.Addr(), *basePath)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if err := srv.App.ShutdownWithTimeout(3 * time.Second); err != nil {
			return err
		}
		return <-done
	}
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
