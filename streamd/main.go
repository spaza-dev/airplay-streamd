package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

var version = "dev"

const (
	defaultConfigPath = "/etc/streamd/config.yaml"

	defaultChunkSeconds   = 3600
	defaultReconnectDelay = 10
	defaultMaxReconnect   = 300
	defaultConcurrency    = 4
)

var (
	bitratePattern = regexp.MustCompile(`^[0-9]+k$`)

	allowedStreamTypes = map[string]bool{
		"radio": true, "tv": true, "hls": true, "direct": true,
		"youtube": true, "twitch": true, "web": true,
	}

	allowedURLSchemes = map[string]bool{
		"http": true, "https": true, "rtmp": true, "rtmps": true,
		"rtsp": true, "udp": true, "mms": true, "srt": true,
	}

	allowedAudioCodecs = map[string]bool{
		"libopus": true, "aac": true, "libmp3lame": true, "copy": true,
	}

	allowedVideoCodecs = map[string]bool{
		"libx264": true, "libx265": true, "copy": true,
	}

	// container -> accepted audio codecs
	audioContainers = map[string][]string{
		"ogg":  {"libopus", "copy"},
		"opus": {"libopus", "copy"},
		"m4a":  {"aac", "copy"},
		"aac":  {"aac", "copy"},
		"mp3":  {"libmp3lame", "copy"},
	}

	videoContainers = map[string][]string{
		"mp4":  {"libx264", "libx265", "copy"},
		"mkv":  {"libx264", "libx265", "copy"},
		"webm": {"libx264", "libx265", "copy"},
	}

	allowedSampleRates = map[int]bool{
		8000: true, 16000: true, 22050: true, 24000: true,
		32000: true, 44100: true, 48000: true, 96000: true,
	}
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type Config struct {
	Server    ServerConfig    `yaml:"server" json:"server"`
	Recording RecordingConfig `yaml:"recording" json:"recording"`
	Uploader  UploaderConfig  `yaml:"uploader" json:"uploader"`
	Storage   StorageConfig   `yaml:"storage" json:"storage"`
	Logging   LoggingConfig   `yaml:"logging" json:"logging"`
	Metrics   MetricsConfig   `yaml:"metrics" json:"metrics"`
	Capacity  CapacityConfig  `yaml:"capacity" json:"capacity"`
	Database  DatabaseConfig  `yaml:"database" json:"database"`
	Discovery DiscoveryConfig `yaml:"discovery" json:"discovery"`
}

type DatabaseConfig struct {
	Path string `yaml:"path" json:"path"`
}

type DiscoveryConfig struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`
	RadioBrowserURL string `yaml:"radio_browser_url" json:"radio_browser_url"`
	TimeoutSeconds  int    `yaml:"timeout_seconds" json:"timeout_seconds"`
	CacheSeconds    int    `yaml:"cache_seconds" json:"cache_seconds"`
	MaxResults      int    `yaml:"max_results" json:"max_results"`
}

type ServerConfig struct {
	Listen     string `yaml:"listen" json:"listen"`
	Socket     string `yaml:"socket" json:"socket"`
	AdminToken string `yaml:"admin_token" json:"-"`
}

type RecordingConfig struct {
	ChunkSeconds             int         `yaml:"chunk_seconds" json:"chunk_seconds"`
	OutputDir                string      `yaml:"output_dir" json:"output_dir"`
	FFmpegPath               string      `yaml:"ffmpeg_path" json:"ffmpeg_path"`
	YtDlpPath                string      `yaml:"yt_dlp_path" json:"yt_dlp_path"`
	ReconnectDelaySeconds    int         `yaml:"reconnect_delay_seconds" json:"reconnect_delay_seconds"`
	MaxReconnectDelaySeconds int         `yaml:"max_reconnect_delay_seconds" json:"max_reconnect_delay_seconds"`
	Audio                    AudioConfig `yaml:"audio" json:"audio"`
	Video                    VideoConfig `yaml:"video" json:"video"`
}

type AudioConfig struct {
	Codec      string `yaml:"codec" json:"codec"`
	Container  string `yaml:"container" json:"container"`
	Bitrate    string `yaml:"bitrate" json:"bitrate"`
	SampleRate int    `yaml:"sample_rate" json:"sample_rate"`
	Channels   int    `yaml:"channels" json:"channels"`
	VBR        *bool  `yaml:"vbr,omitempty" json:"vbr,omitempty"`
}

type VideoConfig struct {
	Enabled   *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Codec     string `yaml:"codec,omitempty" json:"codec,omitempty"`
	Bitrate   string `yaml:"bitrate,omitempty" json:"bitrate,omitempty"`
	Container string `yaml:"container,omitempty" json:"container,omitempty"`
}

type UploaderConfig struct {
	Enabled            bool        `yaml:"enabled" json:"enabled"`
	QueueDB            string      `yaml:"queue_db" json:"queue_db"`
	Concurrency        int         `yaml:"concurrency" json:"concurrency"`
	Retry              RetryConfig `yaml:"retry" json:"retry"`
	CleanupAfterUpload bool        `yaml:"cleanup_after_upload" json:"cleanup_after_upload"`
}

type RetryConfig struct {
	InitialIntervalSeconds int `yaml:"initial_interval_seconds" json:"initial_interval_seconds"`
	MaxIntervalSeconds     int `yaml:"max_interval_seconds" json:"max_interval_seconds"`
	MaxAttempts            int `yaml:"max_attempts" json:"max_attempts"`
}

type StorageConfig struct {
	Endpoint        string `yaml:"endpoint" json:"endpoint"`
	Region          string `yaml:"region" json:"region"`
	Bucket          string `yaml:"bucket" json:"bucket"`
	AccessKeyID     string `yaml:"access_key_id" json:"-"`
	SecretAccessKey string `yaml:"secret_access_key" json:"-"`
	ForcePathStyle  bool   `yaml:"force_path_style" json:"force_path_style"`
}

type LoggingConfig struct {
	Level string `yaml:"level" json:"level"`
	File  string `yaml:"file" json:"file"`
}

type MetricsConfig struct {
	Enabled               bool `yaml:"enabled" json:"enabled"`
	SampleIntervalSeconds int  `yaml:"sample_interval_seconds" json:"sample_interval_seconds"`
	HistorySeconds        int  `yaml:"history_seconds" json:"history_seconds"`
}

type CapacityConfig struct {
	Enforce                    bool    `yaml:"enforce" json:"enforce"`
	CPUTargetPercent           float64 `yaml:"cpu_target_percent" json:"cpu_target_percent"`
	MemoryTargetPercent        float64 `yaml:"memory_target_percent" json:"memory_target_percent"`
	DiskTargetPercent          float64 `yaml:"disk_target_percent" json:"disk_target_percent"`
	NetworkTargetPercent       float64 `yaml:"network_target_percent" json:"network_target_percent"`
	NetworkMbps                float64 `yaml:"network_mbps" json:"network_mbps"`
	DiskHorizonHours           float64 `yaml:"disk_horizon_hours" json:"disk_horizon_hours"`
	MinFreeDiskGB              float64 `yaml:"min_free_disk_gb" json:"min_free_disk_gb"`
	AssumedCPUPerStreamPercent float64 `yaml:"assumed_cpu_per_stream_percent" json:"assumed_cpu_per_stream_percent"`
	AssumedMemoryPerStreamMB   float64 `yaml:"assumed_memory_per_stream_mb" json:"assumed_memory_per_stream_mb"`
}

type StreamConfig struct {
	ID           string         `yaml:"id,omitempty" json:"id"`
	Source       string         `yaml:"source,omitempty" json:"source"`
	Name         string         `yaml:"name" json:"name"`
	Type         string         `yaml:"type" json:"type"`
	URL          string         `yaml:"url" json:"url"`
	Enabled      bool           `yaml:"enabled" json:"enabled"`
	ChunkSeconds int            `yaml:"chunk_seconds,omitempty" json:"chunk_seconds,omitempty"`
	Audio        *AudioConfig   `yaml:"audio,omitempty" json:"audio,omitempty"`
	Video        *VideoConfig   `yaml:"video,omitempty" json:"video,omitempty"`
	Metadata     map[string]any `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Radio        *RadioStation  `yaml:"radio,omitempty" json:"radio,omitempty"`
	CreatedAt    time.Time      `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	UpdatedAt    time.Time      `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
}

