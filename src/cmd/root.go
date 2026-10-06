package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mimalef70/gobale/src/config"
	"github.com/mimalef70/gobale/src/domains"
	"github.com/mimalef70/gobale/src/infrastructure/mediafile"
	"github.com/mimalef70/gobale/src/infrastructure/storage"
	"github.com/mimalef70/gobale/src/internal/balemeow"
	"github.com/mimalef70/gobale/src/ui/rest"
	"github.com/mimalef70/gobale/src/ui/web"
	"github.com/mimalef70/gobale/src/usecase"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func Execute() error { return NewCommand().Execute() }
func NewCommand() *cobra.Command {
	v := viper.New()
	v.SetConfigFile(".env")
	v.SetConfigType("env")
	_ = v.ReadInConfig()
	root := &cobra.Command{Use: "gobale", Short: "A native Go, multi-account Bale REST gateway", Version: config.AppVersion, SilenceUsage: true, SilenceErrors: true}
	flags := root.PersistentFlags()
	flags.String("host", "127.0.0.1", "HTTP listen host")
	flags.Int("port", 3000, "HTTP listen port")
	flags.String("basic-auth", "", "Global username:password (prefer APP_BASIC_AUTH)")
	flags.Bool("ui-enabled", true, "Serve the embedded administrative UI")
	flags.String("ui-public-origin", "", "Exact HTTPS public origin for browser access behind a reverse proxy")
	flags.String("base-path", "", "Optional URL prefix")
	flags.String("database", "storages/gobale.db", "SQLite database")
	flags.String("media-root", "storages/media", "Private media directory")
	flags.String("master-key", "", "Base64 32-byte encryption key (prefer APP_MASTER_KEY_FILE)")
	flags.String("master-key-file", "", "Private encryption key file")
	flags.String("grpc-endpoint", "https://next-ws.bale.ai", "Bale gRPC-Web endpoint")
	flags.String("ws-endpoint", "wss://next-ws.bale.ai/ws/", "Bale WebSocket endpoint")
	flags.Uint32("bale-app-id", 0, "Verified Bale client application ID")
	flags.String("bale-api-key", "", "Verified Bale client application key")
	flags.Uint32("bale-api-version", 173855, "Bale API version")
	flags.String("webhook", "", "Comma-separated global fallback webhook URLs")
	flags.String("webhook-secret", "", "Global HMAC secret")
	flags.Bool("webhook-device-merge-global", false, "Deliver to global URLs in addition to device override")
	flags.Int64("max-media-bytes", 64<<20, "Maximum uploaded/downloaded media bytes")
	flags.Int("send-workers", 4, "Maximum concurrent send workers across accounts")
	flags.Int("webhook-workers", 8, "Maximum concurrent webhook deliveries")
	flags.Int("reconnect-workers", 4, "Maximum concurrent reconnects (1-4)")
	flags.Int("media-workers", 4, "Maximum concurrent media transfers")
	flags.Int("queue-limit", 1000, "Maximum queued, in-flight and unknown sends across accounts")
	bindings := map[string]string{"ui-enabled": "APP_UI_ENABLED", "ui-public-origin": "APP_UI_PUBLIC_ORIGIN", "host": "APP_HOST", "port": "APP_PORT", "basic-auth": "APP_BASIC_AUTH", "base-path": "APP_BASE_PATH", "database": "APP_DATABASE", "media-root": "APP_MEDIA_ROOT", "master-key": "APP_MASTER_KEY", "master-key-file": "APP_MASTER_KEY_FILE", "grpc-endpoint": "BALE_GRPC_ENDPOINT", "ws-endpoint": "BALE_WS_ENDPOINT", "bale-app-id": "BALE_APP_ID", "bale-api-key": "BALE_API_KEY", "bale-api-version": "BALE_API_VERSION", "webhook": "BALE_WEBHOOK", "webhook-secret": "BALE_WEBHOOK_SECRET", "webhook-device-merge-global": "BALE_WEBHOOK_DEVICE_MERGE_GLOBAL", "max-media-bytes": "APP_MAX_MEDIA_BYTES", "send-workers": "APP_SEND_WORKERS", "webhook-workers": "APP_WEBHOOK_WORKERS", "reconnect-workers": "APP_RECONNECT_WORKERS", "media-workers": "APP_MEDIA_WORKERS", "queue-limit": "APP_QUEUE_LIMIT"}
	for flag, env := range bindings {
		_ = v.BindPFlag(flag, flags.Lookup(flag))
		_ = v.BindEnv(flag, env)
		if val := v.Get(env); val != nil {
			v.SetDefault(flag, val)
		}
	}
	root.AddCommand(&cobra.Command{Use: "rest", Short: "Run the authenticated REST service", RunE: func(cmd *cobra.Command, args []string) error {
		cfg, e := config.Load(v)
		if e != nil {
			return e
		}
		return run(cmd.Context(), cfg)
	}})
	initCmd := &cobra.Command{Use: "init", Short: "Create private local configuration and a new encryption key", RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("dir")
		webClient, _ := cmd.Flags().GetBool("bale-web-client")
		return initializeWithClient(dir, webClient)
	}}
	initCmd.Flags().String("dir", ".", "Configuration directory")
	initCmd.Flags().Bool("bale-web-client", false, "Opt in to the verified public Bale web application identity")
	root.AddCommand(initCmd)
	root.AddCommand(loginCommand(v))
	return root
}
func run(parent context.Context, cfg config.Settings) error {
	// Validate bundled UI before opening storage or starting any account client.
	var assets *web.Bundle
	if cfg.UIEnabled {
		var err error
		assets, err = web.Validate(config.AppVersion, config.ContractSHA256)
		if err != nil {
			return err
		}
		if _, err = rest.NewAdminSessions(rest.AdminSessionOptions{BasicAuth: cfg.BasicAuth, BasePath: cfg.BasePath, PublicOrigin: cfg.UIPublicOrigin}); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, e := storage.Open(cfg.Database, cfg.MasterKey)
	if e != nil {
		return e
	}
	defer st.Close()
	targets := make([]storage.WebhookTarget, 0, len(cfg.Webhooks))
	for _, u := range cfg.Webhooks {
		targets = append(targets, storage.WebhookTarget{URL: u, Secret: cfg.WebhookSecret})
	}
	mediaSlots := make(chan struct{}, cfg.MediaWorkers)
	factory := func(d domains.Device) domains.Client {
		return balemeow.New(balemeow.Options{GRPCEndpoint: cfg.GRPCEndpoint, WebSocketEndpoint: cfg.WebSocketEndpoint, Origin: cfg.Origin, AppID: cfg.AppID, APIKey: cfg.APIKey, APIVersion: cfg.APIVersion, DeviceTitle: cfg.DeviceTitle, MaxMediaBytes: cfg.MaxMediaBytes,
			RecoveryVerified: true,
			LoadCheckpoint:   func(ctx context.Context) (string, error) { return st.Checkpoint(ctx, d.ConnectionID) },
			SaveMediaReference: func(ctx context.Context, peer domains.Peer, messageID string, m domains.ProviderMedia) error {
				return st.SaveProviderMedia(ctx, d.ConnectionID, peer, messageID, m)
			},
			OnDiagnostic: func(d balemeow.Diagnostic) {
				logrus.WithFields(logrus.Fields{"code": d.Code, "scheme": d.Scheme, "host": d.Host}).Warn("Provider diagnostic")
			},
			MediaSource: func(ctx context.Context, id string) (io.ReadCloser, balemeow.MediaInfo, error) {
				select {
				case mediaSlots <- struct{}{}:
				case <-ctx.Done():
					return nil, balemeow.MediaInfo{}, ctx.Err()
				}
				m, err := st.GetMedia(ctx, d.ConnectionID, id)
				if err != nil {
					<-mediaSlots
					return nil, balemeow.MediaInfo{}, err
				}
				path, err := mediafile.Resolve(cfg.MediaRoot, m.Path)
				if err != nil {
					<-mediaSlots
					return nil, balemeow.MediaInfo{}, err
				}
				f, err := os.Open(path)
				if err != nil {
					<-mediaSlots
					return nil, balemeow.MediaInfo{}, domains.E("MEDIA_NOT_FOUND", "media unavailable", 404)
				}
				return &releaseReader{ReadCloser: f, release: func() { <-mediaSlots }}, balemeow.MediaInfo{Name: m.Name, ContentType: m.ContentType, Size: m.Size}, nil
			}})
	}

	service := usecase.New(st, usecase.Options{GlobalWebhooks: targets, MergeGlobal: cfg.WebhookMergeGlobal, SendTimeout: 2 * time.Minute, SendWorkers: cfg.SendWorkers, WebhookWorkers: cfg.WebhookWorkers, ReconnectWorkers: cfg.ReconnectWorkers, QueueLimit: cfg.QueueLimit}, factory)
	if e = service.Start(ctx); e != nil {
		return e
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = service.Close(c)
	}()
	server, e := rest.New(service, st, rest.Options{UIEnabled: cfg.UIEnabled, UIPublicOrigin: cfg.UIPublicOrigin, UIAssets: assets, BasicAuth: cfg.BasicAuth, BasePath: cfg.BasePath, Version: config.AppVersion, MediaRoot: cfg.MediaRoot, MaxMediaBytes: cfg.MaxMediaBytes, MediaSlots: mediaSlots, SendWait: cfg.SendWait})
	if e != nil {
		return e
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	logrus.WithFields(logrus.Fields{"address": addr, "version": config.AppVersion, "release_stage": config.ReleaseStage(config.AppVersion)}).Info("GoBale starting")
	done := make(chan error, 1)
	go func() { done <- server.App.Listen(addr) }()
	select {
	case e = <-done:
		return e
	case <-ctx.Done():
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.App.ShutdownWithContext(c)
	}
}
func initialize(dir string) error { return initializeWithClient(dir, false) }
func initializeWithClient(dir string, publicWebClient bool) error {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	envPath := filepath.Join(dir, ".env")
	keyPath := filepath.Join(dir, "master.key")
	for _, p := range []string{envPath, keyPath} {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			return fmt.Errorf("refusing to overwrite existing configuration: %s", p)
		}
	}
	key := make([]byte, 32)
	password := make([]byte, 24)
	if _, e := rand.Read(key); e != nil {
		return e
	}
	if _, e := rand.Read(password); e != nil {
		return e
	}
	if e := writePrivate(keyPath, []byte(base64.StdEncoding.EncodeToString(key)+"\n")); e != nil {
		return e
	}
	body := fmt.Sprintf("APP_HOST=127.0.0.1\nAPP_PORT=3000\nAPP_BASIC_AUTH=admin:%s\nAPP_MASTER_KEY_FILE=master.key\nAPP_DATABASE=storages/gobale.db\nAPP_MEDIA_ROOT=storages/media\n# Configure a verified Bale client identity before login.\nBALE_APP_ID=0\nBALE_API_KEY=\nBALE_API_VERSION=173855\n", base64.RawURLEncoding.EncodeToString(password))
	if publicWebClient {
		body = strings.ReplaceAll(body, "BALE_APP_ID=0\nBALE_API_KEY=\n", fmt.Sprintf("BALE_APP_ID=%d\nBALE_API_KEY=%s\n", config.PublicWebAppID, config.PublicWebAPIKey))
	}
	if e := writePrivate(envPath, []byte(body)); e != nil {
		_ = os.Remove(keyPath)
		return e
	}
	fmt.Printf("Created %s and %s (private permissions). Configure Bale identity locally, then run gobale rest from that directory.\n", envPath, keyPath)
	return nil
}
func writePrivate(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}

type releaseReader struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (r *releaseReader) Close() error { err := r.ReadCloser.Close(); r.once.Do(r.release); return err }
