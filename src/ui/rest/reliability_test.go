package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

func reliabilityApp(s *Server) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: s.handleError})
	app.Use(s.recoverRequest, s.requestDeadline)
	return app
}

func TestRequestPanicKeepsDurableSendAndServerAvailable(t *testing.T) {
	s, svc := setupAPI(t, "")
	d, err := svc.CreateDevice(context.Background(), "panic")
	require.NoError(t, err)
	app := reliabilityApp(s)
	var saved domains.Operation
	app.Post("/panic", func(c fiber.Ctx) error {
		var err error
		saved, err = svc.Send(c.Context(), d.ID, domains.SendRequest{Kind: "text", Peer: domains.Peer{Type: "user", ID: "123"}, Text: "synthetic body"}, "durable-key")
		if err != nil {
			return err
		}
		panic("private-token-and-message")
	})
	app.Get("/healthy", func(c fiber.Ctx) error { return c.SendString("healthy") })
	response, err := app.Test(httptest.NewRequest("POST", "/panic", nil))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 500, response.StatusCode)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(raw), "INTERNAL_SERVER_ERROR")
	require.NotContains(t, string(raw), "private-token")
	require.NotEmpty(t, saved.ID)
	op, err := svc.GetOperation(context.Background(), d.ID, saved.ID)
	require.NoError(t, err)
	require.Contains(t, []string{"queued", "sending"}, op.State)
	require.Equal(t, saved.Request.RequestID, op.Request.RequestID)
	response, err = app.Test(httptest.NewRequest("GET", "/healthy", nil))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	require.EqualValues(t, 1, s.operational.panics.Load())
}

func TestRequestDeadlineAndAcceptedSendRemainDistinct(t *testing.T) {
	s, svc := setupAPI(t, "")
	s.opts.RequestTimeout = 100 * time.Millisecond
	d, err := svc.CreateDevice(context.Background(), "deadline")
	require.NoError(t, err)
	app := reliabilityApp(s)
	app.Get("/slow", func(c fiber.Ctx) error { <-c.Context().Done(); return c.Context().Err() })
	var acceptedID string
	app.Post("/accepted", func(c fiber.Ctx) error {
		op, err := svc.Send(c.Context(), d.ID, domains.SendRequest{Kind: "text", Peer: domains.Peer{Type: "user", ID: "123"}, Text: "synthetic"}, "timeout-key")
		if err != nil {
			return err
		}
		acceptedID = op.ID
		return s.awaitOperation(c, d.ID, op)
	})
	res, err := app.Test(httptest.NewRequest("GET", "/slow", nil))
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 504, res.StatusCode)
	res, err = app.Test(httptest.NewRequest("POST", "/accepted", nil))
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 202, res.StatusCode)
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Contains(t, string(raw), "ACCEPTED")
	require.NotContains(t, string(raw), "REQUEST_TIMEOUT")
	var response struct {
		Results domains.Operation `json:"results"`
	}
	require.NoError(t, json.Unmarshal(raw, &response))
	require.Equal(t, acceptedID, response.Results.ID)
	require.NotEmpty(t, response.Results.Request.RequestID)
	require.Contains(t, []string{"queued", "sending"}, response.Results.State)
	require.EqualValues(t, 2, s.operational.timeouts.Load())
}

type contextReader struct {
	context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.Context.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func TestStreamingResponseDoesNotInheritHandlerDeadline(t *testing.T) {
	s := &Server{opts: Options{BasePath: "/gateway", RequestTimeout: time.Nanosecond}}
	app := reliabilityApp(s)
	app.Get("/gateway/media/:id", func(c fiber.Ctx) error {
		_, hasDeadline := c.Context().Deadline()
		require.False(t, hasDeadline)
		return c.SendStream(contextReader{c.Context(), strings.NewReader("synthetic-stream")})
	})
	res, err := app.Test(httptest.NewRequest("GET", "/GATEWAY/MEDIA/fixture/", nil))
	require.NoError(t, err)
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, "synthetic-stream", string(raw))
}
