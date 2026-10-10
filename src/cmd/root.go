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

	"github.com/mimalef70/goomni/src/config"
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/mediafile"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
	"github.com/mimalef70/goomni/src/infrastructure/storage"
	"github.com/mimalef70/goomni/src/infrastructure/workpool"
	"github.com/mimalef70/goomni/src/internal/balemeow"
	"github.com/mimalef70/goomni/src/internal/eitaameow"
	"github.com/mimalef70/goomni/src/internal/rubikameow"
	"github.com/mimalef70/goomni/src/ui/rest"
	"github.com/mimalef70/goomni/src/ui/web"
	"github.com/mimalef70/goomni/src/usecase"
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
	root := &cobra.Command{Use: "goomni", Short: "A native Go, multi-messenger account gateway", Version: config.AppVersion, SilenceUsage: true, SilenceErrors: true}
	flags := root.PersistentFlags()
	flags.String("host", "127.0.0.1", "HTTP listen host")
	flags.Int("port", 3000, "HTTP listen port")
	flags.String("basic-auth", "", "Global username:password (prefer APP_BASIC_AUTH)")
	flags.Bool("ui-enabled", true, "Serve the embedded administrative UI")
	flags.String("ui-public-origin", "", "Exact HTTPS public origin for browser access behind a reverse proxy")
	flags.String("base-path", "", "Optional URL prefix")
	flags.String("database", "storages/goomni.db", "SQLite database")
	flags.String("media-root", "storages/media", "Private media directory")
	flags.String("master-key", "", "Base64 32-byte encryption key (prefer APP_MASTER_KEY_FILE)")
	flags.String("master-key-file", "", "Private encryption key file")
	flags.String("grpc-endpoint", "https://next-ws.bale.ai", "Bale gRPC-Web endpoint")
	flags.String("ws-endpoint", "wss://next-ws.bale.ai/ws/", "Bale WebSocket endpoint")
	flags.Uint32("bale-app-id", 0, "Verified Bale client application ID")
	flags.String("bale-api-key", "", "Verified Bale client application key")
	flags.Uint32("bale-api-version", 173855, "Bale API version")
	flags.Bool("eitaa-enabled", false, "Enable the native Eitaa adapter (live verification pending)")
	flags.String("eitaa-endpoint", "https://hasan.eitaa.ir/eitaa/", "Eitaa HTTP TL endpoint")
	flags.String("eitaa-upload-endpoint", "https://alzheimer.eitaa.com/eitaa/", "Eitaa upload endpoint")
	flags.String("eitaa-download-endpoint", "https://mohsen.eitaa.com/eitaa/", "Eitaa download endpoint")
	flags.Int32("eitaa-api-id", 2496, "Eitaa public web application ID")
	flags.String("eitaa-api-hash", "8da85b0d5bfe62527e5b244c209159c3", "Eitaa public web application hash")
	flags.Duration("eitaa-poll-interval", 5*time.Second, "Eitaa bounded polling interval")
	flags.Bool("rubika-enabled", false, "Enable the native Rubika adapter (live verification pending)")
	flags.String("rubika-discovery-endpoint", "https://getdcmess.iranlms.ir/", "Rubika data-center discovery endpoint")
	flags.String("rubika-api-endpoint", "", "Override the discovered Rubika API endpoint")
	flags.String("rubika-socket-endpoint", "", "Override the discovered Rubika WebSocket endpoint")
	flags.String("webhook", "", "Comma-separated global fallback webhook URLs")
	flags.String("webhook-secret", "", "Global HMAC secret")
	flags.Bool("webhook-device-merge-global", false, "Deliver to global URLs in addition to device override")
	flags.Int64("max-media-bytes", 64<<20, "Maximum uploaded/downloaded media bytes")
	flags.Int("send-workers", 4, "Maximum concurrent send workers across accounts")
	flags.Int("webhook-workers", 8, "Maximum concurrent webhook deliveries")
	flags.Int("reconnect-workers", 4, "Maximum concurrent reconnects (1-4)")
	flags.Int("media-workers", 4, "Maximum concurrent media transfers")
	flags.Int("queue-limit", 1000, "Maximum queued, in-flight and unknown sends across accounts")
	flags.Int("connection-queue-limit", 100, "Maximum queued, in-flight and unknown sends per connection")
	bindings := map[string]string{"rubika-enabled": "RUBIKA_ENABLED", "rubika-discovery-endpoint": "RUBIKA_DISCOVERY_ENDPOINT", "rubika-api-endpoint": "RUBIKA_API_ENDPOINT", "rubika-socket-endpoint": "RUBIKA_SOCKET_ENDPOINT", "eitaa-enabled": "EITAA_ENABLED", "eitaa-endpoint": "EITAA_ENDPOINT", "eitaa-upload-endpoint": "EITAA_UPLOAD_ENDPOINT", "eitaa-download-endpoint": "EITAA_DOWNLOAD_ENDPOINT", "eitaa-api-id": "EITAA_API_ID", "eitaa-api-hash": "EITAA_API_HASH", "eitaa-poll-interval": "EITAA_POLL_INTERVAL", "ui-enabled": "APP_UI_ENABLED", "ui-public-origin": "APP_UI_PUBLIC_ORIGIN", "host": "APP_HOST", "port": "APP_PORT", "basic-auth": "APP_BASIC_AUTH", "base-path": "APP_BASE_PATH", "database": "APP_DATABASE", "media-root": "APP_MEDIA_ROOT", "master-key": "APP_MASTER_KEY", "master-key-file": "APP_MASTER_KEY_FILE", "grpc-endpoint": "BALE_GRPC_ENDPOINT", "ws-endpoint": "BALE_WS_ENDPOINT", "bale-app-id": "BALE_APP_ID", "bale-api-key": "BALE_API_KEY", "bale-api-version": "BALE_API_VERSION", "webhook": "APP_WEBHOOK", "webhook-secret": "APP_WEBHOOK_SECRET", "webhook-device-merge-global": "APP_WEBHOOK_DEVICE_MERGE_GLOBAL", "max-media-bytes": "APP_MAX_MEDIA_BYTES", "send-workers": "APP_SEND_WORKERS", "webhook-workers": "APP_WEBHOOK_WORKERS", "reconnect-workers": "APP_RECONNECT_WORKERS", "media-workers": "APP_MEDIA_WORKERS", "queue-limit": "APP_QUEUE_LIMIT", "connection-queue-limit": "APP_CONNECTION_QUEUE_LIMIT"}
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
	mediaManager, e := mediafile.Open(cfg.MediaRoot, st.MediaFileRegistered)
	if e != nil {
		return e
	}
	mediaManager.Diagnostic = func(code string) {
		if code == "MEDIA_ORPHAN_UNVERIFIED" {
			logrus.WithField("code", code).Warn("Unregistered media file retained without ownership proof")
			return
		}
		logrus.WithField("code", code).Warn("Temporary media cleanup deferred")
	}
	cleanupContext, cleanupCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := mediaManager.Sweep(cleanupContext, 256); err != nil {
		mediaManager.Diagnostic("MEDIA_CLEANUP_DEFERRED")
	}
	if err := mediaManager.CleanupLegacy(cleanupContext, 256); err != nil {
		mediaManager.Diagnostic("MEDIA_CLEANUP_DEFERRED")
	}
	cleanupCancel()
	mediaContext, stopMedia := context.WithCancel(ctx)
	mediaDone := make(chan struct{})
	go func() {
		defer close(mediaDone)
		mediaManager.Run(mediaContext)
	}()
	defer func() {
		stopMedia()
		<-mediaDone
		_ = mediaManager.Close()
	}()
	targets := make([]storage.WebhookTarget, 0, len(cfg.Webhooks))
	for _, u := range cfg.Webhooks {
		targets = append(targets, storage.WebhookTarget{URL: u, Secret: cfg.WebhookSecret})
	}
	mediaPool := workpool.New(cfg.MediaWorkers)
	pollPool := workpool.New(4)
	pollProviders := []domains.Provider{}
	if cfg.EitaaEnabled {
		pollProviders = append(pollProviders, domains.ProviderEitaa)
	}
	if cfg.RubikaEnabled {
		pollProviders = append(pollProviders, domains.ProviderRubika)
	}
	pollPool.SetProviders(pollProviders)
	refreshMediaProviders := func(ctx context.Context) error {
		devices, err := st.ListDevices(ctx)
		if err != nil {
			return err
		}
		present := map[domains.Provider]bool{}
		list := []domains.Provider{}
		for _, device := range devices {
			if !present[device.Provider] {
				present[device.Provider] = true
				list = append(list, device.Provider)
			}
		}
		mediaPool.SetProviders(list)
		return nil
	}
	factory := func(d domains.Device) domains.Client {
		return balemeow.New(balemeow.Options{GRPCEndpoint: cfg.GRPCEndpoint, WebSocketEndpoint: cfg.WebSocketEndpoint, Origin: cfg.Origin, AppID: cfg.AppID, APIKey: cfg.APIKey, APIVersion: cfg.APIVersion, DeviceTitle: cfg.DeviceTitle, MaxMediaBytes: cfg.MaxMediaBytes,
			RecoveryVerified: true,
			LoadCheckpoint:   func(ctx context.Context) (string, error) { return st.Checkpoint(ctx, d.ConnectionID) },
			SaveMediaReference: func(ctx context.Context, peer domains.Peer, messageID string, m domains.ProviderMedia) (bool, error) {
				return st.SaveProviderMedia(ctx, d.ConnectionID, peer, messageID, m)
			},
			OnDiagnostic: func(d balemeow.Diagnostic) {
				logrus.WithFields(logrus.Fields{"code": d.Code, "scheme": d.Scheme, "host": d.Host}).Warn("Provider diagnostic")
			},
			MediaSource: func(ctx context.Context, id string) (io.ReadCloser, balemeow.MediaInfo, error) {
				reader, info, err := openProviderMedia(ctx, d, id, cfg.MediaRoot, st, mediaPool, refreshMediaProviders)
				return reader, balemeow.MediaInfo{Name: info.Name, ContentType: info.ContentType, Size: info.Size}, err
			}})
	}
	eitaaFactory := func(d domains.Device) domains.Client {
		native, err := eitaameow.New(eitaameow.Config{Endpoint: cfg.EitaaEndpoint, UploadEndpoint: cfg.EitaaUploadEndpoint, DownloadEndpoint: cfg.EitaaDownloadEndpoint, APIID: cfg.EitaaAPIID, APIHash: cfg.EitaaAPIHash, ConnectionID: d.ConnectionID, PollInterval: cfg.EitaaPollInterval, MaxMediaBytes: cfg.MaxMediaBytes,
			SaveMediaReference: func(ctx context.Context, peer domains.Peer, messageID string, media domains.ProviderMedia) (bool, error) {
				return st.SaveProviderMedia(ctx, d.ConnectionID, peer, messageID, media)
			},
			LoadCheckpoint: func(ctx context.Context, scope string) (string, error) {
				return st.ScopedCheckpoint(ctx, d.ConnectionID, scope)
			},
			PollPermit: func(ctx context.Context) (func(), error) { return pollPool.AcquireFor(ctx, d.Provider, d.ConnectionID) },
			MediaSource: func(ctx context.Context, id string) (io.ReadCloser, domains.NativeMediaInfo, error) {
				return openProviderMedia(ctx, d, id, cfg.MediaRoot, st, mediaPool, refreshMediaProviders)
			},
		})
		if err != nil {
			return &unavailableClient{}
		}
		return native
	}
	rubikaFactory := func(d domains.Device) domains.Client {
		native, err := rubikameow.New(rubikameow.Config{DiscoveryEndpoint: cfg.RubikaDiscoveryEndpoint, APIEndpoint: cfg.RubikaAPIEndpoint, SocketEndpoint: cfg.RubikaSocketEndpoint, ConnectionID: d.ConnectionID, MaxMediaBytes: cfg.MaxMediaBytes,
			LoadCheckpoint: func(ctx context.Context, scope string) (string, error) {
				return st.ScopedCheckpoint(ctx, d.ConnectionID, scope)
			},
			PollPermit: func(ctx context.Context) (func(), error) { return pollPool.AcquireFor(ctx, d.Provider, d.ConnectionID) },
			MediaSource: func(ctx context.Context, id string) (io.ReadCloser, domains.NativeMediaInfo, error) {
				return openProviderMedia(ctx, d, id, cfg.MediaRoot, st, mediaPool, refreshMediaProviders)
			},
			SaveMediaReference: func(ctx context.Context, peer domains.Peer, messageID string, m domains.ProviderMedia) (bool, error) {
				return st.SaveProviderMedia(ctx, d.ConnectionID, peer, messageID, m)
			},
		})
		if err != nil {
			return &unavailableClient{}
		}
		return native
	}
	registry, err := domains.NewProviderRegistry(domains.ProviderRegistration{Contract: bale.Contract{}, Factory: factory}, domains.ProviderRegistration{Contract: eitaameow.Contract{Enabled: cfg.EitaaEnabled}, Factory: eitaaFactory}, domains.ProviderRegistration{Contract: rubikameow.Contract{Enabled: cfg.RubikaEnabled}, Factory: rubikaFactory})
	if err != nil {
		return err
	}

	service := usecase.New(st, usecase.Options{Providers: registry, GlobalWebhooks: targets, MergeGlobal: cfg.WebhookMergeGlobal, SendTimeout: 2 * time.Minute, SendWorkers: cfg.SendWorkers, WebhookWorkers: cfg.WebhookWorkers, ReconnectWorkers: cfg.ReconnectWorkers, QueueLimit: cfg.QueueLimit, ConnectionQueueLimit: cfg.ConnectionQueueLimit}, factory)
	if e = service.Start(ctx); e != nil {
		return e
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = service.Close(c)
	}()
	server, e := rest.New(service, st, rest.Options{UIEnabled: cfg.UIEnabled, UIPublicOrigin: cfg.UIPublicOrigin, UIAssets: assets, BasicAuth: cfg.BasicAuth, BasePath: cfg.BasePath, Version: config.AppVersion, MediaRoot: cfg.MediaRoot, MaxMediaBytes: cfg.MaxMediaBytes, MediaPool: mediaPool, MediaManager: mediaManager, SendWait: cfg.SendWait})
	if e != nil {
		return e
	}
	server.StartMetrics(ctx)
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	logrus.WithFields(logrus.Fields{"address": addr, "version": config.AppVersion, "release_stage": config.ReleaseStage(config.AppVersion)}).Info("GoOmni starting")
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
	body := fmt.Sprintf("APP_HOST=127.0.0.1\nAPP_PORT=3000\nAPP_BASIC_AUTH=admin:%s\nAPP_MASTER_KEY_FILE=master.key\nAPP_DATABASE=storages/goomni.db\nAPP_MEDIA_ROOT=storages/media\n# Configure a verified Bale client identity before login.\nBALE_APP_ID=0\nBALE_API_KEY=\nBALE_API_VERSION=173855\n", base64.RawURLEncoding.EncodeToString(password))
	if publicWebClient {
		body = strings.ReplaceAll(body, "BALE_APP_ID=0\nBALE_API_KEY=\n", fmt.Sprintf("BALE_APP_ID=%d\nBALE_API_KEY=%s\n", config.PublicWebAppID, config.PublicWebAPIKey))
	}
	if e := writePrivate(envPath, []byte(body)); e != nil {
		_ = os.Remove(keyPath)
		return e
	}
	fmt.Printf("Created %s and %s (private permissions). Configure Bale identity locally, then run goomni rest from that directory.\n", envPath, keyPath)
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
	io.ReadSeekCloser
	once    sync.Once
	release func()
}

func (r *releaseReader) Close() error {
	err := r.ReadSeekCloser.Close()
	r.once.Do(r.release)
	return err
}
