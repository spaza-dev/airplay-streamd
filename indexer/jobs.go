package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Job struct {
	ID                   int64      `json:"id"`
	StreamID             string     `json:"stream_id"`
	Date                 string     `json:"date"`
	Status               string     `json:"status"`
	Stage                string     `json:"stage"`
	Source               string     `json:"source"`
	Force                bool       `json:"force"`
	Progress             float64    `json:"progress"`
	RecordingsTotal      int        `json:"recordings_total"`
	RecordingsDownloaded int        `json:"recordings_downloaded"`
	RecordingsIndexed    int        `json:"recordings_indexed"`
	Error                string     `json:"error"`
	Attempts             int        `json:"attempts"`
	NextAttemptAt        *time.Time `json:"next_attempt_at,omitempty"`
	CancelRequested      bool       `json:"cancel_requested"`
	BuildID              string     `json:"build_id,omitempty"`
	BuildPrefix          string     `json:"build_prefix,omitempty"`
	TimelineChecksum     string     `json:"timeline_checksum,omitempty"`
	TimelineSummaryJSON  string     `json:"timeline_summary,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	CompletedAt          *time.Time `json:"completed_at,omitempty"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

const jobSelectColumns = `id, stream_id, date, status, stage, source, force, progress,
	recordings_total, recordings_downloaded, recordings_indexed, error, attempts,
	next_attempt_at, cancel_requested, build_id, build_prefix, timeline_checksum,
	timeline_summary_json, created_at, started_at, completed_at, updated_at`

func scanJob(scanner interface{ Scan(...any) error }) (*Job, error) {
	var job Job
	var force, cancel int
	var nextAttempt, started, completed sql.NullString
	var createdAt, updatedAt string
	err := scanner.Scan(&job.ID, &job.StreamID, &job.Date, &job.Status, &job.Stage,
		&job.Source, &force, &job.Progress, &job.RecordingsTotal, &job.RecordingsDownloaded,
		&job.RecordingsIndexed, &job.Error, &job.Attempts, &nextAttempt, &cancel,
		&job.BuildID, &job.BuildPrefix, &job.TimelineChecksum, &job.TimelineSummaryJSON,
		&createdAt, &started, &completed, &updatedAt)
	if err != nil {
		return nil, err
	}
	job.Force = force != 0
	job.CancelRequested = cancel != 0
	job.NextAttemptAt = nullableTime(nextAttempt)
	job.StartedAt = nullableTime(started)
	job.CompletedAt = nullableTime(completed)
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &job, nil
}

func (d *DB) GetJob(ctx context.Context, id int64) (*Job, error) {
	row := d.db.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, id)
	return scanJob(row)
}

func (d *DB) findActiveJob(ctx context.Context, streamID, date string) (*Job, error) {
	row := d.db.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs
		WHERE stream_id = ? AND date = ? AND status IN
		('queued','downloading','caching','storing','validating','uploading')
		ORDER BY id DESC LIMIT 1`, streamID, date)
	return scanJob(row)
}

func (d *DB) findCompletedJob(ctx context.Context, streamID, date string) (*Job, error) {
	row := d.db.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs
		WHERE stream_id = ? AND date = ? AND status = 'completed' AND build_prefix != ''
		ORDER BY id DESC LIMIT 1`, streamID, date)
	return scanJob(row)
}

func (d *DB) CreateJob(ctx context.Context, streamID, date, source string, force bool) (*Job, bool, error) {
	if active, err := d.findActiveJob(ctx, streamID, date); err == nil {
		return active, true, nil
	} else if err != sql.ErrNoRows {
		return nil, false, err
	}
	if !force {
		if completed, err := d.findCompletedJob(ctx, streamID, date); err == nil {
			return completed, true, nil
		} else if err != sql.ErrNoRows {
			return nil, false, err
		}
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	forceInt := 0
	if force {
		forceInt = 1
	}
	res, err := d.db.ExecContext(ctx, `INSERT INTO jobs
		(stream_id, date, status, stage, source, force, created_at, updated_at)
		VALUES (?, ?, 'queued', 'queued', ?, ?, ?, ?)`,
		streamID, date, source, forceInt, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			if active, findErr := d.findActiveJob(ctx, streamID, date); findErr == nil {
				return active, true, nil
			}
		}
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	job, err := d.GetJob(ctx, id)
	return job, false, err
}

func (d *DB) ListJobs(ctx context.Context, status, streamID string, limit, offset int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + jobSelectColumns + ` FROM jobs WHERE 1=1`
	args := []any{}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if streamID != "" {
		query += ` AND stream_id = ?`
		args = append(args, streamID)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *job)
	}
	return out, rows.Err()
}

