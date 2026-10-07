package rest

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"
)

// Normal contract fixtures capture the current immutable instance before
// dispatch. Guard/replacement tests intentionally construct their own headers.
func scopeTestRequest(t *testing.T, s *Server, r *http.Request) {
	t.Helper()
	path := strings.TrimPrefix(r.URL.Path, s.opts.BasePath)
	path = strings.TrimPrefix(path, "/ui/api")
	if r.Method == "POST" && (path == "/devices" || strings.HasPrefix(path, "/send/")) && r.Header.Get("Idempotency-Key") == "" {
		r.Header.Set("Idempotency-Key", rand.Text())
	}
	id := r.Header.Get("X-Device-Id")
	if id == "" {
		id = r.URL.Query().Get("device_id")
	}
	if strings.HasPrefix(path, "/devices/") {
		id = strings.Split(strings.TrimPrefix(path, "/devices/"), "/")[0]
	}
	if id != "" && r.Header.Get("X-Device-Instance") == "" {
		if d, err := s.service.GetDevice(context.Background(), id); err == nil {
			r.Header.Set("X-Device-Instance", d.InstanceToken())
		}
	}
}
