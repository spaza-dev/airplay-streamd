export interface AudioConfig {
  codec: string;
  container: string;
  bitrate: string;
  sample_rate: number;
  channels: number;
  vbr?: boolean | null;
}

export interface VideoConfig {
  enabled?: boolean | null;
  codec?: string;
  bitrate?: string;
  container?: string;
}

export interface StreamView {
  id: string;
  name: string;
  type: string;
  url: string;
  enabled: boolean;
  chunk_seconds: number;
  audio?: AudioConfig;
  video?: VideoConfig;
  metadata?: Record<string, unknown>;
  running: boolean;
  stopped: boolean;
  error?: string;
  last_activity?: string;
}

export interface UploaderStatus {
  enabled: boolean;
  concurrency: number;
  active: number;
  counts: Record<string, number>;
}

export interface StreamsResponse {
  streams: StreamView[];
  uploader: UploaderStatus;
}

export interface LogEntry {
  time: string;
  level: string;
  stream?: string;
  message: string;
}

export interface QueueItem {
  id: number;
  stream_id: string;
  file: string;
  object_key: string;
  file_size: number;
  status: string;
  attempts: number;
  next_attempt_at?: string;
  last_error?: string;
  created_at: string;
  uploaded_at?: string;
}
