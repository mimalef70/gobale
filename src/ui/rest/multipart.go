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
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/mediafile"
)

const multipartRequestLimit = 128 << 10
const multipartOverheadLimit = 16 << 10

func isMultipartRequest(c fiber.Ctx) bool {
	return strings.HasPrefix(strings.ToLower(c.Get("Content-Type")), "multipart/")
}

// Multipart accepts ordinary form fields and an endpoint-named media part.
// The bounded file is streamed to owned local staging storage.
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
	release, err := s.acquireMedia(c.Context(), d)
	if err != nil {
		return err
	}
	defer release()
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
	fields := make(map[string]json.RawMessage)
	metadataBytes := 0
	fileField := kind
	if kind == "voice" {
		fileField = "audio"
	}
	for count := 0; ; count++ {
		part, e := reader.NextRawPart()
		if e == io.EOF {
			break
		}
		if e != nil || count >= 20 {
			return domains.E("INVALID_MULTIPART", "multipart body is malformed or contains too many parts", 400)
		}
		name := part.FormName()
		if seen[name] || name == "" || part.Header.Get("Content-Transfer-Encoding") != "" {
			return domains.E("INVALID_MULTIPART", "unknown, duplicate or encoded multipart field", 400)
		}
		seen[name] = true
		if name != fileField && part.FileName() != "" {
			return domains.E("INVALID_MULTIPART", "only the file part may have a filename", 400)
		}
		if name == fileField {
			filename := filepath.Base(part.FileName())
			if filename == "" || filename == "." || len(filename) > 255 || !utf8.ValidString(filename) || strings.ContainsFunc(filename, unicode.IsControl) {
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
		} else {
			data, e := io.ReadAll(io.LimitReader(part, int64(multipartRequestLimit-metadataBytes+1)))
			metadataBytes += len(data)
			if e != nil || metadataBytes > multipartRequestLimit || !utf8.Valid(data) {
				return domains.E("INVALID_MULTIPART", "form fields must be valid UTF-8 within 128 KiB", 400)
			}
			switch name {
			case "peer", "mentions", "weekdays", "ptt", "day_of_month", "occurrence_limit":
				// Structured fields and scalar booleans/numbers use JSON spelling.
				if domains.ValidateJSONObject(append(append([]byte(`{"value":`), data...), '}')) != nil {
					return domains.E("INVALID_MULTIPART", "form field contains invalid or duplicate JSON values", 400)
				}
				fields[name] = data
			default:
				fields[name], _ = json.Marshal(string(data))
			}

		}
		if e := part.Close(); e != nil {
			return domains.E("INVALID_MULTIPART", "multipart body is incomplete", 400)
		}
	}
	if limited.N <= 0 || !seen[fileField] {
		return domains.E("INVALID_MULTIPART", "one endpoint-named media part and destination fields are required", 400)
	}
	if _, ok := fields["media_id"]; ok {
		return domains.E("INVALID_REQUEST", "multipart assigns media_id inside the gateway", 400)
	}
	req, err = sendRequestFields(fields, kind)
	if err != nil {
		return err
	}
	req.MediaID = media.ID
	if err = s.service.ValidateSend(c.Context(), d.ID, req); err != nil {
		return err
	}
	if err = upload.Publish(); err != nil {
		return domains.E("MEDIA_UPLOAD_FAILED", "media could not be persisted", 503)
	}
	// Provider uploads share these slots. Release our completed local transfer
	// before making work visible, including when only one media slot is configured.
	release()
	if req.IsScheduled() {
		job, err := s.service.ScheduleUpload(c.Context(), d.ID, req, key, media, digest)
		if err != nil {
			return err
		}
		upload.Release()
		return success(c, scheduledSendResponse(job))
	}
	op, err := s.service.SendUpload(c.Context(), d.ID, req, key, media, digest)
	if err != nil {
		return err
	}
	// Release before polling: retries have no registration, while accepted work
	// protects its original asset through the atomic transaction and media pins.
	upload.Release()
	return s.awaitOperation(c, d.ID, op)
}