func boolPtr(v bool) *bool { return &v }

func defaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Listen: "127.0.0.1:8787",
			Socket: "/run/streamd.sock",
		},
		Recording: RecordingConfig{
			ChunkSeconds:             defaultChunkSeconds,
			OutputDir:                "/var/lib/streamd/chunks",
			FFmpegPath:               "/usr/bin/ffmpeg",
			YtDlpPath:                "/usr/local/bin/yt-dlp",
			ReconnectDelaySeconds:    defaultReconnectDelay,
			MaxReconnectDelaySeconds: defaultMaxReconnect,
			Audio: AudioConfig{
				Codec: "libopus", Container: "ogg", Bitrate: "96k",
				SampleRate: 48000, Channels: 2, VBR: boolPtr(true),
			},
			Video: VideoConfig{
				Enabled: boolPtr(false), Codec: "libx264", Bitrate: "2500k", Container: "mp4",
			},
		},
		Uploader: UploaderConfig{
			Enabled:     true,
			QueueDB:     "/var/lib/streamd/state/uploads.db",
			Concurrency: defaultConcurrency,
			Retry: RetryConfig{
				InitialIntervalSeconds: 30,
				MaxIntervalSeconds:     3600,
				MaxAttempts:            0,
			},
			CleanupAfterUpload: true,
		},
		Storage: StorageConfig{Region: "auto"},
		Logging: LoggingConfig{Level: "info", File: "/var/log/streamd/streamd.log"},
		Metrics: MetricsConfig{
			Enabled:               true,
			SampleIntervalSeconds: 5,
			HistorySeconds:        300,
		},
		Capacity: CapacityConfig{
			Enforce:                    true,
			CPUTargetPercent:           80,
			MemoryTargetPercent:        85,
			DiskTargetPercent:          85,
			NetworkTargetPercent:       80,
			NetworkMbps:                0,
			DiskHorizonHours:           24,
			MinFreeDiskGB:              1,
			AssumedCPUPerStreamPercent: 1.5,
			AssumedMemoryPerStreamMB:   64,
		},
		Database: DatabaseConfig{Path: "/var/lib/streamd/state/streamd.db"},
		Discovery: DiscoveryConfig{
			Enabled:         true,
			RadioBrowserURL: "https://all.api.radio-browser.info",
			TimeoutSeconds:  10,
			CacheSeconds:    300,
			MaxResults:      100,
		},
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
	if strings.TrimSpace(c.Server.Socket) == "" {
		return errors.New("server.socket must not be empty")
	}

	r := c.Recording
	if r.ChunkSeconds <= 0 {
		return errors.New("recording.chunk_seconds must be greater than zero")
	}
	if strings.TrimSpace(r.OutputDir) == "" {
		return errors.New("recording.output_dir must not be empty")
	}
	if strings.TrimSpace(r.FFmpegPath) == "" {
		return errors.New("recording.ffmpeg_path must not be empty")
	}
	if r.ReconnectDelaySeconds <= 0 {
		return errors.New("recording.reconnect_delay_seconds must be greater than zero")
	}
	if r.MaxReconnectDelaySeconds < r.ReconnectDelaySeconds {
		return errors.New("recording.max_reconnect_delay_seconds must be >= reconnect_delay_seconds")
	}
	if err := validateAudio(r.Audio); err != nil {
		return fmt.Errorf("recording.audio: %w", err)
	}
	if err := validateVideo(r.Video); err != nil {
		return fmt.Errorf("recording.video: %w", err)
	}

	u := c.Uploader
	if u.Enabled {
		if strings.TrimSpace(u.QueueDB) == "" {
			return errors.New("uploader.queue_db must not be empty")
		}
		if u.Concurrency <= 0 {
			return errors.New("uploader.concurrency must be greater than zero")
		}
		if u.Retry.InitialIntervalSeconds <= 0 {
			return errors.New("uploader.retry.initial_interval_seconds must be greater than zero")
		}
		if u.Retry.MaxIntervalSeconds < u.Retry.InitialIntervalSeconds {
			return errors.New("uploader.retry.max_interval_seconds must be >= initial_interval_seconds")
		}
		if u.Retry.MaxAttempts < 0 {
			return errors.New("uploader.retry.max_attempts must not be negative")
		}
		if strings.TrimSpace(c.Storage.Endpoint) == "" {
			return errors.New("storage.endpoint is required when the uploader is enabled")
		}
		if strings.TrimSpace(c.Storage.Bucket) == "" {
			return errors.New("storage.bucket is required when the uploader is enabled")
		}
		if strings.TrimSpace(c.Storage.AccessKeyID) == "" || strings.TrimSpace(c.Storage.SecretAccessKey) == "" {
			return errors.New("storage.access_key_id and storage.secret_access_key are required when the uploader is enabled")
		}
	}

	switch strings.ToLower(strings.TrimSpace(c.Logging.Level)) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("logging.level %q is not supported", c.Logging.Level)
	}

	if c.Metrics.Enabled {
		if c.Metrics.SampleIntervalSeconds <= 0 {
			return errors.New("metrics.sample_interval_seconds must be greater than zero")
		}
		if c.Metrics.HistorySeconds < c.Metrics.SampleIntervalSeconds {
			return errors.New("metrics.history_seconds must be >= sample_interval_seconds")
		}
	}
	capacity := c.Capacity
	for name, value := range map[string]float64{
		"capacity.cpu_target_percent":     capacity.CPUTargetPercent,
		"capacity.memory_target_percent":  capacity.MemoryTargetPercent,
		"capacity.disk_target_percent":    capacity.DiskTargetPercent,
		"capacity.network_target_percent": capacity.NetworkTargetPercent,
	} {
		if value <= 0 || value > 100 {
			return fmt.Errorf("%s must be greater than zero and at most 100", name)
		}
	}
	if capacity.NetworkMbps < 0 {
		return errors.New("capacity.network_mbps must not be negative")
	}
	if capacity.DiskHorizonHours <= 0 {
		return errors.New("capacity.disk_horizon_hours must be greater than zero")
	}
	if capacity.MinFreeDiskGB < 0 {
		return errors.New("capacity.min_free_disk_gb must not be negative")
	}
	if capacity.AssumedCPUPerStreamPercent < 0 {
		return errors.New("capacity.assumed_cpu_per_stream_percent must not be negative")
	}
	if capacity.AssumedMemoryPerStreamMB < 0 {
		return errors.New("capacity.assumed_memory_per_stream_mb must not be negative")
	}
	if strings.TrimSpace(c.Database.Path) == "" {
		return errors.New("database.path must not be empty")
	}
	if c.Discovery.Enabled {
		if strings.TrimSpace(c.Discovery.RadioBrowserURL) == "" {
			return errors.New("discovery.radio_browser_url must not be empty")
		}
		if c.Discovery.TimeoutSeconds <= 0 {
			return errors.New("discovery.timeout_seconds must be greater than zero")
		}
		if c.Discovery.CacheSeconds < 0 {
			return errors.New("discovery.cache_seconds must not be negative")
		}
		if c.Discovery.MaxResults <= 0 {
			return errors.New("discovery.max_results must be greater than zero")
		}
	}
	return nil
}

