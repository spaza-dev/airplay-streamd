package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed all:web/dist
var embeddedWeb embed.FS

type StreamView struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	URL          string         `json:"url"`
	Enabled      bool           `json:"enabled"`
	ChunkSeconds int            `json:"chunk_seconds"`
	Audio        *AudioConfig   `json:"audio"`
	Video        *VideoConfig   `json:"video"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Running      bool           `json:"running"`
	Stopped      bool           `json:"stopped"`
	Error        string         `json:"error,omitempty"`
	LastActivity *time.Time     `json:"last_activity,omitempty"`
}

type QueueItemView struct {
	ID            int64      `json:"id"`
	StreamID      string     `json:"stream_id"`
	File          string     `json:"file"`
	ObjectKey     string     `json:"object_key"`
	FileSize      int64      `json:"file_size"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UploadedAt    *time.Time `json:"uploaded_at,omitempty"`
}

type WebServer struct {
	app *App

	mu           sync.Mutex
	tcp          *http.Server
	unix         *http.Server
	unixListener net.Listener
}

func NewWebServer(app *App) *WebServer {
	return &WebServer{app: app}
}

func (w *WebServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", w.handleHealth)
	mux.HandleFunc("GET /control/status", w.handleStatus)
	mux.HandleFunc("GET /control/streams", w.handleStreams)
	mux.HandleFunc("POST /control/streams", w.handleAddStream)
	mux.HandleFunc("GET /control/streams/{id}", w.handleStream)
	mux.HandleFunc("PUT /control/streams/{id}", w.handleUpdateStream)
	mux.HandleFunc("DELETE /control/streams/{id}", w.handleRemoveStream)
	mux.HandleFunc("POST /control/streams/{id}/start", w.handleStartStream)
	mux.HandleFunc("POST /control/streams/{id}/stop", w.handleStopStream)
	mux.HandleFunc("POST /control/streams/{id}/restart", w.handleRestartStream)
	mux.HandleFunc("GET /control/logs", w.handleLogs)
	mux.HandleFunc("GET /control/logs/stream/{id}", w.handleStreamLogs)
	mux.HandleFunc("GET /control/uploader/status", w.handleUploaderStatus)
	mux.HandleFunc("GET /control/uploader/queue", w.handleUploaderQueue)
	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
		mux.HandleFunc(method+" /control/", func(rw http.ResponseWriter, r *http.Request) {
			writeError(rw, http.StatusNotFound, "not found")
		})
	}

	sub, err := fs.Sub(embeddedWeb, "web/dist")
	if err != nil {
		return err
	}
	mux.Handle("GET /", spaHandler(sub))

	cfg := w.app.Config()
	tcpListener, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Server.Listen, err)
	}
	w.mu.Lock()
	w.tcp = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	w.mu.Unlock()
	go func() {
		if err := w.tcp.Serve(tcpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			w.app.logger.Errorf("", "http server: %v", err)
		}
	}()

	if socket := cfg.Server.Socket; socket != "" {
		ul, err := systemdListener()
		if err != nil {
			w.app.logger.Warnf("", "systemd socket activation failed: %v", err)
		}
		if ul == nil {
			_ = os.MkdirAll(filepath.Dir(socket), 0o750)
			_ = os.Remove(socket)
			ul, err = net.Listen("unix", socket)
			if err != nil {
				w.app.logger.Warnf("", "unix socket %s unavailable: %v", socket, err)
			} else {
				_ = os.Chmod(socket, 0o660)
			}
		}
		if ul != nil {
			w.mu.Lock()
			w.unixListener = ul
			w.unix = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			w.mu.Unlock()
			go func() {
				if err := w.unix.Serve(ul); err != nil && !errors.Is(err, http.ErrServerClosed) {
					w.app.logger.Warnf("", "unix server: %v", err)
				}
			}()
		}
	}
	return nil
}

func (w *WebServer) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	tcp, unixSrv, ul := w.tcp, w.unix, w.unixListener
	w.mu.Unlock()
	var firstErr error
	if tcp != nil {
		if err := tcp.Shutdown(ctx); err != nil {
			firstErr = err
		}
	}
	if unixSrv != nil {
		if err := unixSrv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if ul != nil {
		_ = ul.Close()
	}
	return firstErr
}

func (w *WebServer) handleHealth(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        version,
		"uptime_seconds": int(time.Since(w.app.startTime).Seconds()),
	})
}

func (w *WebServer) handleStatus(rw http.ResponseWriter, r *http.Request) {
	cfg := w.app.Config()
	payload := map[string]any{
		"version":        version,
		"uptime_seconds": int(time.Since(w.app.startTime).Seconds()),
		"streams":        len(cfg.Streams),
		"uploader":       uploaderStatus(w.app),
	}
	writeJSON(rw, http.StatusOK, payload)
}

