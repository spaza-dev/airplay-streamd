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

const defaultConfigPath = "/etc/indexer/config.yaml"

var streamIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type Config struct {
	Server          ServerConfig   `yaml:"server" json:"server"`
	Database        DatabaseConfig `yaml:"database" json:"database"`
	Olaf            OlafConfig     `yaml:"olaf" json:"olaf"`
	FFprobe         FFprobeConfig  `yaml:"ffprobe" json:"ffprobe"`
	Workers         WorkersConfig  `yaml:"workers" json:"workers"`
	DefaultTimezone string         `yaml:"default_timezone" json:"default_timezone"`
	Logging         LoggingConfig  `yaml:"logging" json:"logging"`
}

type ServerConfig struct {
	Listen               string `yaml:"listen" json:"listen"`
	AdminToken           string `yaml:"admin_token" json:"-"`
	EventToken           string `yaml:"event_token" json:"-"`
	ShutdownGraceSeconds int    `yaml:"shutdown_grace_seconds" json:"shutdown_grace_seconds"`
}

type DatabaseConfig struct {
	Path      string `yaml:"path" json:"path"`
	Workspace string `yaml:"workspace" json:"workspace"`
}

type OlafConfig struct {
	Binary         string `yaml:"binary" json:"binary"`
	Threads        int    `yaml:"threads" json:"threads"`
	TimeoutMinutes int    `yaml:"timeout_minutes" json:"timeout_minutes"`
}

type FFprobeConfig struct {
	Binary         string `yaml:"binary" json:"binary"`
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
}

type WorkersConfig struct {
	MaxActiveJobs     int `yaml:"max_active_jobs" json:"max_active_jobs"`
	DownloadsPerJob   int `yaml:"downloads_per_job" json:"downloads_per_job"`
	UploadsPerJob     int `yaml:"uploads_per_job" json:"uploads_per_job"`
	JobTimeoutMinutes int `yaml:"job_timeout_minutes" json:"job_timeout_minutes"`
	RetryInitialSecs  int `yaml:"retry_initial_seconds" json:"retry_initial_seconds"`
	RetryMaxSecs      int `yaml:"retry_max_seconds" json:"retry_max_seconds"`
	MaxAttempts       int `yaml:"max_attempts" json:"max_attempts"`
}

type LoggingConfig struct {
	Level string `yaml:"level" json:"level"`
	File  string `yaml:"file" json:"file"`
}

func defaultConfig() Config {
	return Config{
		Server:   ServerConfig{Listen: "127.0.0.1:8788", ShutdownGraceSeconds: 30},
		Database: DatabaseConfig{Path: "/var/lib/indexer/indexer.db", Workspace: "/var/lib/indexer/jobs"},
		Olaf:     OlafConfig{Binary: "/usr/local/bin/olaf", Threads: 2, TimeoutMinutes: 240},
		FFprobe:  FFprobeConfig{Binary: "/usr/bin/ffprobe", TimeoutSeconds: 60},
		Workers: WorkersConfig{
			MaxActiveJobs: 1, DownloadsPerJob: 4, UploadsPerJob: 4,
			JobTimeoutMinutes: 360, RetryInitialSecs: 60, RetryMaxSecs: 3600, MaxAttempts: 3,
		},
		DefaultTimezone: "Africa/Nairobi",
		Logging:         LoggingConfig{Level: "info", File: "/var/log/indexer/indexer.log"},
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
	if strings.TrimSpace(c.Olaf.Binary) == "" {
		return errors.New("olaf.binary must not be empty")
	}
	if c.Olaf.Threads <= 0 {
		c.Olaf.Threads = 1
	}
	if c.Olaf.TimeoutMinutes <= 0 {
		return errors.New("olaf.timeout_minutes must be greater than zero")
	}
	if strings.TrimSpace(c.FFprobe.Binary) == "" {
		return errors.New("ffprobe.binary must not be empty")
	}
	if c.FFprobe.TimeoutSeconds <= 0 {
		return errors.New("ffprobe.timeout_seconds must be greater than zero")
	}
	if c.Workers.MaxActiveJobs <= 0 {
		return errors.New("workers.max_active_jobs must be greater than zero")
	}
	if c.Workers.DownloadsPerJob <= 0 {
		return errors.New("workers.downloads_per_job must be greater than zero")
	}
	if c.Workers.UploadsPerJob <= 0 {
		return errors.New("workers.uploads_per_job must be greater than zero")
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
