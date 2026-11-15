package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed all:web/dist
var webAssets embed.FS

//go:embed openapi.json
var openAPIDoc []byte

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, apiError{Error: fmt.Sprintf(format, args...)})
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", a.handleHealth)
	mux.HandleFunc("GET /api/health/ready", a.handleReady)
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openAPIDoc)
	})
	mux.HandleFunc("GET /docs", a.handleDocs)

	mux.HandleFunc("POST /api/jobs", a.handleCreateJob)
	mux.HandleFunc("GET /api/jobs", a.handleListJobs)
	mux.HandleFunc("GET /api/jobs/{id}", a.handleGetJob)
	mux.HandleFunc("POST /api/jobs/{id}/retry", a.handleRetryJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", a.handleCancelJob)
	mux.HandleFunc("GET /api/jobs/{id}/timeline", a.handleTimeline)
	mux.HandleFunc("GET /api/jobs/{id}/timeline/summary", a.handleTimelineSummary)

	mux.HandleFunc("GET /api/discovery/config", a.handleGetDiscoveryConfig)
	mux.HandleFunc("PUT /api/discovery/config", a.handlePutDiscoveryConfig)
	mux.HandleFunc("POST /api/discovery/run", a.handleRunDiscovery)
	mux.HandleFunc("GET /api/discovery/runs", a.handleDiscoveryRuns)
	mux.HandleFunc("GET /api/discovery/runs/{id}", a.handleDiscoveryRun)

	mux.HandleFunc("POST /api/events/recording-complete", a.handleRecordingComplete)

	mux.HandleFunc("GET /api/stats", a.handleStats)
	mux.HandleFunc("GET /api/logs", a.handleLogs)
	mux.HandleFunc("GET /api/settings", a.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", a.handlePutSettings)

	mux.HandleFunc("GET /api/storage", a.handleListStorage)
	mux.HandleFunc("POST /api/storage", a.handleCreateStorage)
	mux.HandleFunc("PUT /api/storage/{id}", a.handleUpdateStorage)
	mux.HandleFunc("DELETE /api/storage/{id}", a.handleDeleteStorage)
	mux.HandleFunc("POST /api/storage/{id}/test", a.handleTestStorage)

	mux.HandleFunc("/", a.handleSPA)
	return a.authMiddleware(mux)
}

func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") ||
			r.URL.Path == "/api/health" ||
			r.URL.Path == "/api/health/ready" ||
			r.URL.Path == "/api/events/recording-complete" {
			next.ServeHTTP(w, r)
			return
		}
		token := a.cfg.Server.AdminToken
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if provided == "" {
			provided = r.Header.Get("X-Admin-Token")
		}
		if provided != token {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": version, "olaf_version": a.olafVersion,
		"uptime_seconds": int(time.Since(a.startTime).Seconds()),
	})
}

func (a *App) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if _, err := a.db.StorageConfigByRole(ctx, "source"); err != nil {
		writeError(w, http.StatusServiceUnavailable, "source storage not configured")
		return
	}
	if _, err := a.db.StorageConfigByRole(ctx, "destination"); err != nil {
		writeError(w, http.StatusServiceUnavailable, "destination storage not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

type createJobRequest struct {
	StreamID string `json:"stream_id"`
	Date     string `json:"date"`
	Force    bool   `json:"force"`
}

func (a *App) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: %v", err)
		return
	}
	req.StreamID = strings.TrimSpace(req.StreamID)
	if !streamIDPattern.MatchString(req.StreamID) {
		writeError(w, http.StatusBadRequest, "invalid stream_id")
		return
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		writeError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	job, existing, err := a.db.CreateJob(r.Context(), req.StreamID, req.Date, "api", req.Force)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create job: %v", err)
		return
	}
	if !existing {
		a.jobs.Kick()
		writeJSON(w, http.StatusCreated, map[string]any{"job": job, "existing": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job, "existing": true})
}

func (a *App) handleListJobs(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	stream := r.URL.Query().Get("stream_id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	jobs, err := a.db.ListJobs(r.Context(), status, stream, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list jobs: %v", err)
		return
	}
	counts, _ := a.db.JobCounts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "counts": counts})
}

func (a *App) jobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return 0, false
	}
	return id, true
}

func (a *App) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	job, err := a.db.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	attempts, _ := a.db.Attempts(r.Context(), id)
	stages, _ := a.db.JobStages(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"job": job, "attempts": attempts, "stages": stages})
}

func (a *App) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	job, err := a.db.RetryJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	a.jobs.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

