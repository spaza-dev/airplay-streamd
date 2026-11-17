package main

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type DB struct {
	db      *sql.DB
	writeMu sync.Mutex
}

func OpenDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	raw.SetMaxOpenConns(1)
	db := &DB{db: raw}
	if _, err := raw.Exec(`PRAGMA busy_timeout=10000`); err != nil {
		raw.Close()
		return nil, err
	}
	if err := db.migrate(); err != nil {
		raw.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d *DB) migrate() error {
	if _, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		var exists int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		sqlBytes, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := d.db.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := d.db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, utcNow()); err != nil {
			return err
		}
	}
	return nil
}

const jobSelectColumns = `id, status, stage, progress, error, scope_json, callback_url,
	requested_by, total_files, total_indexes, processed_indexes, total_matches, attempts,
	next_attempt_at, cancel_requested, pause_requested, created_at, started_at,
	completed_at, updated_at`

func scanJob(scanner interface{ Scan(...any) error }) (*Job, error) {
	var job Job
	var cancel, pause int
	var nextAttempt, started, completed sql.NullString
	var createdAt, updatedAt string
	err := scanner.Scan(&job.ID, &job.Status, &job.Stage, &job.Progress, &job.Error,
		&job.ScopeJSON, &job.CallbackURL, &job.RequestedBy, &job.TotalFiles,
		&job.TotalIndexes, &job.ProcessedIndexes, &job.TotalMatches, &job.Attempts,
		&nextAttempt, &cancel, &pause, &createdAt, &started, &completed, &updatedAt)
	if err != nil {
		return nil, err
	}
	job.CancelRequested = cancel != 0
	job.PauseRequested = pause != 0
	job.NextAttemptAt = nullableTime(nextAttempt)
	job.StartedAt = nullableTime(started)
	job.CompletedAt = nullableTime(completed)
	job.CreatedAt = parseDBTime(createdAt)
	job.UpdatedAt = parseDBTime(updatedAt)
	return &job, nil
}

func (d *DB) CreateJob(ctx context.Context, scopeJSON, callbackURL, requestedBy string) (*Job, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	res, err := d.db.ExecContext(ctx, `INSERT INTO jobs
		(status, stage, scope_json, callback_url, requested_by, created_at, updated_at)
		VALUES ('queued', 'queued', ?, ?, ?, ?, ?)`, scopeJSON, callbackURL, requestedBy, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.getJob(ctx, id)
}

func (d *DB) GetJob(ctx context.Context, id int64) (*Job, error) { return d.getJob(ctx, id) }

func (d *DB) getJob(ctx context.Context, id int64) (*Job, error) {
	row := d.db.QueryRowContext(ctx, `SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, id)
	return scanJob(row)
}

func (d *DB) ListJobs(ctx context.Context, status string, limit, offset int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	query := `SELECT ` + jobSelectColumns + ` FROM jobs WHERE 1=1`
	args := []any{}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
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
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'planning', stage = 'planning',
		attempts = attempts + 1, started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE id = ? AND status = 'queued'`, now, now, job.ID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, nil
	}
	return d.getJob(ctx, job.ID)
}

func (d *DB) UpdateJob(ctx context.Context, id int64, status, stage string, progress float64, errMsg string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = ?, stage = ?, progress = ?, error = ?, updated_at = ?
		WHERE id = ?`, status, stage, progress, errMsg, utcNow(), id)
	return err
}

func (d *DB) SetJobStage(ctx context.Context, id int64, status, stage string, progress float64) error {
	return d.UpdateJob(ctx, id, status, stage, progress, "")
}

func (d *DB) SetJobTotals(ctx context.Context, id int64, totalIndexes, processed, matches int, progress float64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET total_indexes = ?, processed_indexes = ?,
		total_matches = ?, progress = ?, updated_at = ? WHERE id = ?`,
		totalIndexes, processed, matches, progress, utcNow(), id)
	return err
}

func (d *DB) FinishJob(ctx context.Context, id int64, status string, errMsg string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = ?, stage = ?,
		progress = CASE WHEN ? = 'completed' THEN 1 ELSE progress END,
		error = ?, cancel_requested = CASE WHEN ? = 'cancelled' THEN 1 ELSE cancel_requested END,
		pause_requested = 0, completed_at = ?, updated_at = ? WHERE id = ?`,
		status, status, status, errMsg, status, now, now, id)
	return err
}

func (d *DB) ScheduleRetry(ctx context.Context, id int64, errMsg string, delay time.Duration) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	next := time.Now().UTC().Add(delay).Format(time.RFC3339Nano)
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued', stage = 'retry',
		error = ?, next_attempt_at = ?, pause_requested = 0, updated_at = ? WHERE id = ?`,
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

func (d *DB) RequestPause(ctx context.Context, id int64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET pause_requested = 1, updated_at = ?
		WHERE id = ? AND status NOT IN ('completed','failed','cancelled','paused')`, utcNow(), id)
	return err
}

func (d *DB) ResumeJob(ctx context.Context, id int64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued', stage = 'queued',
		pause_requested = 0, next_attempt_at = NULL, updated_at = ?
		WHERE id = ? AND status = 'paused'`, utcNow(), id)
	return err
}

