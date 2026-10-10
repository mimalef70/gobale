package usecase

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mimalef70/goomni/src/domains"
)

type avatarClient struct {
	*fakeClient
	fn func(context.Context, domains.Peer, string) (io.ReadCloser, domains.AvatarInfo, error)
}

func (c *avatarClient) DownloadAvatar(ctx context.Context, peer domains.Peer, size string) (io.ReadCloser, domains.AvatarInfo, error) {
	return c.fn(ctx, peer, size)
}

func TestAvatarSelectsImmutableAccountAndOwnsReaderContext(t *testing.T) {
	var called []string
	var downloadContext context.Context
	s, _ := testService(t, Options{}, func(d domains.Device) domains.Client {
		return &avatarClient{fakeClient: &fakeClient{}, fn: func(ctx context.Context, peer domains.Peer, size string) (io.ReadCloser, domains.AvatarInfo, error) {
			called = append(called, d.ConnectionID)
			downloadContext = ctx
			if peer != (domains.Peer{Type: "user", ID: "42"}) || size != "small" {
				t.Fatalf("unexpected avatar selection: %+v %s", peer, size)
			}
			return io.NopCloser(strings.NewReader("image")), domains.AvatarInfo{Name: "avatar.png", ContentType: "image/png", Size: 5}, nil
		}}
	})
	ctx := context.Background()
	one := mustDevice(t, s, "one")
	two := mustDevice(t, s, "two")
	peer := domains.Peer{Type: "user", ID: "42"}
	for _, invalid := range []struct {
		device string
		peer   domains.Peer
		size   string
	}{
		{"missing", peer, ""}, {"", peer, ""}, {"one", domains.Peer{Type: "group", ID: "42"}, ""},
		{"one", domains.Peer{Type: "user", ID: "0"}, ""}, {"one", peer, "full"},
		{"one", domains.Peer{Type: "user", ID: "42", AccessHash: "999"}, ""},
	} {
		if _, _, err := s.DownloadAvatar(ctx, invalid.device, invalid.peer, invalid.size); err == nil {
			t.Fatal("invalid request accepted", invalid)
		}
	}
	if len(called) != 0 {
		t.Fatal("invalid selector reached provider")
	}
	for _, d := range []domains.Device{one, two} {
		reader, _, err := s.DownloadAvatar(ctx, d.ID, peer, "")
		if err != nil {
			t.Fatal(err)
		}
		if called[len(called)-1] != d.ConnectionID {
			t.Fatal("wrong account selected")
		}
		if downloadContext.Err() != nil {
			t.Fatal("context cancelled before consumption")
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if downloadContext.Err() == nil {
			t.Fatal("reader close did not release context")
		}
	}
	if err := s.DeleteDevice(ctx, one.ID); err != nil {
		t.Fatal(err)
	}
	fresh := mustDevice(t, s, one.ID)
	reader, _, err := s.DownloadAvatar(ctx, fresh.ID, peer, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if called[len(called)-1] == one.ConnectionID {
		t.Fatal("reused alias inherited old client")
	}
}
