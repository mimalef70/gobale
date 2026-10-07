package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func restoreV6ProvisioningSchema(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.db.Exec(`DROP TABLE device_provisioning`)
	require.NoError(t, err)
}

func provisioningRequest(alias string) domains.ProvisionDeviceRequest {
	return domains.ProvisionDeviceRequest{DeviceID: alias, WebhookURL: "https://example.test/hook", WebhookSecret: "synthetic-provisioning-secret", WebhookEvents: []string{"message"}, WebhookFilter: domains.WebhookFilter{Directions: []string{"incoming"}, Peers: []domains.Peer{{Type: "user", ID: "123"}}}}
}

func TestProvisioningReplayPrivacyAndChangedRequest(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	request := provisioningRequest("channel")
	d, replay, err := s.ProvisionDevice(ctx, request, "provision-key")
	require.NoError(t, err)
	require.False(t, replay)
	require.NotEmpty(t, d.ConnectionID)
	require.Equal(t, d.InstanceToken(), d.InstanceID)
	require.Equal(t, request.WebhookURL, d.Webhook.URL)
	require.Equal(t, request.WebhookSecret, d.Webhook.Secret)
	require.Equal(t, request.WebhookEvents, d.Webhook.Events)
	require.Equal(t, request.WebhookFilter, d.Webhook.Filter)
	require.EqualValues(t, 1, d.Webhook.Revision)
	var secret, hash []byte
	require.NoError(t, s.db.QueryRow(`SELECT d.webhook_secret,p.payload_hash FROM devices d JOIN device_provisioning p ON p.connection_id=d.connection_id WHERE p.idempotency_key='provision-key'`).Scan(&secret, &hash))
	require.False(t, bytes.Contains(secret, []byte(request.WebhookSecret)))
	body, err := json.Marshal(request)
	require.NoError(t, err)
	unkeyed := sha256.Sum256(body)
	require.NotEqual(t, unkeyed[:], hash)
	require.Len(t, hash, sha256.Size)
	public, err := json.Marshal(d)
	require.NoError(t, err)
	require.NotContains(t, string(public), request.WebhookSecret)
	otherKeyStore, err := Open(filepath.Join(t.TempDir(), "other.db"), bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	defer otherKeyStore.Close()
	otherHash, err := otherKeyStore.provisioningHash(request)
	require.NoError(t, err)
	require.NotEqual(t, hash, otherHash, "fingerprints must depend on the external storage key")

	for _, test := range []struct {
		name   string
		change func(*domains.ProvisionDeviceRequest)
	}{
		{"alias", func(r *domains.ProvisionDeviceRequest) { r.DeviceID = "other" }},
		{"URL", func(r *domains.ProvisionDeviceRequest) { r.WebhookURL += "/new" }},
		{"secret", func(r *domains.ProvisionDeviceRequest) { r.WebhookSecret += "-changed" }},
		{"events", func(r *domains.ProvisionDeviceRequest) { r.WebhookEvents = []string{"message.edited"} }},
		{"filter", func(r *domains.ProvisionDeviceRequest) { r.WebhookFilter = domains.WebhookFilter{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.change(&changed)
			_, replay, err := s.ProvisionDevice(ctx, changed, "provision-key")
			errorCode(t, err, "IDEMPOTENCY_CONFLICT")
			require.False(t, replay)
		})
	}
	updatedSecret := "later-secret"
	_, err = s.PatchWebhook(ctx, d.ConnectionID, domains.WebhookPatch{Secret: &updatedSecret})
	require.NoError(t, err)
	again, replay, err := s.ProvisionDevice(ctx, request, "provision-key")
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, d.ConnectionID, again.ConnectionID)
	require.Equal(t, d.InstanceID, again.InstanceID)
	require.Equal(t, d.CreatedAt, again.CreatedAt)
	require.Equal(t, updatedSecret, again.Webhook.Secret, "a replay must not restore the initial configuration")
	_, _, err = s.ProvisionDevice(ctx, request, "different-key")
	errorCode(t, err, "DEVICE_EXISTS")
}

func TestProvisioningRetiredConnectionNeverRebindsAlias(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	request := provisioningRequest("reused")
	original, _, err := s.ProvisionDevice(ctx, request, "original-key")
	require.NoError(t, err)
	require.NoError(t, s.DeleteDevice(ctx, original.ConnectionID))
	replacement := device(t, s, request.DeviceID)
	require.NotEqual(t, original.ConnectionID, replacement.ConnectionID)
	got, replay, err := s.ProvisionDevice(ctx, request, "original-key")
	errorCode(t, err, "PROVISIONING_RETIRED")
	require.Empty(t, got.ConnectionID)
	require.False(t, replay)
	require.NoError(t, s.DeleteDevice(ctx, replacement.ConnectionID))
	latest, replay, err := s.ProvisionDevice(ctx, request, "new-lifetime-key")
	require.NoError(t, err)
	require.False(t, replay)
	require.NotEqual(t, original.ConnectionID, latest.ConnectionID)
	_, _, err = s.ProvisionDevice(ctx, request, "original-key")
	errorCode(t, err, "PROVISIONING_RETIRED")
}

func TestProvisioningConcurrentRequestsCommitOnce(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	const count = 24
	type result struct {
		device domains.Device
		replay bool
		err    error
	}
	results := make(chan result, count)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			<-start
			d, replay, err := s.ProvisionDevice(ctx, provisioningRequest("simultaneous"), "same-key")
			results <- result{d, replay, err}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	created := 0
	connection := ""
	for r := range results {
		require.NoError(t, r.err)
		if !r.replay {
			created++
		}
		if connection == "" {
			connection = r.device.ConnectionID
		}
		require.Equal(t, connection, r.device.ConnectionID)
	}
	require.Equal(t, 1, created)
	var rows int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&rows))
	require.Equal(t, 1, rows)
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM device_provisioning`).Scan(&rows))
	require.Equal(t, 1, rows)
}

func TestProvisioningValidationAndTransactionFailuresLeaveNoDevice(t *testing.T) {
	ctx := context.Background()
	for _, stage := range []string{"validation", "journal_insert", "commit"} {
		t.Run(stage, func(t *testing.T) {
			s, _ := testStore(t)
			request := provisioningRequest("atomic")
			switch stage {
			case "validation":
				request.WebhookSecret = ""
			case "journal_insert":
				_, err := s.db.Exec(`CREATE TRIGGER provisioning_failure BEFORE INSERT ON device_provisioning BEGIN SELECT RAISE(ABORT,'synthetic provisioning failure'); END`)
				require.NoError(t, err)
			case "commit":
				_, err := s.db.Exec(`CREATE TABLE provisioning_commit_failure(connection_id TEXT REFERENCES devices(connection_id) DEFERRABLE INITIALLY DEFERRED)`)
				require.NoError(t, err)
				_, err = s.db.Exec(`CREATE TRIGGER provisioning_failure AFTER INSERT ON device_provisioning BEGIN INSERT INTO provisioning_commit_failure VALUES('missing-connection'); END`)
				require.NoError(t, err)
			}
			d, replay, err := s.ProvisionDevice(ctx, request, "atomic-key")
			require.Error(t, err)
			require.Empty(t, d.ConnectionID)
			require.False(t, replay)
			for _, table := range []string{"devices", "device_provisioning"} {
				var rows int
				require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&rows))
				require.Zero(t, rows, "%s must roll back", table)
			}
			_, err = s.db.Exec(`DROP TRIGGER IF EXISTS provisioning_failure`)
			require.NoError(t, err)
			_, replay, err = s.ProvisionDevice(ctx, provisioningRequest("atomic"), "atomic-key")
			require.NoError(t, err)
			require.False(t, replay, "failed attempts must not reserve their key")
		})
	}
}

func TestProvisioningEmptyConfigAndNamespace(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	request := domains.ProvisionDeviceRequest{DeviceID: "empty"}
	_, _, err := s.ProvisionDevice(ctx, request, " ")
	errorCode(t, err, "IDEMPOTENCY_KEY_REQUIRED")
	d, _, err := s.ProvisionDevice(ctx, request, "shared-key")
	require.NoError(t, err)
	require.Empty(t, d.Webhook.URL)
	require.Equal(t, []string{}, d.Webhook.Events)
	request.WebhookEvents = []string{}
	request.WebhookFilter.Directions = []string{}
	_, replay, err := s.ProvisionDevice(ctx, request, "shared-key")
	require.NoError(t, err)
	require.True(t, replay)
	// A provisioning key does not consume this connection's send namespace.
	_, created, err := s.Enqueue(ctx, d.ConnectionID, textRequest("namespace"), "shared-key", AdmissionLimits{Global: 10, Connection: 10})
	require.NoError(t, err)
	require.True(t, created)
}

func TestProvisioningSurvivesProcessExitAfterCommit(t *testing.T) {
	if os.Getenv("GOBALE_TEST_PROVISION_EXIT") == "1" {
		s, err := Open(os.Getenv("GOBALE_TEST_PROVISION_DB"), testKey)
		if err != nil {
			t.Fatal(err)
		}
		d, replay, err := s.ProvisionDevice(context.Background(), provisioningRequest("crash"), "lost-response")
		if err != nil || replay {
			t.Fatalf("provision before exit: replay=%v error=%v", replay, err)
		}
		fmt.Print(d.ConnectionID)
		// Deliberately skip Close and all defers, as after a process loss between
		// commit and the caller receiving its HTTP response.
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "committed.db")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProvisioningSurvivesProcessExitAfterCommit$")
	cmd.Env = append(os.Environ(), "GOBALE_TEST_PROVISION_EXIT=1", "GOBALE_TEST_PROVISION_DB="+path)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	connection := strings.TrimSpace(string(output))
	require.Len(t, connection, 36)
	s, err := Open(path, testKey)
	require.NoError(t, err)
	defer s.Close()
	d, replay, err := s.ProvisionDevice(context.Background(), provisioningRequest("crash"), "lost-response")
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, connection, d.ConnectionID)
	require.Equal(t, provisioningRequest("crash").WebhookSecret, d.Webhook.Secret)
}

func TestProvisioningMigrationPreservesV5AndV6Data(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s, path := testStore(t)
			d := device(t, s, "existing")
			require.NoError(t, s.SaveSession(ctx, d.ConnectionID, &domains.Session{UserID: "123", Token: "synthetic-session"}))
			_, err := s.AppendEvent(ctx, d.ConnectionID, event("preserved", "checkpoint"), []WebhookTarget{{URL: "https://example.test/old", Secret: "old-secret"}})
			require.NoError(t, err)
			if version == 5 {
				restoreV5APISchema(t, s)
			} else {
				restoreV6ProvisioningSchema(t, s)
			}
			_, err = s.db.Exec(`UPDATE gobale_meta SET version=?`, version)
			require.NoError(t, err)
			require.NoError(t, s.Close())
			upgraded, err := Open(path, testKey)
			require.NoError(t, err)
			defer upgraded.Close()
			var actual int
			require.NoError(t, upgraded.db.QueryRow(`SELECT version FROM gobale_meta`).Scan(&actual))
			require.Equal(t, schemaVersion, actual)
			session, err := upgraded.LoadSession(ctx, d.ConnectionID)
			require.NoError(t, err)
			require.Equal(t, "synthetic-session", session.Token)
			checkpoint, err := upgraded.Checkpoint(ctx, d.ConnectionID)
			require.NoError(t, err)
			require.Equal(t, "checkpoint", checkpoint)
			var deliveries int
			require.NoError(t, upgraded.db.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE connection_id=?`, d.ConnectionID).Scan(&deliveries))
			require.Equal(t, 1, deliveries)
			_, _, err = upgraded.ProvisionDevice(ctx, provisioningRequest("new"), "new-key")
			require.NoError(t, err)
		})
	}
}
