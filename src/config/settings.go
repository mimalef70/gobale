// Package config defines application settings and Cobra/Viper flag precedence.
package config

import (
	"encoding/base64"
	"fmt"
	"github.com/spf13/viper"
	"net/url"
	"os"
	"strings"
	"time"
)

const AppVersion = "2.0.0"

type Settings struct {
	UIEnabled                                                                                     bool
	UIPublicOrigin                                                                                string
	Host                                                                                          string
	Port                                                                                          int
	BasicAuth, BasePath, Database, MediaRoot                                                      string
	MasterKey                                                                                     []byte
	GRPCEndpoint, WebSocketEndpoint, Origin, APIKey, DeviceTitle                                  string
	AppID, APIVersion                                                                             uint32
	Webhooks                                                                                      []string
	WebhookSecret                                                                                 string
	WebhookMergeGlobal                                                                            bool
	MaxMediaBytes                                                                                 int64
	SendWait                                                                                      time.Duration
	SendWorkers, WebhookWorkers, ReconnectWorkers, MediaWorkers, QueueLimit, ConnectionQueueLimit int
}

func Load(v *viper.Viper) (Settings, error) {
	s := Settings{Host: v.GetString("host"), Port: v.GetInt("port"), BasicAuth: v.GetString("basic-auth"), BasePath: v.GetString("base-path"), Database: v.GetString("database"), MediaRoot: v.GetString("media-root"), GRPCEndpoint: v.GetString("grpc-endpoint"), WebSocketEndpoint: v.GetString("ws-endpoint"), Origin: "https://web.bale.ai", APIKey: v.GetString("bale-api-key"), AppID: v.GetUint32("bale-app-id"), APIVersion: v.GetUint32("bale-api-version"), DeviceTitle: "GoBale", WebhookSecret: v.GetString("webhook-secret"), WebhookMergeGlobal: v.GetBool("webhook-device-merge-global"), MaxMediaBytes: v.GetInt64("max-media-bytes"), SendWait: 40 * time.Second}
	s.UIEnabled = true
	if v.IsSet("ui-enabled") {
		s.UIEnabled = v.GetBool("ui-enabled")
	}
	s.UIPublicOrigin = strings.TrimSpace(v.GetString("ui-public-origin"))
	if s.UIPublicOrigin != "" {
		u, err := url.Parse(s.UIPublicOrigin)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return s, fmt.Errorf("APP_UI_PUBLIC_ORIGIN must be an HTTPS origin without path, query or credentials")
		}
	}
	for _, option := range []struct {
		name          string
		value         *int
		fallback, max int
	}{
		{"send-workers", &s.SendWorkers, 4, 64}, {"webhook-workers", &s.WebhookWorkers, 8, 64}, {"reconnect-workers", &s.ReconnectWorkers, 4, 4}, {"media-workers", &s.MediaWorkers, 4, 64}, {"queue-limit", &s.QueueLimit, 1000, 100000}, {"connection-queue-limit", &s.ConnectionQueueLimit, 100, 100000},
	} {
		*option.value = option.fallback
		if v.IsSet(option.name) {
			*option.value = v.GetInt(option.name)
		}
		if *option.value < 1 || *option.value > option.max {
			return s, fmt.Errorf("%s must be between 1 and %d", option.name, option.max)
		}
	}
	raw := strings.TrimSpace(v.GetString("master-key"))
	if file := v.GetString("master-key-file"); file != "" {
		info, e := os.Stat(file)
		if e != nil {
			return s, fmt.Errorf("read master key file: %w", e)
		}
		if info.Mode().Perm()&0077 != 0 {
			return s, fmt.Errorf("master key file must be private (chmod 600)")
		}
		b, e := os.ReadFile(file)
		if e != nil {
			return s, e
		}
		raw = strings.TrimSpace(string(b))
	}
	var e error
	s.MasterKey, e = base64.StdEncoding.DecodeString(raw)
	if e != nil || len(s.MasterKey) != 32 {
		return s, fmt.Errorf("APP_MASTER_KEY or APP_MASTER_KEY_FILE must contain a base64-encoded 32-byte key")
	}
	if s.Port < 1 || s.Port > 65535 {
		return s, fmt.Errorf("invalid port")
	}
	if !strings.Contains(s.BasicAuth, ":") || strings.HasSuffix(s.BasicAuth, ":") || strings.HasPrefix(s.BasicAuth, ":") {
		return s, fmt.Errorf("APP_BASIC_AUTH is required (username:password)")
	}
	for _, target := range strings.Split(v.GetString("webhook"), ",") {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		u, e := url.Parse(target)
		if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
			return s, fmt.Errorf("invalid global webhook URL")
		}
		s.Webhooks = append(s.Webhooks, target)
	}
	if len(s.Webhooks) > 0 && strings.TrimSpace(s.WebhookSecret) == "" {
		return s, fmt.Errorf("BALE_WEBHOOK_SECRET required for global webhooks")
	}
	if s.MaxMediaBytes < 1 || s.MaxMediaBytes > 1<<30 {
		return s, fmt.Errorf("APP_MAX_MEDIA_BYTES must be between 1 and 1073741824")
	}
	return s, nil
}