func (d *DB) PauseJob(ctx context.Context, id int64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'paused', stage = 'paused',
		pause_requested = 0, updated_at = ? WHERE id = ? AND status NOT IN ('completed','failed','cancelled')`,
		utcNow(), id)
	return err
}

func (d *DB) CancelQueuedOrPausedJob(ctx context.Context, id int64) (bool, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	res, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'cancelled', stage = 'cancelled',
		cancel_requested = 1, completed_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('queued','paused')`, now, now, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (d *DB) JobFlags(ctx context.Context, id int64) (cancel, pause bool, err error) {
	var c, p int
	err = d.db.QueryRowContext(ctx, `SELECT cancel_requested, pause_requested FROM jobs WHERE id = ?`, id).Scan(&c, &p)
	return c != 0, p != 0, err
}

func (d *DB) RecoverInterruptedJobs(ctx context.Context) (int, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	res, err := d.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued', stage = 'recovered',
		updated_at = ? WHERE status IN ('planning','downloading','querying')`, utcNow())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, _ = d.db.ExecContext(ctx, `UPDATE job_indexes SET status = 'downloaded'
		WHERE status = 'querying'`)
	return int(n), nil
}

func (d *DB) AddJobFile(ctx context.Context, jobID int64, original, stored, path, contentType, sourceID, sourceURL string, size int64) (*JobFile, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	res, err := d.db.ExecContext(ctx, `INSERT INTO job_files
		(job_id, original_name, stored_name, path, source_id, source_url, size_bytes, content_type, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, jobID, original, stored, path, sourceID, sourceURL, size, contentType, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	_, _ = d.db.ExecContext(ctx, `UPDATE jobs SET total_files = (SELECT COUNT(*) FROM job_files WHERE job_id = ?),
		updated_at = ? WHERE id = ?`, jobID, now, jobID)
	return &JobFile{ID: id, JobID: jobID, OriginalName: original, StoredName: stored,
		Path: path, SourceID: sourceID, SourceURL: sourceURL, SizeBytes: size, ContentType: contentType, CreatedAt: parseDBTime(now)}, nil
}

func (d *DB) SetJobFilePath(ctx context.Context, fileID int64, storedName, path string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE job_files SET stored_name = ?, path = ? WHERE id = ?`,
		storedName, path, fileID)
	return err
}

func (d *DB) SetJobFileDownloaded(ctx context.Context, fileID int64, storedName, path, contentType string, size int64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE job_files SET stored_name = ?, path = ?, content_type = ?, size_bytes = ? WHERE id = ?`,
		storedName, path, contentType, size, fileID)
	return err
}

func scanFile(scanner interface{ Scan(...any) error }) (*JobFile, error) {
	var f JobFile
	var created string
	if err := scanner.Scan(&f.ID, &f.JobID, &f.OriginalName, &f.StoredName, &f.Path,
		&f.SourceID, &f.SourceURL, &f.SizeBytes, &f.ContentType, &created); err != nil {
		return nil, err
	}
	f.CreatedAt = parseDBTime(created)
	return &f, nil
}

func (d *DB) JobFiles(ctx context.Context, jobID int64) ([]JobFile, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, job_id, original_name, stored_name, path,
		source_id, source_url, size_bytes, content_type, created_at FROM job_files WHERE job_id = ? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]JobFile, 0)
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (d *DB) InsertIndex(ctx context.Context, idx *JobIndex) (*JobIndex, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	_, err := d.db.ExecContext(ctx, `INSERT INTO job_indexes
		(job_id, stream_id, date, build_id, build_prefix, timeline_key, data_key, lock_key,
		 timezone, status, size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)
		ON CONFLICT(job_id, stream_id, date) DO UPDATE SET
			build_id = excluded.build_id, build_prefix = excluded.build_prefix,
			timeline_key = excluded.timeline_key, data_key = excluded.data_key,
			lock_key = excluded.lock_key, timezone = excluded.timezone,
			size_bytes = excluded.size_bytes, error = ''
		WHERE job_indexes.status IN ('pending','failed')`,
		idx.JobID, idx.StreamID, idx.Date, idx.BuildID, idx.BuildPrefix, idx.TimelineKey,
		idx.DataKey, idx.LockKey, idx.Timezone, idx.SizeBytes, now)
	if err != nil {
		return nil, err
	}
	return d.indexByStreamDate(ctx, idx.JobID, idx.StreamID, idx.Date)
}

