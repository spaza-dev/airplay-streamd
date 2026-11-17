package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

const defaultConfigPath = "/etc/queryd/config.yaml"

var streamIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type Config struct {
	Server          ServerConfig    `yaml:"server" json:"server"`
	Database        DatabaseConfig  `yaml:"database" json:"database"`
	Limits          LimitsConfig    `yaml:"limits" json:"limits"`
	Workers         WorkersConfig   `yaml:"workers" json:"workers"`
	Olaf            OlafConfig      `yaml:"olaf" json:"olaf"`
	Callbacks       CallbackConfig  `yaml:"callbacks" json:"callbacks"`
	Retention       RetentionConfig `yaml:"retention" json:"retention"`
	DefaultTimezone string          `yaml:"default_timezone" json:"default_timezone"`
	Logging         LoggingConfig   `yaml:"logging" json:"logging"`
}

type ServerConfig struct {
	Listen               string `yaml:"listen" json:"listen"`
	AdminToken           string `yaml:"admin_token" json:"-"`
	ShutdownGraceSeconds int    `yaml:"shutdown_grace_seconds" json:"shutdown_grace_seconds"`
}

type DatabaseConfig struct {
	Path      string `yaml:"path" json:"path"`
	Workspace string `yaml:"workspace" json:"workspace"`
}

type LimitsConfig struct {
	MaxUploadFiles   int  `yaml:"max_upload_files" json:"max_upload_files"`
	MaxUploadMB      int  `yaml:"max_upload_mb" json:"max_upload_mb"`
	MaxTotalUploadMB int  `yaml:"max_total_upload_mb" json:"max_total_upload_mb"`
	MaxIndexesPerJob int  `yaml:"max_indexes_per_job" json:"max_indexes_per_job"`
	MaxDownloadGB    int  `yaml:"max_download_gb" json:"max_download_gb"`
	MinFreeDiskGB    int  `yaml:"min_free_disk_gb" json:"min_free_disk_gb"`
	AllowPrivateURLs bool `yaml:"allow_private_urls" json:"allow_private_urls"`
}

type WorkersConfig struct {
	MaxActiveJobs     int `yaml:"max_active_jobs" json:"max_active_jobs"`
	DownloadsPerJob   int `yaml:"downloads_per_job" json:"downloads_per_job"`
	OlafThreads       int `yaml:"olaf_threads" json:"olaf_threads"`
	JobTimeoutMinutes int `yaml:"job_timeout_minutes" json:"job_timeout_minutes"`
	RetryInitialSecs  int `yaml:"retry_initial_seconds" json:"retry_initial_seconds"`
	RetryMaxSecs      int `yaml:"retry_max_seconds" json:"retry_max_seconds"`
	MaxAttempts       int `yaml:"max_attempts" json:"max_attempts"`
}

type OlafConfig struct {
	Binary           string `yaml:"binary" json:"binary"`
	TargetSampleRate int    `yaml:"target_sample_rate" json:"target_sample_rate"`
	MinMatchCount    int    `yaml:"min_match_count" json:"min_match_count"`
	MaxResults       int    `yaml:"max_results" json:"max_results"`
	TimeoutMinutes   int    `yaml:"timeout_minutes" json:"timeout_minutes"`
}

type CallbackConfig struct {
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxAttempts    int    `yaml:"max_attempts" json:"max_attempts"`
	SigningSecret  string `yaml:"signing_secret" json:"-"`
}

type RetentionConfig struct {
	JobsDays int `yaml:"jobs_days" json:"jobs_days"`
	LogsDays int `yaml:"logs_days" json:"logs_days"`
}

type LoggingConfig struct {
	Level string `yaml:"level" json:"level"`
	File  string `yaml:"file" json:"file"`
}

func defaultConfig() Config {
	return Config{
		Server:   ServerConfig{Listen: "127.0.0.1:8790", ShutdownGraceSeconds: 30},
		Database: DatabaseConfig{Path: "/var/lib/queryd/queryd.db", Workspace: "/var/lib/queryd/jobs"},
		Limits: LimitsConfig{
			MaxUploadFiles: 20, MaxUploadMB: 200, MaxTotalUploadMB: 1000,
			MaxIndexesPerJob: 500, MaxDownloadGB: 50, MinFreeDiskGB: 10,
		},
		Workers: WorkersConfig{
			MaxActiveJobs: 1, DownloadsPerJob: 4, OlafThreads: 2, JobTimeoutMinutes: 360,
			RetryInitialSecs: 30, RetryMaxSecs: 1800, MaxAttempts: 3,
		},
		Olaf: OlafConfig{
			Binary: "/usr/local/bin/olaf", TargetSampleRate: 16000,
			MinMatchCount: 20, MaxResults: 500, TimeoutMinutes: 60,
		},
		Callbacks:       CallbackConfig{TimeoutSeconds: 15, MaxAttempts: 8},
		Retention:       RetentionConfig{JobsDays: 30, LogsDays: 30},
		DefaultTimezone: "Africa/Nairobi",
		Logging:         LoggingConfig{Level: "info", File: "/var/log/queryd/queryd.log"},
	}
}