func (a *App) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	if _, err := a.db.GetJob(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	queued, err := a.db.CancelQueuedJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cancel job: %v", err)
		return
	}
	if !queued {
		if err := a.db.RequestCancel(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "cancel job: %v", err)
			return
		}
		a.jobs.Cancel(id)
	}
	job, _ := a.db.GetJob(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

func (a *App) timelineForJob(ctx context.Context, job *Job) (*Timeline, error) {
	if job.BuildPrefix != "" {
		destCfg, err := a.db.StorageConfigByRole(ctx, "destination")
		if err != nil {
			return nil, err
		}
		dest, err := NewStorageClient(ctx, *destCfg)
		if err != nil {
			return nil, err
		}
		data, err := dest.GetBytes(ctx, job.BuildPrefix+"timeline.json", 256<<20)
		if err != nil {
			return nil, err
		}
		var timeline Timeline
		if err := json.Unmarshal(data, &timeline); err != nil {
			return nil, err
		}
		return &timeline, nil
	}
	path := filepath.Join(a.workspacePath(job.ID), "timeline.json")
	return ReadTimeline(path)
}

func (a *App) handleTimelineSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	job, err := a.db.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if timeline, err := a.timelineForJob(r.Context(), job); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"job_id": job.ID, "stream_id": job.StreamID, "date": job.Date,
			"timeline_origin": timeline.TimelineOrigin, "timeline_duration_ms": timeline.TimelineDurationMS,
			"summary": timeline.Summary, "timezone": timeline.Timezone,
		})
		return
	}
	var summary any = map[string]any{}
	if job.TimelineSummaryJSON != "" {
		_ = json.Unmarshal([]byte(job.TimelineSummaryJSON), &summary)
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": job.ID, "summary": summary})
}

func (a *App) handleTimeline(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	job, err := a.db.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	timeline, err := a.timelineForJob(r.Context(), job)
	if err != nil {
		writeError(w, http.StatusNotFound, "timeline not available: %v", err)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	total := len(timeline.Recordings)
	end := offset + limit
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id": job.ID, "stream_id": timeline.StreamID, "date": timeline.Date,
		"timezone": timeline.Timezone, "timeline_origin": timeline.TimelineOrigin,
		"timeline_duration_ms": timeline.TimelineDurationMS, "summary": timeline.Summary,
		"gaps": timeline.Gaps, "overlaps": timeline.Overlaps,
		"recordings": timeline.Recordings[offset:end],
		"total":      total, "offset": offset, "limit": limit,
	})
}

func (a *App) handleGetDiscoveryConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.discovery.LoadConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load discovery config: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (a *App) handlePutDiscoveryConfig(w http.ResponseWriter, r *http.Request) {
	var cfg DiscoveryConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: %v", err)
		return
	}
	if err := a.discovery.SaveConfig(r.Context(), cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid configuration: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (a *App) handleRunDiscovery(w http.ResponseWriter, r *http.Request) {
	run, err := a.discovery.RunNow(r.Context(), "manual")
	if err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (a *App) handleDiscoveryRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := a.db.DiscoveryRuns(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list discovery runs: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (a *App) handleDiscoveryRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	run, err := a.db.DiscoveryRunByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

type recordingEventRequest struct {
	EventID  string `json:"event_id"`
	StreamID string `json:"stream_id"`
	Date     string `json:"date"`
}

func (a *App) handleRecordingComplete(w http.ResponseWriter, r *http.Request) {
	if token := a.cfg.Server.EventToken; token != "" {
		provided := r.Header.Get("X-Event-Token")
		if provided == "" {
			provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if provided != token {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	}
	var req recordingEventRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: %v", err)
		return
	}
	if !streamIDPattern.MatchString(req.StreamID) {
		writeError(w, http.StatusBadRequest, "invalid stream_id")
		return
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		writeError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	if req.EventID == "" {
		req.EventID = fmt.Sprintf("%s:%s", req.StreamID, req.Date)
	}
	payload, _ := json.Marshal(req)
	if _, err := a.db.InsertRecordingEvent(req.EventID, req.StreamID, req.Date, string(payload)); err != nil {
		writeError(w, http.StatusInternalServerError, "store event: %v", err)
		return
	}
	job, err := a.discovery.HandleRecordingEvent(r.Context(), req.StreamID, req.Date)
	if err != nil {
		writeError(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "job": job})
}

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	counts, err := a.db.JobCounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats: %v", err)
		return
	}
	runs, _ := a.db.DiscoveryRuns(r.Context(), 1)
	var lastRun *DiscoveryRun
	if len(runs) > 0 {
		lastRun = &runs[0]
	}
	storageConfigs, _ := a.db.StorageConfigs(r.Context())
	sourceConfigured, destinationConfigured := false, false
	for _, cfg := range storageConfigs {
		if cfg.Role == "source" {
			sourceConfigured = true
		}
		if cfg.Role == "destination" {
			destinationConfigured = true
		}
	}
	diskFree, diskTotal := diskUsage(a.cfg.Database.Workspace)
	discoveryCfg, _ := a.discovery.LoadConfig(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": version, "olaf_version": a.olafVersion,
		"uptime_seconds": int(time.Since(a.startTime).Seconds()),
		"jobs":           counts, "active_jobs": a.jobs.Active(),
		"max_active_jobs": a.jobs.maxActive(),
		"discovery":       map[string]any{"enabled": discoveryCfg.Enabled, "last_run": lastRun},
		"storage":         map[string]any{"source_configured": sourceConfigured, "destination_configured": destinationConfigured},
		"disk":            map[string]any{"free_bytes": diskFree, "total_bytes": diskTotal},
	})
}

func diskUsage(path string) (int64, int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	return int64(stat.Bavail) * int64(stat.Bsize), int64(stat.Blocks) * int64(stat.Bsize)
}

func (a *App) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.runtimeSettings(r.Context()))
}