func (c Config) validateStream(s *StreamConfig) error {
	if _, err := uuid.Parse(s.ID); err != nil {
		return fmt.Errorf("invalid id %q: must be a UUID", s.ID)
	}
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("name is required")
	}
	if !allowedStreamTypes[s.Type] {
		return fmt.Errorf("unsupported type %q", s.Type)
	}
	if strings.TrimSpace(s.URL) == "" {
		return errors.New("url is required")
	}
	parsed, err := url.Parse(s.URL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if !allowedURLSchemes[strings.ToLower(parsed.Scheme)] {
		return fmt.Errorf("url scheme %q is not allowed", parsed.Scheme)
	}
	if (s.Type == "youtube" || s.Type == "twitch" || s.Type == "web") &&
		parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("type %q requires an http(s) url", s.Type)
	}
	if s.ChunkSeconds < 0 {
		return errors.New("chunk_seconds must not be negative")
	}
	if s.ChunkSeconds == 0 {
		s.ChunkSeconds = c.Recording.ChunkSeconds
	}

	eff := c.effectiveStream(*s)
	if err := validateAudio(*eff.Audio); err != nil {
		return fmt.Errorf("audio: %w", err)
	}
	if err := validateVideo(*eff.Video); err != nil {
		return fmt.Errorf("video: %w", err)
	}
	return nil
}

