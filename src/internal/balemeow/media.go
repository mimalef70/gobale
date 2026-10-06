package balemeow

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/internal/balemeow/wire"
)

// MediaInfo is provided by the gateway's immutable, account-scoped media store.
// Optional duration and dimensions are provider milliseconds and pixels; zero
// means unknown. Native voice duration is derived from the media bytes instead.
type MediaInfo struct {
	Name                    string
	ContentType             string
	Size                    int64
	Width, Height, Duration int32
}

type RemoteMedia = domains.ProviderMedia

func (c *Client) sendMedia(ctx context.Context, r domains.SendRequest, peer *wire.Peer, rid int64) (domains.SendResult, error) {
	if c.opts.MediaSource == nil {
		return domains.SendResult{}, domains.Unsupported("media source")
	}
	if r.Kind != "file" && r.Kind != "image" && r.Kind != "video" && r.Kind != "audio" && r.Kind != "voice" {
		return domains.SendResult{}, domains.Unsupported("media kind " + r.Kind)
	}
	// Validate all local inputs before creating an upload or transmitting bytes.
	quoted := int64(0)
	if r.ReplyMessageID != "" {
		var err error
		quoted, err = messageID(r.ReplyMessageID)
		if err != nil {
			return domains.SendResult{}, boundedError("INVALID_REQUEST", "reply_message_id must be a nonzero signed int64", 400)
		}
	}
	source, info, err := c.opts.MediaSource(ctx, r.MediaID)
	if err != nil {
		return domains.SendResult{}, err
	}
	if source == nil {
		return domains.SendResult{}, boundedError("MEDIA_NOT_FOUND", "media source is unavailable", 404)
	}
	defer source.Close()
	if info.Size <= 0 || info.Size > c.opts.MaxMediaBytes || info.Size > math.MaxInt32 {
		return domains.SendResult{}, boundedError("MEDIA_TOO_LARGE", "media is empty or exceeds configured size limit", 413)
	}
	name := filepath.Base(info.Name)
	if name == "" || name == "." || len(name) > 255 || strings.ContainsAny(name, "\r\n\x00") {
		return domains.SendResult{}, boundedError("INVALID_MEDIA", "media name is invalid", 400)
	}
	contentType, mimeParams, err := mime.ParseMediaType(info.ContentType)
	if err != nil {
		return domains.SendResult{}, boundedError("INVALID_MEDIA", "media content type is invalid", 400)
	}
	body := io.Reader(source)
	ext := (*wire.DocumentExt)(nil)
	sendType := int32(6)
	if info.Width < 0 || info.Height < 0 || info.Duration < 0 {
		return domains.SendResult{}, boundedError("INVALID_MEDIA", "media metadata cannot be negative", 400)
	}
	switch r.Kind {
	case "image":
		if !strings.HasPrefix(contentType, "image/") {
			return domains.SendResult{}, boundedError("INVALID_MEDIA", "image upload requires an image content type", 400)
		}
		var header bytes.Buffer
		cfg, format, err := image.DecodeConfig(io.TeeReader(io.LimitReader(source, 1<<20), &header))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > math.MaxInt32 || cfg.Height > math.MaxInt32 {
			return domains.SendResult{}, boundedError("INVALID_MEDIA", "image header is invalid or exceeds the 1 MiB inspection limit", 400)
		}
		canonical := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif"}[format]
		if contentType != canonical {
			return domains.SendResult{}, boundedError("INVALID_MEDIA", "image content type does not match its bytes", 400)
		}
		body = io.MultiReader(bytes.NewReader(header.Bytes()), source)
		ext = &wire.DocumentExt{Photo: &wire.Photo{Width: int32(cfg.Width), Height: int32(cfg.Height)}}
		sendType = 1
	case "video":
		if !strings.HasPrefix(contentType, "video/") {
			return domains.SendResult{}, boundedError("INVALID_MEDIA", "video upload requires a video content type", 400)
		}
		ext = &wire.DocumentExt{Video: &wire.Video{Width: info.Width, Height: info.Height, Duration: info.Duration}}
		sendType = 2
	case "audio":
		if !strings.HasPrefix(contentType, "audio/") {
			return domains.SendResult{}, boundedError("INVALID_MEDIA", "audio upload requires an audio content type", 400)
		}
		ext = &wire.DocumentExt{Audio: &wire.Audio{Duration: info.Duration, Track: name}}
		sendType = 5
	case "voice":
		if (contentType != "audio/ogg" && contentType != "application/ogg" && contentType != "audio/opus") || (mimeParams["codecs"] != "" && !strings.EqualFold(mimeParams["codecs"], "opus")) {
			return domains.SendResult{}, boundedError("VOICE_FORMAT_NOT_SUPPORTED", "native voice requires an Ogg Opus recording; automatic format conversion is not supported", 415)
		}
		prepared, duration, cleanup, err := prepareVoiceSource(ctx, source, info.Size)
		if err != nil {
			return domains.SendResult{}, err
		}
		defer cleanup()
		body = prepared
		contentType = "audio/ogg"
		ext = &wire.DocumentExt{Voice: &wire.Voice{Duration: duration}}
		sendType = 3
	}
	c.mu.Lock()
	account := ""
	if c.session != nil {
		account = c.session.UserID
	}
	c.mu.Unlock()
	uid, err := positiveID(account)
	if err != nil {
		return domains.SendResult{}, unavailable()
	}
	result, err := c.rpc(ctx, "ai.bale.server.Files", "GetNasimFileUploadUrl", &wire.FileUploadRequest{ExpectedSize: int32(info.Size), Uid: uid, Name: name, MimeType: contentType, ExPeer: extendedPeer(peer, r.Peer), SendType: &wire.SendType{Type: sendType}})
	if err != nil {
		return domains.SendResult{}, mediaSetupError(err)
	}
	upload := &wire.FileUploadResponse{}
	if decode(result, upload) != nil {
		return domains.SendResult{}, boundedError("MEDIA_UPLOAD_RESPONSE_INVALID", "provider upload response could not be decoded", 502)
	}
	if upload.FileId == 0 {
		return domains.SendResult{}, boundedError("MEDIA_UPLOAD_REFERENCE_MISSING", "provider upload response has no file reference", 502)
	}
	if !upload.Duplicate {
		if err = c.uploadBytes(ctx, upload.Url, body, info.Size); err != nil {
			return domains.SendResult{}, err
		}
	}
	// The inspected web client uses the authenticated user ID as file accessHash
	// for ordinary user/group uploads (channel uploads are deliberately unsupported).
	document := &wire.DocumentMessage{FileId: upload.FileId, AccessHash: uid, FileSize: int32(info.Size), Name: name, MimeType: contentType, Ext: ext}
	if r.Text != "" {
		document.Caption = &wire.TextMessage{Text: r.Text}
	}
	request := &wire.SendMessageRequest{Peer: peer, ExPeer: extendedPeer(peer, r.Peer), Rid: rid, Message: &wire.Message{Document: document}}
	if quoted != 0 {
		request.QuotedMessage = &wire.MessageReference{Peer: peer, Rid: quoted}
	}
	ack, err := c.rpc(ctx, "bale.messaging.v2.Messaging", "SendMessage", request)
	if err != nil {
		return domains.SendResult{}, err
	}
	sent := &wire.SendMessageResponse{}
	if decode(ack, sent) != nil || sent.Date <= 0 {
		return domains.SendResult{}, ambiguous()
	}
	return domains.SendResult{MessageID: r.RequestID, Date: time.UnixMilli(sent.Date).UTC()}, nil
}