func (d *DB) ClaimNextJob(ctx context.Context) (*Job, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	row := d.db.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs
		WHERE status = 'queued' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY id LIMIT 1`, now)
	job, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'downloading', stage = 'download',
		attempts = attempts + 1, started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE id = ? AND status = 'queued'`, now, now, job.ID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, nil
	}
	return d.GetJob(ctx, job.ID)
}

func (d *DB) UpdateJob(ctx context.Context, id int64, status, stage string, progress float64, errMsg string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = ?, stage = ?, progress = ?, error = ?, updated_at = ?
		WHERE id = ?`, status, stage, progress, errMsg, utcNow(), id)
	return err
}

func (d *DB) UpdateJobCounts(ctx context.Context, id int64, total, downloaded, indexed int, progress float64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET recordings_total = ?, recordings_downloaded = ?,
		recordings_indexed = ?, progress = ?, updated_at = ? WHERE id = ?`,
		total, downloaded, indexed, progress, utcNow(), id)
	return err
}

func (d *DB) SetJobArtifacts(ctx context.Context, id int64, buildID, buildPrefix, checksum, summaryJSON string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET build_id = ?, build_prefix = ?, timeline_checksum = ?,
		timeline_summary_json = ?, updated_at = ? WHERE id = ?`,
		buildID, buildPrefix, checksum, summaryJSON, utcNow(), id)
	return err
}

func (d *DB) FinishJob(ctx context.Context, id int64, status, errMsg string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = ?, stage = ?, error = ?,
		completed_at = ?, updated_at = ? WHERE id = ?`, status, status, errMsg, now, now, id)
	return err
}

func (d *DB) ScheduleRetry(ctx context.Context, id int64, errMsg string, delay time.Duration) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	next := time.Now().UTC().Add(delay).Format(time.RFC3339Nano)
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued', stage = 'retry',
		error = ?, next_attempt_at = ?, cancel_requested = 0, updated_at = ? WHERE id = ?`,
		errMsg, next, utcNow(), id)
	return err
}

func (d *DB) RequestCancel(ctx context.Context, id int64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET cancel_requested = 1, updated_at = ?
		WHERE id = ? AND status NOT IN ('completed','failed','cancelled')`, utcNow(), id)
	return err
}

func (d *DB) CancelQueuedJob(ctx context.Context, id int64) (bool, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	res, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'cancelled', stage = 'cancelled',
		cancel_requested = 1, completed_at = ?, updated_at = ? WHERE id = ? AND status = 'queued'`,
		now, now, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (d *DB) RetryJob(ctx context.Context, id int64) (*Job, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	res, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued', stage = 'retry', attempts = 0,
		next_attempt_at = NULL, cancel_requested = 0, error = '', completed_at = NULL, updated_at = ?
		WHERE id = ? AND status IN ('failed','cancelled')`, now, id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("job %d is not failed or cancelled", id)
	}
	return d.GetJob(ctx, id)
}