func validateAudio(a AudioConfig) error {
	if !allowedAudioCodecs[a.Codec] {
		return fmt.Errorf("unsupported codec %q", a.Codec)
	}
	if a.Container == "" {
		return errors.New("container is required")
	}
	accepted, ok := audioContainers[a.Container]
	if !ok {
		return fmt.Errorf("unsupported container %q", a.Container)
	}
	codecOK := false
	for _, codec := range accepted {
		if codec == a.Codec {
			codecOK = true
			break
		}
	}
	if !codecOK {
		return fmt.Errorf("codec %q is not compatible with container %q", a.Codec, a.Container)
	}
	if a.Codec != "copy" {
		if !bitratePattern.MatchString(a.Bitrate) {
			return fmt.Errorf("invalid bitrate %q (expected e.g. 96k)", a.Bitrate)
		}
		if !allowedSampleRates[a.SampleRate] {
			return fmt.Errorf("unsupported sample_rate %d", a.SampleRate)
		}
		if a.Channels != 1 && a.Channels != 2 {
			return fmt.Errorf("channels must be 1 or 2, got %d", a.Channels)
		}
	}
	return nil
}

func validateVideo(v VideoConfig) error {
	if !videoEnabled(v) {
		return nil
	}
	if !allowedVideoCodecs[v.Codec] {
		return fmt.Errorf("unsupported codec %q", v.Codec)
	}
	accepted, ok := videoContainers[v.Container]
	if !ok {
		return fmt.Errorf("unsupported container %q", v.Container)
	}
	codecOK := false
	for _, codec := range accepted {
		if codec == v.Codec {
			codecOK = true
			break
		}
	}
	if !codecOK {
		return fmt.Errorf("codec %q is not compatible with container %q", v.Codec, v.Container)
	}
	if v.Codec != "copy" && !bitratePattern.MatchString(v.Bitrate) {
		return fmt.Errorf("invalid bitrate %q (expected e.g. 2500k)", v.Bitrate)
	}
	return nil
}

