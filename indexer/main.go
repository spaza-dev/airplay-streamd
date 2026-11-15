package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

var version = "dev"

type App struct {
	cfg         *Config
	db          *DB
	logger      *Logger
	jobs        *JobManager
	discovery   *Discovery
	olafVersion string
	startTime   time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	server      *http.Server
	draining    atomic.Bool
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "serve":
			args = args[1:]
		case "version":
			fmt.Println(version)
			return
		case "-h", "--help", "help":
			fmt.Println("usage: indexer [serve] [--config path] [--env path]")
			return
		default:
			if !strings.HasPrefix(args[0], "-") {
				fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
				os.Exit(2)
			}
		}
	}
	cfgPath, envPath, err := parseServeFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := run(cfgPath, envPath); err != nil {
		fmt.Fprintln(os.Stderr, "indexer:", err)
		os.Exit(1)
	}
}

func run(cfgPath, envPath string) error {
	cfg, err := loadConfig(cfgPath, envPath)
	if err != nil {
		return err
	}
	logger, err := NewLogger(cfg.Logging.Level, cfg.Logging.File)
	if err != nil {
		return err
	}
	defer logger.Close()

	for _, binary := range []string{cfg.Olaf.Binary, cfg.FFprobe.Binary} {
		if info, err := os.Stat(binary); err != nil || info.IsDir() {
			return fmt.Errorf("required executable not available: %s", binary)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	olafVersion, err := DetectOlafVersion(ctx, cfg.Olaf.Binary)
	if err != nil {
		return err
	}

	db, err := OpenDB(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := os.MkdirAll(cfg.Database.Workspace, 0o750); err != nil {
		return err
	}

	app := &App{
		cfg:         cfg,
		db:          db,
		logger:      logger,
		olafVersion: olafVersion,
		startTime:   time.Now().UTC(),
		ctx:         ctx,
		cancel:      cancel,
	}
	app.jobs = NewJobManager(app)
	app.discovery = NewDiscovery(app)
	if err := applyStorageEnv(ctx, app); err != nil {
		logger.Warnf("load S3 configuration from environment: %v", err)
	}
	if err := seedDiscoveryDefaults(ctx, app); err != nil {
		logger.Warnf("seed discovery config: %v", err)
	}
	recovered, err := db.RecoverInterruptedJobs(ctx)
	if err != nil {
		return err
	}
	if recovered > 0 {
		logger.Infof("recovered %d interrupted jobs", recovered)
	}

	go app.jobs.Run(ctx)
	go app.discovery.Run(ctx)

	server := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	app.server = server
	serverErr := make(chan error, 1)
	go func() {
		logger.Infof("listening on %s (olaf %s)", cfg.Server.Listen, olafVersion)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-signalCh:
		logger.Infof("received %s, shutting down", sig)
	case err := <-serverErr:
		logger.Errorf("http server: %v", err)
	}

	app.draining.Store(true)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)

	grace := time.Duration(app.cfg.Server.ShutdownGraceSeconds) * time.Second
	deadline := time.Now().Add(grace)
	for app.jobs.Active() > 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	deadline = time.Now().Add(15 * time.Second)
	for app.jobs.Active() > 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

func applyStorageEnv(ctx context.Context, app *App) error {
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if bucket == "" {
		return nil
	}
	source := StorageConfig{
		Name:      "source",
		Role:      "source",
		Endpoint:  os.Getenv("S3_ENDPOINT"),
		Bucket:    bucket,
		Region:    envOr("S3_REGION", "auto"),
		AccessKey: os.Getenv("S3_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
		Prefix:    os.Getenv("S3_PREFIX"),
		PathStyle: envBool("S3_PATH_STYLE", false),
	}
	destination := StorageConfig{
		Name:      "destination",
		Role:      "destination",
		Endpoint:  envOr("S3_DEST_ENDPOINT", source.Endpoint),
		Bucket:    envOr("S3_DEST_BUCKET", source.Bucket),
		Region:    envOr("S3_DEST_REGION", source.Region),
		AccessKey: envOr("S3_DEST_ACCESS_KEY_ID", source.AccessKey),
		SecretKey: envOr("S3_DEST_SECRET_ACCESS_KEY", source.SecretKey),
		Prefix:    envOr("S3_DEST_PREFIX", source.Prefix),
		PathStyle: envBool("S3_DEST_PATH_STYLE", source.PathStyle),
	}
	for _, cfg := range []StorageConfig{source, destination} {
		if err := upsertStorageConfig(ctx, app, cfg); err != nil {
			return fmt.Errorf("%s storage: %w", cfg.Role, err)
		}
	}
	return nil
}

func upsertStorageConfig(ctx context.Context, app *App, cfg StorageConfig) error {
	existing, err := app.db.StorageConfigByName(ctx, cfg.Name)
	switch {
	case err == nil:
		cfg.ID = existing.ID
	case errors.Is(err, sql.ErrNoRows):
		cfg.ID = 0
	default:
		return err
	}
	return app.db.SaveStorageConfig(ctx, &cfg)
}

func seedDiscoveryDefaults(ctx context.Context, app *App) error {
	raw, _, err := app.db.DiscoveryConfig(ctx)
	if err != nil {
		return err
	}
	if raw != "" {
		return nil
	}
	cfg := defaultDiscoveryConfig()
	return app.discovery.SaveConfig(ctx, cfg)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func (a *App) workspacePath(jobID int64) string {
	return filepath.Join(a.cfg.Database.Workspace, fmt.Sprintf("%d", jobID))
}