func (d *DB) indexByStreamDate(ctx context.Context, jobID int64, streamID, date string) (*JobIndex, error) {
	row := d.db.QueryRowContext(ctx, `SELECT `+indexColumns+` FROM job_indexes
		WHERE job_id = ? AND stream_id = ? AND date = ?`, jobID, streamID, date)
	return scanIndex(row)
}

const indexColumns = `id, job_id, stream_id, date, build_id, build_prefix, timeline_key,
	data_key, lock_key, timezone, status, size_bytes, matches, error, started_at,
	completed_at, created_at`

func scanIndex(scanner interface{ Scan(...any) error }) (*JobIndex, error) {
	var idx JobIndex
	var started, completed sql.NullString
	var created string
	err := scanner.Scan(&idx.ID, &idx.JobID, &idx.StreamID, &idx.Date, &idx.BuildID,
		&idx.BuildPrefix, &idx.TimelineKey, &idx.DataKey, &idx.LockKey, &idx.Timezone,
		&idx.Status, &idx.SizeBytes, &idx.Matches, &idx.Error, &started, &completed, &created)
	if err != nil {
		return nil, err
	}
	idx.StartedAt = nullableTime(started)
	idx.CompletedAt = nullableTime(completed)
	idx.CreatedAt = parseDBTime(created)
	return &idx, nil
}

func (d *DB) JobIndexes(ctx context.Context, jobID int64) ([]JobIndex, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+indexColumns+` FROM job_indexes
		WHERE job_id = ? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]JobIndex, 0)
	for rows.Next() {
		idx, err := scanIndex(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *idx)
	}
	return out, rows.Err()
}

func (d *DB) SetIndexStatus(ctx context.Context, id int64, status, errMsg string, matches int) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	var started, completed any
	switch status {
	case "downloading", "querying":
		started = now
	case "done", "failed", "skipped":
		completed = now
	}
	_, err := d.db.ExecContext(ctx, `UPDATE job_indexes SET status = ?, error = ?, matches = ?,
		started_at = COALESCE(started_at, ?), completed_at = COALESCE(?, completed_at)
		WHERE id = ?`, status, errMsg, matches, started, completed, id)
	return err
}

func (d *DB) ResetFailedIndexes(ctx context.Context, jobID int64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE job_indexes SET status = 'pending', error = ''
		WHERE job_id = ? AND status IN ('failed','querying')`, jobID)
	return err
}

func (d *DB) IndexCount(ctx context.Context, jobID int64) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_indexes WHERE job_id = ?`, jobID).Scan(&count)
	return count, err
}

func (d *DB) IndexProgress(ctx context.Context, jobID int64) (done int, matches int, err error) {
	err = d.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status IN ('done','skipped') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(matches),0) FROM job_indexes WHERE job_id = ?`, jobID).Scan(&done, &matches)
	return
}

func (d *DB) AddMatch(ctx context.Context, m *Match) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO matches
		(job_id, job_index_id, file_id, file_ref_id, file_name, file_url, query_path, resource_id,
		 stream_id, date, recording_start_time, occurrence_time, reference_start_ms, reference_stop_ms,
		 match_count, score, timeline_start_ms, timeline_end_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.JobID, m.JobIndexID, m.FileID, m.FileRefID, m.FileName, m.FileURL, m.QueryPath,
		m.ResourceID, m.StreamID, m.Date, m.RecordingStart.UTC().Format(time.RFC3339Nano),
		m.Occurrence.UTC().Format(time.RFC3339Nano), m.ReferenceStartMS, m.ReferenceStopMS,
		m.MatchCount, m.Score, m.TimelineStartMS, m.TimelineEndMS, utcNow())
	return err
}

func scanMatch(scanner interface{ Scan(...any) error }) (*Match, error) {
	var m Match
	var start, occurrence, created string
	if err := scanner.Scan(&m.ID, &m.JobID, &m.JobIndexID, &m.FileID, &m.FileRefID,
		&m.FileName, &m.FileURL, &m.QueryPath, &m.ResourceID, &m.StreamID, &m.Date,
		&start, &occurrence, &m.ReferenceStartMS, &m.ReferenceStopMS, &m.MatchCount,
		&m.Score, &m.TimelineStartMS, &m.TimelineEndMS, &created); err != nil {
		return nil, err
	}
	m.RecordingStart = parseDBTime(start)
	m.Occurrence = parseDBTime(occurrence)
	m.CreatedAt = parseDBTime(created)
	return &m, nil
}

