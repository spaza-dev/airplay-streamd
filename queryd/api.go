package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	mux.HandleFunc("POST /api/jobs/upload", a.handleCreateUploadJob)
	mux.HandleFunc("GET /api/jobs", a.handleListJobs)
	mux.HandleFunc("GET /api/jobs/logs", a.handleAllLogs)
	mux.HandleFunc("GET /api/jobs/{id}", a.handleGetJob)
	mux.HandleFunc("GET /api/jobs/{id}/matches", a.handleMatches)
	mux.HandleFunc("GET /api/jobs/{id}/logs", a.handleJobLogs)
	mux.HandleFunc("GET /api/jobs/{id}/callbacks", a.handleCallbackDeliveries)
	mux.HandleFunc("POST /api/jobs/{id}/pause", a.handlePauseJob)
	mux.HandleFunc("POST /api/jobs/{id}/resume", a.handleResumeJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", a.handleCancelJob)

	mux.HandleFunc("GET /api/stats", a.handleStats)

	mux.HandleFunc("/", a.handleSPA)
	return a.authMiddleware(mux)
}

func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") ||
			r.URL.Path == "/api/health" ||
			r.URL.Path == "/api/health/ready" {
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
	info := a.versionInfo()
	info["storage_configured"] = a.storage != nil
	writeJSON(w, http.StatusOK, info)
}

func (a *App) handleReady(w http.ResponseWriter, r *http.Request) {
	ready, reason := a.ready()
	if !ready {
		writeError(w, http.StatusServiceUnavailable, "%s", reason)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

type createJobRequest struct {
	Files       []string        `json:"files"`
	Scope       json.RawMessage `json:"scope"`
	CallbackURL string          `json:"callback_url"`
}

func (a *App) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if a.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "S3 storage is not configured")
		return
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		writeError(w, http.StatusUnsupportedMediaType, "multipart is not supported on this endpoint; use /api/jobs/upload")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req createJobRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON request: %v", err)
		return
	}
	if len(req.Files) == 0 {
		writeError(w, http.StatusBadRequest, "at least one file URL is required")
		return
	}
	if len(req.Files) > a.cfg.Limits.MaxUploadFiles {
		writeError(w, http.StatusBadRequest, "too many files (limit %d)", a.cfg.Limits.MaxUploadFiles)
		return
	}
	scope, err := ParseScope(string(req.Scope))
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	callbackURL, err := validateCallbackURL(req.CallbackURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	type fileSource struct {
		url  string
		name string
	}
	sources := make([]fileSource, 0, len(req.Files))
	for index, raw := range req.Files {
		parsed, err := parseFileURL(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "files[%d]: %v", index, err)
			return
		}
		sources = append(sources, fileSource{url: strings.TrimSpace(raw), name: fileNameFromURL(parsed, index)})
	}
	scopeBytes, _ := json.Marshal(scope)
	job, err := a.db.CreateJob(r.Context(), string(scopeBytes), callbackURL, "api")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create job: %v", err)
		return
	}
	for _, source := range sources {
		if _, err := a.db.AddJobFile(r.Context(), job.ID, source.name, "", "", "", source.url, 0); err != nil {
			a.failJobCreation(job.ID, err)
			writeError(w, http.StatusInternalServerError, "store file metadata: %v", err)
			return
		}
	}
	job, _ = a.db.GetJob(r.Context(), job.ID)
	a.jobs.Kick()
	writeJSON(w, http.StatusCreated, map[string]any{"job": job})
}

