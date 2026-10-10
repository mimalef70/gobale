package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func receiverFixture(t *testing.T) (*sql.DB, binding, http.Handler) {
	t.Helper()
	db, err := openInbox(filepath.Join(t.TempDir(), "inbox.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	expected := binding{Secret: "synthetic-webhook-secret", DeviceID: "synthetic-alias", InstanceID: strings.Repeat("a", 64), AccountID: "101"}
	handler, err := receiver(db, expected)
	require.NoError(t, err)
	return db, expected, handler
}

func receiverBody(t *testing.T, expected binding) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"event_id": "synthetic-event", "event": "message", "session_id": expected.DeviceID, "instance_id": expected.InstanceID, "device_id": expected.AccountID, "payload": map[string]string{"message": "synthetic private content"}})
	require.NoError(t, err)
	return body
}

func receiverRequest(body []byte, secret string) *http.Request {
	request := httptest.NewRequest("POST", "/events", bytes.NewReader(body))
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-GoOmni-Event-Id", "synthetic-event")
	return request
}

func TestReceiverCommitsRawBodyAndDeduplicatesConcurrentRetries(t *testing.T) {
	db, expected, handler := receiverFixture(t)
	body := receiverBody(t, expected)
	results := make(chan int, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, receiverRequest(body, expected.Secret))
			results <- response.Code
		})
	}
	wg.Wait()
	close(results)
	for status := range results {
		require.Equal(t, 204, status)
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM inbox`).Scan(&count))
	require.Equal(t, 1, count)
	var stored []byte
	require.NoError(t, db.QueryRow(`SELECT body FROM inbox WHERE event_id=?`, "synthetic-event").Scan(&stored))
	require.Equal(t, body, stored)
	// A duplicate identity cannot replace the already accepted raw evidence.
	changed := bytes.Replace(body, []byte("synthetic private content"), []byte("changed content"), 1)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, receiverRequest(changed, expected.Secret))
	require.Equal(t, 204, response.Code)
	require.NoError(t, db.QueryRow(`SELECT body FROM inbox WHERE event_id=?`, "synthetic-event").Scan(&stored))
	require.Equal(t, body, stored)
}

func TestReceiverRejectsSignedWrongOrAmbiguousIdentity(t *testing.T) {
	db, expected, handler := receiverFixture(t)
	original := receiverBody(t, expected)
	for _, test := range []struct {
		name, old, replacement string
		status                 int
	}{
		{"alias", `"session_id":"synthetic-alias"`, `"session_id":"other-alias"`, 403},
		{"account", `"device_id":"101"`, `"device_id":"202"`, 403},
		{"instance", `"instance_id":"` + expected.InstanceID + `"`, `"instance_id":"` + strings.Repeat("b", 64) + `"`, 403},
		{"missing instance", `"instance_id"`, `"missing_instance"`, 400},
		{"numeric account", `"device_id":"101"`, `"device_id":101`, 400},
		{"null account", `"device_id":"101"`, `"device_id":null`, 400},
		{"duplicate field", `"device_id":"101"`, `"device_id":"101","device_id":"202"`, 400},
		{"encoded duplicate", `"device_id":"101"`, `"device_id":"101","device_\u0069d":"202"`, 400},
		{"case alias", `"device_id":"101"`, `"device_id":"101","DEVICE_ID":"202"`, 400},
		{"case replacement", `"device_id":"101"`, `"DEVICE_ID":"101"`, 400},
		{"header mismatch", `"event_id":"synthetic-event"`, `"event_id":"different-event"`, 400},
		{"empty event", `"event_id":"synthetic-event"`, `"event_id":""`, 400},
		{"control in event", `"event_id":"synthetic-event"`, `"event_id":"line\nbreak"`, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := bytes.Replace(original, []byte(test.old), []byte(test.replacement), 1)
			require.NotEqual(t, original, body, "fixture replacement must take effect")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, receiverRequest(body, expected.Secret))
			require.Equal(t, test.status, response.Code)
			require.NotContains(t, response.Body.String(), expected.Secret)
			require.NotContains(t, response.Body.String(), "synthetic private content")
		})
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM inbox`).Scan(&count))
	require.Zero(t, count)
}

func TestReceiverRejectsSignatureAndHeaderSpoofing(t *testing.T) {
	_, expected, handler := receiverFixture(t)
	body := receiverBody(t, expected)
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{"missing signature", func(r *http.Request) { r.Header.Del("X-Hub-Signature-256") }, 401},
		{"invalid signature", func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256=invalid") }, 401},
		{"duplicate signature", func(r *http.Request) { r.Header.Add("X-Hub-Signature-256", r.Header.Get("X-Hub-Signature-256")) }, 401},
		{"tampered raw body", func(r *http.Request) { r.Body = io.NopCloser(bytes.NewReader(append([]byte(" "), body...))) }, 401},
		{"missing event header", func(r *http.Request) { r.Header.Del("X-GoOmni-Event-Id") }, 400},
		{"duplicate event header", func(r *http.Request) { r.Header.Add("X-GoOmni-Event-Id", "synthetic-event") }, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := receiverRequest(body, expected.Secret)
			test.mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code)
		})
	}
}

type incompleteBody struct{}

func (incompleteBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestReceiverRejectsMalformedOversizedAndIncompleteBodies(t *testing.T) {
	_, expected, handler := receiverFixture(t)
	for _, body := range [][]byte{[]byte(`[]`), []byte(`{"event_id":`), append(receiverBody(t, expected), []byte(` {}`)...), bytes.Replace(receiverBody(t, expected), []byte("synthetic private content"), []byte{0xff}, 1)} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, receiverRequest(body, expected.Secret))
		require.Equal(t, 400, response.Code)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, receiverRequest(bytes.Repeat([]byte("x"), (4<<20)+1), expected.Secret))
	require.Equal(t, 413, response.Code)
	request := receiverRequest(nil, expected.Secret)
	request.Body = io.NopCloser(incompleteBody{})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, 400, response.Code)
}

func TestReceiverStorageFailureDoesNotAcknowledge(t *testing.T) {
	db, expected, handler := receiverFixture(t)
	require.NoError(t, db.Close())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, receiverRequest(receiverBody(t, expected), expected.Secret))
	require.Equal(t, 503, response.Code)
	require.Equal(t, "storage unavailable\n", response.Body.String())
}

func TestReceiverRequiresConfiguredBinding(t *testing.T) {
	db, expected, _ := receiverFixture(t)
	for _, field := range []string{"secret", "alias", "instance", "account"} {
		missing := expected
		switch field {
		case "secret":
			missing.Secret = ""
		case "alias":
			missing.DeviceID = ""
		case "instance":
			missing.InstanceID = ""
		case "account":
			missing.AccountID = ""
		}
		_, err := receiver(db, missing)
		require.Error(t, err)
		require.NotContains(t, err.Error(), expected.Secret)
	}
}
