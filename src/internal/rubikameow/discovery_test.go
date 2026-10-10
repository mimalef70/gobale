package rubikameow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoveryCurrentSocketListAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sockets any
		valid   bool
	}{
		{"current list", []string{"wss://nsocket12.iranlms.ir:80", "wss://nsocket7.iranlms.ir:80"}, true},
		{"empty", []string{}, false},
		{"missing", nil, false},
		{"wrong type", map[string]string{"1": "wss://nsocket12.iranlms.ir:80"}, false},
		{"nonstring", []any{17}, false},
		{"untrusted host", []string{"wss://example.test"}, false},
		{"oversized", make([]string, 33), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "OK", "data": map[string]any{"default_api_urls": []string{"https://messengerg2c466.iranlms.ir"}, "default_sockets": tc.sockets}})
			}))
			defer srv.Close()
			c, err := New(Config{DiscoveryEndpoint: srv.URL, HTTPClient: srv.Client()})
			if err != nil {
				t.Fatal(err)
			}
			err = c.discover(context.Background())
			if (err == nil) != tc.valid {
				t.Fatalf("discovery valid=%t: %v", tc.valid, err)
			}
			if tc.valid && c.socket != "wss://nsocket12.iranlms.ir:80" {
				t.Fatal("wrong selected socket")
			}
			if !tc.valid && (c.api != "" || c.socket != "") {
				t.Fatal("partially published discovery")
			}
		})
	}
}