func mediaSetupError(err error) error {
	var e *domains.Error
	if errors.As(err, &e) && e.Ambiguous {
		return &domains.Error{Code: "MEDIA_SETUP_FAILED", Message: "media setup outcome is unknown; no message send was attempted", HTTP: 502, Retryable: true}
	}
	return err
}

// Diagnostic carries allowlisted operational metadata only. Signed URLs,
// tokens, file hashes, URL paths and query strings are never included.
type Diagnostic struct {
	Code   string
	Scheme string
	Host   string
}

func (c *Client) mediaDiagnostic(code string, u *url.URL) {
	if c.opts.OnDiagnostic == nil {
		return
	}
	d := Diagnostic{Code: code}
	if u != nil {
		d.Scheme = u.Scheme
		d.Host = u.Hostname()
	}
	c.opts.OnDiagnostic(d)
}

// Official file URLs use a numbered gateway pool (observed file-gw1 and
// file-gw4). Match the whole hostname rather than a broad ble.ir wildcard.
func baleFileGateway(host string) bool {
	if !strings.HasPrefix(host, "file-gw") || !strings.HasSuffix(host, ".ble.ir") {
		return false
	}
	number := strings.TrimSuffix(strings.TrimPrefix(host, "file-gw"), ".ble.ir")
	return number != "" && strings.Trim(number, "0123456789") == ""
}

func (c *Client) validateMediaURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		c.mediaDiagnostic("MEDIA_URL_SCHEME_OR_FORMAT_REJECTED", u)
		return nil, boundedError("UNTRUSTED_MEDIA_URL", "provider media URL must use HTTPS", 502)
	}
	host := strings.ToLower(u.Hostname())
	// The provider's native upload/download RPCs returned these HTTPS hostnames.
	// Allow the observed numbered file-gateway pool only; unrelated ble.ir
	// hosts are not implicitly trusted.
	allowed := host == "bale.ai" || strings.HasSuffix(host, ".bale.ai") || host == "upload-ts-siloo.ble.ir" || baleFileGateway(host)
	if len(c.opts.MediaAllowedHosts) > 0 {
		allowed = false
		for _, h := range c.opts.MediaAllowedHosts {
			if strings.EqualFold(h, host) {
				allowed = true
			}
		}
	}
	if !allowed {
		c.mediaDiagnostic("MEDIA_URL_HOST_REJECTED", u)
		return nil, boundedError("UNTRUSTED_MEDIA_URL", "provider media hostname is not allowlisted", 502)
	}
	return u, nil
}

