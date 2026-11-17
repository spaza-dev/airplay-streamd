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
import { COUNTRY_OPTIONS } from "./countries";
import { DiscoveryStation, LogEntry, MetricsSnapshot, StreamView, UploaderStatus } from "./types";

const STREAM_TYPES = ["radio", "tv", "hls", "direct", "youtube", "twitch", "web"];
const LOG_LEVELS = ["debug", "info", "warn", "error"];
const PAGE_SIZE = 10;

type PanelMode = "closed" | "detail" | "add" | "edit";

interface StreamForm {
  name: string;
  type: string;
  url: string;
  enabled: boolean;
  chunk_seconds: string;
  metadata: string;
}

function emptyForm(): StreamForm {
  return {
    name: "",
    type: "radio",
    url: "",
    enabled: false,
    chunk_seconds: "3600",
    metadata: "",
  };
}

function formFromStream(stream: StreamView): StreamForm {
  return {
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

function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index++;
  }
  return `${value.toFixed(value >= 10 || index === 0 ? 0 : 1)} ${units[index]}`;
}

function formatRate(bytesPerSecond: number): string {
  return `${formatBytes(bytesPerSecond)}/s`;
}

function formatBitrate(bytesPerSecond: number): string {
  if (!bytesPerSecond || bytesPerSecond <= 0) return "-";
  return `${((bytesPerSecond * 8) / 1000).toFixed(0)} kbps`;
}

function formatPercent(value: number): string {
  return `${value.toFixed(1)}%`;
}

const emptyCapacity = {
  status: "insufficient",
  recommended_additional_streams: 0,
  confidence: "insufficient",
  active_streams: 0,
  resources: [],
  message: "Collecting system metrics...",
};

function emptyMetrics(): MetricsSnapshot {
  return {
    enabled: false,
    interval_seconds: 0,
    history: [],
    streams: [],
    capacity: emptyCapacity,
  };
}

function streamState(stream: StreamView): string {
  if (!stream.enabled) return "disabled";
  if (stream.error) return "error";
  if (stream.running) return "running";
  if (stream.stopped) return "stopped";
  return "idle";
}

