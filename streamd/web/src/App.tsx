import * as React from "react";
import { Page } from "azure-devops-ui/Page";
import { Header, TitleSize } from "azure-devops-ui/Header";
import { Tab, TabBar, TabSize } from "azure-devops-ui/Tabs";
import { Button } from "azure-devops-ui/Button";
import { TextField } from "azure-devops-ui/TextField";
import { Panel } from "azure-devops-ui/Panel";
import { Spinner } from "azure-devops-ui/Spinner";
import { MessageBar, MessageBarSeverity } from "azure-devops-ui/MessageBar";
import { api } from "./api";
import { LogEntry, StreamView, UploaderStatus } from "./types";

const STREAM_TYPES = ["radio", "tv", "hls", "direct", "youtube", "twitch", "web"];
const LOG_LEVELS = ["debug", "info", "warn", "error"];
const PAGE_SIZE = 10;

type PanelMode = "closed" | "detail" | "add" | "edit";

interface StreamForm {
  id: string;
  name: string;
  type: string;
  url: string;
  enabled: boolean;
  chunk_seconds: string;
  metadata: string;
}

function emptyForm(): StreamForm {
  return {
    id: "",
    name: "",
    type: "radio",
    url: "",
    enabled: false,
    chunk_seconds: "",
    metadata: "",
  };
}

function formFromStream(stream: StreamView): StreamForm {
  return {
    id: stream.id,
    name: stream.name,
    type: stream.type,
    url: stream.url,
    enabled: stream.enabled,
    chunk_seconds: String(stream.chunk_seconds || ""),
    metadata: metadataToText(stream.metadata),
  };
}

function metadataToText(metadata?: Record<string, unknown>): string {
  if (!metadata) return "";
  return Object.keys(metadata)
    .sort()
    .map((key) => `${key}=${String(metadata[key])}`)
    .join("\n");
}

function parseMetadata(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  text.split(/\r?\n/).forEach((line) => {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) return;
    const idx = trimmed.indexOf("=");
    if (idx <= 0) return;
    out[trimmed.slice(0, idx).trim()] = trimmed.slice(idx + 1).trim();
  });
  return out;
}