func (a *App) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var settings runtimeSettings
	if err := decodeJSON(r, &settings); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: %v", err)
		return
	}
	if err := a.saveRuntimeSettings(r.Context(), settings); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	jobID, _ := strconv.ParseInt(r.URL.Query().Get("job_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	entries, err := a.db.JobLogs(r.Context(), jobID, r.URL.Query().Get("level"),
		r.URL.Query().Get("stage"), r.URL.Query().Get("search"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "logs: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "service": a.logger.Recent(200)})
}

func (a *App) handleListStorage(w http.ResponseWriter, r *http.Request) {
	configs, err := a.db.StorageConfigs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage: %v", err)
		return
	}
	for i := range configs {
		if configs[i].AccessKey != "" {
			configs[i].AccessKey = "********"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"storage": configs})
}

func decodeStorage(r *http.Request) (*StorageConfig, error) {
	var cfg StorageConfig
	if err := decodeJSON(r, &cfg); err != nil {
		return nil, err
	}
	if cfg.Name == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("name and bucket are required")
	}
	if cfg.Role != "source" && cfg.Role != "destination" {
		return nil, fmt.Errorf("role must be source or destination")
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	return &cfg, nil
}

func (a *App) handleCreateStorage(w http.ResponseWriter, r *http.Request) {
	cfg, err := decodeStorage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	cfg.ID = 0
	if err := a.db.SaveStorageConfig(r.Context(), cfg); err != nil {
		writeError(w, http.StatusConflict, "save storage: %v", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"storage": cfg})
}

func (a *App) handleUpdateStorage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid storage id")
		return
	}
	cfg, err := decodeStorage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	existing, err := a.db.StorageConfigs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	for _, item := range existing {
		if item.ID == id && cfg.SecretKey == "" {
			cfg.SecretKey = item.SecretKey
		}
		if item.ID == id && (cfg.AccessKey == "" || cfg.AccessKey == "********") {
			cfg.AccessKey = item.AccessKey
		}
	}
	cfg.ID = id
	if err := a.db.SaveStorageConfig(r.Context(), cfg); err != nil {
		writeError(w, http.StatusConflict, "save storage: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"storage": cfg})
}

func (a *App) handleDeleteStorage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid storage id")
		return
	}
	if err := a.db.DeleteStorageConfig(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete storage: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (a *App) handleTestStorage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid storage id")
		return
	}
	configs, err := a.db.StorageConfigs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	var cfg *StorageConfig
	for i := range configs {
		if configs[i].ID == id {
			cfg = &configs[i]
			break
		}
	}
	if cfg == nil {
		writeError(w, http.StatusNotFound, "storage not found")
		return
	}
	client, err := NewStorageClient(r.Context(), *cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := client.Test(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "connection test failed: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>Indexer API</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"></head>
<body><div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>window.onload=function(){SwaggerUIBundle({url:"/openapi.json",dom_id:"#swagger-ui"});};</script>
</body></html>`)
}

func (a *App) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/openapi.json" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	data, err := webAssets.ReadFile("web/dist/" + name)
	if err != nil {
		data, err = webAssets.ReadFile("web/dist/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	switch filepath.Ext(name) {
	case ".js":
		w.Header().Set("Content-Type", "application/javascript")
	case ".css":
		w.Header().Set("Content-Type", "text/css")
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".json":
		w.Header().Set("Content-Type", "application/json")
	}
	_, _ = w.Write(data)
}
