package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var segmentFormats = map[string]string{
	"ogg":  "ogg",
	"opus": "opus",
	"m4a":  "mp4",
	"aac":  "adts",
	"mp3":  "mp3",
	"mp4":  "mp4",
	"mkv":  "matroska",
	"webm": "webm",
}

type WorkerStatus struct {
	Running      bool       `json:"running"`
	Stopped      bool       `json:"stopped"`
	Error        string     `json:"error,omitempty"`
	LastActivity *time.Time `json:"last_activity,omitempty"`
	LastChunkAt  *time.Time `json:"last_chunk_at,omitempty"`
	ChunksTotal  int64      `json:"chunks_total"`
	BytesTotal   int64      `json:"bytes_total"`
}

type Manager struct {
	app *App

	mu      sync.Mutex
	workers map[string]*Worker
}

func NewManager(app *App) *Manager {
	m := &Manager{app: app, workers: map[string]*Worker{}}
	m.cleanupIncoming()
	return m
}

func (m *Manager) StartEnabled() {
	cfg := m.app.Config()
	for _, base := range cfg.Streams {
		if !base.Enabled || m.app.isStopped(base.ID) {
			continue
		}
		m.StartStream(base.ID)
	}
}

func (m *Manager) StartStream(id string) {
	m.mu.Lock()
	if w, ok := m.workers[id]; ok {
		if w.isRunning() {
			m.mu.Unlock()
			return
		}
		// Stale worker record; remove it and start a fresh one.
		delete(m.workers, id)
	}
	m.mu.Unlock()

	cfg := m.app.Config()
	eff, ok := cfg.FindStream(id)
	if !ok {
		m.app.logger.Errorf(id, "cannot start: stream not found")
		return
	}

	ctx, cancel := context.WithCancel(m.app.ctx)
	w := &Worker{
		app:    m.app,
		stream: eff,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.mu.Lock()
	m.workers[id] = w
	m.mu.Unlock()
	m.app.logger.Infof(id, "starting recording worker (%s)", eff.Type)
	go w.run()
}

func (m *Manager) StopStream(id string, manual bool) {
	m.mu.Lock()
	w := m.workers[id]
	delete(m.workers, id)
	m.mu.Unlock()
	if w == nil {
		return
	}
	m.app.logger.Infof(id, "stopping recording worker")
	w.stop()
	<-w.done
}

func (m *Manager) RestartStream(id string) {
	m.StopStream(id, false)
	m.StartStream(id)
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	workers := make([]*Worker, 0, len(m.workers))
	for _, w := range m.workers {
		workers = append(workers, w)
	}
	m.workers = map[string]*Worker{}
	m.mu.Unlock()

	for _, w := range workers {
		w.stop()
	}
	for _, w := range workers {
		<-w.done
	}
}

func (m *Manager) Status(id string) (WorkerStatus, bool) {
	m.mu.Lock()
	w, ok := m.workers[id]
	m.mu.Unlock()
	if !ok {
		return WorkerStatus{Stopped: m.app.isStopped(id)}, false
	}
	return w.status(), true
}

func (m *Manager) Statuses() map[string]WorkerStatus {
	m.mu.Lock()
	workers := make(map[string]*Worker, len(m.workers))
	for id, w := range m.workers {
		workers[id] = w
	}
	m.mu.Unlock()

	out := make(map[string]WorkerStatus, len(workers))
	for id, w := range workers {
		out[id] = w.status()
	}
	return out
}

func (m *Manager) ProcessSnapshot() map[string]int {
	m.mu.Lock()
	workers := make(map[string]*Worker, len(m.workers))
	for id, w := range m.workers {
		workers[id] = w
	}
	m.mu.Unlock()

	out := make(map[string]int, len(workers))
	for id, w := range workers {
		if pid := w.pid(); pid > 0 {
			out[id] = pid
		}
	}
	return out
}

func (m *Manager) cleanupIncoming() {
	root := m.app.Config().Recording.OutputDir
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		incoming := filepath.Join(root, entry.Name(), ".incoming")
		files, err := os.ReadDir(incoming)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			_ = os.Remove(filepath.Join(incoming, f.Name()))
			m.app.logger.Warnf(entry.Name(), "removed incomplete chunk %s", f.Name())
		}
	}
}

type Worker struct {
	app    *App
	stream StreamConfig
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu           sync.Mutex
	running      bool
	errMsg       string
	lastActivity time.Time
	lastChunkAt  time.Time
	chunksTotal  int64
	bytesTotal   int64
	cmd          *exec.Cmd
}

func (w *Worker) setRunning(v bool) {
	w.mu.Lock()
	w.running = v
	w.mu.Unlock()
}

func (w *Worker) setError(err error) {
	w.mu.Lock()
	if err != nil {
		w.errMsg = err.Error()
	} else {
		w.errMsg = ""
	}
	w.mu.Unlock()
}

func (w *Worker) recordChunk(size int64, t time.Time) {
	w.mu.Lock()
	w.lastActivity = t
	w.lastChunkAt = t
	w.errMsg = ""
	w.chunksTotal++
	w.bytesTotal += size
	w.mu.Unlock()
}