func (a *App) handleCreateUploadJob(w http.ResponseWriter, r *http.Request) {
	if a.storage == nil {
		writeError(w, http.StatusServiceUnavailable, "S3 storage is not configured")
		return
	}
	maxTotal := int64(a.cfg.Limits.MaxTotalUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxTotal+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart request: %v", err)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	files := append([]*multipart.FileHeader{}, r.MultipartForm.File["files"]...)
	files = append(files, r.MultipartForm.File["files[]"]...)
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "at least one file is required")
		return
	}
	if len(files) > a.cfg.Limits.MaxUploadFiles {
		writeError(w, http.StatusBadRequest, "too many files (limit %d)", a.cfg.Limits.MaxUploadFiles)
		return
	}
	var total int64
	for _, header := range files {
		if !supportedExtension(header.Filename) {
			writeError(w, http.StatusBadRequest, "unsupported audio extension: %s", header.Filename)
			return
		}
		if header.Size > int64(a.cfg.Limits.MaxUploadMB)<<20 {
			writeError(w, http.StatusBadRequest, "%s exceeds the %d MB file limit", header.Filename, a.cfg.Limits.MaxUploadMB)
			return
		}
		total += header.Size
	}
	if total > maxTotal {
		writeError(w, http.StatusBadRequest, "upload exceeds the %d MB total limit", a.cfg.Limits.MaxTotalUploadMB)
		return
	}
	scope, err := ParseScope(r.FormValue("scope"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	callbackURL, err := validateCallbackURL(r.FormValue("callback_url"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	scopeBytes, _ := json.Marshal(scope)
	job, err := a.db.CreateJob(r.Context(), string(scopeBytes), callbackURL, "api")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create job: %v", err)
		return
	}
	workspace := a.workspacePath(job.ID)
	uploadDir := filepath.Join(workspace, "uploads")
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		a.failJobCreation(job.ID, fmt.Errorf("create workspace: %w", err))
		writeError(w, http.StatusInternalServerError, "create workspace: %v", err)
		return
	}
	for _, header := range files {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(header.Filename)), ".")
		record, err := a.db.AddJobFile(r.Context(), job.ID, header.Filename, "", "", header.Header.Get("Content-Type"), "", header.Size)
		if err != nil {
			a.failJobCreation(job.ID, err)
			writeError(w, http.StatusInternalServerError, "store file metadata: %v", err)
			return
		}
		stored := fmt.Sprintf("%d.%s", record.ID, ext)
		path := filepath.Join(uploadDir, stored)
		if err := saveMultipartFile(header, path); err != nil {
			a.failJobCreation(job.ID, err)
			writeError(w, http.StatusInternalServerError, "store upload: %v", err)
			return
		}
		record.StoredName = stored
		record.Path = path
		if err := a.db.SetJobFilePath(r.Context(), record.ID, stored, path); err != nil {
			a.failJobCreation(job.ID, err)
			writeError(w, http.StatusInternalServerError, "store file path: %v", err)
			return
		}
	}
	job, _ = a.db.GetJob(r.Context(), job.ID)
	a.jobs.Kick()
	writeJSON(w, http.StatusCreated, map[string]any{"job": job})
}

func validateCallbackURL(raw string) (string, error) {
	callbackURL := strings.TrimSpace(raw)
	if callbackURL == "" {
		return "", nil
	}
	parsed, err := url.Parse(callbackURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("callback_url must be an absolute http or https URL")
	}
	return callbackURL, nil
}

func parseFileURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, fmt.Errorf("file URL must not be empty")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("invalid file URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("file URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("file URL must be absolute")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("file URL must not contain credentials")
	}
	return parsed, nil
}

func fileNameFromURL(parsed *url.URL, index int) string {
	name, err := url.PathUnescape(filepath.Base(parsed.Path))
	if err != nil || name == "" || name == "." || name == "/" {
		return fmt.Sprintf("download-%d", index+1)
	}
	return name
}

func (a *App) failJobCreation(jobID int64, err error) {
	_ = a.db.FinishJob(context.Background(), jobID, "failed", err.Error())
	a.logJob(jobID, "error", "upload", "%v", err)
	a.cleanupJob(jobID)
}

func (a *App) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	jobs, err := a.db.ListJobs(r.Context(), r.URL.Query().Get("status"), limit, offset)
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
	files, _ := a.db.JobFiles(r.Context(), id)
	indexes, _ := a.db.JobIndexes(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"job": job, "files": files, "indexes": indexes})
}

