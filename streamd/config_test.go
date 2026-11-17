package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testID(name string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

func validTestConfig(t *testing.T, dir string) Config {
	t.Helper()
	cfg := defaultConfig()
	cfg.Server.Listen = "127.0.0.1:0"
	cfg.Server.Socket = filepath.Join(dir, "streamd.sock")
	cfg.Recording.OutputDir = filepath.Join(dir, "chunks")
	cfg.Recording.FFmpegPath = "/bin/true"
	cfg.Recording.ChunkSeconds = 3
	cfg.Recording.ReconnectDelaySeconds = 1
	cfg.Recording.MaxReconnectDelaySeconds = 2
	cfg.Uploader.Enabled = false
	cfg.Uploader.QueueDB = filepath.Join(dir, "uploads.db")
	cfg.Logging.File = ""
	cfg.Database.Path = filepath.Join(dir, "streamd.db")
	return cfg
}

func TestDefaultRecordingChunkSeconds(t *testing.T) {
	if got := defaultConfig().Recording.ChunkSeconds; got != 3600 {
		t.Fatalf("default chunk seconds = %d", got)
	}
}

func TestLoadConfigExpandsEnvAndDefaults(t *testing.T) {
	dir := t.TempDir()
	envName := "STREAMD_TEST_ENDPOINT_CONFIG"
	t.Setenv(envName, "http://127.0.0.1:9000")

	cfgPath := filepath.Join(dir, "config.yaml")
	body := `
server:
  listen: "127.0.0.1:0"
  socket: "` + filepath.Join(dir, "sock") + `"
recording:
  chunk_seconds: 2
  output_dir: "` + filepath.Join(dir, "chunks") + `"
  ffmpeg_path: "/bin/true"
  reconnect_delay_seconds: 1
  max_reconnect_delay_seconds: 3
uploader:
  enabled: false
storage:
  endpoint: "${` + envName + `}"
logging:
  level: info
  file: ""
database:
  path: "` + filepath.Join(dir, "streams.db") + `"
discovery:
  enabled: false
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(cfgPath, filepath.Join(dir, "missing.env"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Storage.Endpoint != "http://127.0.0.1:9000" {
		t.Fatalf("endpoint not expanded: %q", cfg.Storage.Endpoint)
	}
	if cfg.Recording.ChunkSeconds != 2 {
		t.Fatalf("chunk seconds = %d", cfg.Recording.ChunkSeconds)
	}
	if cfg.Database.Path != filepath.Join(dir, "streams.db") {
		t.Fatalf("database path = %q", cfg.Database.Path)
	}
	if cfg.Discovery.Enabled || cfg.Discovery.MaxResults != 100 {
		t.Fatalf("discovery defaults = %+v", cfg.Discovery)
	}
}

func TestLoadConfigReadsDotEnv(t *testing.T) {
	dir := t.TempDir()
	envName := "STREAMD_TEST_DOTENV_BUCKET_UNIQUE"
	_ = os.Unsetenv(envName)

	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(envName+"=bucket-from-env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	body := `
server:
  listen: "127.0.0.1:0"
  socket: "` + filepath.Join(dir, "sock") + `"
recording:
  chunk_seconds: 2
  output_dir: "` + filepath.Join(dir, "chunks") + `"
  ffmpeg_path: "/bin/true"
  reconnect_delay_seconds: 1
  max_reconnect_delay_seconds: 3
uploader:
  enabled: false
logging:
  level: info
  file: ""
storage:
  bucket: "${` + envName + `}"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(cfgPath, envPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Storage.Bucket != "bucket-from-env" {
		t.Fatalf("bucket = %q", cfg.Storage.Bucket)
	}
	_ = os.Unsetenv(envName)
}

func TestConfigValidation(t *testing.T) {
	newCfg := func(t *testing.T, mutate func(*Config)) Config {
		t.Helper()
		cfg := validTestConfig(t, t.TempDir())
		if mutate != nil {
			mutate(&cfg)
		}
		return cfg
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"valid", nil, ""},
		{"missing database", func(c *Config) { c.Database.Path = "" }, "database.path"},
		{"missing discovery url", func(c *Config) { c.Discovery.RadioBrowserURL = "" }, "radio_browser_url"},
		{"bad discovery timeout", func(c *Config) { c.Discovery.TimeoutSeconds = 0 }, "timeout_seconds"},
		{"bad discovery cache", func(c *Config) { c.Discovery.CacheSeconds = -1 }, "cache_seconds"},
		{"bad discovery limit", func(c *Config) { c.Discovery.MaxResults = 0 }, "max_results"},
		{"bad audio codec", func(c *Config) { c.Recording.Audio.Codec = "flac" }, "unsupported codec"},
		{"bad bitrate", func(c *Config) { c.Recording.Audio.Bitrate = "128" }, "invalid bitrate"},
		{"uploader needs bucket", func(c *Config) {
			c.Uploader.Enabled = true
			c.Storage.Endpoint = "http://127.0.0.1:9000"
		}, "storage.bucket"},
		{"uploader needs endpoint", func(c *Config) {
			c.Uploader.Enabled = true
			c.Storage.Bucket = "b"
			c.Storage.AccessKeyID = "k"
			c.Storage.SecretAccessKey = "s"
		}, "storage.endpoint"},
		{"bad log level", func(c *Config) { c.Logging.Level = "trace" }, "not supported"},
		{"bad metrics interval", func(c *Config) { c.Metrics.SampleIntervalSeconds = 0 }, "sample_interval_seconds"},
		{"bad metrics history", func(c *Config) {
			c.Metrics.SampleIntervalSeconds = 10
			c.Metrics.HistorySeconds = 5
		}, "history_seconds"},
		{"bad cpu target", func(c *Config) { c.Capacity.CPUTargetPercent = 0 }, "cpu_target_percent"},
		{"bad network target", func(c *Config) { c.Capacity.NetworkTargetPercent = 101 }, "network_target_percent"},
		{"bad disk horizon", func(c *Config) { c.Capacity.DiskHorizonHours = 0 }, "disk_horizon_hours"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newCfg(t, tt.mutate)
			err := cfg.Validate()
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestStreamValidation(t *testing.T) {
	cfg := validTestConfig(t, t.TempDir())
	valid := StreamConfig{
		ID: testID("ok"), Name: "OK", Type: "direct", URL: "https://example.com/live.ogg",
	}
	if err := cfg.validateStream(&valid); err != nil {
		t.Fatalf("valid stream rejected: %v", err)
	}
	if valid.ChunkSeconds != cfg.Recording.ChunkSeconds {
		t.Fatalf("chunk seconds = %d", valid.ChunkSeconds)
	}
	cases := []struct {
		name    string
		mutate  func(*StreamConfig)
		wantSub string
	}{
		{"invalid id", func(s *StreamConfig) { s.ID = "not-a-uuid" }, "invalid id"},
		{"bad type", func(s *StreamConfig) { s.Type = "spotify" }, "unsupported type"},
		{"bad scheme", func(s *StreamConfig) { s.URL = "file:///etc/passwd" }, "scheme"},
		{"missing url", func(s *StreamConfig) { s.URL = "" }, "url is required"},
		{"negative chunk", func(s *StreamConfig) { s.ChunkSeconds = -1 }, "chunk_seconds"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			stream := valid
			tt.mutate(&stream)
			err := cfg.validateStream(&stream)
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestFFmpegArgs(t *testing.T) {
	dir := t.TempDir()
	cfg := validTestConfig(t, dir)
	eff := cfg.effectiveStream(StreamConfig{
		ID: testID("tv1"), Name: "TV 1", Type: "hls", URL: "https://example.com/live.m3u8",
		Enabled: true, ChunkSeconds: 5,
		Video: &VideoConfig{Enabled: boolPtr(true), Codec: "libx264", Bitrate: "2500k", Container: "mp4"},
	})
	w := &Worker{app: &App{cfg: &cfg}, stream: eff}

	args := w.ffmpegArgs(cfg, "https://example.com/live.m3u8", "/tmp/in/%Y%m%d%H%M%S.mp4")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-reconnect 1",
		"-c:v libx264",
		"-b:v 2500k",
		"-c:a libopus",
		"-vbr on",
		"-segment_time 5",
		"-segment_format mp4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "-vn") {
		t.Fatalf("video enabled but -vn present: %s", joined)
	}

	redacted := strings.Join(redactArgs(args), " ")
	if strings.Contains(redacted, "example.com") || !strings.Contains(redacted, "-i <source>") {
		t.Fatalf("source not redacted: %s", redacted)
	}
}

func TestGrowDelay(t *testing.T) {
	if got := growDelay(10*time.Second, 300*time.Second); got != 20*time.Second {
		t.Fatalf("got %s", got)
	}
	if got := growDelay(200*time.Second, 300*time.Second); got != 300*time.Second {
		t.Fatalf("got %s", got)
	}
}

func TestRetryAt(t *testing.T) {
	u := &Uploader{cfg: UploaderConfig{Retry: RetryConfig{
		InitialIntervalSeconds: 30,
		MaxIntervalSeconds:     3600,
		MaxAttempts:            3,
	}}}
	before := time.Now()
	next, retry := u.retryAt(1)
	if !retry {
		t.Fatal("expected retry")
	}
	if d := next.Sub(before); d < 29*time.Second || d > 31*time.Second {
		t.Fatalf("first retry delay = %s", d)
	}
	next, retry = u.retryAt(2)
	if !retry || next.Sub(before) < 59*time.Second {
		t.Fatalf("second retry delay = %s retry=%v", next.Sub(before), retry)
	}
	if _, retry = u.retryAt(3); retry {
		t.Fatal("max attempts should stop retries")
	}
}