func (d *DB) Matches(ctx context.Context, jobID, fileID int64, streamID string, limit, offset int) ([]Match, int, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	where := ` WHERE job_id = ?`
	args := []any{jobID}
	if fileID > 0 {
		where += ` AND file_id = ?`
		args = append(args, fileID)
	}
	if streamID != "" {
		where += ` AND stream_id = ?`
		args = append(args, streamID)
	}
	var total int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM matches`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `SELECT id, job_id, job_index_id, file_id, file_ref_id, file_name, file_url,
		query_path, resource_id, stream_id, date, recording_start_time, occurrence_time,
		reference_start_ms, reference_stop_ms, match_count, score, timeline_start_ms,
		timeline_end_ms, created_at
		FROM matches` + where + ` ORDER BY occurrence_time, id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]Match, 0)
	for rows.Next() {
		m, err := scanMatch(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *m)
	}
	return out, total, rows.Err()
}

func (d *DB) ResetIndexesForRetry(ctx context.Context, jobID int64) error {
	return d.ResetFailedIndexes(ctx, jobID)
}

func (d *DB) AddJobLog(jobID int64, level, stage, message string) {
	_, _ = d.db.Exec(`INSERT INTO job_logs (job_id, level, stage, message, created_at)
		VALUES (?, ?, ?, ?, ?)`, jobID, level, stage, message, utcNow())
}

func scanLog(scanner interface{ Scan(...any) error }) (*JobLog, error) {
	var entry JobLog
	var created string
	if err := scanner.Scan(&entry.ID, &entry.JobID, &entry.Level, &entry.Stage, &entry.Message, &created); err != nil {
		return nil, err
	}
	entry.CreatedAt = parseDBTime(created)
	return &entry, nil
}

func (d *DB) JobLogs(ctx context.Context, jobID int64, level, search string, limit, offset int) ([]JobLog, error) {
	if limit <= 0 || limit > 2000 {
		limit = 300
	}
	if offset < 0 {
		offset = 0
	}
	query := `SELECT id, job_id, level, stage, message, created_at FROM job_logs WHERE 1=1`
	args := []any{}
	if jobID > 0 {
		query += ` AND job_id = ?`
		args = append(args, jobID)
	}
	if level != "" {
		query += ` AND level = ?`
		args = append(args, level)
	}
	if search != "" {
		query += ` AND message LIKE ?`
		args = append(args, "%"+search+"%")
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]JobLog, 0)
	for rows.Next() {
		entry, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *entry)
	}
	return out, rows.Err()
}

func (d *DB) AddCallbackDelivery(ctx context.Context, jobID int64, event, url string, status, attempt int, success bool, errMsg, requestBody, responseBody string) {
	successInt := 0
	if success {
		successInt = 1
	}
	_, _ = d.db.ExecContext(ctx, `INSERT INTO callback_deliveries
		(job_id, event, url, status_code, attempt, success, error, request_body, response_body, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, event, url, status, attempt, successInt, errMsg, truncate(requestBody, 8000),
		truncate(responseBody, 2000), utcNow())
}

func (d *DB) CallbackDeliveries(ctx context.Context, jobID int64, limit int) ([]CallbackDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, job_id, event, url, status_code, attempt,
		success, error, response_body, created_at FROM callback_deliveries
		WHERE job_id = ? ORDER BY id DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CallbackDelivery, 0)
	for rows.Next() {
		var item CallbackDelivery
		var success int
		var created string
		if err := rows.Scan(&item.ID, &item.JobID, &item.Event, &item.URL, &item.StatusCode,
			&item.Attempt, &success, &item.Error, &item.ResponseBody, &created); err != nil {
			return nil, err
		}
		item.Success = success != 0
		item.CreatedAt = parseDBTime(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (d *DB) JobCounts(ctx context.Context) (map[string]int, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{
		"queued": 0, "planning": 0, "downloading": 0, "querying": 0,
		"paused": 0, "completed": 0, "failed": 0, "cancelled": 0,
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}

func (d *DB) Totals(ctx context.Context) (indexes, matches int, err error) {
	err = d.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM job_indexes WHERE status = 'done'),
		(SELECT COUNT(*) FROM matches)`).Scan(&indexes, &matches)
	return
}

func (d *DB) ClearFilePaths(ctx context.Context, jobID int64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `UPDATE job_files SET path = '' WHERE job_id = ?`, jobID)
	return err
}

func (d *DB) ExpiredJobIDs(ctx context.Context, before time.Time) ([]int64, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id FROM jobs
		WHERE status IN ('completed','failed','cancelled') AND completed_at IS NOT NULL AND completed_at < ?`,
		before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) DeleteJobs(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	for _, id := range ids {
		if _, err := d.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) PruneLogs(ctx context.Context, before time.Time) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `DELETE FROM job_logs WHERE created_at < ?`,
		before.UTC().Format(time.RFC3339Nano))
	return err
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
