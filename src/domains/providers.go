package domains

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Provider is immutable once a connection has been provisioned.
type Provider string

const (
	ProviderBale   Provider = "bale"
	ProviderEitaa  Provider = "eitaa"
	ProviderRubika Provider = "rubika"
)

func (p Provider) Validate() error {
	switch p {
	case ProviderBale, ProviderEitaa, ProviderRubika:
		return nil
	}
	return E("INVALID_PROVIDER", "provider must be bale, eitaa or rubika", 400)
}
func ValidOpaqueID(id string) bool {
	if id == "" || len(id) > 256 || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ProviderSendCapabilities describes admission for ordinary text/media routes.
// Text limits also apply to captions. Format notes do not establish provider
// acceptance or live interoperability; native validation remains authoritative.
type ProviderSendCapabilities struct {
	Kinds             []string                 `json:"kinds"`
	MaxTextBytes      int                      `json:"max_text_bytes,omitempty"`
	MaxTextCharacters int                      `json:"max_text_characters,omitempty"`
	MentionsSupported bool                     `json:"mentions_supported"`
	MaxMentions       int                      `json:"max_mentions"`
	ReplySupported    bool                     `json:"reply_supported"`
	MediaFormatNotes  ProviderMediaFormatNotes `json:"media_format_notes"`
}

type ProviderMediaFormatNotes struct {
	Voice string `json:"voice,omitempty"`
	Audio string `json:"audio,omitempty"`
	Video string `json:"video,omitempty"`
}

// OperationPreset selects a reviewed public operation, not a provider RPC.
// Merge its parameters with the selected peer where required, then use the
// ordinary guarded/idempotent operation contract. Availability is not delivery.
type OperationPreset struct {
	Operation  string          `json:"operation"`
	Parameters json.RawMessage `json:"parameters"`
}

// ProviderInteractions exposes differing semantics without inferring receipts
// or claiming that socket connectivity establishes inbox completeness.
type ProviderInteractions struct {
	Typing          *OperationPreset `json:"typing,omitempty"`
	TypingStop      *OperationPreset `json:"typing_stop,omitempty"`
	Online          *OperationPreset `json:"online,omitempty"`
	Offline         *OperationPreset `json:"offline,omitempty"`
	ReadOperation   string           `json:"read_operation,omitempty"`
	ReadArgument    string           `json:"read_argument,omitempty"`
	ReceiptModel    string           `json:"receipt_model"`
	ReadEvents      bool             `json:"read_events"`
	DeliveredEvents bool             `json:"delivered_events"`
	SenderNames     string           `json:"sender_names"`
	AvatarDownload  bool             `json:"avatar_download"`
	PartialEdits    bool             `json:"partial_edits"`
}

type ProviderDescriptor struct {
	ID              Provider                 `json:"id"`
	Name            string                   `json:"name"`
	Enabled         bool                     `json:"enabled"`
	Verification    string                   `json:"verification"`
	DeliveryMethods []string                 `json:"delivery_methods"`
	Send            ProviderSendCapabilities `json:"send"`
	Interactions    ProviderInteractions     `json:"interactions"`
}

// ProviderContract is the admission boundary. Validation is performed before
// durable acceptance, independently of whether the account is online.
type ProviderContract interface {
	Descriptor() ProviderDescriptor
	Operations() []OperationContract
	ValidateSend(SendRequest) error
	NormalizeOperation(string, json.RawMessage) (json.RawMessage, Peer, error)
	ValidatePeer(Peer) error
	ValidateUserID(string) bool
	ValidateSession(*Session) error
}

// BatchSink durably accepts a complete, bounded provider update transaction.
type BatchSink func(context.Context, EventBatch) error
type BatchClient interface {
	ConnectBatch(context.Context, *Session, BatchSink) error
}
type SessionPersister func(context.Context, *Session) error
type AuthChallengeSource interface{ CurrentChallenge() Challenge }
type SessionPersistenceClient interface{ SetSessionPersister(SessionPersister) }
type MessageIdentityContract interface{ ValidateMessageID(string) bool }

// SenderIdentityContract validates actors independently of login account IDs.
type SenderIdentityContract interface{ ValidateSenderID(string) bool }
type PeerTypeContract interface{ SupportsPeerType(string) bool }

func ValidProviderPeerType(c ProviderContract, kind string) bool {
	if types, ok := c.(PeerTypeContract); ok {
		return types.SupportsPeerType(kind)
	}
	return kind == "user" || kind == "group" || kind == "channel"
}

func ValidProviderSender(c ProviderContract, id string) bool {
	if actors, ok := c.(SenderIdentityContract); ok {
		return actors.ValidateSenderID(id)
	}
	return c.ValidateUserID(id)
}

// Registry contains immutable registrations, assembled before Service.Start.
type ProviderRegistration struct {
	Contract ProviderContract
	Factory  ClientFactory
}
type ProviderRegistry struct {
	registrations map[Provider]ProviderRegistration
}

func NewProviderRegistry(registrations ...ProviderRegistration) (*ProviderRegistry, error) {
	r := &ProviderRegistry{registrations: make(map[Provider]ProviderRegistration)}
	for _, registration := range registrations {
		if registration.Contract == nil {
			return nil, E("SERVICE_CONFIGURATION", "provider contract is required", 500)
		}
		d := registration.Contract.Descriptor()
		if d.ID.Validate() != nil || r.registrations[d.ID].Contract != nil {
			return nil, E("SERVICE_CONFIGURATION", "invalid provider registration", 500)
		}
		r.registrations[d.ID] = registration
	}
	return r, nil
}
func (r *ProviderRegistry) Get(p Provider) (ProviderRegistration, error) {
	if err := p.Validate(); err != nil {
		return ProviderRegistration{}, err
	}
	if r != nil {
		if v, ok := r.registrations[p]; ok {
			return v, nil
		}
	}
	return ProviderRegistration{}, Unsupported(string(p))
}
func (r *ProviderRegistry) List() []ProviderDescriptor {
	result := make([]ProviderDescriptor, 0, 3)
	for _, p := range []Provider{ProviderBale, ProviderEitaa, ProviderRubika} {
		if v, err := r.Get(p); err == nil {
			result = append(result, v.Contract.Descriptor())
		} else {
			names := map[Provider]string{ProviderBale: "Bale", ProviderEitaa: "Eitaa", ProviderRubika: "Rubika"}
			result = append(result, ProviderDescriptor{ID: p, Name: names[p], Enabled: false, Verification: "pending", DeliveryMethods: []string{}, Send: ProviderSendCapabilities{Kinds: []string{}}, Interactions: ProviderInteractions{ReceiptModel: "none", SenderNames: "unavailable"}})
		}
	}
	return result
}
