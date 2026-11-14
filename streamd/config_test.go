package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	return cfg
}

func TestLoadConfigExpandsEnvAndMergesStream(t *testing.T) {
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
streams:
  - id: "stream_one"
    type: "radio"
    url: "http://example.com/live.ogg"
    audio:
      codec: "libopus"
      container: "ogg"
      bitrate: "64k"
      sample_rate: 48000
      channels: 1
    video:
      enabled: true
      codec: "libx264"
      container: "mp4"
      bitrate: "1500k"
    metadata:
      country: "NL"
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

	eff, ok := cfg.FindStream("stream_one")
	if !ok {
		t.Fatal("stream not found")
	}
	if eff.Name != "stream_one" {
		t.Fatalf("name default = %q", eff.Name)
	}
	if eff.ChunkSeconds != 2 {
		t.Fatalf("effective chunk seconds = %d", eff.ChunkSeconds)
	}
	if eff.Audio.Codec != "libopus" || eff.Audio.Bitrate != "64k" || eff.Audio.Channels != 1 {
		t.Fatalf("audio merge wrong: %+v", eff.Audio)
	}
	if !videoEnabled(*eff.Video) || eff.Video.Container != "mp4" || eff.Video.Codec != "libx264" {
		t.Fatalf("video merge wrong: %+v", eff.Video)
	}
	if cfg.streamFormat(eff) != "mp4" {
		t.Fatalf("stream format = %q", cfg.streamFormat(eff))
	}
	if eff.Metadata["country"] != "NL" {
		t.Fatalf("metadata lost: %+v", eff.Metadata)
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
		cfg.Streams = []StreamConfig{{
			ID: "ok", Type: "direct", URL: "https://example.com/live.ogg", Enabled: true,
		}}
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
		{"invalid id", func(c *Config) { c.Streams[0].ID = "-bad" }, "invalid id"},
		{"bad type", func(c *Config) { c.Streams[0].Type = "spotify" }, "unsupported type"},
		{"bad scheme", func(c *Config) { c.Streams[0].URL = "file:///etc/passwd" }, "scheme"},
		{"missing url", func(c *Config) { c.Streams[0].URL = "" }, "url is required"},
		{"negative chunk", func(c *Config) { c.Streams[0].ChunkSeconds = -1 }, "chunk_seconds"},
		{"duplicate id", func(c *Config) {
			c.Streams = append(c.Streams, c.Streams[0])
		}, "duplicate stream id"},
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

func TestFFmpegArgs(t *testing.T) {
	dir := t.TempDir()
	cfg := validTestConfig(t, dir)
	cfg.Streams = []StreamConfig{{
		ID: "tv1", Type: "hls", URL: "https://example.com/live.m3u8",
		Enabled: true, ChunkSeconds: 5,
		Video: &VideoConfig{Enabled: boolPtr(true), Codec: "libx264", Bitrate: "2500k", Container: "mp4"},
	}}
	eff := cfg.effectiveStream(cfg.Streams[0])
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

func TestSaveConfigRedactsCredentials(t *testing.T) {
	dir := t.TempDir()
	cfg := validTestConfig(t, dir)
	cfg.Storage = StorageConfig{
		Endpoint: "https://example.r2.cloudflarestorage.com", Region: "auto",
		Bucket: "bucket", AccessKeyID: "super-secret-key", SecretAccessKey: "super-secret-value",
	}
	cfg.Streams = []StreamConfig{{ID: "one", Type: "direct", URL: "https://example.com/a.ogg"}}
	cfgPath := filepath.Join(dir, "config.yaml")
	logger, err := NewLogger("error", "")
	if err != nil {
		t.Fatal(err)
	}
	state := &RuntimeState{Stopped: map[string]bool{}, MetaCreatedAt: map[string]string{}}
	app := newApp(cfgPath, "", &cfg, logger, state, filepath.Join(dir, "runtime.json"))
	app.cfgMu.Lock()
	err = app.saveConfigLocked()
	app.cfgMu.Unlock()
	if err != nil {
		t.Fatalf("saveConfigLocked: %v", err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "super-secret") {
		t.Fatalf("credentials persisted to config:\n%s", text)
	}
	if !strings.Contains(text, "${S3_ACCESS_KEY_ID}") || !strings.Contains(text, "${S3_SECRET_ACCESS_KEY}") {
		t.Fatalf("credential placeholders missing:\n%s", text)
	}

	t.Setenv("S3_ACCESS_KEY_ID", "super-secret-key")
	t.Setenv("S3_SECRET_ACCESS_KEY", "super-secret-value")
	loaded, err := loadConfig(cfgPath, filepath.Join(dir, "missing.env"))
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if loaded.Storage.AccessKeyID != "super-secret-key" || loaded.Storage.SecretAccessKey != "super-secret-value" {
		t.Fatalf("credentials not restored from env: %+v", loaded.Storage)
	}
}
