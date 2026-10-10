package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/mimalef70/goomni/src/config"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func loginCommand(v *viper.Viper) *cobra.Command {
	return loginCommandWithSecretReader(v, readSecret)
}

func loginCommandWithSecretReader(v *viper.Viper, secretReader func(string) (string, error)) *cobra.Command {
	c := &cobra.Command{Use: "login", Short: "Connect a device via the running local REST API; prompts keep codes out of shell history", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, e := config.Load(v)
		if e != nil {
			return e
		}
		id, _ := cmd.Flags().GetString("device")
		providerName, _ := cmd.Flags().GetString("provider")
		provider := domains.Provider(providerName)
		if err := provider.Validate(); err != nil {
			return err
		}
		phone, _ := cmd.Flags().GetString("phone")
		if (domains.ProvisionDeviceRequest{Provider: provider, DeviceID: id}).Validate() != nil {
			return fmt.Errorf("--device must be a device alias")
		}
		if phone == "" {
			fmt.Print("Phone (+countrycode): ")
			line, e := bufio.NewReader(os.Stdin).ReadString('\n')
			if e != nil {
				return e
			}
			phone = strings.TrimSpace(line)
		}
		host := cfg.Host
		if host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		base := "http://" + net.JoinHostPort(host, fmt.Sprint(cfg.Port)) + cfg.BasePath
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("login helper requires a loopback APP_HOST; use HTTPS REST for a remote server")
		}
		client := http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer client.CloseIdleConnections()
		call := func(method, path string, body any, key, instance string) (loginAPIResponse, error) {
			var out loginAPIResponse
			var reader io.Reader
			if body != nil {
				b, err := json.Marshal(body)
				if err != nil {
					return out, err
				}
				reader = bytes.NewReader(b)
			}
			r, err := http.NewRequestWithContext(cmd.Context(), method, base+path, reader)
			if err != nil {
				return out, err
			}
			parts := strings.SplitN(cfg.BasicAuth, ":", 2)
			r.SetBasicAuth(parts[0], parts[1])
			if body != nil {
				r.Header.Set("Content-Type", "application/json")
			}
			if key != "" {
				r.Header.Set("Idempotency-Key", key)
			}
			if instance != "" {
				r.Header.Set("X-Device-Instance", instance)
			}
			res, err := client.Do(r)
			if err != nil {
				return out, errLoginAPIUnavailable
			}
			defer res.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			if err != nil {
				return out, errLoginAPIUnavailable
			}
			if err = json.Unmarshal(raw, &out); err != nil {
				return out, fmt.Errorf("invalid local API response")
			}
			if res.StatusCode < 200 || res.StatusCode >= 300 {
				return out, fmt.Errorf("%s: %s", out.Code, out.Message)
			}
			return out, nil
		}
		// Select an existing alias once. Never refresh this selection during the
		// authentication flow: a replacement must fail its immutable instance guard.
		out, e := call(http.MethodGet, "/devices", nil, "", "")
		if e != nil {
			return e
		}
		var devices []domains.Device
		if json.Unmarshal(out.Results, &devices) != nil || devices == nil {
			return fmt.Errorf("invalid device list from local API")
		}
		var selected domains.Device
		for _, device := range devices {
			if device.ID == id {
				selected = device
				break
			}
		}
		if selected.ID != "" && selected.Provider != provider {
			return fmt.Errorf("selected device belongs to another provider")
		}
		if selected.ID == "" {
			key, err := uuid.NewRandom()
			if err != nil {
				return err
			}
			request := domains.ProvisionDeviceRequest{Provider: provider, DeviceID: id}
			out, e = call(http.MethodPost, "/devices", request, key.String(), "")
			if errors.Is(e, errLoginAPIUnavailable) && cmd.Context().Err() == nil {
				// Only provisioning can be retried safely after a lost response.
				// Keep its key and body; login/code/password are never replayed here.
				out, e = call(http.MethodPost, "/devices", request, key.String(), "")
			}
			if e != nil {
				return e
			}
			if json.Unmarshal(out.Results, &selected) != nil || selected.ID != id {
				return fmt.Errorf("invalid provisioned device from local API")
			}
		}
		if selected.InstanceID == "" {
			return fmt.Errorf("device response lacked instance_id")
		}
		authCall := func(path string, body any) (loginAPIResponse, error) {
			return call(http.MethodPost, "/devices/"+id+path, body, "", selected.InstanceID)
		}
		out, e = authCall("/login", map[string]any{"phone": phone})
		if e != nil {
			return e
		}
		var response domains.Challenge
		_ = json.Unmarshal(out.Results, &response)
		challenge := response.ID
		if challenge == "" {
			return fmt.Errorf("login response lacked challenge_id")
		}
		state := response.State
		authenticated := false
		// Providers own the order: some request the password before sending OTP.
		// Follow explicit states; never replay an authentication write after loss.
		for step := 0; step < 4; step++ {
			var value, path, field string
			switch state {
			case "awaiting_code":
				value, e = secretReader("Code: ")
				value = strings.TrimSpace(value)
				path, field = "/login/code", "code"
			case "awaiting_password":
				value, e = secretReader("Two-step password: ")
				path, field = "/login/password", "password"
			default:
				return fmt.Errorf("unsupported authentication state from local API")
			}
			if e != nil {
				return e
			}
			out, e = authCall(path, map[string]any{"challenge_id": challenge, field: value})
			value = ""
			var status domains.ConnectionStatus
			_ = json.Unmarshal(out.Results, &status)
			if e != nil && out.Code != "PASSWORD_REQUIRED" {
				return e
			}
			if e == nil && status.Auth == "authenticated" {
				authenticated = true
				break
			}
			// Read only current local challenge metadata under the same immutable
			// instance guard. A provider may rotate its local challenge at a step.
			out, e = call(http.MethodGet, "/devices/"+id+"/login", nil, "", selected.InstanceID)
			if e != nil {
				return e
			}
			var current domains.LoginState
			if json.Unmarshal(out.Results, &current) != nil || current.Challenge == nil || current.Challenge.ID == "" {
				return fmt.Errorf("authentication continuation lacked a challenge")
			}
			state, challenge = current.State, current.Challenge.ID
		}
		if !authenticated {
			return fmt.Errorf("authentication did not complete within the supported steps")
		}

		fmt.Println("Account authenticated. Inspect device status for connection/recovery state.")
		return nil
	}}
	c.Flags().String("provider", "", "Required messenger: bale, eitaa or rubika")
	c.Flags().String("device", "", "Device alias")
	c.Flags().String("phone", "", "Phone number (omit to prompt)")
	return c
}

type loginAPIResponse struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Results json.RawMessage `json:"results"`
}

var errLoginAPIUnavailable = errors.New("local API unavailable")

func readSecret(prompt string) (string, error) {
	return readTerminalSecret(prompt, int(os.Stdin.Fd()), term.IsTerminal, term.ReadPassword)
}

func readTerminalSecret(prompt string, fd int, isTerminal func(int) bool, readPassword func(int) ([]byte, error)) (string, error) {
	fmt.Print(prompt)
	if !isTerminal(fd) {
		return "", fmt.Errorf("interactive terminal required; use REST for automated login")
	}
	b, e := readPassword(fd)
	fmt.Println()
	// ReadPassword already removes the terminal line ending. Whitespace may be
	// part of a password; only the code-specific caller may normalize its input.
	return string(b), e
}
