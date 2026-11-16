package main

import (
	"context"
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
	storage     *StorageClient
	storageCfg  StorageConfig
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
			fmt.Println("usage: queryd [serve] [--config path] [--env path]")
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
		fmt.Fprintln(os.Stderr, "queryd:", err)
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

	if info, err := os.Stat(cfg.Olaf.Binary); err != nil || info.IsDir() {
		return fmt.Errorf("required executable not available: %s", cfg.Olaf.Binary)
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
		storageCfg:  storageConfigFromEnv(),
		olafVersion: olafVersion,
		startTime:   time.Now().UTC(),
		ctx:         ctx,
		cancel:      cancel,
	}
	if app.storageCfg.Bucket != "" {
		app.storage, err = NewStorageClient(ctx, app.storageCfg)
		if err != nil {
			return fmt.Errorf("configure S3 storage: %w", err)
		}
	} else {
		logger.Warnf("S3_BUCKET is not configured; job creation is disabled")
	}
	app.jobs = NewJobManager(app)
	recovered, err := db.RecoverInterruptedJobs(ctx)
	if err != nil {
		return err
	}
	if recovered > 0 {
		logger.Infof("recovered %d interrupted jobs", recovered)
	}

	go app.jobs.Run(ctx)
	go app.retentionLoop(ctx)

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

	grace := time.Duration(cfg.Server.ShutdownGraceSeconds) * time.Second
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

func (a *App) ready() (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		return false, "database unavailable"
	}
	if a.storageCfg.Bucket == "" || a.storage == nil {
		return false, "S3 storage not configured"
	}
	if _, err := os.Stat(a.cfg.Database.Workspace); err != nil {
		return false, "workspace unavailable"
	}
	return true, ""
}

func (a *App) versionInfo() map[string]any {
	return map[string]any{
		"status":         "ok",
		"version":        version,
		"olaf_version":   a.olafVersion,
		"uptime_seconds": int(time.Since(a.startTime).Seconds()),
	}
}

func (a *App) workspaceForJob(jobID int64) string {
	return filepath.Join(a.cfg.Database.Workspace, fmt.Sprintf("%d", jobID))
}
