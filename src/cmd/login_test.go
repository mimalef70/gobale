package cmd

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/viper"
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

func TestLoginPreservesPasswordAndNormalizesOnlyOTP(t *testing.T) {
	password := "  synthetic password\u2003 "
	requests := make(chan map[string]string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}
		body["path"] = r.URL.Path
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/devices":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{}}`))
		case "/devices/test/login":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"challenge_id":"synthetic"}}`))
		case "/devices/test/login/code":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"auth":"awaiting_password"}}`))
		case "/devices/test/login/password":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"auth":"authenticated"}}`))
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()
	host, portString, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portString)
	require.NoError(t, err)
	v := viper.New()
	v.Set("host", host)
	v.Set("port", port)
	v.Set("basic-auth", "test:password")
	v.Set("master-key", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	v.Set("max-media-bytes", 1024)
	prompts := 0
	cmd := loginCommandWithSecretReader(v, func(prompt string) (string, error) {
		prompts++
		if prompt == "Code: " {
			return "  synthetic-code  ", nil
		}
		require.Equal(t, "Two-step password: ", prompt)
		return password, nil
	})
	cmd.SetArgs([]string{"--device", "test", "--phone", "+10000000000"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, 2, prompts)
	require.Len(t, requests, 4)
	for len(requests) > 0 {
		request := <-requests
		switch request["path"] {
		case "/devices/test/login/code":
			require.Equal(t, "synthetic-code", request["code"])
		case "/devices/test/login/password":
			require.Equal(t, password, request["password"])
		}
	}
}
