CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    stream_id TEXT NOT NULL,
    date TEXT NOT NULL,
    status TEXT NOT NULL,
    stage TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    force INTEGER NOT NULL DEFAULT 0,
    progress REAL NOT NULL DEFAULT 0,
    recordings_total INTEGER NOT NULL DEFAULT 0,
    recordings_downloaded INTEGER NOT NULL DEFAULT 0,
    recordings_indexed INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    build_id TEXT NOT NULL DEFAULT '',
    build_prefix TEXT NOT NULL DEFAULT '',
    timeline_checksum TEXT NOT NULL DEFAULT '',
    timeline_summary_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_stream_date ON jobs(stream_id, date);
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_one_active
    ON jobs(stream_id, date)
    WHERE status IN ('queued','downloading','caching','storing','validating','uploading');

CREATE TABLE IF NOT EXISTS job_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    attempt_number INTEGER NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    completed_at TEXT,
    error TEXT NOT NULL DEFAULT '',
    workspace_path TEXT NOT NULL DEFAULT '',
    UNIQUE(job_id, attempt_number)
);

CREATE TABLE IF NOT EXISTS job_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER NOT NULL DEFAULT 0,
    level TEXT NOT NULL,
    stage TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_job_logs_job ON job_logs(job_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_job_logs_created ON job_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_job_logs_level ON job_logs(level);

CREATE TABLE IF NOT EXISTS discovery_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER NOT NULL DEFAULT 0,
    config_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS discovery_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trigger TEXT NOT NULL DEFAULT 'scheduler',
    started_at TEXT NOT NULL,
    completed_at TEXT,
    status TEXT NOT NULL,
    streams_examined INTEGER NOT NULL DEFAULT 0,
    dates_examined INTEGER NOT NULL DEFAULT 0,
    eligible_dates INTEGER NOT NULL DEFAULT 0,
    jobs_created INTEGER NOT NULL DEFAULT 0,
    jobs_skipped INTEGER NOT NULL DEFAULT 0,
    errors TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_discovery_runs_started ON discovery_runs(started_at DESC);

CREATE TABLE IF NOT EXISTS storage_configs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    role TEXT NOT NULL,
    endpoint TEXT NOT NULL DEFAULT '',
    bucket TEXT NOT NULL,
    region TEXT NOT NULL DEFAULT 'auto',
    access_key TEXT NOT NULL DEFAULT '',
    secret_key TEXT NOT NULL DEFAULT '',
    prefix TEXT NOT NULL DEFAULT '',
    path_style INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS recording_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    stream_id TEXT NOT NULL,
    date TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    received_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_recording_events_stream_date ON recording_events(stream_id, date);