func (w *WebServer) handleStreams(rw http.ResponseWriter, r *http.Request) {
	cfg := w.app.Config()
	statuses := w.app.recorder.Statuses()
	views := make([]StreamView, 0, len(cfg.Streams))
	for _, base := range cfg.Streams {
		eff, ok := cfg.FindStream(base.ID)
		if !ok {
			continue
		}
		views = append(views, makeStreamView(w.app, eff, statuses[base.ID]))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	writeJSON(rw, http.StatusOK, map[string]any{"streams": views, "uploader": uploaderStatus(w.app)})
}

func (w *WebServer) handleStream(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := w.app.Config()
	eff, ok := cfg.FindStream(id)
	if !ok {
		writeError(rw, http.StatusNotFound, "stream not found")
		return
	}
	st, _ := w.app.recorder.Status(id)
	writeJSON(rw, http.StatusOK, makeStreamView(w.app, eff, st))
}

func (w *WebServer) handleAddStream(rw http.ResponseWriter, r *http.Request) {
	var s StreamConfig
	if err := decodeJSON(r, &s); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if err := w.app.AddStream(s); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusCreated, map[string]any{"ok": true, "id": s.ID})
}

func (w *WebServer) handleUpdateStream(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var s StreamConfig
	if err := decodeJSON(r, &s); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if err := w.app.UpdateStream(id, s); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (w *WebServer) handleRemoveStream(rw http.ResponseWriter, r *http.Request) {
	if err := w.app.RemoveStream(r.PathValue("id")); err != nil {
		writeError(rw, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *WebServer) handleStartStream(rw http.ResponseWriter, r *http.Request) {
	if err := w.app.StartStream(r.PathValue("id")); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *WebServer) handleStopStream(rw http.ResponseWriter, r *http.Request) {
	if err := w.app.StopStream(r.PathValue("id")); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *WebServer) handleRestartStream(rw http.ResponseWriter, r *http.Request) {
	if err := w.app.RestartStream(r.PathValue("id")); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *WebServer) handleLogs(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := atoiOr(q.Get("limit"), 200)
	entries := w.app.logger.Entries(q.Get("stream"), q.Get("level"), q.Get("search"), limit)
	writeJSON(rw, http.StatusOK, map[string]any{"entries": entries})
}

func (w *WebServer) handleStreamLogs(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := atoiOr(q.Get("limit"), 200)
	entries := w.app.logger.Entries(r.PathValue("id"), q.Get("level"), q.Get("search"), limit)
	writeJSON(rw, http.StatusOK, map[string]any{"entries": entries})
}

func (w *WebServer) handleUploaderStatus(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, uploaderStatus(w.app))
}

func (w *WebServer) handleUploaderQueue(rw http.ResponseWriter, r *http.Request) {
	if w.app.uploader == nil {
		writeJSON(rw, http.StatusOK, map[string]any{"queue": []QueueItemView{}})
		return
	}
	q := r.URL.Query()
	limit := atoiOr(q.Get("limit"), 100)
	items, err := w.app.uploader.Queue(q.Get("status"), limit)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]QueueItemView, 0, len(items))
	for _, item := range items {
		views = append(views, QueueItemView{
			ID: item.ID, StreamID: item.StreamID,
			File: filepath.Base(item.LocalPath), ObjectKey: item.ObjectKey,
			FileSize: item.FileSize, Status: item.Status, Attempts: item.Attempts,
			NextAttemptAt: item.NextAttemptAt, LastError: item.LastError,
			CreatedAt: item.CreatedAt, UploadedAt: item.UploadedAt,
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"queue": views})
}

func makeStreamView(app *App, eff StreamConfig, st WorkerStatus) StreamView {
	return StreamView{
		ID: eff.ID, Name: eff.Name, Type: eff.Type, URL: eff.URL,
		Enabled: eff.Enabled, ChunkSeconds: eff.ChunkSeconds,
		Audio: eff.Audio, Video: eff.Video, Metadata: eff.Metadata,
		Running: st.Running, Stopped: app.isStopped(eff.ID), Error: st.Error,
		LastActivity: st.LastActivity,
	}
}

func uploaderStatus(app *App) UploaderStatus {
	if app.uploader == nil {
		return UploaderStatus{
			Enabled: false,
			Counts:  map[string]int{"pending": 0, "uploading": 0, "failed": 0, "completed": 0},
		}
	}
	st, err := app.uploader.Status()
	if err != nil {
		return UploaderStatus{
			Enabled: true,
			Counts:  map[string]int{"pending": 0, "uploading": 0, "failed": 0, "completed": 0},
		}
	}
	return st
}

func systemdListener() (net.Listener, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, nil
	}
	if os.Getenv("LISTEN_FDS") != "1" {
		return nil, nil
	}
	f := os.NewFile(uintptr(3), "systemd-socket")
	if f == nil {
		return nil, nil
	}
	defer f.Close()
	l, err := net.FileListener(f)
	if err != nil {
		return nil, err
	}
	return l, nil
}

func spaHandler(sub fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" || p == "." {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(rw, r)
	})
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}

func writeJSON(rw http.ResponseWriter, status int, payload any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(payload)
}

func writeError(rw http.ResponseWriter, status int, msg string) {
	writeJSON(rw, status, map[string]any{"error": msg})
}

func parseLimit(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
