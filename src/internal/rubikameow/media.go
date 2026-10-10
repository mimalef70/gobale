package rubikameow

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/mediautil"
)

const transferChunk = 256 << 10

var dcPattern = regexp.MustCompile(`^[0-9]{1,6}$`)

type privateMedia struct {
	Version int    `json:"version"`
	Account string `json:"account"`
	DC      string `json:"dc"`
	ID      string `json:"id"`
	Hash    string `json:"hash"`
	Size    int64  `json:"size"`
}

func stage(ctx context.Context, n int, name, state, nonce string, data any) error {
	raw, e := json.Marshal(data)
	if e != nil {
		return e
	}
	return domains.RecordOperationStage(ctx, domains.OperationStage{Number: n, Name: name, State: state, Nonce: nonce, Data: raw})
}
func uncertain(err error) error {
	var d *domains.Error
	if errors.As(err, &d) {
		copy := *d
		copy.Ambiguous = true
		return &copy
	}
	return &domains.Error{Code: "SEND_UNKNOWN", Message: "Rubika media operation could not be recorded", HTTP: 502, Ambiguous: true}
}
func (c *Client) sendMedia(ctx context.Context, r domains.SendRequest) (domains.SendResult, error) {
	if c.cfg.MediaSource == nil {
		return domains.SendResult{}, domains.E("MEDIA_UNAVAILABLE", "local media source is unavailable", 503)
	}
	source, info, e := c.cfg.MediaSource(ctx, r.MediaID)
	if e != nil {
		return domains.SendResult{}, e
	}
	defer source.Close()
	if info.Size < 1 || info.Size > c.cfg.MaxMediaBytes {
		return domains.SendResult{}, domains.E("MEDIA_TOO_LARGE", "media size exceeds configured limit", 413)
	}
	reader := io.Reader(source)
	metadata := object{}
	fileType := "File"
	switch r.Kind {
	case "image":
		buffer := bufio.NewReader(source)
		config, format, e := image.DecodeConfig(buffer)
		if e != nil || config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 16777216 {
			return domains.SendResult{}, domains.E("INVALID_MEDIA", "image must be a bounded JPEG, PNG or GIF", 400)
		}
		seek, ok := source.(io.Seeker)
		if !ok {
			return domains.SendResult{}, domains.E("INVALID_MEDIA", "owned image source must support seeking", 400)
		}
		if _, e = seek.Seek(0, io.SeekStart); e != nil {
			return domains.SendResult{}, e
		}
		thumb, e := imageThumbnail(ctx, source)
		if e != nil {
			return domains.SendResult{}, e
		}
		metadata["thumb_inline"] = thumb
		if seek, ok := source.(io.Seeker); ok {
			if _, e = seek.Seek(0, io.SeekStart); e != nil {
				return domains.SendResult{}, e
			}
			reader = source
		} else {
			return domains.SendResult{}, domains.E("INVALID_MEDIA", "owned image source must support seeking", 400)
		}
		fileType = "Image"
		metadata["width"] = config.Width
		metadata["height"] = config.Height
		info.ContentType = "image/" + format
	case "voice":
		prepared, duration, cleanup, e := mediautil.PrepareVoiceSource(ctx, source, info.Size)
		if e != nil {
			return domains.SendResult{}, e
		}
		defer cleanup()
		reader = prepared
		fileType = "Voice"
		metadata["time"] = duration
		info.ContentType = "audio/ogg"
	case "audio":
		prepared, cleanup, e := mediautil.SeekableSource(ctx, source, info.Size)
		if e != nil {
			return domains.SendResult{}, e
		}
		defer cleanup()
		duration, e := mediautil.InspectMP3(ctx, prepared, info.Size)
		if e != nil {
			if ctx.Err() != nil {
				return domains.SendResult{}, ctx.Err()
			}
			return domains.SendResult{}, domains.E("INVALID_MEDIA", "Rubika music requires a complete bounded MPEG layer III stream; Ogg Opus is supported as voice", 400)
		}
		reader = prepared
		fileType = "Music"
		if duration < 1000 {
			return domains.SendResult{}, domains.E("INVALID_MEDIA", "music must contain at least one second of audio", 400)
		}
		metadata["time"] = duration / 1000
		metadata["music_performer"] = ""
		info.ContentType = "audio/mpeg"
	case "video":
		prepared, video, cleanup, e := mediautil.PrepareVideoSource(ctx, source, info.Size)
		if e != nil {
			return domains.SendResult{}, e
		}
		defer cleanup()
		reader = prepared
		fileType = "Video"
		seek, ok := prepared.(io.ReadSeeker)
		if !ok {
			return domains.SendResult{}, domains.E("INVALID_MEDIA", "video source must support seeking", 400)
		}
		frame, e := mediautil.VideoPreview(ctx, seek, info.Size, video)
		if e != nil {
			return domains.SendResult{}, e
		}
		thumb, e := encodeThumbnail(ctx, frame)
		if e != nil {
			return domains.SendResult{}, e
		}
		if _, e = seek.Seek(0, io.SeekStart); e != nil {
			return domains.SendResult{}, e
		}
		metadata["thumb_inline"] = thumb
		metadata["time"] = video.DurationMilliseconds
		metadata["width"] = video.Width
		metadata["height"] = video.Height
		info.ContentType = "video/mp4"
	case "file":
	default:
		return domains.SendResult{}, domains.Unsupported("Rubika media type")
	}
	name := filepath.Base(info.Name)
	if name == "." || name == "" || len(name) > 255 || strings.ContainsAny(name, "\r\n\x00") {
		return domains.SendResult{}, domains.E("INVALID_MEDIA", "invalid media name", 400)
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if extension == "" {
		extension = "bin"
	}
	if len(extension) > 24 {
		return domains.SendResult{}, domains.E("INVALID_MEDIA", "invalid media extension", 400)
	}
	if e = stage(ctx, 1, "upload", "started", r.RequestID, nil); e != nil {
		return domains.SendResult{}, e
	}
	uploaded, e := c.upload(ctx, reader, info.Size, name, extension)
	if e != nil {
		_ = stage(ctx, 1, "upload", "unknown", r.RequestID, nil)
		return domains.SendResult{}, uncertain(e)
	}
	if e = stage(ctx, 1, "upload", "succeeded", r.RequestID, uploaded); e != nil {
		return domains.SendResult{}, uncertain(e)
	}
	uploaded["type"] = fileType
	for key, value := range metadata {
		uploaded[key] = value
	}
	input := object{"object_guid": r.Peer.ID, "rnd": r.RequestID, "file_inline": uploaded}
	if r.Text != "" {
		input["text"] = r.Text
	}
	if r.ReplyMessageID != "" {
		input["reply_to_message_id"] = r.ReplyMessageID
	}
	if e = stage(ctx, 2, "send", "started", r.RequestID, nil); e != nil {
		return domains.SendResult{}, uncertain(e)
	}
	result, e := c.invoke(ctx, "sendMessage", input, true, "")
	if e != nil {
		_ = stage(ctx, 2, "send", "unknown", r.RequestID, nil)
		return domains.SendResult{}, uncertain(e)
	}
	sent, e := sendResult(result, r.Peer)
	if e != nil {
		_ = stage(ctx, 2, "send", "unknown", r.RequestID, nil)
		return sent, e
	}
	if e = stage(ctx, 2, "send", "succeeded", r.RequestID, object{"message_id": sent.MessageID}); e != nil {
		return domains.SendResult{}, uncertain(e)
	}
	return sent, nil
}
func (c *Client) upload(ctx context.Context, source io.Reader, size int64, name, extension string) (object, error) {
	o, e := c.invoke(ctx, "requestSendFile", object{"file_name": name, "size": size, "mime": extension}, true, "")
	if e != nil {
		return nil, e
	}
	id, dc, endpoint, hash := o.str("id"), o.str("dc_id"), o.str("upload_url"), o.str("access_hash_send")
	if !validMessageID(id) || !dcPattern.MatchString(dc) || !validEndpoint(endpoint, false, c.cfg.HTTPClient != nil) || !validSecret(hash) {
		return nil, protocolError()
	}
	c.mu.RLock()
	auth := c.session.Auth
	c.mu.RUnlock()
	parts := (size + transferChunk - 1) / transferChunk
	buffer := make([]byte, transferChunk)
	var response object
	for part := int64(1); part <= parts; part++ {
		length := min(int64(transferChunk), size-(part-1)*transferChunk)
		if _, e = io.ReadFull(source, buffer[:length]); e != nil {
			return nil, e
		}
		request, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buffer[:length]))
		if e != nil {
			return nil, protocolError()
		}
		request.Header.Set("auth", auth)
		request.Header.Set("file-id", id)
		request.Header.Set("total-part", strconv.FormatInt(parts, 10))
		request.Header.Set("part-number", strconv.FormatInt(part, 10))
		request.Header.Set("chunk-size", strconv.FormatInt(length, 10))
		request.Header.Set("access-hash-send", hash)
		response, e = c.mediaRequest(request)
		if e != nil {
			return nil, e
		}
		if response.str("status") != "OK" || response.str("status_det") != "OK" {
			return nil, protocolError()
		}
	}
	var extra [1]byte
	if n, e := source.Read(extra[:]); n != 0 || e != io.EOF {
		return nil, domains.E("INVALID_MEDIA", "media size changed during upload", 400)
	}
	received := asObject(response["data"]).str("access_hash_rec")
	if !validSecret(received) {
		return nil, protocolError()
	}
	return object{"mime": extension, "size": size, "dc_id": dc, "file_id": id, "file_name": name, "access_hash_rec": received}, nil
}
func validSecret(s string) bool {
	return len(s) > 0 && len(s) <= 2048 && !strings.ContainsAny(s, "\r\n\x00")
}
func (c *Client) mediaRequest(request *http.Request) (object, error) {
	response, e := c.http.Do(request)
	if e != nil {
		return nil, domains.E("PROVIDER_TRANSPORT_ERROR", "Rubika media request did not complete", 502)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, protocolError()
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if e != nil || len(raw) > 64<<10 {
		return nil, protocolError()
	}
	o, e := jsonObject(raw)
	if e != nil {
		return nil, protocolError()
	}
	return o, nil
}
func (c *Client) projectMedia(file object) (*domains.ProviderMedia, *domains.MessageMedia, error) {
	id, dc, hash, size := file.str("file_id"), file.str("dc_id"), file.str("access_hash_rec"), file.num("size")
	if !validMessageID(id) || !dcPattern.MatchString(dc) || !validSecret(hash) || size < 1 || size > c.cfg.MaxMediaBytes {
		return nil, nil, protocolError()
	}
	c.mu.RLock()
	account := c.session.UserID
	c.mu.RUnlock()
	data, _ := json.Marshal(privateMedia{Version: 1, Account: account, DC: dc, ID: id, Hash: hash, Size: size})
	kind := map[string]string{"File": "file", "Image": "image", "Voice": "voice", "Music": "audio", "Video": "video", "Gif": "animation"}[file.str("type")]
	if kind == "" {
		return nil, nil, nil
	}
	name := filepath.Base(file.str("file_name"))
	content := mime.TypeByExtension("." + strings.ToLower(file.str("mime")))
	if content == "" {
		content = "application/octet-stream"
	}
	ref := &domains.ProviderMedia{Provider: domains.ProviderRubika, Version: 1, Data: data, FileID: id, Size: size, Name: name, ContentType: content}
	public := &domains.MessageMedia{Type: kind, FileID: id, Size: size, Name: name, MIMEType: content}
	return ref, public, nil
}

type downloadReader struct {
	c      *Client
	ctx    context.Context
	cancel context.CancelFunc
	ref    privateMedia
	auth   string
	offset int64
	buffer *bytes.Reader
	mu     sync.Mutex
	closed bool
}

func (c *Client) Download(ctx context.Context, ref domains.ProviderMedia) (io.ReadCloser, error) {
	var private privateMedia
	if ref.Provider != domains.ProviderRubika || ref.Version != 1 || len(ref.Data) > 4096 || json.Unmarshal(ref.Data, &private) != nil {
		return nil, protocolError()
	}
	c.mu.RLock()
	account, auth := c.session.UserID, c.session.Auth
	c.mu.RUnlock()
	if private.Version != 1 || private.Account != account || private.ID != ref.FileID || private.Size != ref.Size || private.Size < 1 || private.Size > c.cfg.MaxMediaBytes || !validMessageID(private.ID) || !dcPattern.MatchString(private.DC) || !validSecret(private.Hash) {
		return nil, domains.E("INVALID_MEDIA_REFERENCE", "media reference does not belong to this account", 400)
	}
	downloadCtx, cancel := context.WithCancel(ctx)
	return &downloadReader{c: c, ctx: downloadCtx, cancel: cancel, ref: private, auth: auth}, nil
}
func (r *downloadReader) Close() error {
	r.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.buffer = nil
	return nil
}
func (r *downloadReader) Read(dst []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if len(dst) == 0 {
		return 0, nil
	}
	if r.buffer != nil && r.buffer.Len() > 0 {
		return r.buffer.Read(dst)
	}
	if r.offset >= r.ref.Size {
		return 0, io.EOF
	}
	length := min(int64(transferChunk), r.ref.Size-r.offset)
	endpoint := "https://messenger" + r.ref.DC + ".iranlms.ir/GetFile.ashx"
	request, e := http.NewRequestWithContext(r.ctx, http.MethodPost, endpoint, nil)
	if e != nil {
		return 0, protocolError()
	}
	request.Header.Set("auth", r.auth)
	request.Header.Set("access-hash-rec", r.ref.Hash)
	request.Header.Set("file-id", r.ref.ID)
	request.Header.Set("start-index", strconv.FormatInt(r.offset, 10))
	request.Header.Set("last-index", strconv.FormatInt(r.offset+length-1, 10))
	response, e := r.c.http.Do(request)
	if e != nil {
		return 0, domains.E("PROVIDER_TRANSPORT_ERROR", "Rubika media download failed", 502)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return 0, protocolError()
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, length+1))
	if e != nil || int64(len(raw)) != length {
		return 0, protocolError()
	}
	r.offset += length
	r.buffer = bytes.NewReader(raw)
	return r.buffer.Read(dst)
}

var _ domains.MediaDownloader = (*Client)(nil)

// imageThumbnail derives a small JPEG preview from the actual bounded image.
func imageThumbnail(ctx context.Context, source io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	original, _, err := image.Decode(source)
	if err != nil {
		return "", domains.E("INVALID_MEDIA", "image cannot be decoded", 400)
	}
	return encodeThumbnail(ctx, original)
}
func encodeThumbnail(ctx context.Context, original image.Image) (string, error) {
	raw, err := mediautil.ThumbnailJPEG(ctx, original)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
