// Package domains contains transport-independent GoBale contracts.
package domains

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mimalef70/gobale/src/domains/send"
	"time"
)

type Error struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	HTTP              int    `json:"-"`
	Ambiguous         bool   `json:"-"`
	Retryable         bool   `json:"-"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
}

func (e *Error) Error() string { return e.Message }
func E(code, message string, status int) error {
	return &Error{Code: code, Message: message, HTTP: status}
}
func Unsupported(feature string) error {
	return E("FEATURE_NOT_SUPPORTED", feature+" is not verified for this provider", 501)
}

type Peer struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	AccessHash string `json:"-"`
}

func (p Peer) Key() string { return p.Type + ":" + p.ID }
func (p Peer) Validate() error {
	if p.Type != "user" && p.Type != "group" && p.Type != "channel" {
		return E("INVALID_PEER", "peer.type must be user, group or channel", 400)
	}
	if p.ID == "" {
		return E("INVALID_PEER", "peer.id is required", 400)
	}
	for _, c := range p.ID {
		if c < '0' || c > '9' {
			return E("INVALID_PEER", "peer.id must be a decimal string", 400)
		}
	}
	return nil
}

type WebhookConfig struct {
	URL      string        `json:"webhook_url"`
	Secret   string        `json:"-"`
	Events   []string      `json:"webhook_events"`
	Revision int64         `json:"revision"`
	Filter   WebhookFilter `json:"webhook_filter"`
}
type WebhookPatch struct {
	URL    *string        `json:"webhook_url"`
	Secret *string        `json:"webhook_secret"`
	Events *[]string      `json:"webhook_events"`
	Filter *WebhookFilter `json:"webhook_filter"`
}
type Device struct {
	ID           string        `json:"id"`
	ConnectionID string        `json:"-"`
	InstanceID   string        `json:"instance_id"`
	AccountID    string        `json:"account_id,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	Webhook      WebhookConfig `json:"webhook"`
}
type Challenge struct {
	ID                     string    `json:"challenge_id"`
	State                  string    `json:"state"`
	ExpiresAt              time.Time `json:"expires_at,omitempty"`
	SentCodeType           int32     `json:"sent_code_type,omitempty"`
	NextSendCodeType       int32     `json:"next_send_code_type,omitempty"`
	ResendAfterSeconds     *int64    `json:"resend_after_seconds,omitempty"`
	AvailableSendCodeTypes []int32   `json:"available_send_code_types,omitempty"`
}

