package domains

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// InstanceToken identifies a connection without publishing the storage key.
// It remains stable across restarts and changes if a deleted alias is reused.
func (d Device) InstanceToken() string {
	if d.ConnectionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("gobale:device-instance:v1\x00" + d.ConnectionID))
	return hex.EncodeToString(sum[:])
}

type DeliveryCounts struct {
	Pending int64 `json:"pending"`
	Failed  int64 `json:"failed"`
	Paused  int64 `json:"paused"`
}

type DeviceOverview struct {
	Device
	Status     ConnectionStatus `json:"status"`
	Deliveries DeliveryCounts   `json:"deliveries"`
}

type DevicesOverview struct {
	ServerTime time.Time        `json:"server_time"`
	Devices    []DeviceOverview `json:"devices"`
}

// PublicChallenge contains only local metadata safe for an authenticated admin.
// Provider transaction hashes and complete phone numbers never cross this API.
type PublicChallenge struct {
	Delivery               string    `json:"delivery"`
	NextDelivery           string    `json:"next_delivery,omitempty"`
	AvailableDeliveries    []string  `json:"available_deliveries"`
	ID                     string    `json:"challenge_id"`
	ExpiresAt              time.Time `json:"expires_at"`
	ResendAvailableAt      time.Time `json:"resend_available_at"`
	SentCodeType           int32     `json:"-"`
	NextSendCodeType       int32     `json:"-"`
	AvailableSendCodeTypes []int32   `json:"-"`
	MaskedPhone            string    `json:"masked_phone"`
}

type LoginState struct {
	State      string           `json:"state"`
	Challenge  *PublicChallenge `json:"challenge"`
	ServerTime time.Time        `json:"server_time"`
}

// DeviceStatus is a local snapshot for one immutable account connection.
// Transport/authentication state alone cannot establish inbox synchronization.
type DeviceStatus struct {
	Provider Provider `json:"provider"`
	ConnectionStatus
	DeviceID   string           `json:"device_id"`
	InstanceID string           `json:"instance_id"`
	AccountID  string           `json:"account_id"`
	Challenge  *PublicChallenge `json:"challenge"`
	ServerTime time.Time        `json:"server_time"`
}

type WebhookRoutingRule struct {
	Source           string        `json:"source"`
	URL              string        `json:"url"`
	Events           []string      `json:"events"`
	Filter           WebhookFilter `json:"filter"`
	SecretConfigured bool          `json:"secret_configured"`
}

type WebhookDetails struct {
	WebhookConfig
	SecretConfigured bool                 `json:"secret_configured"`
	RoutingMode      string               `json:"routing_mode"`
	RoutingRules     []WebhookRoutingRule `json:"routing_rules"`
}
