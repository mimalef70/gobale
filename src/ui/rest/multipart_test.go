package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

type multipartRESTClient struct {
	testClient
	send func(context.Context, domains.SendRequest) (domains.SendResult, error)
}

func (c *multipartRESTClient) Send(ctx context.Context, r domains.SendRequest) (domains.SendResult, error) {
	return c.send(ctx, r)
}

func multipartRequest(t *testing.T, s *Server, device, route, key, request, data string, fields ...string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	// File first proves that invalid metadata after streaming still gets cleaned.
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="sample.ogg"`)
	h.Set("Content-Type", "audio/ogg")
	p, err := w.CreatePart(h)
	require.NoError(t, err)
	_, err = io.WriteString(p, data)
	require.NoError(t, err)
	require.NoError(t, w.WriteField("request", request))
	for i := 0; i < len(fields); i += 2 {
		require.NoError(t, w.WriteField(fields[i], fields[i+1]))
	}
	require.NoError(t, w.Close())
	r := httptest.NewRequest("POST", route, &body)
	r.SetBasicAuth("test", "password")
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("X-Device-Id", device)
	r.Header.Set("Idempotency-Key", key)
	scopeTestRequest(t, s, r)
	return r
}

func assertUploadFiles(t *testing.T, s *Server, count int) {
	t.Helper()
	files := 0
	err := filepath.WalkDir(s.opts.MediaRoot, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() != ".gobale-media.lock" {
			require.NotContains(t, path, ".gobale-staging", "temporary upload was not cleaned")
			files++
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, count, files)
}

func TestMultipartSendJournalsFileRIDAndRetainsConnectionIdempotency(t *testing.T) {
	var calls atomic.Int32
	var srv *Server
	srv, svc := setupAPIWithFactory(t, "", func(d domains.Device) domains.Client {
		return &multipartRESTClient{send: func(ctx context.Context, r domains.SendRequest) (domains.SendResult, error) {
			// Shared provider media capacity must be free even with one slot.
			select {
			case srv.mediaSlots <- struct{}{}:
				defer func() { <-srv.mediaSlots }()
			case <-ctx.Done():
				return domains.SendResult{}, ctx.Err()
			}
			ops, err := srv.store.ListOperations(ctx, d.ConnectionID, 10, 0)
			require.NoError(t, err)
			require.Len(t, ops, 1)
			require.Equal(t, r.RequestID, ops[0].Request.RequestID)
			require.NotEmpty(t, r.RequestID)
			require.Equal(t, "voice", r.Kind)
			m, err := srv.store.GetMedia(ctx, d.ConnectionID, r.MediaID)
			require.NoError(t, err)
			body, err := os.ReadFile(filepath.Join(srv.opts.MediaRoot, m.Path))
			require.NoError(t, err)
			require.Equal(t, "synthetic bytes", string(body))
			calls.Add(1)
			return domains.SendResult{MessageID: r.RequestID, Date: time.Now()}, nil
		}}
	})
	srv.mediaSlots = make(chan struct{}, 1)
	ctx := context.Background()
	for _, id := range []string{"one", "two"} {
		_, err := svc.CreateDevice(ctx, id)
		require.NoError(t, err)
		ch, err := svc.StartAuth(ctx, id, "+10000000000")
		require.NoError(t, err)
		_, err = svc.SubmitCode(ctx, id, ch.ID, "synthetic")
		require.NoError(t, err)
	}
	request := `{"peer":{"type":"user","id":"42"},"message":"caption","reply_message_id":"-777"}`
	var first domains.Operation
	for _, id := range []string{"one", "one", "two"} {
		r := multipartRequest(t, srv, id, "/send/audio", "same-key", request, "synthetic bytes", "ptt", "true")
		res, err := srv.App.Test(r)
		require.NoError(t, err)
		require.Equal(t, 200, res.StatusCode)
		var response struct {
			Results domains.Operation `json:"results"`
		}
		require.NoError(t, json.NewDecoder(res.Body).Decode(&response))
		res.Body.Close()
		if first.ID == "" {
			first = response.Results
		} else if id == "one" {
			require.Equal(t, first.ID, response.Results.ID)
			require.Equal(t, first.Request.MediaID, response.Results.Request.MediaID)
		} else {
			require.NotEqual(t, first.ID, response.Results.ID)
			require.NotEqual(t, first.Request.MediaID, response.Results.Request.MediaID)
		}
	}
	require.EqualValues(t, 2, calls.Load())
	for _, variant := range []struct{ request, body string }{{request, "changed bytes"}, {strings.Replace(request, "caption", "different", 1), "synthetic bytes"}} {
		res, err := srv.App.Test(multipartRequest(t, srv, "one", "/send/audio", "same-key", variant.request, variant.body, "ptt", "true"))
		require.NoError(t, err)
		require.Equal(t, 409, res.StatusCode)
		res.Body.Close()
	}
	for _, metadata := range [][2]string{{"sample.ogg", "other.ogg"}, {"audio/ogg", "application/ogg"}} {
		r := multipartRequest(t, srv, "one", "/send/audio", "same-key", request, "synthetic bytes", "ptt", "true")
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		data = bytes.Replace(data, []byte(metadata[0]), []byte(metadata[1]), 1)
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
		res, err := srv.App.Test(r)
		require.NoError(t, err)
		require.Equal(t, 409, res.StatusCode)
		res.Body.Close()
	}
	require.EqualValues(t, 2, calls.Load())
	assertUploadFiles(t, srv, 2)
}

func TestMultipartRejectsInvalidInputsWithoutRetainingUploads(t *testing.T) {
	srv, svc := setupAPI(t, "")
	d, err := svc.CreateDevice(context.Background(), "one")
	require.NoError(t, err)
	valid := `{"peer":{"type":"user","id":"42"}}`
	cases := []struct {
		name, request, data, route string
		fields                     []string
		status                     int
	}{
		{"oversize", valid, strings.Repeat("x", 1025), "/send/file", nil, 413},
		{"empty", valid, "", "/send/file", nil, 400},
		{"bad json", "{", "bytes", "/send/file", nil, 400},
		{"duplicate json", `{"peer":{"type":"user","id":"42","id":"43"}}`, "bytes", "/send/file", nil, 400},
		{"wrong peer", `{"peer":{"type":"user","id":"bad"}}`, "bytes", "/send/file", nil, 400},
		{"provided media", `{"peer":{"type":"user","id":"42"},"media_id":"other"}`, "bytes", "/send/file", nil, 400},
		{"schedule", `{"peer":{"type":"user","id":"42"},"scheduled_at":"2030-01-01T00:00:00Z","timezone":"UTC"}`, "bytes", "/send/file", nil, 400},
		{"unknown part", valid, "bytes", "/send/file", []string{"unknown", "value"}, 400},
		{"duplicate part", valid, "bytes", "/send/file", []string{"request", valid}, 400},
		{"invalid ptt", valid, "bytes", "/send/audio", []string{"ptt", "yes"}, 400},
		{"ptt file", valid, "bytes", "/send/file", []string{"ptt", "true"}, 400},
		{"text route", valid, "bytes", "/send/message", nil, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := srv.App.Test(multipartRequest(t, srv, "one", tc.route, tc.name, tc.request, tc.data, tc.fields...))
			require.NoError(t, err)
			require.Equal(t, tc.status, res.StatusCode)
			res.Body.Close()
			assertUploadFiles(t, srv, 0)
		})
	}
	for _, header := range []string{"Idempotency-Key", "X-Device-Instance"} {
		r := multipartRequest(t, srv, "one", "/send/file", "guard", valid, "bytes")
		r.Header.Del(header)
		res, err := srv.App.Test(r)
		require.NoError(t, err)
		require.GreaterOrEqual(t, res.StatusCode, 400)
		res.Body.Close()
	}
	ops, err := srv.store.ListOperations(context.Background(), d.ConnectionID, 10, 0)
	require.NoError(t, err)
	require.Empty(t, ops)
	assertUploadFiles(t, srv, 0)
}
