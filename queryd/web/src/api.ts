import { CallbackDelivery, Job, JobDetail, LogEntry, Match, Stats } from "./types";

const TOKEN_KEY = "queryd_admin_token";

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
    ...(init && init.headers ? (init.headers as Record<string, string>) : {}),
  };
  if (!(init && init.body instanceof FormData)) {
    headers["Content-Type"] = "application/json";
  }
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
  jobs: (params: { status?: string; limit?: number; offset?: number }) => {
    const query = new URLSearchParams();
    if (params.status) query.set("status", params.status);
    query.set("limit", String(params.limit || 100));
    query.set("offset", String(params.offset || 0));
    return request<{ jobs: Job[]; counts: Record<string, number> }>(`/api/jobs?${query.toString()}`);
  },
  job: (id: number) => request<JobDetail>(`/api/jobs/${id}`),
  createJob: (form: FormData) =>
    request<{ job: Job }>("/api/jobs/upload", { method: "POST", body: form }),
  pauseJob: (id: number) => request<{ job: Job }>(`/api/jobs/${id}/pause`, { method: "POST" }),
  resumeJob: (id: number) => request<{ job: Job }>(`/api/jobs/${id}/resume`, { method: "POST" }),
  cancelJob: (id: number) => request<{ job: Job }>(`/api/jobs/${id}/cancel`, { method: "POST" }),
  matches: (id: number, offset = 0, limit = 500) =>
    request<{ matches: Match[]; total: number }>(`/api/jobs/${id}/matches?offset=${offset}&limit=${limit}`),
  jobLogs: (id: number) => request<{ entries: LogEntry[] }>(`/api/jobs/${id}/logs?limit=500`),
  allLogs: (params: { level?: string; search?: string }) => {
    const query = new URLSearchParams();
    if (params.level) query.set("level", params.level);
    if (params.search) query.set("search", params.search);
    query.set("limit", "500");
    return request<{ entries: LogEntry[]; service: LogEntry[] }>(`/api/jobs/logs?${query.toString()}`);
  },
  callbacks: (id: number) =>
    request<{ deliveries: CallbackDelivery[] }>(`/api/jobs/${id}/callbacks?limit=200`),
};