func (a *App) handleMatches(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	fileID, _ := strconv.ParseInt(r.URL.Query().Get("file_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	matches, total, err := a.db.Matches(r.Context(), id, fileID, r.URL.Query().Get("stream_id"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "matches: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": matches, "total": total})
}

func (a *App) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	entries, err := a.db.JobLogs(r.Context(), id, r.URL.Query().Get("level"), r.URL.Query().Get("search"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "logs: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (a *App) handleAllLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	entries, err := a.db.JobLogs(r.Context(), 0, r.URL.Query().Get("level"), r.URL.Query().Get("search"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "logs: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "service": a.logger.Recent(200)})
}

func (a *App) handleCallbackDeliveries(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	deliveries, err := a.db.CallbackDeliveries(r.Context(), id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "callback deliveries: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": deliveries})
}

func (a *App) handlePauseJob(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	job, err := a.db.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	switch job.Status {
	case "completed", "failed", "cancelled", "paused":
		writeJSON(w, http.StatusOK, map[string]any{"job": job})
		return
	case "queued":
		if err := a.db.PauseJob(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "pause job: %v", err)
			return
		}
		if updated, err := a.db.GetJob(r.Context(), id); err == nil {
			go a.deliverCallback(context.Background(), updated, "job.paused", nil)
		}
	default:
		if err := a.db.RequestPause(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "pause job: %v", err)
			return
		}
		a.jobs.Cancel(id)
	}
	updated, _ := a.db.GetJob(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"job": updated})
}

func (a *App) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	id, ok := a.jobID(w, r)
	if !ok {
		return
	}
	job, err := a.db.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Status != "paused" {
		writeError(w, http.StatusConflict, "job is not paused")
		return
	}
	if err := a.db.ResumeJob(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "resume job: %v", err)
		return
	}
	updated, _ := a.db.GetJob(r.Context(), id)
	go a.deliverCallback(context.Background(), updated, "job.resumed", nil)
	a.jobs.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"job": updated})
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
	queued, err := a.db.CancelQueuedOrPausedJob(r.Context(), id)
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
	} else {
		if updated, err := a.db.GetJob(r.Context(), id); err == nil {
			go a.deliverCallback(context.Background(), updated, "job.cancelled", nil)
		}
		a.cleanupJob(id)
	}
	updated, _ := a.db.GetJob(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"job": updated})
}

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	counts, err := a.db.JobCounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats: %v", err)
		return
	}
	indexes, matches, _ := a.db.Totals(r.Context())
	free, total := diskUsage(a.cfg.Database.Workspace)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": version, "olaf_version": a.olafVersion,
		"uptime_seconds": int(time.Since(a.startTime).Seconds()),
		"jobs":           counts, "active_jobs": a.jobs.Active(), "max_active_jobs": a.cfg.Workers.MaxActiveJobs,
		"indexes_queried": indexes, "matches_total": matches,
		"storage": map[string]any{
			"configured": a.storage != nil, "bucket": a.storageCfg.Bucket,
			"endpoint": a.storageCfg.Endpoint, "prefix": a.storageCfg.Prefix,
		},
		"limits": map[string]any{
			"max_upload_files":    a.cfg.Limits.MaxUploadFiles,
			"max_upload_mb":       a.cfg.Limits.MaxUploadMB,
			"max_indexes_per_job": a.cfg.Limits.MaxIndexesPerJob,
			"max_download_gb":     a.cfg.Limits.MaxDownloadGB,
			"allow_private_urls":  a.cfg.Limits.AllowPrivateURLs,
		},
		"disk": map[string]any{"free_bytes": free, "total_bytes": total},
	})
}

func diskUsage(path string) (int64, int64) {
	free, total := diskFree(path)
	return free, total
}

func (a *App) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>queryd API</title>
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

func saveMultipartFile(header *multipart.FileHeader, path string) error {
	src, err := header.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := path + ".part"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