func (c *Client) mediaHTTP() *http.Client {
	h := *c.opts.HTTPClient
	h.Jar = nil // presigned URLs need no account cookie or token.
	h.Timeout = c.opts.MediaTimeout
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &h
}
func (c *Client) uploadBytes(ctx context.Context, raw string, source io.Reader, size int64) error {
	u, err := c.validateMediaURL(raw)
	if err != nil {
		return err
	}
	lifetime, cancel := context.WithTimeout(ctx, c.opts.MediaTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(lifetime, http.MethodPut, u.String(), io.LimitReader(source, size))
	if err != nil {
		return boundedError("INVALID_MEDIA", "unable to construct media upload", 400)
	}
	request.ContentLength = size
	// Current official client sends the raw file with this content type, without
	// MIME multipart wrapping or credentials. Do not substitute a form encoder.
	request.Header.Set("Content-Type", "multipart/form-data")
	response, err := c.mediaHTTP().Do(request)
	if err != nil {
		return &domains.Error{Code: "MEDIA_UPLOAD_FAILED", Message: "media upload did not complete; no message send was attempted", HTTP: 502, Retryable: true}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return boundedError("MEDIA_UPLOAD_REJECTED", "provider rejected media upload; no message send was attempted", 502)
	}
	return nil
}

// Download streams a provider file into an account-authorized gateway response.
// The caller must own RemoteMedia for the selected immutable connection. Signed
// URLs never leave this method. Close the returned body to cancel the download.
func (c *Client) Download(ctx context.Context, file RemoteMedia) (io.ReadCloser, error) {
	id, err := signedFileID(file.FileID)
	if err != nil {
		return nil, boundedError("INVALID_MEDIA", "file_id must be a nonzero signed int64 decimal string", 400)
	}
	hash, err := strconv.ParseInt(file.AccessHash, 10, 64)
	if err != nil {
		return nil, boundedError("INVALID_MEDIA", "access_hash must be an int64 decimal string", 400)
	}
	if file.Size < 0 || file.Size > c.opts.MaxMediaBytes {
		return nil, boundedError("MEDIA_TOO_LARGE", "remote media exceeds configured size limit", 413)
	}
	data, err := c.rpc(ctx, "ai.bale.server.Files", "GetNasimFileUrl", &wire.FileDownloadRequest{File: &wire.FileLocation{FileId: id, AccessHash: hash}})
	if err != nil {
		return nil, err
	}
	reply := &wire.FileDownloadResponse{}
	if decode(data, reply) != nil || reply.FileUrl == nil || reply.FileUrl.FileId != id {
		return nil, protocolError()
	}
	if reply.FileUrl.Algorithm != "" || len(reply.FileUrl.FileKey) != 0 {
		return nil, domains.Unsupported("encrypted media download")
	}
	u, err := c.validateMediaURL(reply.FileUrl.Url)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithTimeout(ctx, c.opts.MediaTimeout)
	request, err := http.NewRequestWithContext(lifetime, http.MethodGet, u.String(), nil)
	if err != nil {
		cancel()
		return nil, boundedError("INVALID_MEDIA", "unable to construct media download", 400)
	}
	response, err := c.mediaHTTP().Do(request)
	if err != nil {
		cancel()
		return nil, boundedError("MEDIA_DOWNLOAD_FAILED", "unable to download media", 502)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		cancel()
		return nil, boundedError("MEDIA_DOWNLOAD_REJECTED", "provider rejected media download", 502)
	}
	if response.ContentLength > c.opts.MaxMediaBytes {
		_ = response.Body.Close()
		cancel()
		return nil, boundedError("MEDIA_TOO_LARGE", "download exceeds configured size limit", 413)
	}
	return &boundedBody{body: response.Body, cancel: cancel, remaining: c.opts.MaxMediaBytes}, nil
}

type boundedBody struct {
	body      io.ReadCloser
	cancel    context.CancelFunc
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.body.Read(probe[:])
		if n > 0 {
			return 0, boundedError("MEDIA_TOO_LARGE", "download exceeds configured size limit", 413)
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	return n, err
}
func (b *boundedBody) Close() error { b.cancel(); return b.body.Close() }

// Bale file references are signed int64 values. Negative IDs are valid and must
// remain exact decimal strings across the gateway JSON boundary.
func signedFileID(value string) (int64, error) {
	if value == "" || strings.HasPrefix(value, "+") {
		return 0, errors.New("invalid file id")
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n == 0 {
		return 0, errors.New("invalid file id")
	}
	return n, nil
}