func (w *Worker) status() WorkerStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := WorkerStatus{
		Running:     w.running,
		Error:       w.errMsg,
		ChunksTotal: w.chunksTotal,
		BytesTotal:  w.bytesTotal,
	}
	if !w.lastActivity.IsZero() {
		t := w.lastActivity
		st.LastActivity = &t
	}
	if !w.lastChunkAt.IsZero() {
		t := w.lastChunkAt
		st.LastChunkAt = &t
	}
	return st
}

func (w *Worker) isRunning() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

func (w *Worker) pid() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cmd != nil && w.cmd.Process != nil {
		return w.cmd.Process.Pid
	}
	return 0
}

func (w *Worker) setCmd(cmd *exec.Cmd) {
	w.mu.Lock()
	w.cmd = cmd
	w.mu.Unlock()
}

func (w *Worker) clearCmd() {
	w.mu.Lock()
	w.cmd = nil
	w.mu.Unlock()
}

func (w *Worker) stop() {
	w.cancel()
	w.mu.Lock()
	cmd := w.cmd
	w.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

func (w *Worker) run() {
	defer close(w.done)
	w.setRunning(true)
	defer w.setRunning(false)

	cfg := w.app.Config()
	initial := time.Duration(cfg.Recording.ReconnectDelaySeconds) * time.Second
	maxDelay := time.Duration(cfg.Recording.MaxReconnectDelaySeconds) * time.Second
	delay := initial

	for {
		if w.ctx.Err() != nil {
			return
		}

		url, err := w.resolveURL()
		if err != nil {
			w.setError(err)
			w.app.logger.Warnf(w.stream.ID, "source resolution failed: %v", err)
			if !sleepContext(w.ctx, delay) {
				return
			}
			delay = growDelay(delay, maxDelay)
			continue
		}

		started := time.Now()
		err = w.record(url)
		if w.ctx.Err() != nil {
			return
		}
		if err != nil {
			w.setError(err)
			w.app.logger.Warnf(w.stream.ID, "recording stopped: %v", err)
		}
		if time.Since(started) > 2*time.Minute {
			delay = initial
		}
		if !sleepContext(w.ctx, delay) {
			return
		}
		delay = growDelay(delay, maxDelay)
	}
}

func growDelay(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		next = max
	}
	return next
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (w *Worker) resolveURL() (string, error) {
	if !needsYtDLP(w.stream.Type) {
		return w.stream.URL, nil
	}
	cfg := w.app.Config()
	bin := cfg.Recording.YtDlpPath
	if _, err := os.Stat(bin); err != nil {
		if p, lookErr := exec.LookPath("yt-dlp"); lookErr == nil {
			bin = p
		}
	}
	ctx, cancel := context.WithTimeout(w.ctx, 60*time.Second)
	defer cancel()

	format := "bestaudio/best"
	if videoEnabled(*w.stream.Video) {
		format = "best"
	}
	args := []string{"--no-playlist", "--no-warnings", "--no-progress", "-g", "-f", format, w.stream.URL}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("yt-dlp: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			w.app.logger.Debugf(w.stream.ID, "resolved source via yt-dlp")
			return line, nil
		}
	}
	return "", errors.New("yt-dlp returned no media url")
}

func (w *Worker) record(sourceURL string) error {
	cfg := w.app.Config()
	pattern, err := w.prepareOutput(cfg)
	if err != nil {
		return err
	}

	args := w.ffmpegArgs(cfg, sourceURL, pattern)
	w.app.logger.Debugf(w.stream.ID, "ffmpeg %s", strings.Join(redactArgs(args), " "))
	cmd := exec.Command(cfg.Recording.FFmpegPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = &lineWriter{logger: w.app.logger, stream: w.stream.ID, prefix: "ffmpeg: "}

	w.setCmd(cmd)
	if err := cmd.Start(); err != nil {
		w.clearCmd()
		return err
	}

	exited := make(chan struct{})
	go func() {
		select {
		case <-w.ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			}
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			select {
			case <-exited:
				return
			case <-timer.C:
				if cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
			}
		case <-exited:
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	seen := map[string]bool{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		w.handleSegmentLine(cfg, line, seen)
	}
	if err := scanner.Err(); err != nil {
		w.app.logger.Warnf(w.stream.ID, "segment list read error: %v", err)
	}

	err = cmd.Wait()
	close(exited)
	w.clearCmd()
	w.flushIncoming(cfg, seen)
	return err
}

func (w *Worker) flushIncoming(cfg Config, seen map[string]bool) {
	incoming := filepath.Join(cfg.Recording.OutputDir, w.stream.ID, ".incoming")
	entries, err := os.ReadDir(incoming)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if seen[filepath.Join(incoming, entry.Name())] {
			continue
		}
		w.handleSegmentLine(cfg, entry.Name(), seen)
	}
}

func (w *Worker) prepareOutput(cfg Config) (string, error) {
	format := cfg.streamFormat(w.stream)
	incoming := filepath.Join(cfg.Recording.OutputDir, w.stream.ID, ".incoming")
	if err := os.MkdirAll(incoming, 0o750); err != nil {
		return "", err
	}
	if entries, err := os.ReadDir(incoming); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				_ = os.Remove(filepath.Join(incoming, entry.Name()))
			}
		}
	}
	return filepath.Join(incoming, "%Y%m%d%H%M%S."+format), nil
}

