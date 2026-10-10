package cmd

import (
	"encoding/json"
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
	cmd.SetArgs([]string{"--provider", "bale", "--device", "test", "--phone", "+10000000000"})
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
	cmd.SetArgs([]string{"--provider", "bale", "--device", "test", "--phone", "+10000000000"})
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
		_, _ = w.Write([]byte(`{"code":"SUCCESS","results":[{"provider":"bale","id":"test"}]}`))
	}))
	defer server.Close()
	cmd := loginCommandWithSecretReader(loginFixtureConfig(t, server.URL), func(string) (string, error) {
		t.Error("missing instance must not prompt for a secret")
		return "", nil
	})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--provider", "bale", "--device", "test", "--phone", "+10000000000"})
	require.ErrorContains(t, cmd.Execute(), "instance_id")
}

func TestLoginFollowsPasswordBeforeOTPAndRotatedChallenge(t *testing.T) {
	var requests []string
	password := "  synthetic secret  "
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/devices" {
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":[{"provider":"rubika","id":"test","instance_id":"immutable-instance"}]}`))
			return
		}
		require.Equal(t, "immutable-instance", r.Header.Get("X-Device-Instance"))
		switch {
		case r.Method == "POST" && r.URL.Path == "/devices/test/login":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"challenge_id":"first","state":"awaiting_password"}}`))
		case r.Method == "POST" && r.URL.Path == "/devices/test/login/password":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, password, body["password"])
			require.Equal(t, "first", body["challenge_id"])
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"auth":"awaiting_code","transport":"disconnected","recovery":"degraded"}}`))
		case r.Method == "GET" && r.URL.Path == "/devices/test/login":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"state":"awaiting_code","challenge":{"challenge_id":"second"}}}`))
		case r.Method == "POST" && r.URL.Path == "/devices/test/login/code":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "12345", body["code"])
			require.Equal(t, "second", body["challenge_id"])
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"auth":"authenticated","transport":"connected","recovery":"degraded"}}`))
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	var prompts []string
	cmd := loginCommandWithSecretReader(loginFixtureConfig(t, server.URL), func(prompt string) (string, error) {
		prompts = append(prompts, prompt)
		if prompt == "Two-step password: " {
			return password, nil
		}
		return " 12345 ", nil
	})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--provider", "rubika", "--device", "test", "--phone", "+10000000000"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, []string{"Two-step password: ", "Code: "}, prompts)
	require.Equal(t, []string{"GET /devices", "POST /devices/test/login", "POST /devices/test/login/password", "GET /devices/test/login", "POST /devices/test/login/code"}, requests)
}
