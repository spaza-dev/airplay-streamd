export interface Job {
  id: number;
  stream_id: string;
  date: string;
  status: string;
  stage: string;
  source: string;
  force: boolean;
  progress: number;
  recordings_total: number;
  recordings_downloaded: number;
  recordings_indexed: number;
  error: string;
  attempts: number;
  next_attempt_at?: string;
  cancel_requested: boolean;
  build_id?: string;
  build_prefix?: string;
  timeline_checksum?: string;
  timeline_summary?: string;
  created_at: string;
  started_at?: string;
  completed_at?: string;
  updated_at: string;
}

export interface TimelineSummary {
  recordings_discovered?: number;
  recordings_indexed?: number;
  recordings_failed?: number;
  total_recorded_duration_ms?: number;
  total_gap_duration_ms?: number;
  gap_count?: number;
  overlap_count?: number;
}

export interface Recording {
  index?: number;
  resource_id?: string;
  object_key: string;
  start_time?: string;
  end_time?: string;
  timeline_start_ms?: number;
  timeline_end_ms?: number;
  duration_ms?: number;
  status: string;
  error?: string;
}

export interface Gap {
  start_time: string;
  end_time: string;
  timeline_start_ms: number;
  timeline_end_ms: number;
  duration_ms: number;
  reason: string;
}

export interface Overlap {
  first_object_key: string;
  second_object_key: string;
  timeline_start_ms: number;
  timeline_end_ms: number;
  duration_ms: number;
}

export interface Timeline {
  job_id: number;
  stream_id: string;
  date: string;
  timezone: string;
  timeline_origin: string;
  timeline_duration_ms: number;
  summary: TimelineSummary;
  gaps?: Gap[];
  overlaps?: Overlap[];
  recordings: Recording[];
  total: number;
  offset: number;
  limit: number;
}

export interface TimelineSummaryResponse {
  job_id: number;
  stream_id?: string;
  date?: string;
  timezone?: string;
  timeline_origin?: string;
  timeline_duration_ms?: number;
  summary: TimelineSummary;
}

export interface LogEntry {
  time: string;
  level: string;
  message: string;
  job_id?: number;
  stage?: string;
}

export interface Stats {
  status: string;
  version: string;
  olaf_version: string;
  uptime_seconds: number;
  active_jobs: number;
  max_active_jobs: number;
  jobs: Record<string, number>;
  discovery: { enabled: boolean; last_run?: DiscoveryRun | null };
  storage: { source_configured: boolean; destination_configured: boolean };
  disk: { free_bytes: number; total_bytes: number };
}

export interface DiscoveryConfig {
  enabled: boolean;
  scan_interval_minutes: number;
  source_storage: string;
  stream_scope: string;
  selected_streams: string[];
  date_scope: string;
  lookback_days: number;
  dates?: string[];
  readiness: {
    strategy: string;
    grace_period_minutes: number;
    minimum_recordings: number;
    minimum_duration_seconds: number;
  };
  automatic_enqueue: boolean;
  max_jobs_per_scan: number;
  dry_run: boolean;
}

export interface DiscoveryRun {
  id: number;
  trigger: string;
  started_at: string;
  completed_at?: string;
  status: string;
  streams_examined: number;
  dates_examined: number;
  eligible_dates: number;
  jobs_created: number;
  jobs_skipped: number;
  errors: string;
}

export interface StorageConfig {
  id: number;
  name: string;
  role: string;
  endpoint: string;
  bucket: string;
  region: string;
  access_key: string;
  secret_key?: string;
  prefix: string;
  path_style: boolean;
  updated_at?: string;
}

export interface RuntimeSettings {
  max_active_jobs: number;
  retry_initial_seconds: number;
  retry_max_seconds: number;
  max_attempts: number;
  keep_failed_workspaces: boolean;
  min_free_disk_gb: number;
  builds_retained: number;
}

export interface JobDetailResponse {
  job: Job;
  attempts: Array<Record<string, unknown>>;
}
