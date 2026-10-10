package domains

import (
	"context"
	"encoding/json"
	"io"
)

// NativeMediaInfo describes an owned local file opened for a native adapter.
// Wire-specific duration and dimensions must be inspected from its bytes.
type NativeMediaInfo struct {
	Name        string
	ContentType string
	Size        int64
}
type MediaSource func(context.Context, string) (io.ReadCloser, NativeMediaInfo, error)

// ProviderMedia is a reference obtained from this account's provider updates or
// history. AccessHash is an internal capability and must never leave the gateway.
type ProviderMedia struct {
	Provider    Provider        `json:"provider"`
	Version     int             `json:"version"`
	Data        json.RawMessage `json:"-"`
	FileID      string          `json:"file_id"`
	AccessHash  string          `json:"-"`
	Size        int64           `json:"size"`
	Name        string          `json:"name"`
	ContentType string          `json:"content_type"`
}

// MediaDownloader is optional so ordinary test clients do not require media.
// The gateway passes only a reference recovered from the selected connection's
// trusted storage; it must not forward arbitrary file IDs or hashes from callers.
type MediaDownloader interface {
	Download(context.Context, ProviderMedia) (io.ReadCloser, error)
}

// AvatarInfo describes a verified profile image. Provider URLs, file IDs and
// access hashes are deliberately absent from this public-safe projection.
type AvatarInfo struct {
	Name, ContentType string
	Size              int64
	Width, Height     int
}

// AvatarDownloader resolves the image through the selected authenticated
// account. Missing and privacy-restricted avatars have the same public error.
type AvatarDownloader interface {
	DownloadAvatar(context.Context, Peer, string) (io.ReadCloser, AvatarInfo, error)
}