function streamCountry(stream: StreamView): string {
  if (stream.radio?.country) return stream.radio.country.trim();
  const country = stream.metadata?.country;
  return typeof country === "string" ? country.trim() : "";
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
  const [metrics, setMetrics] = React.useState<MetricsSnapshot>(emptyMetrics());
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [warning, setWarning] = React.useState("");
  const [notice, setNotice] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const [tab, setTab] = React.useState("streams");
  const [search, setSearch] = React.useState("");
  const [typeFilter, setTypeFilter] = React.useState("all");
  const [statusFilter, setStatusFilter] = React.useState("all");
  const [countryFilter, setCountryFilter] = React.useState("all");
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
  const [radioCountry, setRadioCountry] = React.useState("");
  const [radioResults, setRadioResults] = React.useState<DiscoveryStation[]>([]);
  const [radioSearching, setRadioSearching] = React.useState(false);

  const refresh = React.useCallback(async () => {
    try {
      const data = await api.streams();
      setStreams(data.streams);
      setUploader(data.uploader);
      setError("");
      try {
        setMetrics(await api.metrics());
      } catch {
        // metrics are optional and must not break stream management
      }
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

  React.useEffect(() => {
    if (!warning) return;
    const timer = window.setTimeout(() => setWarning(""), 10000);
    return () => window.clearTimeout(timer);
  }, [warning]);

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
  }, [search, typeFilter, statusFilter, countryFilter]);

  const countries = React.useMemo(
    () =>
      Array.from(new Set(streams.map(streamCountry).filter(Boolean))).sort((a, b) =>
        a.localeCompare(b)
      ),
    [streams]
  );

  const filtered = streams.filter((stream) => {
    const query = search.trim().toLowerCase();
    if (
      query &&
      !`${stream.id} ${stream.name} ${stream.type} ${streamCountry(stream)}`.toLowerCase().includes(query)
    ) {
      return false;
    }
    if (typeFilter !== "all" && stream.type !== typeFilter) {
      return false;
    }
    if (statusFilter !== "all" && streamState(stream) !== statusFilter) {
      return false;
    }
    if (countryFilter !== "all" && streamCountry(stream) !== countryFilter) {
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
      return true;
    } catch (e) {
      setError((e as Error).message);
      return false;
    } finally {
      setBusy(false);
    }
  };

  const removeStream = async (id: string, label: string) => {
    if (!window.confirm(`Remove stream ${label}? Recordings and queued uploads are kept.`)) {
      return;
    }
    const wasSelected = selectedId === id;
    const removed = await runAction(() => api.remove(id), `Removed ${label}`);
    if (removed) {
      if (wasSelected) {
        setPanelMode("closed");
        setSelectedId(null);
      }
      setRadioResults((results) =>
        results.map((station) =>
          station.stationuuid === id || station.existing_stream_id === id
            ? { ...station, already_added: false, existing_stream_id: undefined }
            : station
        )
      );
    }
  };

  const startStream = async (id: string) => {
    setBusy(true);
    setError("");
    try {
      try {
        await api.start(id);
      } catch (e) {
        const message = (e as Error).message;
        if (!/capacity/i.test(message)) {
          throw e;
        }
        if (!window.confirm(`${message}\n\nStart anyway?`)) {
          return;
        }
        await api.start(id, true);
      }
      setNotice(`Started ${id}`);
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
    setRadioCountry("");
    setRadioResults([]);
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
    if (!form.name.trim() || !form.url.trim() || !form.type.trim()) {
      setError("name, type and url are required for a custom stream");
      return;
    }
    const payload: Record<string, unknown> = {
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
        const result = await api.add(payload);
        setNotice(`Stream ${result.id} added`);
        if (result.warnings && result.warnings.length > 0) {
          setWarning(result.warnings.join(" "));
        }
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

  const searchRadio = async () => {
    setRadioSearching(true);
    setError("");
    try {
      const data = await api.searchRadio({
        countrycode: radioCountry,
        limit: 50,
        hidebroken: "true",
      });
      setRadioResults(data.stations);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setRadioSearching(false);
    }
  };

  const addRadio = async (station: DiscoveryStation) => {
    setBusy(true);
    setError("");
    try {
      const { already_added, existing_stream_id, ...radio } = station;
      const result = await api.add({ radio, enabled: form.enabled });
      setNotice(`Stream ${result.id} added`);
      if (result.warnings && result.warnings.length > 0) {
        setWarning(result.warnings.join(" "));
      }
      setRadioResults((results) =>
        results.map((item) =>
          item.stationuuid === station.stationuuid
            ? { ...item, already_added: true, existing_stream_id: result.id }
            : item
        )
      );
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const counts = uploader.counts || {};
  const selectedState = selected ? streamState(selected) : "";
  const selectedMetrics = React.useMemo(
    () => metrics.streams.find((stream) => stream.id === selectedId) || null,
    [metrics, selectedId]
  );

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
        {warning ? (
          <MessageBar severity={MessageBarSeverity.Warning} onDismiss={() => setWarning("")}>
            {warning}
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
          <Tab id="system" name="System" />
          <Tab id="logs" name="Logs" />
        </TabBar>

        {tab === "streams" ? (
          <div className="streamd-content">
            <div className="streamd-toolbar">
              <TextField
                className="streamd-search"
                placeholder="Search streams by id, name, type or country"
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
              <label className="streamd-filter">
                Country
                <select value={countryFilter} onChange={(e) => setCountryFilter(e.target.value)}>
                  <option value="all">All</option>
                  {countries.map((country) => (
                    <option key={country} value={country}>
                      {country}
                    </option>
                  ))}
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
              <div className="streamd-table-wrap">
              <table className="streamd-table streamd-streams-table">
                <colgroup>
                  <col style={{ width: "20%" }} />
                  <col style={{ width: "15%" }} />
                  <col style={{ width: "7%" }} />
                  <col style={{ width: "9%" }} />
                  <col style={{ width: "11%" }} />
                  <col style={{ width: "7%" }} />
                  <col style={{ width: "11%" }} />
                  <col style={{ width: "20%" }} />
                </colgroup>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>ID</th>
                    <th>Type</th>
                    <th>Country</th>
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
                          <div className="streamd-name streamd-truncate" title={stream.name}>{stream.name}</div>
                          <div className="streamd-muted streamd-small streamd-truncate" title={stream.url}>{stream.url}</div>
                        </td>
                        <td className="streamd-mono">
                          <div className="streamd-truncate" title={stream.id}>{stream.id}</div>
                        </td>
                        <td>{stream.type}</td>
                        <td>
                          <div className="streamd-truncate" title={streamCountry(stream)}>
                            {streamCountry(stream) || "-"}
                          </div>
                        </td>
                        <td>
                          <span className={`streamd-status ${stateTone(state)}`}>
                            <span className="streamd-dot" />
                            {stateLabel(state)}
                          </span>
                          {stream.error ? (
                            <div className="streamd-small streamd-error-text streamd-truncate" title={stream.error}>
                              {stream.error}
                            </div>
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
                                onClick={() => startStream(stream.id)}
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
                              onClick={() => removeStream(stream.id, stream.name || stream.id)}
                            />
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                  {pageRows.length === 0 ? (
                    <tr>
                      <td colSpan={8} className="streamd-empty">
                        No streams match the current filters.
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
              </div>
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
        ) : tab === "system" ? (
          <SystemView metrics={metrics} streams={streams} />
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
                    onClick: () => startStream(selected.id),
                  },
              { text: "Edit", onClick: () => openEdit(selected) },
              {
                text: "Delete",
                danger: true,
                disabled: busy,
                onClick: () => removeStream(selected.id, selected.name || selected.id),
              },
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
                <Definition label="Source">{selected.source || "custom"}</Definition>
                <Definition label="Country">{streamCountry(selected) || "-"}</Definition>
                {selected.radio ? (
                  <>
                    <Definition label="Station">{selected.radio.name}</Definition>
                    <Definition label="Codec">
                      {selected.radio.codec || "-"} {selected.radio.bitrate ? `· ${selected.radio.bitrate} kbps` : ""}
                    </Definition>
                    <Definition label="Station UUID">
                      <span className="streamd-mono streamd-break">{selected.radio.stationuuid}</span>
                    </Definition>
                  </>
                ) : null}
                <Definition label="Source URL">
                  <span className="streamd-break">{selected.url}</span>
                </Definition>
                <Definition label="Enabled">{selected.enabled ? "Yes" : "No"}</Definition>
                <Definition label="Chunk seconds">{selected.chunk_seconds}</Definition>
                <Definition label="Last activity">{formatTime(selected.last_activity)}</Definition>
                {selectedMetrics ? (
                  <>
                    <Definition label="CPU">{formatPercent(selectedMetrics.cpu_percent)}</Definition>
                    <Definition label="Memory">{formatBytes(selectedMetrics.memory_bytes)}</Definition>
                    <Definition label="Output rate">
                      {formatRate(selectedMetrics.output_bytes_per_sec)}
                    </Definition>
                    <Definition label="Chunks / min">
                      {selectedMetrics.chunks_per_minute.toFixed(1)}
                    </Definition>
                    <Definition label="Last chunk">{formatTime(selectedMetrics.last_chunk_at)}</Definition>
                  </>
                ) : null}
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
            description={panelMode === "edit" ? editing?.id : "Discover radio stations or register a custom source"}
            footerButtonProps={[
              { text: "Cancel", onClick: closePanel },
              { text: panelMode === "edit" ? "Save" : "Add", primary: true, onClick: saveStream },
            ]}
          >
            {panelMode === "add" ? (
              <div className="streamd-discovery">
                <div className="streamd-discovery-search">
                  <label className="streamd-filter streamd-discovery-country">
                    Country
                    <select
                      value={radioCountry}
                      onChange={(event) => setRadioCountry(event.target.value)}
                    >
                      <option value="">Select a country</option>
                      {COUNTRY_OPTIONS.map((country) => (
                        <option key={country.code} value={country.code}>
                          {country.name}
                        </option>
                      ))}
                    </select>
                  </label>
                  <Button
                    text={radioSearching ? "Searching..." : "Search"}
                    disabled={radioSearching || !radioCountry}
                    onClick={searchRadio}
                  />
                </div>
                {radioResults.length > 0 ? (
                  <div className="streamd-discovery-results">
                    {radioResults.map((station) => (
                      <div className="streamd-discovery-row" key={station.stationuuid}>
                        <div className="streamd-discovery-copy">
                          <div className="streamd-name streamd-truncate" title={station.name}>{station.name}</div>
                          <div
                            className="streamd-muted streamd-small streamd-truncate"
                            title={[station.country, station.codec, station.bitrate ? `${station.bitrate} kbps` : "", station.hls ? "HLS" : ""]
                              .filter(Boolean)
                              .join(" · ")}
                          >
                            {[station.country, station.codec, station.bitrate ? `${station.bitrate} kbps` : "", station.hls ? "HLS" : ""]
                              .filter(Boolean)
                              .join(" · ")}
                          </div>
                        </div>
                        <div className="streamd-discovery-actions">
                          <Button
                            text={station.already_added ? "Remove" : "Add"}
                            primary={!station.already_added}
                            subtle={station.already_added}
                            danger={station.already_added}
                            disabled={busy || (station.already_added && !station.existing_stream_id)}
                            onClick={() =>
                              station.already_added && station.existing_stream_id
                                ? removeStream(station.existing_stream_id, station.name)
                                : addRadio(station)
                            }
                          />
                        </div>
                      </div>
                    ))}
                  </div>
                ) : null}
                <div className="streamd-discovery-divider">or add a custom stream</div>
              </div>
            ) : null}
            <div className="streamd-form">
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
                    placeholder="3600"
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

function Metric(props: {
  label: string;
  value: React.ReactNode;
  tone: string;
  hint?: React.ReactNode;
}) {
  return (
    <div className="streamd-metric">
      <div className={`streamd-metric-value ${props.tone}`}>{props.value}</div>
      <div className="streamd-metric-label">{props.label}</div>
      {props.hint ? <div className="streamd-metric-hint">{props.hint}</div> : null}
    </div>
  );
}

function capacityTone(status: string): string {
  switch (status) {
    case "ok":
      return "success";
    case "warning":
      return "warning";
    case "at_capacity":
      return "error";
    default:
      return "muted";
  }
}

function capacityLabel(status: string): string {
  switch (status) {
    case "ok":
      return "Healthy";
    case "warning":
      return "Limited headroom";
    case "at_capacity":
      return "At capacity";
    default:
      return "Collecting";
  }
}

function resourceValue(resource: { unit: string; used: number; limit: number }): string {
  if (resource.unit === "percent") {
    return `${resource.used.toFixed(1)}% / ${resource.limit.toFixed(0)}%`;
  }
  if (resource.unit === "bytes_per_sec") {
    return `${formatRate(resource.used)} / ${formatRate(resource.limit)}`;
  }
  return `${formatBytes(resource.used)} / ${formatBytes(resource.limit)}`;
}

function Sparkline(props: { values: number[]; tone?: string }) {
  const values = props.values.filter((value) => Number.isFinite(value));
  if (values.length < 2) {
    return <div className="streamd-sparkline-empty">-</div>;
  }
  const width = 120;
  const height = 28;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const points = values
    .map((value, index) => {
      const x = (index / (values.length - 1)) * width;
      const y = height - ((value - min) / span) * height;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  return (
    <svg className={`streamd-sparkline ${props.tone || "info"}`} viewBox={`0 0 ${width} ${height}`}>
      <polyline points={points} />
    </svg>
  );
}

function SystemView(props: { metrics: MetricsSnapshot; streams: StreamView[] }) {
  const current = props.metrics.current;
  const capacity = props.metrics.capacity;
  const history = props.metrics.history || [];
  const streamMetrics = props.metrics.streams || [];
  const stateByID = React.useMemo(() => {
    const map: Record<string, StreamView> = {};
    props.streams.forEach((stream) => {
      map[stream.id] = stream;
    });
    return map;
  }, [props.streams]);

  if (!props.metrics.enabled) {
    return (
      <div className="streamd-content">
        <p className="streamd-muted">
          Metrics collection is disabled. Enable <code>metrics.enabled</code> in the configuration to
          see capacity information.
        </p>
      </div>
    );
  }

  return (
    <div className="streamd-content">
      <div className="streamd-system-strip">
        <Metric label="Active streams" value={current ? current.active_streams : "-"} tone="info" />
        <Metric
          label="CPU"
          value={current ? formatPercent(current.cpu_percent) : "-"}
          tone={(current?.cpu_percent || 0) >= 80 ? "error" : "success"}
          hint={current ? `load ${current.load_1.toFixed(2)}` : ""}
        />
        <Metric
          label="Memory"
          value={current ? formatBytes(current.memory_used_bytes) : "-"}
          tone="info"
          hint={current ? `of ${formatBytes(current.memory_total_bytes)}` : ""}
        />
        <Metric
          label="Disk free"
          value={current ? formatBytes(current.disk_free_bytes) : "-"}
          tone="info"
          hint={current ? `of ${formatBytes(current.disk_total_bytes)}` : ""}
        />
        <Metric
          label="Network RX"
          value={current ? formatRate(current.net_rx_bytes_per_sec) : "-"}
          tone="info"
        />
        <Metric
          label="Network TX"
          value={current ? formatRate(current.net_tx_bytes_per_sec) : "-"}
          tone="info"
        />
        <Metric
          label="Can add"
          value={
            capacity.recommended_additional_streams >= 9999
              ? "not limiting"
              : capacity.recommended_additional_streams || 0
          }
          tone={capacityTone(capacity.status)}
          hint={`${capacityLabel(capacity.status)} · ${capacity.confidence}`}
        />
      </div>

      <div className="streamd-capacity">
        <div className="streamd-capacity-header">
          <div>
            <div className="streamd-section-title">Capacity</div>
            <div className="streamd-muted">
              {capacity.message || "Waiting for the first metrics sample."}
            </div>
          </div>
          <span className={`streamd-pill ${capacityTone(capacity.status)}`}>
            {capacityLabel(capacity.status)}
          </span>
        </div>
        <div className="streamd-resource-list">
          {(capacity.resources || []).map((resource) => (
            <div className="streamd-resource" key={resource.name}>
              <div className="streamd-resource-head">
                <span className="streamd-resource-name">
                  {resource.name}
                  {resource.limiting ? <span className="streamd-resource-tag">bottleneck</span> : null}
                </span>
                <span className="streamd-muted">{resourceValue(resource)}</span>
              </div>
              <div className="streamd-bar">
                <div
                  className={`streamd-bar-fill ${capacityTone(
                    resource.used_percent >= 100 ? "at_capacity" : resource.used_percent >= 85 ? "warning" : "ok"
                  )}`}
                  style={{ width: `${Math.min(100, Math.max(0, resource.used_percent))}%` }}
                />
              </div>
              <div className="streamd-resource-foot">
                <span className="streamd-muted">
                  {resource.additional_streams >= 9999
                    ? "not limiting"
                    : `${resource.additional_streams} more stream(s)`}
                </span>
              </div>
            </div>
          ))}
          {(capacity.resources || []).length === 0 ? (
            <p className="streamd-muted">No capacity data yet.</p>
          ) : null}
        </div>
        <div className="streamd-sparkline-row">
          <div>
            <div className="streamd-muted streamd-small">CPU history</div>
            <Sparkline values={history.map((point) => point.cpu_percent)} tone="success" />
          </div>
          <div>
            <div className="streamd-muted streamd-small">Network RX history</div>
            <Sparkline values={history.map((point) => point.net_rx_bytes_per_sec)} tone="info" />
          </div>
          <div>
            <div className="streamd-muted streamd-small">Backlog history</div>
            <Sparkline values={history.map((point) => point.backlog_bytes)} tone="warning" />
          </div>
        </div>
      </div>

      <div className="streamd-section-title streamd-section-spaced">Per-stream usage</div>
      <table className="streamd-table">
        <thead>
          <tr>
            <th>Stream</th>
            <th>Status</th>
            <th>CPU</th>
            <th>Memory</th>
            <th>Output rate</th>
            <th>Chunks / min</th>
            <th>Last chunk</th>
          </tr>
        </thead>
        <tbody>
          {streamMetrics.map((stream) => {
            const view = stateByID[stream.id];
            const state = view ? streamState(view) : "idle";
            return (
              <tr key={stream.id}>
                <td className="streamd-mono">{stream.id}</td>
                <td>
                  <span className={`streamd-status ${stateTone(state)}`}>
                    <span className="streamd-dot" />
                    {stateLabel(state)}
                  </span>
                </td>
                <td>{formatPercent(stream.cpu_percent)}</td>
                <td>{formatBytes(stream.memory_bytes)}</td>
                <td>{formatBitrate(stream.output_bytes_per_sec)}</td>
                <td>{stream.chunks_per_minute.toFixed(1)}</td>
                <td>{formatTime(stream.last_chunk_at)}</td>
              </tr>
            );
          })}
          {streamMetrics.length === 0 ? (
            <tr>
              <td colSpan={7} className="streamd-empty">
                No running streams have metrics yet.
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
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
