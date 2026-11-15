import {
  DiscoveryConfig,
  DiscoveryRun,
  Job,
  JobDetailResponse,
  LogEntry,
  Stats,
  StorageConfig,
  Timeline,
  RuntimeSettings,
  TimelineSummaryResponse,
} from "./types";

const TOKEN_KEY = "indexer_admin_token";

export function getToken(): string {
  return window.localStorage.getItem(TOKEN_KEY) || "";
}

export function setToken(token: string) {
  if (token) {
    window.localStorage.setItem(TOKEN_KEY, token);
  } else {
    window.localStorage.removeItem(TOKEN_KEY);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(init && init.headers ? (init.headers as Record<string, string>) : {}),
  };
  const token = getToken();
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const response = await fetch(path, { ...init, headers });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const body = await response.json();
      if (body && body.error) message = body.error;
    } catch {
      // ignore
    }
    throw new Error(message);
  }
  if (response.status === 204) return undefined as unknown as T;
  return (await response.json()) as T;
}

export const api = {
  stats: () => request<Stats>("/api/stats"),
  health: () => request<Record<string, unknown>>("/api/health"),
  jobs: (params: { status?: string; stream_id?: string; limit?: number; offset?: number }) => {
    const query = new URLSearchParams();
    if (params.status) query.set("status", params.status);
    if (params.stream_id) query.set("stream_id", params.stream_id);
    query.set("limit", String(params.limit || 50));
    query.set("offset", String(params.offset || 0));
    return request<{ jobs: Job[]; counts: Record<string, number> }>(`/api/jobs?${query.toString()}`);
  },
  job: (id: number) => request<JobDetailResponse>(`/api/jobs/${id}`),
  createJob: (body: { stream_id: string; date: string; force: boolean }) =>
    request<{ job: Job; existing: boolean }>("/api/jobs", { method: "POST", body: JSON.stringify(body) }),
  retryJob: (id: number) => request<{ job: Job }>(`/api/jobs/${id}/retry`, { method: "POST" }),
  cancelJob: (id: number) => request<{ job: Job }>(`/api/jobs/${id}/cancel`, { method: "POST" }),
  timeline: (id: number, offset = 0, limit = 500) =>
    request<Timeline>(`/api/jobs/${id}/timeline?offset=${offset}&limit=${limit}`),
  timelineSummary: (id: number) => request<TimelineSummaryResponse>(`/api/jobs/${id}/timeline/summary`),
  discoveryConfig: () => request<DiscoveryConfig>("/api/discovery/config"),
  saveDiscoveryConfig: (config: DiscoveryConfig) =>
    request<DiscoveryConfig>("/api/discovery/config", { method: "PUT", body: JSON.stringify(config) }),
  runDiscovery: () => request<{ run: DiscoveryRun }>("/api/discovery/run", { method: "POST" }),
  discoveryRuns: (limit = 25) => request<{ runs: DiscoveryRun[] }>(`/api/discovery/runs?limit=${limit}`),
  logs: (params: { job_id?: number; level?: string; stage?: string; search?: string; limit?: number; offset?: number }) => {
    const query = new URLSearchParams();
    if (params.job_id) query.set("job_id", String(params.job_id));
    if (params.level) query.set("level", params.level);
    if (params.stage) query.set("stage", params.stage);
    if (params.search) query.set("search", params.search);
    query.set("limit", String(params.limit || 300));
    query.set("offset", String(params.offset || 0));
    return request<{ entries: LogEntry[]; service: LogEntry[] }>(`/api/logs?${query.toString()}`);
  },
  settings: () => request<RuntimeSettings>("/api/settings"),
  saveSettings: (settings: RuntimeSettings) =>
    request<RuntimeSettings>("/api/settings", { method: "PUT", body: JSON.stringify(settings) }),
  storage: () => request<{ storage: StorageConfig[] }>("/api/storage"),
  saveStorage: (config: StorageConfig) =>
    config.id
      ? request<{ storage: StorageConfig }>(`/api/storage/${config.id}`, { method: "PUT", body: JSON.stringify(config) })
      : request<{ storage: StorageConfig }>("/api/storage", { method: "POST", body: JSON.stringify(config) }),
  deleteStorage: (id: number) => request<{ deleted: boolean }>(`/api/storage/${id}`, { method: "DELETE" }),
  testStorage: (id: number) => request<{ ok: boolean }>(`/api/storage/${id}/test`, { method: "POST" }),
};
