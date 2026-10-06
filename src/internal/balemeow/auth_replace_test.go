package balemeow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

func TestFailedNativeChallengeReplacementCannotUsePreviousCode(t *testing.T) {
	var starts, validations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "StartPhoneAuth") {
			if starts.Add(1) > 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "auth", Value: "synthetic-cookie"})
			_, _ = w.Write(grpcFrame(0, marshal(t, &wire.StartPhoneAuthResponse{TransactionHash: "synthetic-private-transaction"})))
			_, _ = w.Write(grpcFrame(128, []byte("grpc-status: 0\r\n")))
			return
		}
		validations.Add(1)
		t.Error("stale challenge reached provider")
	}))
	defer server.Close()
	c := New(Options{GRPCEndpoint: server.URL, AppID: 99, APIKey: "synthetic"})
	old, err := c.StartAuth(context.Background(), "+15550000123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.StartAuth(context.Background(), "+15550000123"); codeOf(err) != "AUTH_HTTP_ERROR" {
		t.Fatalf("replacement: %v", err)
	}
	if c.Status().Auth != "auth_required" {
		t.Fatalf("stale status: %+v", c.Status())
	}
	if _, err = c.SubmitCode(context.Background(), old.ID, "12345"); codeOf(err) != "CHALLENGE_EXPIRED" {
		t.Fatalf("stale code: %v", err)
	}
	if _, err = c.SubmitPassword(context.Background(), old.ID, "synthetic"); codeOf(err) != "CHALLENGE_EXPIRED" {
		t.Fatalf("stale password: %v", err)
	}
	if validations.Load() != 0 {
		t.Fatal("stale challenge validated")
	}
}
