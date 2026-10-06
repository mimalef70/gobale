// A minimal durable consumer. Run with WEBHOOK_SECRET set, then point a GoBale
// device webhook at http://127.0.0.1:8080/events with the same secret.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mimalef70/gobale/src/pkg/sqlite"
)

func main() {
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		log.Fatal("Set WEBHOOK_SECRET before starting")
	}
	f, err := os.OpenFile("webhook-inbox.db", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		log.Fatal(err)
	}
	_ = f.Close()
	db, err := sql.Open(sqlite.DriverName, "webhook-inbox.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS inbox(event_id TEXT PRIMARY KEY, body BLOB NOT NULL)`); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /events", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
		if err != nil {
			http.Error(w, "body too large", 413)
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		sig, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
		if err != nil || !strings.HasPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=") || !hmac.Equal(sig, mac.Sum(nil)) {
			http.Error(w, "invalid signature", 401)
			return
		}
		var event struct {
			ID string `json:"event_id"`
		}
		if json.Unmarshal(body, &event) != nil || event.ID == "" || len(event.ID) > 256 {
			http.Error(w, "invalid event", 400)
			return
		}
		// A separate consumer may process committed inbox rows. Keep deduplication
		// and business effects transactional, or use another durable outbox.
		if _, err = db.ExecContext(r.Context(), `INSERT INTO inbox(event_id,body) VALUES(?,?) ON CONFLICT(event_id) DO NOTHING`, event.ID, body); err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: "127.0.0.1:8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10}
	log.Print("Example durable receiver listening on 127.0.0.1:8080")
	log.Fatal(server.ListenAndServe())
}