// Session is private persistence data; never return it directly from REST.
type Session struct {
	UserID     string          `json:"user_id"`
	Token      string          `json:"token"`
	DeviceHash string          `json:"device_hash"`
	Phone      string          `json:"phone,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}
type ConnectionStatus struct {
	Auth      string `json:"auth"`
	Transport string `json:"transport"`
	Recovery  string `json:"recovery"`
	LastError string `json:"last_error,omitempty"`
}
type SendRequest struct {
	Operation      string          `json:"operation,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Peer           Peer            `json:"peer"`
	Phone          string          `json:"phone,omitempty"`
	Kind           string          `json:"kind,omitempty"`
	Text           string          `json:"message,omitempty"`
	Mentions       []string        `json:"mentions,omitempty"`
	ReplyMessageID string          `json:"reply_message_id,omitempty"`
	MediaID        string          `json:"media_id,omitempty"`
	RequestID      string          `json:"request_id,omitempty"`
	send.ScheduleOptions
}
type SendResult struct {
	Data      json.RawMessage `json:"data,omitempty"`
	MessageID string          `json:"message_id,omitempty"`
	Date      time.Time       `json:"date,omitzero"`
}
type Event struct {
	ID         string          `json:"event_id"`
	Type       string          `json:"event"`
	AccountID  string          `json:"device_id"`
	SessionID  string          `json:"session_id"`
	InstanceID string          `json:"instance_id,omitempty"`
	Peer       Peer            `json:"peer"`
	MessageID  string          `json:"message_id,omitempty"`
	SenderID   string          `json:"sender_id,omitempty"`
	Direction  string          `json:"direction,omitempty"`
	Time       time.Time       `json:"timestamp"`
	Payload    json.RawMessage `json:"payload"`
	Checkpoint string          `json:"-"`
	Media      *ProviderMedia  `json:"-"`
}
type Sink func(context.Context, Event) error
type Client interface {
	StartAuth(context.Context, string) (Challenge, error)
	SubmitCode(context.Context, string, string) (*Session, error)
	SubmitPassword(context.Context, string, string) (*Session, error)
	Connect(context.Context, *Session, Sink) error
	Disconnect(context.Context) error
	Logout(context.Context) error
	Status() ConnectionStatus
	Send(context.Context, SendRequest) (SendResult, error)
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
}
type ClientFactory func(Device) Client
type Operation struct {
	ID             string      `json:"send_id"`
	ScheduleID     string      `json:"schedule_id,omitempty"`
	ScheduledFor   *time.Time  `json:"scheduled_for,omitempty"`
	ConnectionID   string      `json:"-"`
	DeviceID       string      `json:"device_id"`
	Request        SendRequest `json:"request"`
	IdempotencyKey string      `json:"-"`
	PayloadHash    string      `json:"-"`
	State          string      `json:"state"`
	Result         *SendResult `json:"result,omitempty"`
	ErrorCode      string      `json:"error_code,omitempty"`
	ErrorMessage   string      `json:"error_message,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}
type Delivery struct {
	Device       bool            `json:"-"`
	ID           string          `json:"delivery_id"`
	EventID      string          `json:"event_id"`
	ConnectionID string          `json:"-"`
	DeviceID     string          `json:"device_id"`
	URL          string          `json:"url"`
	Secret       string          `json:"-"`
	Revision     int64           `json:"revision"`
	Body         json.RawMessage `json:"payload"`
	State        string          `json:"state"`
	Attempts     int             `json:"attempts"`
	NextAt       time.Time       `json:"next_attempt_at"`
	LastError    string          `json:"last_error,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}
type Schedule struct {
	ID                        string      `json:"id"`
	ConnectionID              string      `json:"-"`
	DeviceID                  string      `json:"device_id"`
	Request                   SendRequest `json:"request"`
	State                     string      `json:"status"`
	NextAt                    time.Time   `json:"next_run_at"`
	Count                     int         `json:"occurrence_count"`
	OccurrenceHistoryComplete bool        `json:"occurrence_history_complete"`
	CreatedAt                 time.Time   `json:"created_at"`
}
type Media struct {
	ID           string    `json:"id"`
	ConnectionID string    `json:"-"`
	Name         string    `json:"name"`
	ContentType  string    `json:"content_type"`
	Size         int64     `json:"size"`
	Path         string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

func (r SendRequest) Validate() error {
	if err := ValidateMentions(r.Text, r.Mentions); err != nil {
		return err
	}
	if r.Phone != "" && (r.Peer.Type != "" || r.Peer.ID != "") {
		return E("INVALID_REQUEST", "provide exactly one destination: peer or phone", 400)
	}
	if r.Phone == "" {
		if err := r.Peer.Validate(); err != nil {
			return err
		}
	}
	// Media captions share the same bounded message field, including voice and
	// scheduled sends. Validate before either path can persist an oversized body.
	if len(r.Text) > 65536 {
		return E("INVALID_REQUEST", "message exceeds 64 KiB", 400)
	}
	if r.Kind == "" || r.Kind == "text" {
		if r.Text == "" {
			return E("INVALID_REQUEST", "message is required", 400)
		}
	} else if r.MediaID == "" {
		return E("INVALID_REQUEST", fmt.Sprintf("media_id is required for %s", r.Kind), 400)
	}
	return nil
}