func loadConfig(cfgPath, envPath string) (*Config, error) {
	if cfgPath == "" {
		cfgPath = defaultConfigPath
	}
	if envPath == "" {
		envPath = filepath.Join(filepath.Dir(cfgPath), ".env")
	}
	if _, err := os.Stat(envPath); err == nil {
		if err := godotenv.Load(envPath); err != nil {
			return nil, fmt.Errorf("load env file %s: %w", envPath, err)
		}
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", cfgPath, err)
	}
	expanded := os.Expand(string(data), os.Getenv)
	cfg := defaultConfig()
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", cfgPath, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) Validate() error {
	if strings.TrimSpace(c.Server.Listen) == "" {
		return errors.New("server.listen must not be empty")
	}
	if c.Server.ShutdownGraceSeconds < 0 {
		return errors.New("server.shutdown_grace_seconds must not be negative")
	}
	if strings.TrimSpace(c.Database.Path) == "" {
		return errors.New("database.path must not be empty")
	}
	if strings.TrimSpace(c.Database.Workspace) == "" {
		return errors.New("database.workspace must not be empty")
	}
	if c.Limits.MaxUploadFiles <= 0 {
		return errors.New("limits.max_upload_files must be greater than zero")
	}
	if c.Limits.MaxUploadMB <= 0 || c.Limits.MaxTotalUploadMB <= 0 {
		return errors.New("upload limits must be greater than zero")
	}
	if c.Limits.MaxIndexesPerJob <= 0 {
		return errors.New("limits.max_indexes_per_job must be greater than zero")
	}
	if c.Limits.MaxDownloadGB <= 0 {
		return errors.New("limits.max_download_gb must be greater than zero")
	}
	if c.Limits.MinFreeDiskGB < 0 {
		return errors.New("limits.min_free_disk_gb must not be negative")
	}
	if c.Workers.MaxActiveJobs <= 0 {
		return errors.New("workers.max_active_jobs must be greater than zero")
	}
	if c.Workers.DownloadsPerJob <= 0 {
		return errors.New("workers.downloads_per_job must be greater than zero")
	}
	if c.Workers.OlafThreads <= 0 {
		c.Workers.OlafThreads = 1
	}
	if c.Workers.JobTimeoutMinutes <= 0 {
		return errors.New("workers.job_timeout_minutes must be greater than zero")
	}
	if c.Workers.RetryInitialSecs <= 0 {
		return errors.New("workers.retry_initial_seconds must be greater than zero")
	}
	if c.Workers.RetryMaxSecs < c.Workers.RetryInitialSecs {
		return errors.New("workers.retry_max_seconds must be >= retry_initial_seconds")
	}
	if c.Workers.MaxAttempts < 0 {
		return errors.New("workers.max_attempts must not be negative")
	}
	if strings.TrimSpace(c.Olaf.Binary) == "" {
		return errors.New("olaf.binary must not be empty")
	}
	if c.Olaf.TargetSampleRate <= 0 {
		return errors.New("olaf.target_sample_rate must be greater than zero")
	}
	if c.Olaf.MinMatchCount <= 0 {
		return errors.New("olaf.min_match_count must be greater than zero")
	}
	if c.Olaf.MaxResults <= 0 {
		return errors.New("olaf.max_results must be greater than zero")
	}
	if c.Olaf.TimeoutMinutes <= 0 {
		return errors.New("olaf.timeout_minutes must be greater than zero")
	}
	if c.Callbacks.TimeoutSeconds <= 0 {
		return errors.New("callbacks.timeout_seconds must be greater than zero")
	}
	if c.Callbacks.MaxAttempts <= 0 {
		return errors.New("callbacks.max_attempts must be greater than zero")
	}
	if c.Retention.JobsDays < 0 || c.Retention.LogsDays < 0 {
		return errors.New("retention days must not be negative")
	}
	if _, err := time.LoadLocation(c.DefaultTimezone); err != nil {
		return fmt.Errorf("invalid default_timezone %q: %w", c.DefaultTimezone, err)
	}
	switch strings.ToLower(strings.TrimSpace(c.Logging.Level)) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("logging.level %q is not supported", c.Logging.Level)
	}
	return nil
}

func parseServeFlags(args []string) (cfgPath, envPath string, err error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.StringVar(&cfgPath, "config", "", "configuration file")
	fs.StringVar(&envPath, "env", "", "environment file")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	return cfgPath, envPath, nil
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
