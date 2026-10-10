package usecase

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

type downloadClient struct {
	*fakeClient
	downloadFn func(context.Context, domains.ProviderMedia) (io.ReadCloser, error)
}

func (f *downloadClient) Download(ctx context.Context, media domains.ProviderMedia) (io.ReadCloser, error) {
	return f.downloadFn(ctx, media)
}
func TestDownloadUsesScopedEncryptedReferenceAndReaderOwnsContext(t *testing.T) {
	ctx := context.Background()
	var downloadContext context.Context
	calls := 0
	f := &downloadClient{fakeClient: &fakeClient{status: readyStatus()}}
	f.downloadFn = func(ctx context.Context, media domains.ProviderMedia) (io.ReadCloser, error) {
		calls++
		downloadContext = ctx
		if media.FileID != "99" || media.AccessHash != "-7" {
			t.Fatalf("wrong reference %+v", media)
		}
		return io.NopCloser(strings.NewReader("hello")), nil
	}
	s, st := testService(t, Options{}, func(domains.Device) domains.Client { return f })
	one := mustDevice(t, s, "one")
	two := mustDevice(t, s, "two")
	peer := domains.Peer{Type: "user", ID: "42"}
	media := domains.ProviderMedia{Provider: domains.ProviderBale, Version: 1, FileID: "99", AccessHash: "-7", Name: "file.txt", ContentType: "text/plain", Size: 5}
	if _, e := st.SaveProviderMedia(ctx, one.ConnectionID, peer, "10", media); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.Download(ctx, two.ID, peer, "10"); e == nil {
		t.Fatal("cross-account file reference accepted")
	}
	if _, _, e := s.Download(ctx, one.ID, domains.Peer{Type: "user", ID: "43"}, "10"); e == nil {
		t.Fatal("cross-chat file reference accepted")
	}
	if calls != 0 {
		t.Fatal("invalid scope called provider")
	}
	reader, descriptor, err := s.Download(ctx, one.ID, peer, "10")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-downloadContext.Done():
		t.Fatal("download context ended at method return")
	default:
	}
	encoded, _ := json.Marshal(descriptor)
	if strings.Contains(string(encoded), "access_hash") || strings.Contains(string(encoded), "-7") {
		t.Fatal("reference leaked", string(encoded))
	}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "hello" {
		t.Fatal(string(body), err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-downloadContext.Done():
	case <-time.After(time.Second):
		t.Fatal("closing download reader did not cancel provider context")
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
}