func (c Config) effectiveStream(s StreamConfig) StreamConfig {
	out := s
	a := c.Recording.Audio
	if s.Audio != nil {
		if s.Audio.Codec != "" {
			a.Codec = s.Audio.Codec
		}
		if s.Audio.Container != "" {
			a.Container = s.Audio.Container
		}
		if s.Audio.Bitrate != "" {
			a.Bitrate = s.Audio.Bitrate
		}
		if s.Audio.SampleRate != 0 {
			a.SampleRate = s.Audio.SampleRate
		}
		if s.Audio.Channels != 0 {
			a.Channels = s.Audio.Channels
		}
		if s.Audio.VBR != nil {
			a.VBR = s.Audio.VBR
		}
	}
	out.Audio = &a

	v := c.Recording.Video
	if s.Video != nil {
		if s.Video.Enabled != nil {
			v.Enabled = s.Video.Enabled
		}
		if s.Video.Codec != "" {
			v.Codec = s.Video.Codec
		}
		if s.Video.Bitrate != "" {
			v.Bitrate = s.Video.Bitrate
		}
		if s.Video.Container != "" {
			v.Container = s.Video.Container
		}
	}
	out.Video = &v

	if out.ChunkSeconds == 0 {
		out.ChunkSeconds = c.Recording.ChunkSeconds
	}
	if out.Name == "" {
		out.Name = out.ID
	}
	return out
}

func audioVBR(a AudioConfig) bool { return a.VBR != nil && *a.VBR }

func videoEnabled(v VideoConfig) bool { return v.Enabled != nil && *v.Enabled }

func (c Config) streamFormat(s StreamConfig) string {
	if videoEnabled(*s.Video) {
		return s.Video.Container
	}
	return s.Audio.Container
}

// ---------------------------------------------------------------------------
// Logger
// ---------------------------------------------------------------------------

type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Stream  string    `json:"stream,omitempty"`
	Message string    `json:"message"`
}

type Logger struct {
	mu      sync.RWMutex
	level   int
	entries []LogEntry
	max     int
	file    *os.File
}

var logLevels = map[string]int{"debug": 0, "info": 1, "warn": 2, "warning": 2, "error": 3}

func NewLogger(level, filePath string) (*Logger, error) {
	l := &Logger{level: logLevels[strings.ToLower(level)], max: 5000}
	if l.level == 0 && strings.ToLower(level) != "debug" {
		l.level = 1
	}
	if filePath != "" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			return nil, err
		}
		l.file = f
	}
	return l, nil
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

