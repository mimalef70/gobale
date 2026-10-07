package config

import (
	"bytes"
	"encoding/base64"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"testing"
)

func configFixture() *viper.Viper {
	v := viper.New()
	v.Set("master-key", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	v.Set("basic-auth", "admin:test")
	v.Set("port", 3000)
	v.Set("max-media-bytes", 64<<20)
	return v
}
func TestConfigRejectsUnsafeValues(t *testing.T) {
	for _, tt := range []struct {
		k string
		v any
	}{{"send-workers", 0}, {"webhook-workers", 65}, {"reconnect-workers", 5}, {"media-workers", -1}, {"queue-limit", 100001}, {"connection-queue-limit", 0}, {"connection-queue-limit", 100001}, {"basic-auth", ":password"}, {"basic-auth", "admin:"}, {"master-key", "bad"}, {"port", 0}, {"max-media-bytes", -1}, {"max-media-bytes", int64(1) << 62}, {"webhook", "https://example.com/hook#fragment"}, {"webhook", "file:///etc/passwd"}} {
		t.Run(tt.k+"bad", func(t *testing.T) { v := configFixture(); v.Set(tt.k, tt.v); _, e := Load(v); require.Error(t, e) })
	}
}

func TestConnectionQueueConfigurationDefaultAndEnvironment(t *testing.T) {
	v := configFixture()
	s, err := Load(v)
	require.NoError(t, err)
	require.Equal(t, 100, s.ConnectionQueueLimit)
	require.Equal(t, 1000, s.QueueLimit)
	t.Setenv("APP_CONNECTION_QUEUE_LIMIT", "7")
	require.NoError(t, v.BindEnv("connection-queue-limit", "APP_CONNECTION_QUEUE_LIMIT"))
	s, err = Load(v)
	require.NoError(t, err)
	require.Equal(t, 7, s.ConnectionQueueLimit)
	// The effective bound is the lower available capacity; a larger configured
	// connection cap does not disable the independent global bound.
	v.Set("connection-queue-limit", 2000)
	s, err = Load(v)
	require.NoError(t, err)
	require.Equal(t, 2000, s.ConnectionQueueLimit)
	require.Equal(t, 1000, s.QueueLimit)
}
func TestLocalWebhookWithSecret(t *testing.T) {
	v := configFixture()
	v.Set("webhook", "http://127.0.0.1:8080/hook")
	v.Set("webhook-secret", "synthetic")
	s, e := Load(v)
	require.NoError(t, e)
	require.Len(t, s.Webhooks, 1)
	v.Set("webhook-secret", " \t\n")
	_, e = Load(v)
	require.Error(t, e)
}

func TestAdministrativeUIConfiguration(t *testing.T) {
	v := configFixture()
	s, err := Load(v)
	require.NoError(t, err)
	require.True(t, s.UIEnabled)
	v.Set("ui-enabled", false)
	v.Set("ui-public-origin", "https://gateway.example.test:8443")
	s, err = Load(v)
	require.NoError(t, err)
	require.False(t, s.UIEnabled)
	require.Equal(t, "https://gateway.example.test:8443", s.UIPublicOrigin)
	for _, origin := range []string{"http://example.test", "https://example.test/ui", "https://user:pass@example.test", "https://example.test?q=1", "https://example.test#fragment", "https://"} {
		v.Set("ui-public-origin", origin)
		_, err = Load(v)
		require.Error(t, err, origin)
	}
}
