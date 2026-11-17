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

export interface RadioStation {
  stationuuid: string;
  name: string;
  url: string;
  url_resolved: string;
  homepage: string;
  favicon: string;
  tags: string;
  country: string;
  countrycode: string;
  state: string;
  language: string;
  codec: string;
  bitrate: number;
  hls: number;
  lastcheckok: number;
  votes: number;
}

export interface DiscoveryStation extends RadioStation {
  already_added: boolean;
  existing_stream_id?: string;
}

export interface StreamView {
  id: string;
  source: string;
  name: string;
  type: string;
  url: string;
  enabled: boolean;
  chunk_seconds: number;
  audio?: AudioConfig;
  video?: VideoConfig;
  metadata?: Record<string, unknown>;
  radio?: RadioStation;
  created_at: string;
  updated_at: string;
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

export interface MetricPoint {
  time: string;
  cpu_percent: number;
  memory_used_bytes: number;
  memory_total_bytes: number;
  disk_free_bytes: number;
  disk_total_bytes: number;
  net_rx_bytes_per_sec: number;
  net_tx_bytes_per_sec: number;
  active_streams: number;
  backlog_bytes: number;
  load_1: number;
}

export interface StreamMetrics {
  id: string;
  pid?: number;
  cpu_percent: number;
  memory_bytes: number;
  output_bytes_per_sec: number;
  chunks_per_minute: number;
  last_chunk_at?: string;
  bytes_total: number;
  chunks_total: number;
}

export interface ResourceHeadroom {
  name: string;
  unit: string;
  used: number;
  limit: number;
  used_percent: number;
  additional_streams: number;
  limiting: boolean;
}

export interface CapacityEstimate {
  status: string;
  bottleneck?: string;
  recommended_additional_streams: number;
  confidence: string;
  active_streams: number;
  resources: ResourceHeadroom[];
  message?: string;
}

export interface MetricsSnapshot {
  enabled: boolean;
  interval_seconds: number;
  current?: MetricPoint;
  history: MetricPoint[];
  streams: StreamMetrics[];
  capacity: CapacityEstimate;
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