func (l *Logger) Log(level int, stream, msg string) {
	if level < l.level {
		return
	}
	name := "info"
	switch level {
	case 0:
		name = "debug"
	case 2:
		name = "warn"
	case 3:
		name = "error"
	}
	entry := LogEntry{Time: time.Now(), Level: name, Stream: stream, Message: msg}
	line := fmt.Sprintf("%s %-5s [%s] %s\n", entry.Time.Format("2006-01-02T15:04:05Z07:00"), name, stream, msg)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
	if len(l.entries) > l.max {
		l.entries = l.entries[len(l.entries)-l.max:]
	}
	_, _ = io.WriteString(os.Stderr, line)
	if l.file != nil {
		_, _ = io.WriteString(l.file, line)
	}
}

func (l *Logger) Debugf(stream, format string, args ...any) {
	l.Log(0, stream, fmt.Sprintf(format, args...))
}
func (l *Logger) Infof(stream, format string, args ...any) {
	l.Log(1, stream, fmt.Sprintf(format, args...))
}
func (l *Logger) Warnf(stream, format string, args ...any) {
	l.Log(2, stream, fmt.Sprintf(format, args...))
}
func (l *Logger) Errorf(stream, format string, args ...any) {
	l.Log(3, stream, fmt.Sprintf(format, args...))
}

