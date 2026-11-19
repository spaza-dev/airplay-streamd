package main

import (
	"context"
	"crypto/subtle"
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

	"github.com/google/uuid"
)

//go:embed all:web/dist
var embeddedWeb embed.FS

//go:embed openapi.json
var openAPIDoc []byte

type StreamView struct {
	ID           string         `json:"id"`
	Source       string         `json:"source"`
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	URL          string         `json:"url"`
	Enabled      bool           `json:"enabled"`
	ChunkSeconds int            `json:"chunk_seconds"`
	Audio        *AudioConfig   `json:"audio"`
	Video        *VideoConfig   `json:"video"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Radio        *RadioStation  `json:"radio,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	Running      bool           `json:"running"`
	Stopped      bool           `json:"stopped"`
	Error        string         `json:"error,omitempty"`
	LastActivity *time.Time     `json:"last_activity,omitempty"`
}

type PublicStreamView struct {
	ID           string         `json:"id"`
	Source       string         `json:"source"`
	Name         string         `json:"name"`
	Status       string         `json:"status"`
	Type         string         `json:"type"`
	URL          string         `json:"url"`
	Enabled      bool           `json:"enabled"`
	ChunkSeconds int            `json:"chunk_seconds"`
	Country      string         `json:"country,omitempty"`
	Audio        *AudioConfig   `json:"audio"`
	Video        *VideoConfig   `json:"video"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Radio        *RadioStation  `json:"radio,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	LastActivity *time.Time     `json:"last_activity"`
	Error        string         `json:"error,omitempty"`
}

type PublicStreamsResponse struct {
	Streams []PublicStreamView `json:"streams"`
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
	handler, err := w.handler()
	if err != nil {
		return err
	}

	cfg := w.app.Config()
	tcpListener, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Server.Listen, err)
	}
	w.mu.Lock()
	w.tcp = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
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
			w.unix = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
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

func (w *WebServer) handler() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", w.handleHealth)
	mux.HandleFunc("GET /api/streams", w.handlePublicStreams)
	mux.HandleFunc("GET /openapi.json", w.handleOpenAPI)
	mux.HandleFunc("GET /docs", w.handleDocs)
	mux.HandleFunc("GET /control/status", w.handleStatus)
	mux.HandleFunc("GET /control/metrics", w.handleMetrics)
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
	mux.HandleFunc("GET /control/discovery/radio", w.handleDiscoveryRadio)
	mux.HandleFunc("GET /control/discovery/radio/{uuid}", w.handleDiscoveryRadioStation)
	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
		mux.HandleFunc(method+" /control/", func(rw http.ResponseWriter, r *http.Request) {
			writeError(rw, http.StatusNotFound, "not found")
		})
		mux.HandleFunc(method+" /api/", func(rw http.ResponseWriter, r *http.Request) {
			writeError(rw, http.StatusNotFound, "not found")
		})
	}

	sub, err := fs.Sub(embeddedWeb, "web/dist")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /", spaHandler(sub))
	return w.authMiddleware(mux), nil
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

func (w *WebServer) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(rw, r)
			return
		}
		token := w.app.Config().Server.AdminToken
		if token == "" {
			next.ServeHTTP(rw, r)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if provided == "" {
			provided = r.Header.Get("X-Admin-Token")
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			writeError(rw, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func (w *WebServer) handlePublicStreams(rw http.ResponseWriter, r *http.Request) {
	streams, err := w.app.ListStreams(r.Context())
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	statuses := w.app.recorder.Statuses()
	cfg := w.app.Config()
	views := make([]PublicStreamView, 0, len(streams))
	for _, base := range streams {
		if !base.Enabled {
			continue
		}
		views = append(views, makePublicStreamView(w.app, cfg.effectiveStream(base), statuses[base.ID]))
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Name != views[j].Name {
			return views[i].Name < views[j].Name
		}
		return views[i].ID < views[j].ID
	})
	writeJSON(rw, http.StatusOK, PublicStreamsResponse{Streams: views})
}

func (w *WebServer) handleOpenAPI(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write(openAPIDoc)
}

func (w *WebServer) handleDocs(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(rw, `<!doctype html>
<html><head><meta charset="utf-8"><title>streamd API</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"></head>
<body><div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>window.onload=function(){SwaggerUIBundle({url:"/openapi.json",dom_id:"#swagger-ui"});};</script>
</body></html>`)
}

func (w *WebServer) handleHealth(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        version,
		"uptime_seconds": int(time.Since(w.app.startTime).Seconds()),
	})
}

