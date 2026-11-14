import {
  LogEntry,
  QueueItem,
  StreamsResponse,
  StreamView,
  UploaderStatus,
} from "./types";

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  if (!response.ok) {
    let message = response.statusText;
    try {
      const body = await response.json();
      if (body && body.error) {
        message = body.error;
      }
    } catch {
      // ignore
    }
    throw new Error(message || `request failed (${response.status})`);
  }
  return (await response.json()) as T;
}

export const api = {
  streams(): Promise<StreamsResponse> {
    return request<StreamsResponse>("/control/streams");
  },
  stream(id: string): Promise<StreamView> {
    return request<StreamView>(`/control/streams/${encodeURIComponent(id)}`);
  },
  logs(params: {
    stream?: string;
    level?: string;
    search?: string;
    limit?: number;
  }): Promise<{ entries: LogEntry[] }> {
    const query = new URLSearchParams();
    if (params.stream) query.set("stream", params.stream);
    if (params.level) query.set("level", params.level);
    if (params.search) query.set("search", params.search);
    if (params.limit) query.set("limit", String(params.limit));
    return request<{ entries: LogEntry[] }>(`/control/logs?${query.toString()}`);
  },
  start(id: string): Promise<{ ok: boolean }> {
    return request(`/control/streams/${encodeURIComponent(id)}/start`, { method: "POST" });
  },
  stop(id: string): Promise<{ ok: boolean }> {
    return request(`/control/streams/${encodeURIComponent(id)}/stop`, { method: "POST" });
  },
  restart(id: string): Promise<{ ok: boolean }> {
    return request(`/control/streams/${encodeURIComponent(id)}/restart`, { method: "POST" });
  },
  remove(id: string): Promise<{ ok: boolean }> {
    return request(`/control/streams/${encodeURIComponent(id)}`, { method: "DELETE" });
  },
  add(stream: unknown): Promise<{ ok: boolean; id: string }> {
    return request("/control/streams", { method: "POST", body: JSON.stringify(stream) });
  },
  update(id: string, stream: unknown): Promise<{ ok: boolean }> {
    return request(`/control/streams/${encodeURIComponent(id)}`, {
      method: "PUT",
      body: JSON.stringify(stream),
    });
  },
  uploaderStatus(): Promise<UploaderStatus> {
    return request<UploaderStatus>("/control/uploader/status");
  },
  queue(status?: string, limit = 100): Promise<{ queue: QueueItem[] }> {
    const query = new URLSearchParams();
    if (status) query.set("status", status);
    query.set("limit", String(limit));
    return request<{ queue: QueueItem[] }>(`/control/uploader/queue?${query.toString()}`);
  },
};
