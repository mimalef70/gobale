package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretReadPreservesPasswordBytes(t *testing.T) {
	for _, password := range []string{"  synthetic password  ", "\u2003synthetic\t", "   "} {
		value, err := readTerminalSecret("Synthetic input: ", 7,
			func(fd int) bool { require.Equal(t, 7, fd); return true },
			func(fd int) ([]byte, error) { require.Equal(t, 7, fd); return []byte(password), nil })
		require.NoError(t, err)
		require.Equal(t, password, value)
	}
	_, err := readTerminalSecret("Synthetic input: ", 7, func(int) bool { return false },
		func(int) ([]byte, error) { t.Fatal("non-terminal input must not be read"); return nil, nil })
	require.ErrorContains(t, err, "interactive terminal required")
}

func TestLoginRefusesRemoteHost(t *testing.T) {
	v := loginFixtureConfig(t, "http://127.0.0.1:3000")
	v.Set("host", "example.invalid")
	cmd := loginCommandWithSecretReader(v, func(string) (string, error) {
		t.Fatal("remote login must not prompt for a secret")
		return "", nil
	})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--device", "test", "--phone", "+10000000000"})
	require.ErrorContains(t, cmd.Execute(), "loopback")
}

func TestLoginDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("redirect target must not receive credentials or requests")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	cmd := loginCommandWithSecretReader(loginFixtureConfig(t, server.URL), func(string) (string, error) {
		t.Error("redirect response must not prompt for a secret")
		return "", nil
	})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--device", "test", "--phone", "+10000000000"})
	require.Error(t, cmd.Execute())
}

func TestLoginRequiresInstanceFromDeviceList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/devices" {
			t.Error("invalid instance must not reach authentication or provisioning")
			http.Error(w, "unexpected request", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"SUCCESS","results":[{"id":"test"}]}`))
	}))
	defer server.Close()
	cmd := loginCommandWithSecretReader(loginFixtureConfig(t, server.URL), func(string) (string, error) {
		t.Error("missing instance must not prompt for a secret")
		return "", nil
	})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--device", "test", "--phone", "+10000000000"})
	require.ErrorContains(t, cmd.Execute(), "instance_id")
}
