CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    status TEXT NOT NULL DEFAULT 'queued',
    stage TEXT NOT NULL DEFAULT 'queued',
    progress REAL NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    scope_json TEXT NOT NULL DEFAULT '{}',
    callback_url TEXT NOT NULL DEFAULT '',
    requested_by TEXT NOT NULL DEFAULT 'api',
    total_files INTEGER NOT NULL DEFAULT 0,
    total_indexes INTEGER NOT NULL DEFAULT 0,
    processed_indexes INTEGER NOT NULL DEFAULT 0,
    total_matches INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    pause_requested INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);

CREATE TABLE IF NOT EXISTS job_files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    original_name TEXT NOT NULL,
    stored_name TEXT NOT NULL,
    path TEXT NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    content_type TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_job_files_job ON job_files(job_id);

CREATE TABLE IF NOT EXISTS job_indexes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    stream_id TEXT NOT NULL,
    date TEXT NOT NULL,
    build_id TEXT NOT NULL DEFAULT '',
    build_prefix TEXT NOT NULL DEFAULT '',
    timeline_key TEXT NOT NULL DEFAULT '',
    data_key TEXT NOT NULL DEFAULT '',
    lock_key TEXT NOT NULL DEFAULT '',
    timezone TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    matches INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL,
    UNIQUE(job_id, stream_id, date)
);
CREATE INDEX IF NOT EXISTS idx_job_indexes_job ON job_indexes(job_id, id);
CREATE INDEX IF NOT EXISTS idx_job_indexes_status ON job_indexes(status);

CREATE TABLE IF NOT EXISTS matches (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    job_index_id INTEGER NOT NULL REFERENCES job_indexes(id) ON DELETE CASCADE,
    file_id INTEGER NOT NULL DEFAULT 0,
    file_name TEXT NOT NULL DEFAULT '',
    query_path TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL,
    stream_id TEXT NOT NULL,
    date TEXT NOT NULL,
    recording_start_time TEXT NOT NULL,
    occurrence_time TEXT NOT NULL,
    reference_start_ms INTEGER NOT NULL DEFAULT 0,
    reference_stop_ms INTEGER NOT NULL DEFAULT 0,
    match_count INTEGER NOT NULL DEFAULT 0,
    score REAL NOT NULL DEFAULT 0,
    timeline_start_ms INTEGER NOT NULL DEFAULT 0,
    timeline_end_ms INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_matches_job ON matches(job_id, id);
CREATE INDEX IF NOT EXISTS idx_matches_job_file ON matches(job_id, file_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_matches_unique
    ON matches(job_id, job_index_id, file_id, resource_id, reference_start_ms);

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

CREATE TABLE IF NOT EXISTS callback_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    event TEXT NOT NULL,
    url TEXT NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    attempt INTEGER NOT NULL DEFAULT 0,
    success INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    request_body TEXT NOT NULL DEFAULT '',
    response_body TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_callback_deliveries_job ON callback_deliveries(job_id, id DESC);
