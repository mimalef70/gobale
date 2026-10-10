// A minimal durable receiver for one explicitly configured account. See
// docs/consumer-integration.md; this is not a tenant authorization service.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/pkg/sqlite"
)

type binding struct {
	Secret, DeviceID, InstanceID, AccountID string
}

func (b binding) validate() error {
	if strings.TrimSpace(b.Secret) == "" || len(b.Secret) > 4096 {
		return errors.New("set a nonempty WEBHOOK_SECRET of at most 4096 bytes")
	}
	for _, id := range []string{b.DeviceID, b.InstanceID, b.AccountID} {
		if id == "" || len(id) > 256 || !utf8.ValidString(id) || strings.TrimSpace(id) != id || strings.ContainsFunc(id, unicode.IsControl) {
			return errors.New("set WEBHOOK_DEVICE_ID, WEBHOOK_INSTANCE_ID and WEBHOOK_ACCOUNT_ID to the saved device binding")
		}
	}
	return nil
}

func openInbox(path string) (*sql.DB, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	db, err := sql.Open(sqlite.DriverName, path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS inbox(event_id TEXT PRIMARY KEY, body BLOB NOT NULL)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func receiver(db *sql.DB, expected binding) (http.Handler, error) {
	if err := expected.validate(); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, errors.New("inbox storage is required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /events", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "body too large", 413)
			} else {
				http.Error(w, "incomplete body", 400)
			}
			return
		}
		mac := hmac.New(sha256.New, []byte(expected.Secret))
		_, _ = mac.Write(body)
		signatures := r.Header.Values("X-Hub-Signature-256")
		if len(signatures) != 1 || !hmac.Equal([]byte(signatures[0]), []byte("sha256="+hex.EncodeToString(mac.Sum(nil)))) {
			http.Error(w, "invalid signature", 401)
			return
		}
		if !utf8.Valid(body) || domains.ValidateJSONObject(body) != nil {
			http.Error(w, "invalid event", 400)
			return
		}
		// Read exact keys. Struct unmarshalling would also match case aliases such
		// as INSTANCE_ID; these must not override the authenticated binding.
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) != nil {
			http.Error(w, "invalid event", 400)
			return
		}
		ids := map[string]string{}
		for _, name := range []string{"event_id", "session_id", "instance_id", "device_id"} {
			var value string
			if json.Unmarshal(fields[name], &value) != nil || value == "" || len(value) > 256 || strings.ContainsFunc(value, unicode.IsControl) {
				http.Error(w, "invalid event identity", 400)
				return
			}
			for key := range fields {
				if key != name && strings.EqualFold(key, name) {
					http.Error(w, "ambiguous event identity", 400)
					return
				}
			}
			ids[name] = value
		}
		eventHeaders := r.Header.Values("X-GoOmni-Event-Id")
		if len(eventHeaders) != 1 || eventHeaders[0] != ids["event_id"] {
			http.Error(w, "event identity mismatch", 400)
			return
		}
		if ids["session_id"] != expected.DeviceID || ids["instance_id"] != expected.InstanceID || ids["device_id"] != expected.AccountID {
			http.Error(w, "event binding mismatch", 403)
			return
		}
		// A separate consumer may process committed inbox rows. Keep deduplication
		// and business effects transactional, or use another durable outbox. Event
		// IDs are connection-scoped; retain the existing inbox schema for upgrades.
		if _, err := db.ExecContext(r.Context(), `INSERT INTO inbox(event_id,body) VALUES(?,?) ON CONFLICT(event_id) DO NOTHING`, ids["event_id"], body); err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux, nil
}

func main() {
	expected := binding{Secret: os.Getenv("WEBHOOK_SECRET"), DeviceID: os.Getenv("WEBHOOK_DEVICE_ID"), InstanceID: os.Getenv("WEBHOOK_INSTANCE_ID"), AccountID: os.Getenv("WEBHOOK_ACCOUNT_ID")}
	if err := expected.validate(); err != nil {
		log.Fatal(err)
	}
	db, err := openInbox("webhook-inbox.db")
	if err != nil {
		log.Fatal("inbox initialization failed")
	}
	defer db.Close()
	mux, err := receiver(db, expected)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: "127.0.0.1:8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10}
	log.Print("Example durable receiver listening on 127.0.0.1:8080")
	log.Fatal(server.ListenAndServe())
}