func (l *Logger) Entries(stream, level, search string, limit int) []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	minLevel := -1
	if level != "" {
		if v, ok := logLevels[strings.ToLower(level)]; ok {
			minLevel = v
		}
	}
	search = strings.ToLower(search)
	out := make([]LogEntry, 0, len(l.entries))
	for i := len(l.entries) - 1; i >= 0; i-- {
		e := l.entries[i]
		if stream != "" && e.Stream != stream {
			continue
		}
		if minLevel >= 0 && logLevels[e.Level] < minLevel {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(e.Message), search) && !strings.Contains(strings.ToLower(e.Stream), search) {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Runtime state
// ---------------------------------------------------------------------------

type RuntimeState struct {
	Stopped map[string]bool `json:"stopped"`
}

func loadRuntimeState(path string) (*RuntimeState, error) {
	st := &RuntimeState{Stopped: map[string]bool{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, err
	}
	if st.Stopped == nil {
		st.Stopped = map[string]bool{}
	}
	return st, nil
}

func saveRuntimeState(path string, st *RuntimeState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------------------------------------------------------------------------
// Application
// ---------------------------------------------------------------------------

type App struct {
	cfgPath string
	envPath string

	cfgMu sync.RWMutex
	cfg   *Config

	stateMu   sync.Mutex
	state     *RuntimeState
	statePath string

	logger *Logger

	streams   *StreamStore
	discovery *RadioBrowserClient
	recorder  *Manager
	uploader  *Uploader
	metrics   *MetricsCollector

	startTime time.Time
	ctx       context.Context
	cancel    context.CancelFunc
}

func newApp(cfgPath, envPath string, cfg *Config, logger *Logger, state *RuntimeState, statePath string) *App {
	ctx, cancel := context.WithCancel(context.Background())
	return &App{
		cfgPath: cfgPath, envPath: envPath, cfg: cfg, logger: logger,
		state: state, statePath: statePath, startTime: time.Now(),
		ctx: ctx, cancel: cancel,
	}
}

func (a *App) Config() Config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return *a.cfg
}

func (a *App) loggerFor(id string) *Logger { return a.logger }

func (a *App) saveStateLocked() error {
	return saveRuntimeState(a.statePath, a.state)
}

func (a *App) isStopped(id string) bool {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.state.Stopped[id]
}

func (a *App) setStopped(id string, stopped bool) error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if stopped {
		a.state.Stopped[id] = true
	} else {
		delete(a.state.Stopped, id)
	}
	return a.saveStateLocked()
}

func (a *App) ListStreams(ctx context.Context) ([]StreamConfig, error) {
	return a.streams.List(ctx)
}

func (a *App) FindStream(ctx context.Context, id string) (StreamConfig, bool, error) {
	stream, ok, err := a.streams.Get(ctx, id)
	if err != nil || !ok {
		return StreamConfig{}, ok, err
	}
	return a.Config().effectiveStream(stream), true, nil
}

func (a *App) AddStream(s StreamConfig) (StreamConfig, []string, error) {
	if s.Radio != nil {
		if _, err := uuid.Parse(s.Radio.StationUUID); err != nil {
			return StreamConfig{}, nil, fmt.Errorf("invalid station uuid %q", s.Radio.StationUUID)
		}
		s.ID = s.Radio.StationUUID
		s.Source = "radio-browser"
		if strings.TrimSpace(s.Name) == "" {
			s.Name = s.Radio.Name
		}
		if strings.TrimSpace(s.URL) == "" {
			s.URL = s.Radio.resolvedURL()
		}
		if s.Type == "" {
			if s.Radio.IsHLS() {
				s.Type = "hls"
			} else {
				s.Type = "radio"
			}
		}
		s.Metadata = stationMetadata(s.Radio, s.Metadata)
	} else {
		s.ID = uuid.NewString()
		s.Source = "custom"
	}
	if s.Name == "" {
		s.Name = s.ID
	}
	if err := a.Config().validateStream(&s); err != nil {
		return StreamConfig{}, nil, err
	}
	ctx := context.Background()
	if s.Radio != nil {
		existing, ok, err := a.streams.GetByStationUUID(ctx, s.Radio.StationUUID)
		if err != nil {
			return StreamConfig{}, nil, err
		}
		if ok {
			return StreamConfig{}, nil, fmt.Errorf("%w: station already added as %s", ErrStreamConflict, existing.ID)
		}
	}
	now := time.Now().UTC()
	s.CreatedAt = now
	s.UpdatedAt = now
	if err := a.streams.Create(ctx, s); err != nil {
		return StreamConfig{}, nil, err
	}
	eff := a.Config().effectiveStream(s)

	var warnings []string
	if a.metrics != nil {
		warnings = a.metrics.WarningsForStream(eff)
		for _, warning := range warnings {
			a.logger.Warnf(s.ID, "%s", warning)
		}
	}

	a.logger.Infof(s.ID, "stream added")
	if a.uploader != nil {
		a.uploader.PublishMeta(eff)
	}
	if eff.Enabled && !a.isStopped(eff.ID) {
		a.recorder.StartStream(eff.ID)
	}
	return s, warnings, nil
}

func (a *App) UpdateStream(id string, s StreamConfig) error {
	ctx := context.Background()
	existing, ok, err := a.streams.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrStreamNotFound
	}
	s.ID = id
	s.Source = existing.Source
	s.Radio = existing.Radio
	s.CreatedAt = existing.CreatedAt
	if s.Name == "" {
		s.Name = existing.Name
	}
	if err := a.Config().validateStream(&s); err != nil {
		return err
	}
	if err := a.streams.Update(ctx, s); err != nil {
		return err
	}
	eff := a.Config().effectiveStream(s)

	a.logger.Infof(id, "stream updated")
	if a.uploader != nil {
		a.uploader.PublishMeta(eff)
	}
	if !eff.Enabled {
		a.recorder.StopStream(id, false)
		_ = a.setStopped(id, false)
	} else if a.isStopped(id) {
		a.recorder.StopStream(id, false)
	} else {
		a.recorder.RestartStream(id)
	}
	return nil
}

func (a *App) RemoveStream(id string) error {
	ctx := context.Background()
	if err := a.streams.Delete(ctx, id); err != nil {
		return err
	}
	a.recorder.StopStream(id, false)
	_ = a.setStopped(id, false)
	a.logger.Infof(id, "stream removed")
	return nil
}

func (a *App) StartStream(id string, force bool) error {
	eff, ok, err := a.FindStream(context.Background(), id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrStreamNotFound
	}
	if status, ok := a.recorder.Status(id); ok && status.Running {
		return nil
	}
	if !force && a.Config().Capacity.Enforce && a.metrics != nil {
		snapshot := a.metrics.Snapshot()
		if snapshot.Capacity.Confidence == "measured" && snapshot.Capacity.Status == "at_capacity" {
			return &CapacityExceededError{StreamID: id, Estimate: snapshot.Capacity}
		}
	}
	if err := a.setStopped(id, false); err != nil {
		return err
	}
	a.recorder.StartStream(eff.ID)
	return nil
}

func (a *App) StopStream(id string) error {
	if _, ok, err := a.FindStream(context.Background(), id); err != nil {
		return err
	} else if !ok {
		return ErrStreamNotFound
	}
	if err := a.setStopped(id, true); err != nil {
		return err
	}
	a.recorder.StopStream(id, true)
	return nil
}

func (a *App) RestartStream(id string) error {
	if _, ok, err := a.FindStream(context.Background(), id); err != nil {
		return err
	} else if !ok {
		return ErrStreamNotFound
	}
	if err := a.setStopped(id, false); err != nil {
		return err
	}
	a.recorder.RestartStream(id)
	return nil
}

func stationMetadata(station *RadioStation, existing map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range existing {
		out[key] = value
	}
	set := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	set("source", "radio-browser")
	set("source_id", station.StationUUID)
	set("country", station.Country)
	set("country_code", station.CountryCode)
	set("state", station.State)
	set("language", station.Language)
	set("tags", station.Tags)
	set("codec", station.Codec)
	set("homepage", station.Homepage)
	set("favicon", station.Favicon)
	if station.Bitrate > 0 {
		out["bitrate"] = fmt.Sprintf("%d kbps", station.Bitrate)
	}
	return out
}

func (a *App) publishAllMetadata() {
	if a.uploader == nil {
		return
	}
	streams, err := a.ListStreams(context.Background())
	if err != nil {
		a.logger.Errorf("", "list streams for metadata: %v", err)
		return
	}
	cfg := a.Config()
	for _, base := range streams {
		a.uploader.PublishMeta(cfg.effectiveStream(base))
	}
}

func (a *App) shutdown() {
	a.logger.Infof("", "shutting down")
	a.recorder.StopAll()
	if a.metrics != nil {
		a.metrics.Stop()
	}
	if a.uploader != nil {
		a.uploader.Stop()
	}
	a.cancel()
	if a.streams != nil {
		_ = a.streams.Close()
	}
	a.stateMu.Lock()
	_ = a.saveStateLocked()
	a.stateMu.Unlock()
	_ = a.logger.Close()
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "streamd:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:])
	case "status", "stream", "logs", "config":
		return cmdCLI(args)
	case "help", "-h", "--help":
		printUsage()
		return nil
	case "version", "--version":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try 'streamd help')", args[0])
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `streamd %s — stream capture service

Usage:
  streamd serve                 Start the daemon (foreground)
  streamd status                Show daemon and stream status
  streamd stream list           List streams
  streamd stream show <id>      Show one stream
  streamd stream search         Search Radio Browser stations
  streamd stream add            Add a stream (--radio-uuid or flags)
  streamd stream update <id>    Update a stream
  streamd stream remove <id>    Remove a stream
  streamd stream start <id>     Start recording a stream (--force overrides capacity)
  streamd stream stop <id>      Stop recording a stream
  streamd stream restart <id>   Restart a stream
  streamd logs [stream]         Show service or stream logs
  streamd config validate       Validate configuration

Global flags (per command): --config <path> --env <path> --json
`, version)
}

