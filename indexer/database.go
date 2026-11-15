package main

import (
	"context"
	"database/sql"
	"embed"
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
		err = d.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists)
		if err != nil {
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
		if _, err := d.db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}

func utcNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func nullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func timeValue(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (d *DB) GetSetting(key, fallback string) string {
	var value string
	err := d.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return fallback
	}
	return value
}

func (d *DB) SetSetting(key, value string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, utcNow())
	return err
}

func (d *DB) AddJobLog(jobID int64, level, stage, message string) {
	_, _ = d.db.Exec(`INSERT INTO job_logs (job_id, level, stage, message, created_at) VALUES (?, ?, ?, ?, ?)`,
		jobID, level, stage, message, utcNow())
}

func (d *DB) addAttempt(jobID int64, attempt int, workspace string) (int64, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	res, err := d.db.Exec(`INSERT INTO job_attempts (job_id, attempt_number, status, started_at, workspace_path)
		VALUES (?, ?, 'running', ?, ?)`, jobID, attempt, utcNow(), workspace)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) finishAttempt(id int64, status, errMsg string) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.Exec(`UPDATE job_attempts SET status = ?, completed_at = ?, error = ? WHERE id = ?`,
		status, utcNow(), errMsg, id)
	return err
}

func (d *DB) JobCounts(ctx context.Context) (map[string]int, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{
		"queued": 0, "downloading": 0, "caching": 0, "storing": 0,
		"validating": 0, "uploading": 0, "completed": 0, "failed": 0, "cancelled": 0,
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

func (d *DB) InsertRecordingEvent(eventID, streamID, date, payload string) (bool, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	res, err := d.db.Exec(`INSERT OR IGNORE INTO recording_events (event_id, stream_id, date, payload_json, received_at)
		VALUES (?, ?, ?, ?, ?)`, eventID, streamID, date, payload, utcNow())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (d *DB) HasRecordingEvent(streamID, date string) bool {
	var count int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM recording_events WHERE stream_id = ? AND date = ?`, streamID, date).Scan(&count)
	return err == nil && count > 0
}

func (d *DB) StorageConfigs(ctx context.Context) ([]StorageConfig, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, role, endpoint, bucket, region, access_key, secret_key, prefix, path_style, updated_at
		FROM storage_configs ORDER BY role, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StorageConfig
	for rows.Next() {
		var c StorageConfig
		var pathStyle int
		if err := rows.Scan(&c.ID, &c.Name, &c.Role, &c.Endpoint, &c.Bucket, &c.Region,
			&c.AccessKey, &c.SecretKey, &c.Prefix, &pathStyle, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.PathStyle = pathStyle != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) StorageConfigByName(ctx context.Context, name string) (*StorageConfig, error) {
	var c StorageConfig
	var pathStyle int
	err := d.db.QueryRowContext(ctx, `SELECT id, name, role, endpoint, bucket, region, access_key, secret_key, prefix, path_style, updated_at
		FROM storage_configs WHERE name = ?`, name).Scan(&c.ID, &c.Name, &c.Role, &c.Endpoint, &c.Bucket,
		&c.Region, &c.AccessKey, &c.SecretKey, &c.Prefix, &pathStyle, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	c.PathStyle = pathStyle != 0
	return &c, nil
}

func (d *DB) StorageConfigByRole(ctx context.Context, role string) (*StorageConfig, error) {
	var c StorageConfig
	var pathStyle int
	err := d.db.QueryRowContext(ctx, `SELECT id, name, role, endpoint, bucket, region, access_key, secret_key, prefix, path_style, updated_at
		FROM storage_configs WHERE role = ? ORDER BY id LIMIT 1`, role).Scan(&c.ID, &c.Name, &c.Role,
		&c.Endpoint, &c.Bucket, &c.Region, &c.AccessKey, &c.SecretKey, &c.Prefix, &pathStyle, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	c.PathStyle = pathStyle != 0
	return &c, nil
}

func (d *DB) SaveStorageConfig(ctx context.Context, c *StorageConfig) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	now := utcNow()
	pathStyle := 0
	if c.PathStyle {
		pathStyle = 1
	}
	if c.ID == 0 {
		res, err := d.db.ExecContext(ctx, `INSERT INTO storage_configs
			(name, role, endpoint, bucket, region, access_key, secret_key, prefix, path_style, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.Name, c.Role, c.Endpoint, c.Bucket, c.Region, c.AccessKey, c.SecretKey, c.Prefix, pathStyle, now)
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		c.UpdatedAt = now
		return nil
	}
	_, err := d.db.ExecContext(ctx, `UPDATE storage_configs SET name = ?, role = ?, endpoint = ?, bucket = ?,
		region = ?, access_key = ?, secret_key = ?, prefix = ?, path_style = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.Role, c.Endpoint, c.Bucket, c.Region, c.AccessKey, c.SecretKey, c.Prefix, pathStyle, now, c.ID)
	c.UpdatedAt = now
	return err
}

func (d *DB) DeleteStorageConfig(ctx context.Context, id int64) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_, err := d.db.ExecContext(ctx, `DELETE FROM storage_configs WHERE id = ?`, id)
	return err
}

func (d *DB) DiscoveryConfig(ctx context.Context) (string, bool, error) {
	var configJSON string
	var enabled int
	err := d.db.QueryRowContext(ctx, `SELECT config_json, enabled FROM discovery_config WHERE id = 1`).Scan(&configJSON, &enabled)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return configJSON, enabled != 0, nil
}

func (d *DB) SaveDiscoveryConfig(ctx context.Context, configJSON string, enabled bool) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO discovery_config (id, enabled, config_json, updated_at)
		VALUES (1, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET enabled = excluded.enabled,
		config_json = excluded.config_json, updated_at = excluded.updated_at`,
		enabledInt, configJSON, utcNow())
	return err
}

type DiscoveryRun struct {
	ID              int64      `json:"id"`
	Trigger         string     `json:"trigger"`
	StartedAt       time.Time  `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	Status          string     `json:"status"`
	StreamsExamined int        `json:"streams_examined"`
	DatesExamined   int        `json:"dates_examined"`
	EligibleDates   int        `json:"eligible_dates"`
	JobsCreated     int        `json:"jobs_created"`
	JobsSkipped     int        `json:"jobs_skipped"`
	Errors          string     `json:"errors"`
}

func (d *DB) StartDiscoveryRun(ctx context.Context, trigger string) (int64, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	res, err := d.db.ExecContext(ctx, `INSERT INTO discovery_runs (trigger, started_at, status)
		VALUES (?, ?, 'running')`, trigger, utcNow())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) FinishDiscoveryRun(ctx context.Context, id int64, run DiscoveryRun) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	status := run.Status
	if status == "" {
		status = "completed"
	}
	_, err := d.db.ExecContext(ctx, `UPDATE discovery_runs SET completed_at = ?, status = ?,
		streams_examined = ?, dates_examined = ?, eligible_dates = ?, jobs_created = ?,
		jobs_skipped = ?, errors = ? WHERE id = ?`,
		utcNow(), status, run.StreamsExamined, run.DatesExamined, run.EligibleDates,
		run.JobsCreated, run.JobsSkipped, run.Errors, id)
	return err
}

func scanDiscoveryRun(scanner interface{ Scan(...any) error }) (DiscoveryRun, error) {
	var run DiscoveryRun
	var completed sql.NullString
	err := scanner.Scan(&run.ID, &run.Trigger, &run.StartedAt, &completed, &run.Status,
		&run.StreamsExamined, &run.DatesExamined, &run.EligibleDates, &run.JobsCreated,
		&run.JobsSkipped, &run.Errors)
	if err != nil {
		return run, err
	}
	run.CompletedAt = nullableTime(completed)
	return run, nil
}

func (d *DB) DiscoveryRuns(ctx context.Context, limit int) ([]DiscoveryRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, trigger, started_at, completed_at, status,
		streams_examined, dates_examined, eligible_dates, jobs_created, jobs_skipped, errors
		FROM discovery_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscoveryRun
	for rows.Next() {
		run, err := scanDiscoveryRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (d *DB) DiscoveryRunByID(ctx context.Context, id int64) (*DiscoveryRun, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, trigger, started_at, completed_at, status,
		streams_examined, dates_examined, eligible_dates, jobs_created, jobs_skipped, errors
		FROM discovery_runs WHERE id = ?`, id)
	run, err := scanDiscoveryRun(row)
	if err != nil {
		return nil, err
	}
	return &run, nil
}

type JobLog struct {
	ID        int64     `json:"id"`
	JobID     int64     `json:"job_id"`
	Level     string    `json:"level"`
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

func (d *DB) JobLogs(ctx context.Context, jobID int64, level, stage, search string, limit, offset int) ([]JobLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
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
	if stage != "" {
		query += ` AND stage = ?`
		args = append(args, stage)
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
	var out []JobLog
	for rows.Next() {
		var entry JobLog
		if err := rows.Scan(&entry.ID, &entry.JobID, &entry.Level, &entry.Stage, &entry.Message, &entry.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (d *DB) Ping(ctx context.Context) error {
	var value int
	return d.db.QueryRowContext(ctx, `SELECT 1`).Scan(&value)
}
