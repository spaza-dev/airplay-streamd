export interface Job {
  id: number;
  status: string;
  stage: string;
  progress: number;
  error: string;
  scope: string;
  callback_url: string;
  requested_by: string;
  total_files: number;
  total_indexes: number;
  processed_indexes: number;
  total_matches: number;
  attempts: number;
  next_attempt_at?: string;
  cancel_requested: boolean;
  pause_requested: boolean;
  created_at: string;
  started_at?: string;
  completed_at?: string;
  updated_at: string;
}

export interface JobFile {
  id: number;
  job_id: number;
  original_name: string;
  stored_name: string;
  size_bytes: number;
  content_type: string;
  created_at: string;
}

export interface JobIndex {
  id: number;
  job_id: number;
  stream_id: string;
  date: string;
  build_id: string;
  status: string;
  size_bytes: number;
  matches: number;
  error: string;
  created_at: string;
}

export interface Match {
  id: number;
  job_id: number;
  job_index_id: number;
  file_id: number;
  file_name: string;
  query_path: string;
  resource_id: string;
  stream_id: string;
  date: string;
  recording_start_time: string;
  occurrence_time: string;
  reference_start_ms: number;
  reference_stop_ms: number;
  match_count: number;
  score: number;
  timeline_start_ms: number;
  timeline_end_ms: number;
}

export interface LogEntry {
  id?: number;
  job_id?: number;
  time?: string;
  level: string;
  stage?: string;
  message: string;
  created_at?: string;
}

export interface CallbackDelivery {
  id: number;
  job_id: number;
  event: string;
  url: string;
  status_code: number;
  attempt: number;
  success: boolean;
  error: string;
  response_body: string;
  created_at: string;
}

export interface Stats {
  status: string;
  version: string;
  olaf_version: string;
  uptime_seconds: number;
  jobs: Record<string, number>;
  active_jobs: number;
  max_active_jobs: number;
  indexes_queried: number;
  matches_total: number;
  storage: {
    configured: boolean;
    bucket: string;
    endpoint: string;
    prefix: string;
  };
  limits: {
    max_upload_files: number;
    max_upload_mb: number;
    max_indexes_per_job: number;
    max_download_gb: number;
  };
  disk: { free_bytes: number; total_bytes: number };
}

export interface JobDetail {
  job: Job;
  files: JobFile[];
  indexes: JobIndex[];
}