func cmdServe(args []string) error {
	fs := newFlagSet("serve")
	cfgPath := fs.String("config", defaultConfigPath, "configuration file")
	envPath := fs.String("env", "", "env file (default: .env next to config)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(*cfgPath, *envPath)
	if err != nil {
		return err
	}
	logger, err := NewLogger(cfg.Logging.Level, cfg.Logging.File)
	if err != nil {
		return fmt.Errorf("initialize logger: %w", err)
	}
	logger.Infof("", "streamd %s starting", version)

	statePath := filepath.Join(filepath.Dir(cfg.Uploader.QueueDB), "runtime.json")
	state, err := loadRuntimeState(statePath)
	if err != nil {
		logger.Errorf("", "load runtime state: %v", err)
		state = &RuntimeState{Stopped: map[string]bool{}}
	}

	streams, err := OpenStreamStore(cfg.Database.Path)
	if err != nil {
		logger.Errorf("", "open stream database: %v", err)
		_ = logger.Close()
		return fmt.Errorf("open stream database: %w", err)
	}
	app := newApp(*cfgPath, *envPath, cfg, logger, state, statePath)
	app.streams = streams
	if cfg.Discovery.Enabled {
		app.discovery = NewRadioBrowserClient(cfg.Discovery)
	}
	app.recorder = NewManager(app)

	if cfg.Uploader.Enabled {
		up, err := NewUploader(app)
		if err != nil {
			logger.Errorf("", "initialize uploader: %v", err)
		} else {
			app.uploader = up
			up.Start()
		}
	}

	app.metrics = newMetricsCollector(app)
	app.metrics.Start()

	web := NewWebServer(app)
	if err := web.Start(); err != nil {
		app.shutdown()
		return err
	}
	logger.Infof("", "control API listening on %s (socket %s)", cfg.Server.Listen, cfg.Server.Socket)

	app.publishAllMetadata()
	app.recorder.StartEnabled()

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-sigCtx.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = web.Shutdown(ctx)
	app.shutdown()
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func httpBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