function formatTime(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (isNaN(date.getTime())) return "-";
  return date.toLocaleString();
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function streamState(stream: StreamView): string {
  if (!stream.enabled) return "disabled";
  if (stream.error) return "error";
  if (stream.running) return "running";
  if (stream.stopped) return "stopped";
  return "idle";
}

function stateLabel(state: string): string {
  switch (state) {
    case "running":
      return "Running";
    case "error":
      return "Error";
    case "stopped":
      return "Stopped";
    case "disabled":
      return "Disabled";
    default:
      return "Idle";
  }
}

function stateTone(state: string): string {
  switch (state) {
    case "running":
      return "success";
    case "error":
      return "error";
    case "stopped":
      return "warning";
    case "disabled":
      return "muted";
    default:
      return "info";
  }
}

export function App() {
  const [streams, setStreams] = React.useState<StreamView[]>([]);
  const [uploader, setUploader] = React.useState<UploaderStatus>({
    enabled: false,
    concurrency: 0,
    active: 0,
    counts: {},
  });
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [notice, setNotice] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const [tab, setTab] = React.useState("streams");
  const [search, setSearch] = React.useState("");
  const [typeFilter, setTypeFilter] = React.useState("all");
  const [statusFilter, setStatusFilter] = React.useState("all");
  const [page, setPage] = React.useState(0);

  const [selectedId, setSelectedId] = React.useState<string | null>(null);
  const [detailTab, setDetailTab] = React.useState("overview");
  const [detailLogs, setDetailLogs] = React.useState<LogEntry[]>([]);

  const [logs, setLogs] = React.useState<LogEntry[]>([]);
  const [logSearch, setLogSearch] = React.useState("");
  const [logLevel, setLogLevel] = React.useState("");
  const [logStream, setLogStream] = React.useState("");

  const [panelMode, setPanelMode] = React.useState<PanelMode>("closed");
  const [form, setForm] = React.useState<StreamForm>(emptyForm());

  const refresh = React.useCallback(async () => {
    try {
      const data = await api.streams();
      setStreams(data.streams);
      setUploader(data.uploader);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    refresh();
    const timer = window.setInterval(refresh, 3000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  React.useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(""), 4000);
    return () => window.clearTimeout(timer);
  }, [notice]);

  const selected = React.useMemo(
    () => streams.find((stream) => stream.id === selectedId) || null,
    [streams, selectedId]
  );
  const editing = React.useMemo(
    () => (panelMode === "edit" && selectedId ? streams.find((s) => s.id === selectedId) || null : null),
    [panelMode, selectedId, streams]
  );

  React.useEffect(() => {
    if (panelMode !== "detail" || !selected || detailTab !== "logs") {
      return;
    }
    let active = true;
    const load = async () => {
      try {
        const data = await api.logs({ stream: selected.id, limit: 300 });
        if (active) setDetailLogs(data.entries);
      } catch {
        // ignore transient errors
      }
    };
    load();
    const timer = window.setInterval(load, 3000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [panelMode, selectedId, detailTab]);

  React.useEffect(() => {
    if (tab !== "logs") {
      return;
    }
    let active = true;
    const load = async () => {
      try {
        const data = await api.logs({
          stream: logStream || undefined,
          level: logLevel || undefined,
          search: logSearch || undefined,
          limit: 500,
        });
        if (active) setLogs(data.entries);
      } catch {
        // ignore transient errors
      }
    };
    load();
    const timer = window.setInterval(load, 3000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [tab, logLevel, logSearch, logStream]);

  React.useEffect(() => {
    setPage(0);
  }, [search, typeFilter, statusFilter]);

  const filtered = streams.filter((stream) => {
    const query = search.trim().toLowerCase();
    if (query && !`${stream.id} ${stream.name} ${stream.type}`.toLowerCase().includes(query)) {
      return false;
    }
    if (typeFilter !== "all" && stream.type !== typeFilter) {
      return false;
    }
    if (statusFilter !== "all" && streamState(stream) !== statusFilter) {
      return false;
    }
    return true;
  });

  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const currentPage = Math.min(page, pageCount - 1);
  const pageRows = filtered.slice(currentPage * PAGE_SIZE, currentPage * PAGE_SIZE + PAGE_SIZE);

  const runAction = async (action: () => Promise<unknown>, message: string) => {
    setBusy(true);
    setError("");
    try {
      await action();
      setNotice(message);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const openDetail = (stream: StreamView) => {
    setSelectedId(stream.id);
    setDetailTab("overview");
    setDetailLogs([]);
    setPanelMode("detail");
  };

  const openAdd = () => {
    setSelectedId(null);
    setForm(emptyForm());
    setPanelMode("add");
  };

  const openEdit = (stream: StreamView) => {
    setSelectedId(stream.id);
    setForm(formFromStream(stream));
    setPanelMode("edit");
  };

  const closePanel = () => {
    setPanelMode("closed");
    setSelectedId(null);
  };

  const saveStream = async () => {
    if (!form.id.trim() || !form.name.trim() || !form.url.trim()) {
      setError("id, name and url are required");
      return;
    }
    const payload: Record<string, unknown> = {
      id: form.id.trim(),
      name: form.name.trim(),
      type: form.type,
      url: form.url.trim(),
      enabled: form.enabled,
      metadata: parseMetadata(form.metadata),
    };
    const chunk = parseInt(form.chunk_seconds, 10);
    if (!isNaN(chunk) && chunk > 0) {
      payload.chunk_seconds = chunk;
    }
    if (editing) {
      payload.id = editing.id;
      payload.audio = editing.audio;
      payload.video = editing.video;
    }
    setBusy(true);
    setError("");
    try {
      if (editing) {
        await api.update(editing.id, payload);
        setNotice(`Stream ${editing.id} updated`);
      } else {
        await api.add(payload);
        setNotice(`Stream ${payload.id} added`);
      }
      setPanelMode("closed");
      setSelectedId(null);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const counts = uploader.counts || {};
  const selectedState = selected ? streamState(selected) : "";

  return (
    <Page className="streamd-page">
      <div className="streamd-shell">
        <div className="streamd-masthead">
          <div>
            <div className="streamd-product">Spaza Media</div>
            <div className="streamd-title">streamd</div>
            <div className="streamd-subtitle">Stream capture and upload service</div>
          </div>
          <div className="streamd-masthead-actions">
            <span className={`streamd-pill ${uploader.enabled ? "success" : "muted"}`}>
              Uploader {uploader.enabled ? "enabled" : "disabled"}
            </span>
            <Button text="Add stream" primary onClick={openAdd} />
          </div>
        </div>

        <div className="streamd-uploader-strip">
          <Metric label="Pending" value={counts.pending || 0} tone="info" />
          <Metric label="Uploading" value={counts.uploading || 0} tone="info" />
          <Metric label="Failed / retrying" value={counts.failed || 0} tone="warning" />
          <Metric label="Completed" value={counts.completed || 0} tone="success" />
          <Metric label="Active workers" value={uploader.active || 0} tone="muted" />
          <Metric label="Concurrency" value={uploader.concurrency || 0} tone="muted" />
        </div>

        {error ? (
          <MessageBar severity={MessageBarSeverity.Error} onDismiss={() => setError("")}>
            {error}
          </MessageBar>
        ) : null}
        {notice ? (
          <MessageBar severity={MessageBarSeverity.Success} onDismiss={() => setNotice("")}>
            {notice}
          </MessageBar>
        ) : null}

        <TabBar
          className="streamd-tabs"
          selectedTabId={tab}
          onSelectedTabChanged={setTab}
          tabSize={TabSize.Compact}
        >
          <Tab id="streams" name="Streams" />
          <Tab id="logs" name="Logs" />
        </TabBar>

        {tab === "streams" ? (
          <div className="streamd-content">
            <div className="streamd-toolbar">
              <TextField
                className="streamd-search"
                placeholder="Search streams by id, name or type"
                value={search}
                onChange={(_event, value) => setSearch(value || "")}
              />
              <label className="streamd-filter">
                Type
                <select value={typeFilter} onChange={(e) => setTypeFilter(e.target.value)}>
                  <option value="all">All</option>
                  {STREAM_TYPES.map((type) => (
                    <option key={type} value={type}>
                      {type}
                    </option>
                  ))}
                </select>
              </label>
              <label className="streamd-filter">
                Status
                <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
                  <option value="all">All</option>
                  <option value="running">Running</option>
                  <option value="stopped">Stopped</option>
                  <option value="error">Error</option>
                  <option value="disabled">Disabled</option>
                  <option value="idle">Idle</option>
                </select>
              </label>
              <div className="streamd-toolbar-spacer" />
              <span className="streamd-muted">{filtered.length} streams</span>
            </div>

            {loading ? (
              <div className="streamd-loading">
                <Spinner label="Loading streams..." />
              </div>
            ) : (
              <table className="streamd-table">
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>ID</th>
                    <th>Type</th>
                    <th>Status</th>
                    <th>Enabled</th>
                    <th>Last activity</th>
                    <th className="streamd-actions-col">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {pageRows.map((stream) => {
                    const state = streamState(stream);
                    return (
                      <tr key={stream.id} onClick={() => openDetail(stream)}>
                        <td>
                          <div className="streamd-name">{stream.name}</div>
                          <div className="streamd-muted streamd-small">{stream.url}</div>
                        </td>
                        <td className="streamd-mono">{stream.id}</td>
                        <td>{stream.type}</td>
                        <td>
                          <span className={`streamd-status ${stateTone(state)}`}>
                            <span className="streamd-dot" />
                            {stateLabel(state)}
                          </span>
                          {stream.error ? (
                            <div className="streamd-small streamd-error-text">{stream.error}</div>
                          ) : null}
                        </td>
                        <td>{stream.enabled ? "Yes" : "No"}</td>
                        <td>{formatTime(stream.last_activity)}</td>
                        <td className="streamd-actions-col">
                          <div className="streamd-row-actions" onClick={(event) => event.stopPropagation()}>
                            {stream.running ? (
                              <Button
                                text="Stop"
                                subtle
                                disabled={busy}
                                onClick={() =>
                                  runAction(() => api.stop(stream.id), `Stopped ${stream.id}`)
                                }
                              />
                            ) : (
                              <Button
                                text="Start"
                                subtle
                                disabled={busy}
                                onClick={() =>
                                  runAction(() => api.start(stream.id), `Started ${stream.id}`)
                                }
                              />
                            )}
                            <Button
                              text="Restart"
                              subtle
                              disabled={busy}
                              onClick={() =>
                                runAction(() => api.restart(stream.id), `Restarted ${stream.id}`)
                              }
                            />
                            <Button text="Edit" subtle onClick={() => openEdit(stream)} />
                            <Button
                              text="Remove"
                              subtle
                              danger
                              disabled={busy}
                              onClick={() => {
                                if (window.confirm(`Remove stream ${stream.id}? Recordings and queued uploads are kept.`)) {
                                  if (selectedId === stream.id) closePanel();
                                  runAction(() => api.remove(stream.id), `Removed ${stream.id}`);
                                }
                              }}
                            />
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                  {pageRows.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="streamd-empty">
                        No streams match the current filters.
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            )}

            <div className="streamd-pagination">
              <Button
                text="Previous"
                subtle
                disabled={currentPage === 0}
                onClick={() => setPage(Math.max(0, currentPage - 1))}
              />
              <span className="streamd-muted">
                Page {currentPage + 1} of {pageCount}
              </span>
              <Button
                text="Next"
                subtle
                disabled={currentPage >= pageCount - 1}
                onClick={() => setPage(Math.min(pageCount - 1, currentPage + 1))}
              />
            </div>
          </div>
        ) : (
          <div className="streamd-content">
            <div className="streamd-toolbar">
              <TextField
                className="streamd-search"
                placeholder="Search log messages"
                value={logSearch}
                onChange={(_event, value) => setLogSearch(value || "")}
              />
              <label className="streamd-filter">
                Level
                <select value={logLevel} onChange={(e) => setLogLevel(e.target.value)}>
                  <option value="">All</option>
                  {LOG_LEVELS.map((level) => (
                    <option key={level} value={level}>
                      {level}
                    </option>
                  ))}
                </select>
              </label>
              <label className="streamd-filter">
                Stream
                <select value={logStream} onChange={(e) => setLogStream(e.target.value)}>
                  <option value="">All</option>
                  {streams.map((stream) => (
                    <option key={stream.id} value={stream.id}>
                      {stream.id}
                    </option>
                  ))}
                </select>
              </label>
              <div className="streamd-toolbar-spacer" />
              <span className="streamd-muted">{logs.length} entries</span>
            </div>
            <LogTable entries={logs} />
          </div>
        )}

        {panelMode === "detail" && selected ? (
          <Panel
            onDismiss={closePanel}
            contentClassName="streamd-panel-content"
            titleProps={{ text: selected.name }}
            description={selected.id}
            footerButtonProps={[
              { text: "Close", onClick: closePanel },
              selected.running
                ? {
                    text: "Stop",
                    onClick: () => runAction(() => api.stop(selected.id), `Stopped ${selected.id}`),
                  }
                : {
                    text: "Start",
                    onClick: () => runAction(() => api.start(selected.id), `Started ${selected.id}`),
                  },
              { text: "Edit", onClick: () => openEdit(selected) },
            ]}
          >
            <TabBar
              className="streamd-panel-tabs"
              selectedTabId={detailTab}
              onSelectedTabChanged={setDetailTab}
              tabSize={TabSize.Compact}
            >
              <Tab id="overview" name="Overview" />
              <Tab id="metadata" name="Metadata" />
              <Tab id="logs" name="Logs" />
            </TabBar>

            {detailTab === "overview" ? (
              <div className="streamd-panel-body">
                <Definition label="Status">
                  <span className={`streamd-status ${stateTone(selectedState)}`}>
                    <span className="streamd-dot" />
                    {stateLabel(selectedState)}
                  </span>
                </Definition>
                <Definition label="Source type">{selected.type}</Definition>
                <Definition label="Source URL">
                  <span className="streamd-break">{selected.url}</span>
                </Definition>
                <Definition label="Enabled">{selected.enabled ? "Yes" : "No"}</Definition>
                <Definition label="Chunk seconds">{selected.chunk_seconds}</Definition>
                <Definition label="Last activity">{formatTime(selected.last_activity)}</Definition>
                {selected.error ? (
                  <Definition label="Error">
                    <span className="streamd-error-text">{selected.error}</span>
                  </Definition>
                ) : null}
                {selected.audio ? (
                  <Definition label="Audio">
                    {selected.audio.codec} · {selected.audio.container} · {selected.audio.bitrate} ·{" "}
                    {selected.audio.sample_rate} Hz · {selected.audio.channels} ch
                  </Definition>
                ) : null}
                {selected.video && selected.video.enabled ? (
                  <Definition label="Video">
                    {selected.video.codec} · {selected.video.bitrate} · {selected.video.container}
                  </Definition>
                ) : (
                  <Definition label="Video">Audio only</Definition>
                )}
              </div>
            ) : null}

            {detailTab === "metadata" ? (
              <div className="streamd-panel-body">
                {selected.metadata && Object.keys(selected.metadata).length > 0 ? (
                  <table className="streamd-kv">
                    <tbody>
                      {Object.keys(selected.metadata)
                        .sort()
                        .map((key) => (
                          <tr key={key}>
                            <th>{key}</th>
                            <td>{String(selected.metadata![key])}</td>
                          </tr>
                        ))}
                    </tbody>
                  </table>
                ) : (
                  <p className="streamd-muted">No metadata configured for this stream.</p>
                )}
              </div>
            ) : null}

            {detailTab === "logs" ? (
              <div className="streamd-panel-body">
                <LogTable entries={detailLogs} compact />
              </div>
            ) : null}
          </Panel>
        ) : null}

        {panelMode === "add" || panelMode === "edit" ? (
          <Panel
            onDismiss={closePanel}
            contentClassName="streamd-panel-content"
            titleProps={{ text: panelMode === "edit" ? "Edit stream" : "Add stream" }}
            description={panelMode === "edit" ? form.id : "Register a new capture source"}
            footerButtonProps={[
              { text: "Cancel", onClick: closePanel },
              { text: panelMode === "edit" ? "Save" : "Add", primary: true, onClick: saveStream },
            ]}
          >
            <div className="streamd-form">
              <label>
                <span>ID</span>
                <TextField
                  value={form.id}
                  disabled={panelMode === "edit"}
                  onChange={(_event, value) => setForm({ ...form, id: value || "" })}
                  placeholder="capital-fm-ke"
                />
              </label>
              <label>
                <span>Name</span>
                <TextField
                  value={form.name}
                  onChange={(_event, value) => setForm({ ...form, name: value || "" })}
                  placeholder="Capital FM Kenya"
                />
              </label>
              <label>
                <span>Type</span>
                <select
                  value={form.type}
                  onChange={(event) => setForm({ ...form, type: event.target.value })}
                >
                  {STREAM_TYPES.map((type) => (
                    <option key={type} value={type}>
                      {type}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                <span>Source URL</span>
                <TextField
                  value={form.url}
                  onChange={(_event, value) => setForm({ ...form, url: value || "" })}
                  placeholder="https://example.com/stream"
                />
              </label>
              <div className="streamd-form-row">
                <label className="streamd-checkbox">
                  <input
                    type="checkbox"
                    checked={form.enabled}
                    onChange={(event) => setForm({ ...form, enabled: event.target.checked })}
                  />
                  <span>Enabled on startup</span>
                </label>
                <label>
                  <span>Chunk seconds</span>
                  <TextField
                    value={form.chunk_seconds}
                    onChange={(_event, value) =>
                      setForm({ ...form, chunk_seconds: (value || "").replace(/[^0-9]/g, "") })
                    }
                    placeholder="300"
                  />
                </label>
              </div>
              <label>
                <span>Metadata (one key=value per line)</span>
                <textarea
                  className="streamd-textarea"
                  value={form.metadata}
                  onChange={(event) => setForm({ ...form, metadata: event.target.value })}
                  rows={7}
                  placeholder={"country=Kenya\ncity=Nairobi\nlanguage=English"}
                />
              </label>
            </div>
          </Panel>
        ) : null}
      </div>
    </Page>
  );
}

function Metric(props: { label: string; value: number; tone: string }) {
  return (
    <div className="streamd-metric">
      <div className={`streamd-metric-value ${props.tone}`}>{props.value}</div>
      <div className="streamd-metric-label">{props.label}</div>
    </div>
  );
}

function Definition(props: { label: string; children: React.ReactNode }) {
  return (
    <div className="streamd-definition">
      <div className="streamd-definition-label">{props.label}</div>
      <div className="streamd-definition-value">{props.children}</div>
    </div>
  );
}

function LogTable(props: { entries: LogEntry[]; compact?: boolean }) {
  return (
    <div className={props.compact ? "streamd-log-wrap compact" : "streamd-log-wrap"}>
      <table className="streamd-log-table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Level</th>
            <th>Stream</th>
            <th>Message</th>
          </tr>
        </thead>
        <tbody>
          {props.entries.map((entry, index) => (
            <tr key={`${entry.time}-${index}`}>
              <td className="streamd-mono streamd-small">{formatTime(entry.time)}</td>
              <td>
                <span className={`streamd-level ${entry.level}`}>{entry.level}</span>
              </td>
              <td className="streamd-mono streamd-small">{entry.stream || "-"}</td>
              <td className="streamd-log-message">{entry.message}</td>
            </tr>
          ))}
          {props.entries.length === 0 ? (
            <tr>
              <td colSpan={4} className="streamd-empty">
                No log entries.
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  );
}