func (d *DB) RecoverInterruptedJobs(ctx context.Context) (int, error) {
	d.writeMu.Lock()
	rows, err := d.db.QueryContext(ctx, `SELECT id, cancel_requested, attempts FROM jobs
		WHERE status IN ('downloading','caching','storing','validating','uploading')`)
	if err != nil {
		d.writeMu.Unlock()
		return 0, err
	}
	type pending struct {
		id       int64
		cancel   bool
		attempts int
	}
	var jobs []pending
	for rows.Next() {
		var p pending
		var cancel int
		if err := rows.Scan(&p.id, &cancel, &p.attempts); err != nil {
			rows.Close()
			d.writeMu.Unlock()
			return 0, err
		}
		p.cancel = cancel != 0
		jobs = append(jobs, p)
	}
	rows.Close()
	now := utcNow()
	for _, p := range jobs {
		if p.cancel {
			_, _ = d.db.ExecContext(ctx, `UPDATE jobs SET status = 'cancelled', stage = 'cancelled',
				error = 'cancelled during restart recovery', completed_at = ?, updated_at = ? WHERE id = ?`, now, now, p.id)
		} else {
			_, _ = d.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued', stage = 'recovered',
				error = 'recovered after restart', next_attempt_at = NULL, updated_at = ? WHERE id = ?`, now, p.id)
		}
		_, _ = d.db.ExecContext(ctx, `UPDATE job_attempts SET status = 'interrupted', completed_at = ?
			WHERE job_id = ? AND status = 'running'`, now, p.id)
	}
	d.writeMu.Unlock()
	return len(jobs), nil
}

func (d *DB) AddAttempt(jobID int64, workspace string) (int64, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	res, err := d.db.Exec(`INSERT INTO job_attempts (job_id, attempt_number, status, started_at, workspace_path)
		VALUES (?, (SELECT COALESCE(MAX(attempt_number),0)+1 FROM job_attempts WHERE job_id = ?),
		'running', ?, ?)`, jobID, jobID, utcNow(), workspace)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) Attempts(ctx context.Context, jobID int64) ([]map[string]any, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, attempt_number, status, started_at, completed_at, error, workspace_path
		FROM job_attempts WHERE job_id = ? ORDER BY attempt_number DESC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, attempt int64
		var status, started, errMsg, workspace string
		var completed sql.NullString
		if err := rows.Scan(&id, &attempt, &status, &started, &completed, &errMsg, &workspace); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "attempt_number": attempt, "status": status, "started_at": started,
			"completed_at": completed.String, "error": errMsg, "workspace_path": workspace,
		})
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Job manager
// ---------------------------------------------------------------------------

type JobManager struct {
	app    *App
	mu     sync.Mutex
	active map[int64]context.CancelFunc
	kick   chan struct{}
}

func NewJobManager(app *App) *JobManager {
	return &JobManager{app: app, active: map[int64]context.CancelFunc{}, kick: make(chan struct{}, 1)}
}

func (m *JobManager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *JobManager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

func (m *JobManager) maxActive() int {
	return m.app.runtimeSettings(context.Background()).MaxActiveJobs
}

func (m *JobManager) Cancel(id int64) {
	m.mu.Lock()
	cancel := m.active[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *JobManager) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		m.dispatch(ctx)
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, cancel := range m.active {
				cancel()
			}
			m.mu.Unlock()
			return
		case <-ticker.C:
		case <-m.kick:
		}
	}
}

func (m *JobManager) dispatch(ctx context.Context) {
	for {
		if m.app.draining.Load() {
			return
		}
		m.mu.Lock()
		count := len(m.active)
		m.mu.Unlock()
		if count >= m.maxActive() {
			return
		}
		job, err := m.app.db.ClaimNextJob(ctx)
		if err != nil {
			m.app.logger.Errorf("claim job: %v", err)
			return
		}
		if job == nil {
			return
		}
		jobCtx, cancel := context.WithTimeout(ctx, time.Duration(m.app.cfg.Workers.JobTimeoutMinutes)*time.Minute)
		m.mu.Lock()
		m.active[job.ID] = cancel
		m.mu.Unlock()
		m.app.logger.Infof("starting job %d stream=%s date=%s attempt=%d", job.ID, job.StreamID, job.Date, job.Attempts)
		go func(job *Job, jobCtx context.Context, cancel context.CancelFunc) {
			defer func() {
				cancel()
				m.mu.Lock()
				delete(m.active, job.ID)
				m.mu.Unlock()
				m.Kick()
			}()
			m.runJob(jobCtx, job)
		}(job, jobCtx, cancel)
	}
}

type processArtifacts struct {
	BuildID       string
	BuildPrefix   string
	TimelineCheck string
	SummaryJSON   string
	Discovered    int
	Downloaded    int
	Indexed       int
}

type runtimeSettings struct {
	MaxActiveJobs        int  `json:"max_active_jobs"`
	RetryInitialSeconds  int  `json:"retry_initial_seconds"`
	RetryMaxSeconds      int  `json:"retry_max_seconds"`
	MaxAttempts          int  `json:"max_attempts"`
	KeepFailedWorkspaces bool `json:"keep_failed_workspaces"`
	MinFreeDiskGB        int  `json:"min_free_disk_gb"`
	BuildsRetained       int  `json:"builds_retained"`
}

func (a *App) runtimeSettings(ctx context.Context) runtimeSettings {
	settings := runtimeSettings{
		MaxActiveJobs:        a.cfg.Workers.MaxActiveJobs,
		RetryInitialSeconds:  a.cfg.Workers.RetryInitialSecs,
		RetryMaxSeconds:      a.cfg.Workers.RetryMaxSecs,
		MaxAttempts:          a.cfg.Workers.MaxAttempts,
		KeepFailedWorkspaces: true,
		MinFreeDiskGB:        1,
		BuildsRetained:       3,
	}
	if value := a.db.GetSetting("workers.max_active_jobs", ""); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			settings.MaxActiveJobs = parsed
		}
	}
	if value := a.db.GetSetting("workers.retry_initial_seconds", ""); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			settings.RetryInitialSeconds = parsed
		}
	}
	if value := a.db.GetSetting("workers.retry_max_seconds", ""); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			settings.RetryMaxSeconds = parsed
		}
	}
	if value := a.db.GetSetting("workers.max_attempts", ""); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
			settings.MaxAttempts = parsed
		}
	}
	if value := a.db.GetSetting("workers.keep_failed_workspaces", ""); value != "" {
		settings.KeepFailedWorkspaces = value == "true" || value == "1"
	}
	if value := a.db.GetSetting("workers.min_free_disk_gb", ""); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
			settings.MinFreeDiskGB = parsed
		}
	}
	if value := a.db.GetSetting("workers.builds_retained", ""); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			settings.BuildsRetained = parsed
		}
	}
	if settings.RetryMaxSeconds < settings.RetryInitialSeconds {
		settings.RetryMaxSeconds = settings.RetryInitialSeconds
	}
	return settings
}

func (s runtimeSettings) Validate() error {
	if s.MaxActiveJobs <= 0 {
		return errors.New("max active jobs must be greater than zero")
	}
	if s.RetryInitialSeconds <= 0 {
		return errors.New("retry initial seconds must be greater than zero")
	}
	if s.RetryMaxSeconds < s.RetryInitialSeconds {
		return errors.New("retry max seconds must be at least the initial retry")
	}
	if s.MaxAttempts < 0 {
		return errors.New("max attempts must not be negative")
	}
	if s.MinFreeDiskGB < 0 {
		return errors.New("minimum free disk must not be negative")
	}
	if s.BuildsRetained <= 0 {
		return errors.New("builds retained must be greater than zero")
	}
	return nil
}

func (a *App) saveRuntimeSettings(ctx context.Context, settings runtimeSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	values := map[string]string{
		"workers.max_active_jobs":        strconv.Itoa(settings.MaxActiveJobs),
		"workers.retry_initial_seconds":  strconv.Itoa(settings.RetryInitialSeconds),
		"workers.retry_max_seconds":      strconv.Itoa(settings.RetryMaxSeconds),
		"workers.max_attempts":           strconv.Itoa(settings.MaxAttempts),
		"workers.keep_failed_workspaces": strconv.FormatBool(settings.KeepFailedWorkspaces),
		"workers.min_free_disk_gb":       strconv.Itoa(settings.MinFreeDiskGB),
		"workers.builds_retained":        strconv.Itoa(settings.BuildsRetained),
	}
	for key, value := range values {
		if err := a.db.SetSetting(key, value); err != nil {
			return err
		}
	}
	return nil
}

var supportedAudioExtensions = map[string]bool{
	".ogg": true, ".opus": true, ".mp3": true, ".m4a": true, ".aac": true,
	".wav": true, ".flac": true, ".mp4": true, ".mkv": true, ".webm": true,
	".wv": true, ".ape": true, ".wma": true, ".raw": true,
}

func supportedExtensionList() []string {
	out := make([]string, 0, len(supportedAudioExtensions))
	for ext := range supportedAudioExtensions {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

func audioExtension(key string) string {
	return strings.ToLower(filepath.Ext(key))
}

func (m *JobManager) logJob(jobID int64, level, stage, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	m.app.db.AddJobLog(jobID, level, stage, message)
	switch level {
	case "error":
		m.app.logger.Errorf("job %d [%s] %s", jobID, stage, message)
	case "warn":
		m.app.logger.Warnf("job %d [%s] %s", jobID, stage, message)
	default:
		m.app.logger.Infof("job %d [%s] %s", jobID, stage, message)
	}
}

func (m *JobManager) setStage(ctx context.Context, job *Job, status, stage string, progress float64) error {
	if err := m.app.db.UpdateJob(ctx, job.ID, status, stage, progress, job.Error); err != nil {
		return err
	}
	m.logJob(job.ID, "info", stage, "stage=%s progress=%.0f%%", stage, progress*100)
	return nil
}

func (m *JobManager) runJob(ctx context.Context, job *Job) {
	workspace := filepath.Join(m.app.cfg.Database.Workspace, fmt.Sprintf("%d", job.ID))
	attemptID, err := m.app.db.AddAttempt(job.ID, workspace)
	if err != nil {
		m.app.logger.Errorf("job %d: create attempt: %v", job.ID, err)
		_ = m.app.db.FinishJob(ctx, job.ID, "failed", err.Error())
		return
	}
	if job.CancelRequested {
		_ = m.app.db.FinishJob(ctx, job.ID, "cancelled", "cancelled before start")
		_ = m.app.db.finishAttempt(attemptID, "cancelled", "cancelled before start")
		return
	}
	_ = os.RemoveAll(workspace)
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		_ = m.app.db.FinishJob(ctx, job.ID, "failed", err.Error())
		_ = m.app.db.finishAttempt(attemptID, "failed", err.Error())
		return
	}

	artifacts, processErr := m.processJob(ctx, job, workspace)
	if processErr == nil {
		_ = m.app.db.FinishJob(ctx, job.ID, "completed", "")
		_ = m.app.db.finishAttempt(attemptID, "completed", "")
		m.logJob(job.ID, "info", "completed", "index published: %s", artifacts.BuildPrefix)
		_ = os.RemoveAll(workspace)
		return
	}

	m.logJob(job.ID, "error", job.Stage, "%v", processErr)
	_ = m.app.db.finishAttempt(attemptID, "failed", processErr.Error())

	latest, latestErr := m.app.db.GetJob(context.Background(), job.ID)
	if latestErr == nil {
		job = latest
	}
	if job.CancelRequested {
		_ = m.app.db.FinishJob(context.Background(), job.ID, "cancelled", "cancelled")
		_ = os.RemoveAll(workspace)
		return
	}
	if m.app.ctx.Err() != nil {
		_ = m.app.db.UpdateJob(context.Background(), job.ID, "queued", "recovered", job.Progress, "interrupted by shutdown")
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		_ = m.app.db.FinishJob(context.Background(), job.ID, "cancelled", "cancelled")
		_ = os.RemoveAll(workspace)
		return
	}
	settings := m.app.runtimeSettings(context.Background())
	maxAttempts := settings.MaxAttempts
	if maxAttempts > 0 && job.Attempts >= maxAttempts {
		_ = m.app.db.FinishJob(context.Background(), job.ID, "failed", processErr.Error())
		return
	}
	delay := time.Duration(settings.RetryInitialSeconds) * time.Second
	for i := 1; i < job.Attempts; i++ {
		delay *= 2
		if delay >= time.Duration(settings.RetryMaxSeconds)*time.Second {
			delay = time.Duration(settings.RetryMaxSeconds) * time.Second
			break
		}
	}
	_ = m.app.db.ScheduleRetry(context.Background(), job.ID, processErr.Error(), delay)
	m.logJob(job.ID, "warn", "retry", "retry scheduled in %s", delay)
	if !settings.KeepFailedWorkspaces {
		_ = os.RemoveAll(workspace)
	}
}

func (m *JobManager) processJob(ctx context.Context, job *Job, workspace string) (*processArtifacts, error) {
	sourceCfg, err := m.app.db.StorageConfigByRole(ctx, "source")
	if err != nil {
		return nil, fmt.Errorf("source storage is not configured: %w", err)
	}
	destCfg, err := m.app.db.StorageConfigByRole(ctx, "destination")
	if err != nil {
		return nil, fmt.Errorf("destination storage is not configured: %w", err)
	}
	source, err := NewStorageClient(ctx, *sourceCfg)
	if err != nil {
		return nil, err
	}
	dest, err := NewStorageClient(ctx, *destCfg)
	if err != nil {
		return nil, err
	}

	tzName, loc, err := resolveStreamTimezone(ctx, m.app, source, job.StreamID)
	if err != nil {
		return nil, err
	}
	m.logJob(job.ID, "info", "validate", "stream=%s date=%s timezone=%s", job.StreamID, job.Date, tzName)

	prefix := fmt.Sprintf("%s/recordings/%s/", job.StreamID, strings.ReplaceAll(job.Date, "-", "/"))
	objects, err := source.ListObjects(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list recordings: %w", err)
	}
	if len(objects) == 0 {
		return nil, fmt.Errorf("no recordings found under %s", prefix)
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })

	inputs := make([]RecordingInput, 0, len(objects))
	for _, object := range objects {
		ext := audioExtension(object.Key)
		if !supportedAudioExtensions[ext] {
			m.logJob(job.ID, "warn", "validate", "ignoring unsupported object %s", object.Key)
			continue
		}
		start, parseErr := ParseRecordingKey(object.Key, job.StreamID, job.Date, loc)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid recording object %s: %w", object.Key, parseErr)
		}
		inputs = append(inputs, RecordingInput{ObjectKey: object.Key, Start: start, Size: object.Size})
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("no supported audio recordings found under %s", prefix)
	}
	var totalBytes int64
	for _, input := range inputs {
		totalBytes += input.Size
	}
	settings := m.app.runtimeSettings(ctx)
	freeBytes, _ := diskUsage(m.app.cfg.Database.Workspace)
	requiredBytes := totalBytes + totalBytes/5 + int64(settings.MinFreeDiskGB)*1<<30
	if freeBytes > 0 && freeBytes < requiredBytes {
		return nil, fmt.Errorf("insufficient disk space: need %d bytes, %d available", requiredBytes, freeBytes)
	}
	_ = m.app.db.UpdateJobCounts(ctx, job.ID, len(inputs), 0, 0, 0.05)
	m.logJob(job.ID, "info", "download", "discovered %d recordings", len(inputs))

	audioDir := filepath.Join(workspace, "audio")
	if err := os.MkdirAll(audioDir, 0o750); err != nil {
		return nil, err
	}
	paths := map[string]string{}
	var pathMu sync.Mutex
	downloaded := 0
	var downloadMu sync.Mutex
	if err := runParallel(ctx, m.app.cfg.Workers.DownloadsPerJob, len(inputs), func(ctx context.Context, index int) error {
		input := &inputs[index]
		localPath := filepath.Join(audioDir, fmt.Sprintf("%05d_%s", index, filepath.Base(input.ObjectKey)))
		n, err := source.DownloadToFile(ctx, input.ObjectKey, localPath)
		if err != nil {
			return fmt.Errorf("download %s: %w", input.ObjectKey, err)
		}
		if input.Size > 0 && n != input.Size {
			_ = os.Remove(localPath)
			return fmt.Errorf("download %s: expected %d bytes, got %d", input.ObjectKey, input.Size, n)
		}
		pathMu.Lock()
		paths[input.ObjectKey] = localPath
		pathMu.Unlock()
		downloadMu.Lock()
		downloaded++
		current := downloaded
		downloadMu.Unlock()
		_ = m.app.db.UpdateJobCounts(ctx, job.ID, len(inputs), current, 0, 0.05+0.2*float64(current)/float64(len(inputs)))
		return nil
	}); err != nil {
		return nil, err
	}

	if err := m.setStage(ctx, job, "downloading", "probe", 0.25); err != nil {
		return nil, err
	}
	if err := runParallel(ctx, m.app.cfg.Workers.DownloadsPerJob, len(inputs), func(ctx context.Context, index int) error {
		input := &inputs[index]
		probe, err := ProbeAudio(ctx, m.app.cfg, paths[input.ObjectKey])
		if err != nil {
			input.ProbeError = err.Error()
			m.logJob(job.ID, "error", "probe", "%s: %v", input.ObjectKey, err)
			return nil
		}
		input.DurationMS = probe.DurationMS
		return nil
	}); err != nil {
		return nil, err
	}

	timeline, buildErr := BuildTimeline(job.StreamID, job.Date, loc, inputs, m.app.olafVersion)
	timelinePath := filepath.Join(workspace, "timeline.json")
	if timeline != nil {
		_ = WriteTimeline(timelinePath, timeline)
	}
	if buildErr != nil {
		return nil, fmt.Errorf("build timeline: %w", buildErr)
	}
	failed := 0
	for _, recording := range timeline.Recordings {
		if recording.Status == "failed" {
			failed++
		}
	}
	if failed > 0 {
		return nil, fmt.Errorf("%d recordings could not be inspected", failed)
	}

	if err := m.setStage(ctx, job, "caching", "cache", 0.4); err != nil {
		return nil, err
	}
	olafHome := filepath.Join(workspace, "olaf-home")
	if err := writeOlafConfig(olafHome, filepath.Join(workspace, "index"), filepath.Join(workspace, "cache")); err != nil {
		return nil, err
	}
	if err := RunOlafCache(ctx, m.app.cfg, workspace, timeline.Recordings, paths); err != nil {
		return nil, err
	}

	if err := m.setStage(ctx, job, "storing", "store", 0.65); err != nil {
		return nil, err
	}
	if err := RunOlafStoreCached(ctx, m.app.cfg, workspace); err != nil {
		return nil, err
	}

	if err := m.setStage(ctx, job, "validating", "validate", 0.8); err != nil {
		return nil, err
	}
	if _, err := ValidateIndex(workspace, len(timeline.Recordings)); err != nil {
		return nil, err
	}
	count, statsOutput, err := RunOlafStats(ctx, m.app.cfg, workspace)
	if err != nil {
		return nil, err
	}
	if count != len(timeline.Recordings) {
		return nil, fmt.Errorf("Olaf database contains %d resources, expected %d\n%s", count, len(timeline.Recordings), statsOutput)
	}
	fingerprints, err := OlafFingerprintCount(statsOutput)
	if err != nil {
		return nil, err
	}
	if fingerprints == 0 {
		return nil, fmt.Errorf("Olaf database contains no fingerprints")
	}
	for _, recording := range timeline.Recordings {
		if !strings.Contains(statsOutput, recording.OlafReferenceID) {
			return nil, fmt.Errorf("Olaf database is missing resource %s", recording.OlafReferenceID)
		}
	}
	for i := range timeline.Recordings {
		timeline.Recordings[i].Status = "indexed"
		timeline.Recordings[i].Error = ""
	}
	timeline.Summary = summarizeTimeline(timeline)
	if err := ValidateTimeline(timeline); err != nil {
		return nil, fmt.Errorf("timeline validation: %w", err)
	}
	if err := WriteTimeline(timelinePath, timeline); err != nil {
		return nil, err
	}
	timelineBytes, err := os.ReadFile(timelinePath)
	if err != nil {
		return nil, err
	}
	checksum := ChecksumBytes(timelineBytes)
	summaryJSON, _ := json.Marshal(timeline.Summary)
	_ = m.app.db.UpdateJobCounts(ctx, job.ID, len(timeline.Recordings), len(timeline.Recordings), len(timeline.Recordings), 0.85)

	if err := m.setStage(ctx, job, "uploading", "upload", 0.9); err != nil {
		return nil, err
	}
	buildID := fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102T150405Z"), job.ID)
	basePrefix := fmt.Sprintf("%s/indexes/%s/", job.StreamID, strings.ReplaceAll(job.Date, "-", "/"))
	buildPrefix := basePrefix + "builds/" + buildID + "/"

	files := []struct{ name, contentType string }{
		{"data.mdb", "application/octet-stream"},
		{"lock.mdb", "application/octet-stream"},
	}
	if err := runParallel(ctx, m.app.cfg.Workers.UploadsPerJob, len(files), func(ctx context.Context, index int) error {
		file := files[index]
		local := filepath.Join(workspace, "index", file.name)
		info, err := os.Stat(local)
		if err != nil {
			return fmt.Errorf("required index file %s: %w", file.name, err)
		}
		if err := dest.PutFile(ctx, buildPrefix+file.name, local, file.contentType); err != nil {
			return fmt.Errorf("upload %s: %w", file.name, err)
		}
		remote, err := dest.Head(ctx, buildPrefix+file.name)
		if err != nil {
			return fmt.Errorf("verify %s: %w", file.name, err)
		}
		if remote.Size != info.Size() {
			return fmt.Errorf("verify %s: size %d != %d", file.name, remote.Size, info.Size())
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := dest.PutFile(ctx, buildPrefix+"timeline.json", timelinePath, "application/json"); err != nil {
		return nil, fmt.Errorf("upload timeline: %w", err)
	}
	timelineInfo, err := os.Stat(timelinePath)
	if err != nil {
		return nil, err
	}
	remoteTimeline, err := dest.Head(ctx, buildPrefix+"timeline.json")
	if err != nil {
		return nil, fmt.Errorf("verify timeline upload: %w", err)
	}
	if remoteTimeline.Size != timelineInfo.Size() {
		return nil, fmt.Errorf("verify timeline upload: size %d != %d", remoteTimeline.Size, timelineInfo.Size())
	}
	remoteTimelineData, err := dest.GetBytes(ctx, buildPrefix+"timeline.json", 512<<20)
	if err != nil {
		return nil, fmt.Errorf("verify timeline checksum: %w", err)
	}
	if ChecksumBytes(remoteTimelineData) != checksum {
		return nil, fmt.Errorf("verify timeline checksum: uploaded file does not match")
	}

	metaDoc := map[string]any{
		"status":       "complete",
		"stream_id":    job.StreamID,
		"date":         job.Date,
		"timezone":     tzName,
		"build_id":     buildID,
		"build_prefix": "builds/" + buildID + "/",
		"created_at":   job.CreatedAt.UTC().Format(time.RFC3339),
		"completed_at": time.Now().UTC().Format(time.RFC3339),
		"olaf_version": m.app.olafVersion,
		"fingerprint_config": map[string]any{
			"threads":                       m.app.cfg.Olaf.Threads,
			"skip_duplicates":               true,
			"allowed_audio_file_extensions": supportedExtensionList(),
		},
		"recordings": map[string]any{
			"discovered": timeline.Summary.RecordingsDiscovered,
			"downloaded": len(timeline.Recordings),
			"indexed":    timeline.Summary.RecordingsIndexed,
			"failed":     timeline.Summary.RecordingsFailed,
		},
		"timeline": map[string]any{"file": "timeline.json", "version": timeline.Version, "checksum": checksum},
		"index":    map[string]any{"format": "lmdb", "files": []string{"data.mdb", "lock.mdb"}},
	}
	metaBytes, err := json.MarshalIndent(metaDoc, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := dest.PutBytes(ctx, basePrefix+"meta.json", metaBytes, "application/json"); err != nil {
		return nil, fmt.Errorf("upload completion metadata: %w", err)
	}
	if err := retainBuilds(ctx, dest, basePrefix, buildID, settings.BuildsRetained); err != nil {
		m.logJob(job.ID, "warn", "cleanup", "retain old builds: %v", err)
	}
	if err := m.app.db.SetJobArtifacts(ctx, job.ID, buildID, buildPrefix, checksum, string(summaryJSON)); err != nil {
		return nil, err
	}
	return &processArtifacts{
		BuildID: buildID, BuildPrefix: buildPrefix, TimelineCheck: checksum,
		SummaryJSON: string(summaryJSON), Discovered: len(timeline.Recordings),
		Downloaded: len(timeline.Recordings), Indexed: timeline.Summary.RecordingsIndexed,
	}, nil
}

func retainBuilds(ctx context.Context, dest *StorageClient, basePrefix, currentBuildID string, retain int) error {
	builds, err := dest.ListCommonPrefixes(ctx, basePrefix+"builds/")
	if err != nil {
		return err
	}
	var ids []string
	for _, build := range builds {
		if build != currentBuildID {
			ids = append(ids, build)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	if len(ids) <= retain-1 {
		return nil
	}
	for _, build := range ids[retain-1:] {
		if err := dest.DeletePrefix(ctx, basePrefix+"builds/"+build+"/"); err != nil {
			return err
		}
	}
	return nil
}

func resolveStreamTimezone(ctx context.Context, app *App, source *StorageClient, streamID string) (string, *time.Location, error) {
	fallback := app.cfg.DefaultTimezone
	data, err := source.GetBytes(ctx, streamID+"/meta.json", 1<<20)
	if err != nil {
		loc, loadErr := time.LoadLocation(fallback)
		if loadErr != nil {
			return "", nil, loadErr
		}
		app.logger.Warnf("stream %s: metadata unavailable, using %s: %v", streamID, fallback, err)
		return fallback, loc, nil
	}
	var doc struct {
		Timezone string         `json:"timezone"`
		Metadata map[string]any `json:"metadata"`
	}
	_ = json.Unmarshal(data, &doc)
	name := doc.Timezone
	if name == "" && doc.Metadata != nil {
		if value, ok := doc.Metadata["timezone"].(string); ok {
			name = value
		}
	}
	if name == "" {
		name = fallback
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		app.logger.Warnf("stream %s: invalid timezone %q, using %s", streamID, name, fallback)
		name = fallback
		loc, err = time.LoadLocation(name)
		if err != nil {
			return "", nil, err
		}
	}
	return name, loc, nil
}

func runParallel(ctx context.Context, concurrency, count int, fn func(context.Context, int) error) error {
	if concurrency <= 0 {
		concurrency = 1
	}
	if count == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	items := make(chan int)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	workers := concurrency
	if workers > count {
		workers = count
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range items {
				if ctx.Err() != nil {
					return
				}
				if err := fn(ctx, index); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
					return
				}
			}
		}()
	}
	for i := 0; i < count; i++ {
		select {
		case items <- i:
		case <-ctx.Done():
			break
		}
	}
	close(items)
	wg.Wait()
	return firstErr
}