func (w *Worker) ffmpegArgs(cfg Config, sourceURL, pattern string) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}

	if usesHTTPReconnect(w.stream.Type) {
		args = append(args, "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "5")
	}
	args = append(args, "-i", sourceURL)

	audio := *w.stream.Audio
	video := *w.stream.Video
	if !videoEnabled(video) {
		args = append(args, "-vn")
	} else if video.Codec == "copy" {
		args = append(args, "-c:v", "copy")
	} else {
		args = append(args, "-c:v", video.Codec, "-b:v", video.Bitrate)
	}

	if audio.Codec == "copy" {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args,
			"-c:a", audio.Codec,
			"-b:a", audio.Bitrate,
			"-ar", strconv.Itoa(audio.SampleRate),
			"-ac", strconv.Itoa(audio.Channels),
		)
		if audio.Codec == "libopus" {
			if audioVBR(audio) {
				args = append(args, "-vbr", "on")
			} else {
				args = append(args, "-vbr", "off")
			}
		}
	}

	container := cfg.streamFormat(w.stream)
	segmentFormat := segmentFormats[container]
	if segmentFormat == "" {
		segmentFormat = container
	}
	args = append(args,
		"-f", "segment",
		"-segment_time", strconv.Itoa(w.stream.ChunkSeconds),
		"-reset_timestamps", "1",
		"-strftime", "1",
		"-segment_list", "pipe:1",
		"-segment_list_type", "csv",
		"-segment_format", segmentFormat,
		pattern,
	)
	return args
}

func (w *Worker) handleSegmentLine(cfg Config, line string, seen map[string]bool) {
	reader := csv.NewReader(strings.NewReader(line))
	reader.FieldsPerRecord = -1
	record, err := reader.Read()
	if err != nil || len(record) == 0 {
		w.app.logger.Warnf(w.stream.ID, "unexpected segment list line: %q", line)
		return
	}
	path := strings.TrimSpace(record[0])
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cfg.Recording.OutputDir, w.stream.ID, ".incoming", path)
	}
	if seen[path] {
		if _, err := os.Stat(path); err != nil {
			return
		}
	}
	seen[path] = true

	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	start, err := time.ParseInLocation("20060102150405", name, time.Local)
	if err != nil {
		w.app.logger.Warnf(w.stream.ID, "cannot parse chunk timestamp %q: %v", name, err)
		return
	}

	dateDir := filepath.Join(cfg.Recording.OutputDir, w.stream.ID, start.Format("2006-01-02"))
	if err := os.MkdirAll(dateDir, 0o750); err != nil {
		w.app.logger.Errorf(w.stream.ID, "create chunk directory: %v", err)
		return
	}

	target := uniquePath(filepath.Join(dateDir, start.Format("150405")+ext))
	if err := os.Rename(path, target); err != nil {
		w.app.logger.Errorf(w.stream.ID, "finalize chunk: %v", err)
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		w.app.logger.Errorf(w.stream.ID, "stat chunk: %v", err)
		return
	}
	if info.Size() == 0 {
		_ = os.Remove(target)
		w.app.logger.Warnf(w.stream.ID, "discarded empty chunk %s", filepath.Base(target))
		return
	}

	key := fmt.Sprintf("%s/recordings/%s/%s", w.stream.ID, start.Format("2006/01/02"), filepath.Base(target))
	if w.app.uploader != nil {
		if err := w.app.uploader.Enqueue(QueueItem{
			StreamID:  w.stream.ID,
			LocalPath: target,
			ObjectKey: key,
			FileSize:  info.Size(),
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			w.app.logger.Errorf(w.stream.ID, "register upload: %v", err)
		}
	}
	w.recordChunk(info.Size(), time.Now())
	w.app.logger.Infof(w.stream.ID, "finalized chunk %s (%d bytes) key=%s", filepath.Base(target), info.Size(), key)
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return path
}

func needsYtDLP(streamType string) bool {
	switch streamType {
	case "youtube", "twitch", "web":
		return true
	default:
		return false
	}
}

func usesHTTPReconnect(streamType string) bool {
	switch streamType {
	case "radio", "tv", "hls", "direct":
		return true
	default:
		return false
	}
}

func redactArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := range out {
		if i > 0 && (out[i-1] == "-i") {
			out[i] = "<source>"
		}
	}
	return out
}

type lineWriter struct {
	logger *Logger
	stream string
	prefix string

	mu  sync.Mutex
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buf[:idx]))
		w.buf = w.buf[idx+1:]
		if line != "" {
			w.logger.Warnf(w.stream, "%s%s", w.prefix, line)
		}
	}
	if len(w.buf) > 64*1024 {
		w.buf = w.buf[len(w.buf)-64*1024:]
	}
	return len(p), nil
}