func (w *WebServer) handleStatus(rw http.ResponseWriter, r *http.Request) {
	streams, err := w.app.ListStreams(r.Context())
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	payload := map[string]any{
		"version":        version,
		"uptime_seconds": int(time.Since(w.app.startTime).Seconds()),
		"streams":        len(streams),
		"uploader":       uploaderStatus(w.app),
		"capacity":       w.app.metrics.Snapshot().Capacity,
	}
	writeJSON(rw, http.StatusOK, payload)
}

func (w *WebServer) handleMetrics(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, w.app.metrics.Snapshot())
}

func (w *WebServer) handleStreams(rw http.ResponseWriter, r *http.Request) {
	streams, err := w.app.ListStreams(r.Context())
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	statuses := w.app.recorder.Statuses()
	cfg := w.app.Config()
	views := make([]StreamView, 0, len(streams))
	for _, base := range streams {
		views = append(views, makeStreamView(w.app, cfg.effectiveStream(base), statuses[base.ID]))
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Name != views[j].Name {
			return views[i].Name < views[j].Name
		}
		return views[i].ID < views[j].ID
	})
	writeJSON(rw, http.StatusOK, map[string]any{"streams": views, "uploader": uploaderStatus(w.app)})
}

func (w *WebServer) handleStream(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	eff, ok, err := w.app.FindStream(r.Context(), id)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
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
	created, warnings, err := w.app.AddStream(s)
	if err != nil {
		writeStreamError(rw, err)
		return
	}
	payload := map[string]any{"ok": true, "id": created.ID}
	if len(warnings) > 0 {
		payload["warnings"] = warnings
	}
	writeJSON(rw, http.StatusCreated, payload)
}

