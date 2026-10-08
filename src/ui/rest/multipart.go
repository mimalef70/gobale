package rest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/mediafile"
)

const multipartRequestLimit = 128 << 10
const multipartOverheadLimit = 16 << 10

func isMultipartRequest(c fiber.Ctx) bool {
	return strings.HasPrefix(strings.ToLower(c.Get("Content-Type")), "multipart/")
}

// Multipart keeps the existing SendRequest contract in one JSON request part.
// The independently bounded file is streamed to an owned local staging file.
func (s *Server) sendMultipart(c fiber.Ctx, d domains.Device, kind, key string) error {
	switch kind {
	case "file", "image", "audio", "video", "voice":
	default:
		return domains.E("INVALID_REQUEST", "multipart sends require a media endpoint", 400)
	}
	contentType, params, err := mime.ParseMediaType(c.Get("Content-Type"))
	if err != nil || contentType != "multipart/form-data" || params["boundary"] == "" || len(params["boundary"]) > 70 {
		return domains.E("INVALID_MULTIPART", "a valid multipart/form-data boundary is required", 400)
	}
	select {
	case s.mediaSlots <- struct{}{}:
	default:
		return domains.E("MEDIA_BUSY", "media transfer capacity reached", 503)
	}
	slotHeld := true
	defer func() {
		if slotHeld {
			<-s.mediaSlots
		}
	}()
	var body io.Reader = c.Request().BodyStream()
	if body == nil {
		body = bytes.NewReader(c.Body())
	}
	limited := &io.LimitedReader{R: body, N: s.opts.MaxMediaBytes + multipartRequestLimit + multipartOverheadLimit + 1}
	reader := multipart.NewReader(limited, params["boundary"])
	var upload *mediafile.Upload
	defer func() {
		if upload != nil {
			upload.Release()
		}
	}()
	var req domains.SendRequest
	var media domains.Media
	var digest string
	seen := make(map[string]bool)
	ptt := false
	for count := 0; ; count++ {
		part, e := reader.NextRawPart()
		if e == io.EOF {
			break
		}
		if e != nil || count >= 3 {
			return domains.E("INVALID_MULTIPART", "multipart body is malformed or contains too many parts", 400)
		}
		name := part.FormName()
		if seen[name] || (name != "request" && name != "file" && name != "ptt") || part.Header.Get("Content-Transfer-Encoding") != "" {
			return domains.E("INVALID_MULTIPART", "unknown, duplicate or encoded multipart field", 400)
		}
		seen[name] = true
		if name != "file" && part.FileName() != "" {
			return domains.E("INVALID_MULTIPART", "only the file part may have a filename", 400)
		}
		switch name {
		case "request":
			data, e := io.ReadAll(io.LimitReader(part, multipartRequestLimit+1))
			if e != nil || len(data) > multipartRequestLimit {
				return domains.E("INVALID_MULTIPART", "request part exceeds 128 KiB or is incomplete", 400)
			}
			if e = domains.ValidateJSONObject(data); e != nil {
				return e
			}
			var fields map[string]json.RawMessage
			if e = json.Unmarshal(data, &fields); e != nil {
				return domains.E("INVALID_REQUEST", "request part must be a JSON object", 400)
			}
			for field := range fields {
				switch field {
				case "peer", "phone", "message", "mentions", "reply_message_id":
				case "scheduled_at", "timezone", "recurrence", "cron", "interval", "schedule_id":
					return domains.E("USE_SCHEDULE_ENDPOINT", "upload media separately and use the schedule endpoint for delayed sends", 400)
				default:
					return domains.E("INVALID_REQUEST", "multipart request contains an unsupported field", 400)
				}
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF {
				return domains.E("INVALID_REQUEST", "request part must be one JSON send request", 400)
			}
		case "ptt":
			data, e := io.ReadAll(io.LimitReader(part, 6))
			if e != nil || kind != "audio" || (string(data) != "true" && string(data) != "false") {
				return domains.E("INVALID_REQUEST", "ptt must be true or false and is only valid for audio sends", 400)
			}
			ptt = string(data) == "true"
		case "file":
			filename := filepath.Base(part.FileName())
			if filename == "" || filename == "." || len(filename) > 255 || strings.ContainsFunc(filename, unicode.IsControl) {
				return domains.E("INVALID_MEDIA", "file part requires a valid filename of at most 255 bytes", 400)
			}
			fileType := part.Header.Get("Content-Type")
			if fileType == "" {
				fileType = "application/octet-stream"
			}
			mediaType, mediaParams, e := mime.ParseMediaType(fileType)
			if e != nil {
				return domains.E("INVALID_MEDIA", "file content type is invalid", 400)
			}
			manager, e := s.uploadManager()
			if e != nil {
				return e
			}
			upload, e = manager.Begin(c.Context(), d.ConnectionID)
			if e != nil {
				return domains.E("MEDIA_UPLOAD_FAILED", "media staging is unavailable", 503)
			}
			hash := sha256.New()
			n, e := io.Copy(io.MultiWriter(upload.File, hash), io.LimitReader(part, s.opts.MaxMediaBytes+1))
			if n > s.opts.MaxMediaBytes {
				return domains.E("MEDIA_TOO_LARGE", "media exceeds configured limit", 413)
			}
			if e != nil || n == 0 {
				return domains.E("MEDIA_UPLOAD_FAILED", "file is empty or incomplete", 400)
			}
			digest = hex.EncodeToString(hash.Sum(nil))
			media = domains.Media{ID: upload.ID(), ConnectionID: d.ConnectionID, Path: upload.RelativePath(), Name: filename, ContentType: mime.FormatMediaType(mediaType, mediaParams), Size: n}
		}
		if e := part.Close(); e != nil {
			return domains.E("INVALID_MULTIPART", "multipart body is incomplete", 400)
		}
	}
	if limited.N <= 0 || !seen["request"] || !seen["file"] {
		return domains.E("INVALID_MULTIPART", "one request part and one bounded file part are required", 400)
	}
	if req.MediaID != "" || req.RequestID != "" || req.Operation != "" || len(req.Payload) > 0 {
		return domains.E("INVALID_REQUEST", "multipart sends assign media_id and request_id inside the gateway", 400)
	}
	if req.IsScheduled() {
		return domains.E("USE_SCHEDULE_ENDPOINT", "upload media separately and use the schedule endpoint for delayed sends", 400)
	}
	req.Kind = kind
	if ptt {
		req.Kind = "voice"
	}
	req.MediaID = media.ID
	if err = req.Validate(); err != nil {
		return err
	}
	if err = upload.Publish(); err != nil {
		return domains.E("MEDIA_UPLOAD_FAILED", "media could not be persisted", 503)
	}
	// Provider uploads share these slots. Release our completed local transfer
	// before making work visible, including when only one media slot is configured.
	<-s.mediaSlots
	slotHeld = false
	op, err := s.service.SendUpload(c.Context(), d.ID, req, key, media, digest)
	if err != nil {
		return err
	}
	// Release before polling: retries have no registration, while accepted work
	// protects its original asset through the atomic transaction and media pins.
	upload.Release()
	return s.awaitOperation(c, d.ID, op)
}
