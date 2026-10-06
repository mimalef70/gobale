package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/mimalef70/gobale/src/config"
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
	c := &cobra.Command{Use: "login", Short: "Connect a device via the running local REST API; prompts keep codes out of shell history", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, e := config.Load(v)
		if e != nil {
			return e
		}
		id, _ := cmd.Flags().GetString("device")
		phone, _ := cmd.Flags().GetString("phone")
		if id == "" || strings.ContainsAny(id, "/?#") {
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
		call := func(path string, body any) (map[string]any, error) {
			b, e := json.Marshal(body)
			if e != nil {
				return nil, e
			}
			r, e := http.NewRequestWithContext(cmd.Context(), "POST", base+path, bytes.NewReader(b))
			if e != nil {
				return nil, e
			}
			parts := strings.SplitN(cfg.BasicAuth, ":", 2)
			r.SetBasicAuth(parts[0], parts[1])
			r.Header.Set("Content-Type", "application/json")
			res, e := client.Do(r)
			if e != nil {
				return nil, fmt.Errorf("local API unavailable")
			}
			defer res.Body.Close()
			raw, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			if e != nil {
				return nil, e
			}
			var out map[string]any
			if e = json.Unmarshal(raw, &out); e != nil {
				return nil, e
			}
			if res.StatusCode >= 400 {
				return out, fmt.Errorf("%v: %v", out["code"], out["message"])
			}
			return out, nil
		}
		// A pre-existing alias is expected on repeat login; all other errors surface.
		if out, e := call("/devices", map[string]any{"device_id": id}); e != nil {
			if out == nil || (out["code"] != "DEVICE_EXISTS" && out["code"] != "CONFLICT") {
				return e
			}
		}
		out, e := call("/devices/"+id+"/login", map[string]any{"phone": phone})
		if e != nil {
			return e
		}
		results, _ := out["results"].(map[string]any)
		challenge, _ := results["challenge_id"].(string)
		if challenge == "" {
			return fmt.Errorf("login response lacked challenge_id")
		}
		code, e := readSecret("Code: ")
		if e != nil {
			return e
		}
		out, e = call("/devices/"+id+"/login/code", map[string]any{"challenge_id": challenge, "code": code})
		code = ""
		needPassword := false
		if out != nil {
			if out["code"] == "PASSWORD_REQUIRED" {
				needPassword = true
			}
			if r, ok := out["results"].(map[string]any); ok && r["auth"] == "awaiting_password" {
				needPassword = true
			}
		}
		if needPassword {
			password, pe := readSecret("Two-step password: ")
			if pe != nil {
				return pe
			}
			_, e = call("/devices/"+id+"/login/password", map[string]any{"challenge_id": challenge, "password": password})
			password = ""
		}
		if e != nil {
			return e
		}
		fmt.Println("Account authenticated. Inspect device status for connection/recovery state.")
		return nil
	}}
	c.Flags().String("device", "", "Device alias")
	c.Flags().String("phone", "", "Phone number (omit to prompt)")
	return c
}
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("interactive terminal required; use REST for automated login")
	}
	b, e := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return strings.TrimSpace(string(b)), e
}