func (w *WebServer) handleUpdateStream(rw http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var s StreamConfig
	if err := decodeJSON(r, &s); err != nil {
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if err := w.app.UpdateStream(id, s); err != nil {
		writeStreamError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (w *WebServer) handleRemoveStream(rw http.ResponseWriter, r *http.Request) {
	if err := w.app.RemoveStream(r.PathValue("id")); err != nil {
		writeStreamError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (w *WebServer) handleStartStream(rw http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force")
	err := w.app.StartStream(r.PathValue("id"), force == "true" || force == "1")
	if err != nil {
		var capacityErr *CapacityExceededError
		if errors.As(err, &capacityErr) {
			writeJSON(rw, http.StatusConflict, map[string]any{
				"error":    err.Error(),
				"capacity": capacityErr.Estimate,
			})
			return
		}
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

func (w *WebServer) handleDiscoveryRadio(rw http.ResponseWriter, r *http.Request) {
	if w.app.discovery == nil {
		writeError(rw, http.StatusNotFound, "radio discovery is disabled")
		return
	}
	query := r.URL.Query()
	reverse := true
	if raw := strings.TrimSpace(query.Get("reverse")); raw != "" {
		reverse, _ = strconv.ParseBool(raw)
	}
	hideBroken := true
	if raw := strings.TrimSpace(query.Get("hidebroken")); raw != "" {
		hideBroken, _ = strconv.ParseBool(raw)
	}
	q := RadioQuery{
		Name:        query.Get("name"),
		Country:     query.Get("country"),
		CountryCode: query.Get("countrycode"),
		State:       query.Get("state"),
		Language:    query.Get("language"),
		Tag:         query.Get("tag"),
		Codec:       query.Get("codec"),
		Limit:       atoiOr(query.Get("limit"), 20),
		Offset:      atoiOr(query.Get("offset"), 0),
		HideBroken:  hideBroken,
		Order:       query.Get("order"),
		Reverse:     reverse,
	}
	stations, err := w.app.discovery.Search(r.Context(), q)
	if err != nil {
		writeError(rw, http.StatusBadGateway, err.Error())
		return
	}
	added, err := w.app.streams.StationUUIDs(r.Context())
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]DiscoveryStation, 0, len(stations))
	for _, station := range stations {
		view := DiscoveryStation{RadioStation: station}
		if id, ok := added[station.StationUUID]; ok {
			view.AlreadyAdded = true
			view.ExistingStreamID = id
		}
		views = append(views, view)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"stations": views})
}

func (w *WebServer) handleDiscoveryRadioStation(rw http.ResponseWriter, r *http.Request) {
	if w.app.discovery == nil {
		writeError(rw, http.StatusNotFound, "radio discovery is disabled")
		return
	}
	stationUUID := r.PathValue("uuid")
	if _, err := uuid.Parse(stationUUID); err != nil {
		writeError(rw, http.StatusBadRequest, "invalid station uuid")
		return
	}
	station, err := w.app.discovery.ByUUID(r.Context(), stationUUID)
	if err != nil {
		if errors.Is(err, ErrStreamNotFound) {
			writeError(rw, http.StatusNotFound, "station not found")
			return
		}
		writeError(rw, http.StatusBadGateway, err.Error())
		return
	}
	view := DiscoveryStation{RadioStation: station}
	added, err := w.app.streams.StationUUIDs(r.Context())
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if id, ok := added[station.StationUUID]; ok {
		view.AlreadyAdded = true
		view.ExistingStreamID = id
	}
	writeJSON(rw, http.StatusOK, view)
}

func writeStreamError(rw http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrStreamNotFound):
		writeError(rw, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrStreamConflict):
		writeError(rw, http.StatusConflict, err.Error())
	default:
		writeError(rw, http.StatusBadRequest, err.Error())
	}
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
		ID: eff.ID, Source: eff.Source, Name: eff.Name, Type: eff.Type, URL: eff.URL,
		Enabled: eff.Enabled, ChunkSeconds: eff.ChunkSeconds,
		Audio: eff.Audio, Video: eff.Video, Metadata: eff.Metadata, Radio: eff.Radio,
		CreatedAt: eff.CreatedAt, UpdatedAt: eff.UpdatedAt,
		Running: st.Running, Stopped: app.isStopped(eff.ID), Error: st.Error,
		LastActivity: st.LastActivity,
	}
}

func makePublicStreamView(app *App, eff StreamConfig, st WorkerStatus) PublicStreamView {
	status := "idle"
	switch {
	case st.Error != "":
		status = "error"
	case st.Running:
		status = "running"
	case app.isStopped(eff.ID):
		status = "stopped"
	}
	return PublicStreamView{
		ID: eff.ID, Source: eff.Source, Name: eff.Name, Status: status,
		Type: eff.Type, URL: eff.URL, Enabled: eff.Enabled,
		ChunkSeconds: eff.ChunkSeconds, Country: streamCountry(eff),
		Audio: eff.Audio, Video: eff.Video, Metadata: eff.Metadata, Radio: eff.Radio,
		CreatedAt: eff.CreatedAt, UpdatedAt: eff.UpdatedAt,
		LastActivity: st.LastActivity, Error: st.Error,
	}
}

func streamCountry(eff StreamConfig) string {
	if eff.Radio != nil {
		if country := strings.TrimSpace(eff.Radio.Country); country != "" {
			return country
		}
	}
	if country, ok := eff.Metadata["country"].(string); ok {
		return strings.TrimSpace(country)
	}
	return ""
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
