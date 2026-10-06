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
	}{{"send-workers", 0}, {"webhook-workers", 65}, {"reconnect-workers", 5}, {"media-workers", -1}, {"queue-limit", 100001}, {"basic-auth", ":password"}, {"basic-auth", "admin:"}, {"master-key", "bad"}, {"port", 0}, {"max-media-bytes", -1}, {"max-media-bytes", int64(1) << 62}, {"webhook", "https://example.com/hook#fragment"}, {"webhook", "file:///etc/passwd"}} {
		t.Run(tt.k+"bad", func(t *testing.T) { v := configFixture(); v.Set(tt.k, tt.v); _, e := Load(v); require.Error(t, e) })
	}
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
