import * as React from "react";
import { Page } from "azure-devops-ui/Page";
import { Tab, TabBar, TabSize } from "azure-devops-ui/Tabs";
import { Button } from "azure-devops-ui/Button";
import { TextField } from "azure-devops-ui/TextField";
import { Panel } from "azure-devops-ui/Panel";
import { Spinner } from "azure-devops-ui/Spinner";
import { MessageBar, MessageBarSeverity } from "azure-devops-ui/MessageBar";
import { api, getToken, setToken } from "./api";
import {
  DiscoveryConfig,
  DiscoveryRun,
  Gap,
  Job,
  LogEntry,
  Recording,
  RuntimeSettings,
  Stats,
  StorageConfig,
  Timeline,
  TimelineSummary,
} from "./types";

type JobPanelTab = "overview" | "timeline" | "logs";
type PanelMode = "closed" | "job" | "new-job" | "storage";

const JOB_STATUSES = [
  "queued",
  "downloading",
  "caching",
  "storing",
  "validating",
  "uploading",
  "completed",
  "failed",
  "cancelled",
];

const LOG_LEVELS = ["debug", "info", "warn", "error"];

function formatTime(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (isNaN(date.getTime())) return "-";
  return date.toLocaleString();
}

function formatBytes(bytes: number): string {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let index = 0;
  let value = bytes;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index++;
  }
  return `${value.toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

function formatDuration(ms?: number): string {
  if (!ms || ms <= 0) return "-";
  const total = Math.floor(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return h > 0 ? `${h}h ${m}m ${s}s` : m > 0 ? `${m}m ${s}s` : `${s}s`;
}

function localDate(offsetDays = 0): string {
  const date = new Date();
  date.setDate(date.getDate() + offsetDays);
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${date.getFullYear()}-${month}-${day}`;
}

function statusTone(status: string): string {
  switch (status) {
    case "completed":
      return "success";
    case "failed":
      return "error";
    case "cancelled":
      return "muted";
    case "queued":
      return "info";
    case "warning":
      return "warning";
    default:
      return "running";
  }
}

function parseSummary(job: Job): TimelineSummary {
  if (!job.timeline_summary) return {};
  try {
    return JSON.parse(job.timeline_summary);
  } catch {
    return {};
  }
}

function Definition(props: { label: string; children: React.ReactNode }) {
  return (
    <div className="idx-definition">
      <div className="idx-definition-label">{props.label}</div>
      <div className="idx-definition-value">{props.children}</div>
    </div>
  );
}

function Metric(props: { label: string; value: React.ReactNode; tone?: string; hint?: string }) {
  return (
    <div className={`idx-metric ${props.tone || "info"}`}>
      <div className="idx-metric-value">{props.value}</div>
      <div className="idx-metric-label">{props.label}</div>
      {props.hint ? <div className="idx-metric-hint">{props.hint}</div> : null}
    </div>
  );
}

function Status(props: { status: string; label?: string }) {
  return (
    <span className={`idx-status ${statusTone(props.status)}`}>
      <span className="idx-dot" />
      {props.label || props.status}
    </span>
  );
}

function emptyStorage(): StorageConfig {
  return {
    id: 0,
    name: "",
    role: "source",
    endpoint: "",
    bucket: "",
    region: "auto",
    access_key: "",
    secret_key: "",
    prefix: "",
    path_style: false,
  };
}

export function App() {
  const [stats, setStats] = React.useState<Stats | null>(null);
  const [jobs, setJobs] = React.useState<Job[]>([]);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [notice, setNotice] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const [tab, setTab] = React.useState("overview");
  const [newJobStream, setNewJobStream] = React.useState("");
  const [newJobDate, setNewJobDate] = React.useState(localDate(-1));
  const [newJobForce, setNewJobForce] = React.useState(false);

  const [statusFilter, setStatusFilter] = React.useState("");
  const [streamFilter, setStreamFilter] = React.useState("");

  const [panelMode, setPanelMode] = React.useState<PanelMode>("closed");
  const [selectedJobId, setSelectedJobId] = React.useState<number | null>(null);
  const [selectedJob, setSelectedJob] = React.useState<Job | null>(null);
  const [attempts, setAttempts] = React.useState<Array<Record<string, unknown>>>([]);
  const [panelTab, setPanelTab] = React.useState<JobPanelTab>("overview");
  const [summary, setSummary] = React.useState<TimelineSummary>({});
  const [timeline, setTimeline] = React.useState<Timeline | null>(null);
  const [jobLogs, setJobLogs] = React.useState<LogEntry[]>([]);

  const [discovery, setDiscovery] = React.useState<DiscoveryConfig | null>(null);
  const [runs, setRuns] = React.useState<DiscoveryRun[]>([]);
  const [selectedStreams, setSelectedStreams] = React.useState("");
  const [explicitDates, setExplicitDates] = React.useState("");

  const [logs, setLogs] = React.useState<LogEntry[]>([]);
  const [serviceLogs, setServiceLogs] = React.useState<LogEntry[]>([]);
  const [logLevel, setLogLevel] = React.useState("");
  const [logSearch, setLogSearch] = React.useState("");
  const [logJob, setLogJob] = React.useState("");

  const [storage, setStorage] = React.useState<StorageConfig[]>([]);
  const [storageForm, setStorageForm] = React.useState<StorageConfig>(emptyStorage());
  const [runtime, setRuntime] = React.useState<RuntimeSettings>({
    max_active_jobs: 1,
    retry_initial_seconds: 60,
    retry_max_seconds: 3600,
    max_attempts: 3,
    keep_failed_workspaces: true,
    min_free_disk_gb: 1,
    builds_retained: 3,
  });
  const [token, setTokenState] = React.useState(getToken());

  const refreshJobs = React.useCallback(async () => {
    try {
      const data = await api.jobs({
        status: statusFilter || undefined,
        stream_id: streamFilter || undefined,
        limit: 200,
      });
      setJobs(data.jobs);
      setCounts(data.counts);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [statusFilter, streamFilter]);

  const refreshStats = React.useCallback(async () => {
    try {
      setStats(await api.stats());
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  React.useEffect(() => {
    refreshJobs();
    refreshStats();
    const timer = window.setInterval(() => {
      refreshJobs();
      refreshStats();
    }, 5000);
    return () => window.clearInterval(timer);
  }, [refreshJobs, refreshStats]);

  React.useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(""), 4000);
    return () => window.clearTimeout(timer);
  }, [notice]);

  React.useEffect(() => {
    if (tab !== "discovery") return;
    let active = true;
    const load = async () => {
      try {
        const [config, runData] = await Promise.all([api.discoveryConfig(), api.discoveryRuns()]);
        if (!active) return;
        setDiscovery(config);
        setSelectedStreams(config.selected_streams.join(", "));
        setExplicitDates((config.dates || []).join(", "));
        setRuns(runData.runs);
      } catch (e) {
        setError((e as Error).message);
      }
    };
    load();
    const timer = window.setInterval(load, 10000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [tab]);

  React.useEffect(() => {
    if (tab !== "logs") return;
    let active = true;
    const load = async () => {
      try {
        const data = await api.logs({
          job_id: logJob ? Number(logJob) : undefined,
          level: logLevel || undefined,
          search: logSearch || undefined,
          limit: 400,
        });
        if (!active) return;
        setLogs(data.entries);
        setServiceLogs(data.service || []);
      } catch (e) {
        setError((e as Error).message);
      }
    };
    load();
    const timer = window.setInterval(load, 5000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [tab, logLevel, logSearch, logJob]);

  React.useEffect(() => {
    if (tab !== "settings") return;
    Promise.all([api.storage(), api.settings()])
      .then(([storageData, settings]) => {
        setStorage(storageData.storage);
        setRuntime(settings);
      })
      .catch((e: Error) => setError(e.message));
  }, [tab]);

  const loadJob = React.useCallback(async (id: number) => {
    try {
      const detail = await api.job(id);
      setSelectedJob(detail.job);
      setAttempts(detail.attempts || []);
      setSummary(parseSummary(detail.job));
      api
        .timelineSummary(id)
        .then((data) => setSummary({ ...parseSummary(detail.job), ...data.summary }))
        .catch(() => undefined);
      const logs = await api.logs({ job_id: id, limit: 300 });
      setJobLogs(logs.entries);
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  React.useEffect(() => {
    if (panelMode !== "job" || !selectedJobId) return;
    loadJob(selectedJobId);
    const timer = window.setInterval(() => loadJob(selectedJobId), 4000);
    return () => window.clearInterval(timer);
  }, [panelMode, selectedJobId, loadJob]);

  React.useEffect(() => {
    if (panelMode !== "job" || panelTab !== "timeline" || !selectedJobId || timeline) return;
    api
      .timeline(selectedJobId, 0, 1000)
      .then(setTimeline)
      .catch((e: Error) => setError(e.message));
  }, [panelMode, panelTab, selectedJobId, timeline]);

  const runAction = async (action: () => Promise<unknown>, message: string) => {
    setBusy(true);
    try {
      await action();
      setNotice(message);
      setError("");
      await refreshJobs();
      await refreshStats();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const openJob = (job: Job) => {
    setSelectedJobId(job.id);
    setSelectedJob(job);
    setTimeline(null);
    setPanelTab("overview");
    setPanelMode("job");
  };

  const closePanel = () => {
    setPanelMode("closed");
    setSelectedJobId(null);
    setSelectedJob(null);
  };

  const submitNewJob = async () => {
    if (!newJobStream.trim()) {
      setError("stream_id is required");
      return;
    }
    setBusy(true);
    try {
      const result = await api.createJob({
        stream_id: newJobStream.trim(),
        date: newJobDate,
        force: newJobForce,
      });
      setNotice(result.existing ? `Job ${result.job.id} already exists` : `Created job ${result.job.id}`);
      closePanel();
      await refreshJobs();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const saveDiscovery = async () => {
    if (!discovery) return;
    const config: DiscoveryConfig = {
      ...discovery,
      selected_streams: selectedStreams
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean),
      dates: explicitDates
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean),
    };
    setBusy(true);
    try {
      const saved = await api.saveDiscoveryConfig(config);
      setDiscovery(saved);
      setNotice("Discovery configuration saved");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const runDiscoveryNow = async () => {
    setBusy(true);
    try {
      const result = await api.runDiscovery();
      setNotice(
        `Discovery ${result.run.status}: ${result.run.jobs_created} created, ${result.run.jobs_skipped} skipped`
      );
      const runData = await api.discoveryRuns();
      setRuns(runData.runs);
      await refreshJobs();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const saveRuntime = async () => {
    setBusy(true);
    try {
      const saved = await api.saveSettings(runtime);
      setRuntime(saved);
      setNotice("Runtime worker settings saved");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const saveStorage = async () => {
    setBusy(true);
    try {
      await api.saveStorage(storageForm);
      setNotice("Storage configuration saved");
      setPanelMode("closed");
      const data = await api.storage();
      setStorage(data.storage);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const renderOverview = () => {
    const countsSafe = counts || {};
    const recent = jobs.slice(0, 12);
    return (
      <div className="idx-section">
        <div className="idx-metrics">
          <Metric label="Queued" value={countsSafe.queued || 0} tone="info" />
          <Metric label="Active" value={stats ? stats.active_jobs : 0} tone="running" hint={stats ? `limit ${stats.max_active_jobs}` : ""} />
          <Metric label="Completed" value={countsSafe.completed || 0} tone="success" />
          <Metric label="Failed" value={countsSafe.failed || 0} tone="error" />
          <Metric label="Cancelled" value={countsSafe.cancelled || 0} tone="muted" />
          <Metric
            label="Disk free"
            value={stats ? formatBytes(stats.disk.free_bytes) : "-"}
            tone="info"
            hint={stats ? `of ${formatBytes(stats.disk.total_bytes)}` : ""}
          />
        </div>

        <div className="idx-two-col">
          <div className="idx-card">
            <div className="idx-card-title">Service</div>
            <Definition label="Version">{stats ? stats.version : "-"}</Definition>
            <Definition label="Olaf">{stats ? stats.olaf_version : "-"}</Definition>
            <Definition label="Uptime">{stats ? formatDuration(stats.uptime_seconds * 1000) : "-"}</Definition>
            <Definition label="Source storage">
              {stats && stats.storage.source_configured ? <Status status="completed" label="configured" /> : <Status status="failed" label="missing" />}
            </Definition>
            <Definition label="Destination storage">
              {stats && stats.storage.destination_configured ? (
                <Status status="completed" label="configured" />
              ) : (
                <Status status="failed" label="missing" />
              )}
            </Definition>
          </div>
          <div className="idx-card">
            <div className="idx-card-title">Discovery</div>
            <Definition label="Enabled">{stats && stats.discovery.enabled ? "Yes" : "No"}</Definition>
            <Definition label="Last run">{stats && stats.discovery.last_run ? formatTime(stats.discovery.last_run.started_at) : "-"}</Definition>
            <Definition label="Status">{stats && stats.discovery.last_run ? stats.discovery.last_run.status : "-"}</Definition>
            <Definition label="Jobs created">{stats && stats.discovery.last_run ? stats.discovery.last_run.jobs_created : 0}</Definition>
            <Definition label="Jobs skipped">{stats && stats.discovery.last_run ? stats.discovery.last_run.jobs_skipped : 0}</Definition>
          </div>
        </div>

        <div className="idx-card">
          <div className="idx-card-header">
            <div className="idx-card-title">Recent jobs</div>
            <Button text="View all" subtle onClick={() => setTab("jobs")} />
          </div>
          <JobTable jobs={recent} onOpen={openJob} />
        </div>
      </div>
    );
  };

  const renderJobs = () => (
    <div className="idx-section">
      <div className="idx-toolbar">
        <label className="idx-filter">
          Status
          <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
            <option value="">All</option>
            {JOB_STATUSES.map((status) => (
              <option key={status} value={status}>
                {status}
              </option>
            ))}
          </select>
        </label>
        <TextField
          className="idx-search"
          placeholder="Filter by stream id"
          value={streamFilter}
          onChange={(_event, value) => setStreamFilter(value || "")}
        />
        <Button text="Refresh" onClick={() => refreshJobs()} />
        <Button text="New job" primary onClick={() => setPanelMode("new-job")} />
      </div>
      <div className="idx-card">
        {loading ? (
          <div className="idx-loading">
            <Spinner label="Loading jobs" />
          </div>
        ) : (
          <JobTable jobs={jobs} onOpen={openJob} />
        )}
      </div>
    </div>
  );

  const renderDiscovery = () => {
    if (!discovery) {
      return (
        <div className="idx-loading">
          <Spinner label="Loading discovery configuration" />
        </div>
      );
    }
    const update = (patch: Partial<DiscoveryConfig>) => setDiscovery({ ...discovery, ...patch });
    const updateReadiness = (patch: Partial<DiscoveryConfig["readiness"]>) =>
      setDiscovery({ ...discovery, readiness: { ...discovery.readiness, ...patch } });
    return (
      <div className="idx-section">
        <div className="idx-toolbar">
          <Button text="Run scan now" primary onClick={runDiscoveryNow} disabled={busy} />
          <Button text="Save configuration" onClick={saveDiscovery} disabled={busy} />
          <span className="idx-muted">
            Last scan {stats && stats.discovery.last_run ? formatTime(stats.discovery.last_run.started_at) : "-"}
          </span>
        </div>

        <div className="idx-two-col">
          <div className="idx-card">
            <div className="idx-card-title">Scheduling</div>
            <label className="idx-check">
              <input type="checkbox" checked={discovery.enabled} onChange={(e) => update({ enabled: e.target.checked })} />
              Enable discovery
            </label>
            <Field label="Scan interval (minutes)">
              <input
                type="number"
                min={1}
                value={discovery.scan_interval_minutes}
                onChange={(e) => update({ scan_interval_minutes: Number(e.target.value) })}
              />
            </Field>
            <Field label="Source storage name">
              <input value={discovery.source_storage} onChange={(e) => update({ source_storage: e.target.value })} />
            </Field>
            <Field label="Stream scope">
              <select value={discovery.stream_scope} onChange={(e) => update({ stream_scope: e.target.value })}>
                <option value="all">All streams</option>
                <option value="selected">Selected streams</option>
              </select>
            </Field>
            {discovery.stream_scope === "selected" ? (
              <Field label="Selected streams (comma separated)">
                <input value={selectedStreams} onChange={(e) => setSelectedStreams(e.target.value)} />
              </Field>
            ) : null}
            <Field label="Date scope">
              <select value={discovery.date_scope} onChange={(e) => update({ date_scope: e.target.value })}>
                <option value="previous_days">Previous days</option>
                <option value="explicit">Explicit dates</option>
              </select>
            </Field>
            {discovery.date_scope === "previous_days" ? (
              <Field label="Lookback days">
                <input
                  type="number"
                  min={1}
                  value={discovery.lookback_days}
                  onChange={(e) => update({ lookback_days: Number(e.target.value) })}
                />
              </Field>
            ) : (
              <Field label="Dates (YYYY-MM-DD, comma separated)">
                <input value={explicitDates} onChange={(e) => setExplicitDates(e.target.value)} />
              </Field>
            )}
          </div>

          <div className="idx-card">
            <div className="idx-card-title">Readiness</div>
            <Field label="Strategy">
              <select
                value={discovery.readiness.strategy}
                onChange={(e) => updateReadiness({ strategy: e.target.value })}
              >
                <option value="grace_period">Grace period</option>
                <option value="completion_event">Completion event</option>
              </select>
            </Field>
            {discovery.readiness.strategy === "grace_period" ? (
              <Field label="Grace period (minutes)">
                <input
                  type="number"
                  min={0}
                  value={discovery.readiness.grace_period_minutes}
                  onChange={(e) => updateReadiness({ grace_period_minutes: Number(e.target.value) })}
                />
              </Field>
            ) : null}
            <Field label="Minimum recordings">
              <input
                type="number"
                min={0}
                value={discovery.readiness.minimum_recordings}
                onChange={(e) => updateReadiness({ minimum_recordings: Number(e.target.value) })}
              />
            </Field>
            <Field label="Minimum duration (seconds)">
              <input
                type="number"
                min={0}
                value={discovery.readiness.minimum_duration_seconds}
                onChange={(e) => updateReadiness({ minimum_duration_seconds: Number(e.target.value) })}
              />
            </Field>
            <Field label="Maximum jobs per scan">
              <input
                type="number"
                min={1}
                value={discovery.max_jobs_per_scan}
                onChange={(e) => update({ max_jobs_per_scan: Number(e.target.value) })}
              />
            </Field>
            <label className="idx-check">
              <input
                type="checkbox"
                checked={discovery.automatic_enqueue}
                onChange={(e) => update({ automatic_enqueue: e.target.checked })}
              />
              Automatically enqueue eligible dates
            </label>
            <label className="idx-check">
              <input type="checkbox" checked={discovery.dry_run} onChange={(e) => update({ dry_run: e.target.checked })} />
              Dry run (report only)
            </label>
          </div>
        </div>

        <div className="idx-card">
          <div className="idx-card-title">Scan history</div>
          <table className="idx-table">
            <thead>
              <tr>
                <th>ID</th>
                <th>Trigger</th>
                <th>Started</th>
                <th>Status</th>
                <th>Streams</th>
                <th>Dates</th>
                <th>Eligible</th>
                <th>Created</th>
                <th>Skipped</th>
                <th>Errors</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((run) => (
                <tr key={run.id}>
                  <td>{run.id}</td>
                  <td>{run.trigger}</td>
                  <td>{formatTime(run.started_at)}</td>
                  <td>
                    <Status status={run.status === "completed" ? "completed" : run.status === "running" ? "uploading" : "failed"} label={run.status} />
                  </td>
                  <td>{run.streams_examined}</td>
                  <td>{run.dates_examined}</td>
                  <td>{run.eligible_dates}</td>
                  <td>{run.jobs_created}</td>
                  <td>{run.jobs_skipped}</td>
                  <td className="idx-error-text">{run.errors}</td>
                </tr>
              ))}
              {runs.length === 0 ? (
                <tr>
                  <td colSpan={10} className="idx-empty">
                    No discovery runs yet.
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </div>
    );
  };

  const renderLogs = () => (
    <div className="idx-section">
      <div className="idx-toolbar">
        <TextField placeholder="Search log messages" value={logSearch} onChange={(_e, value) => setLogSearch(value || "")} />
        <label className="idx-filter">
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
        <label className="idx-filter">
          Job
          <input value={logJob} onChange={(e) => setLogJob(e.target.value)} placeholder="job id" />
        </label>
      </div>
      <div className="idx-two-col idx-logs-col">
        <div className="idx-card">
          <div className="idx-card-title">Job logs</div>
          <LogList entries={logs} empty="No job logs match the filters." />
        </div>
        <div className="idx-card">
          <div className="idx-card-title">Service log</div>
          <LogList entries={serviceLogs} empty="No service log entries." />
        </div>
      </div>
    </div>
  );

  const renderApi = () => (
    <div className="idx-section">
      <div className="idx-card">
        <div className="idx-card-title">REST API</div>
        <p>
          Interactive documentation is available at{" "}
          <a href="/docs" target="_blank" rel="noreferrer">
            /docs
          </a>
          . The OpenAPI document is at{" "}
          <a href="/openapi.json" target="_blank" rel="noreferrer">
            /openapi.json
          </a>
          .
        </p>
        <Definition label="Health">GET /api/health</Definition>
        <Definition label="Jobs">GET/POST /api/jobs</Definition>
        <Definition label="Discovery">GET/PUT /api/discovery/config</Definition>
        <Definition label="Recorder event">POST /api/events/recording-complete</Definition>
        <Definition label="Storage">GET/POST /api/storage</Definition>
        <div className="idx-hint">
          When an admin token is configured, send it as <code>Authorization: Bearer &lt;token&gt;</code> or use the
          token field under Settings.
        </div>
      </div>
    </div>
  );

  const renderSettings = () => (
    <div className="idx-section">
      <div className="idx-card">
        <div className="idx-card-title">Admin access</div>
        <p className="idx-muted">The token is stored only in this browser and sent with API requests.</p>
        <div className="idx-inline">
          <TextField
            placeholder="Admin token (leave empty if not configured)"
            value={token}
            onChange={(_e, value) => setTokenState(value || "")}
          />
          <Button
            text="Save token"
            onClick={() => {
              setToken(token.trim());
              setNotice("Admin token saved in this browser");
              refreshJobs();
              refreshStats();
            }}
          />
        </div>
      </div>

      <div className="idx-card">
        <div className="idx-card-header">
          <div className="idx-card-title">Worker runtime settings</div>
          <Button text="Save" primary onClick={saveRuntime} disabled={busy} />
        </div>
        <div className="idx-two-col">
          <div>
            <Field label="Maximum active jobs">
              <input
                type="number"
                min={1}
                value={runtime.max_active_jobs}
                onChange={(e) => setRuntime({ ...runtime, max_active_jobs: Number(e.target.value) })}
              />
            </Field>
            <Field label="Retry initial (seconds)">
              <input
                type="number"
                min={1}
                value={runtime.retry_initial_seconds}
                onChange={(e) => setRuntime({ ...runtime, retry_initial_seconds: Number(e.target.value) })}
              />
            </Field>
            <Field label="Retry maximum (seconds)">
              <input
                type="number"
                min={1}
                value={runtime.retry_max_seconds}
                onChange={(e) => setRuntime({ ...runtime, retry_max_seconds: Number(e.target.value) })}
              />
            </Field>
          </div>
          <div>
            <Field label="Maximum attempts (0 = unlimited)">
              <input
                type="number"
                min={0}
                value={runtime.max_attempts}
                onChange={(e) => setRuntime({ ...runtime, max_attempts: Number(e.target.value) })}
              />
            </Field>
            <Field label="Minimum free disk (GB)">
              <input
                type="number"
                min={0}
                value={runtime.min_free_disk_gb}
                onChange={(e) => setRuntime({ ...runtime, min_free_disk_gb: Number(e.target.value) })}
              />
            </Field>
            <Field label="Builds retained per date">
              <input
                type="number"
                min={1}
                value={runtime.builds_retained}
                onChange={(e) => setRuntime({ ...runtime, builds_retained: Number(e.target.value) })}
              />
            </Field>
            <label className="idx-check">
              <input
                type="checkbox"
                checked={runtime.keep_failed_workspaces}
                onChange={(e) => setRuntime({ ...runtime, keep_failed_workspaces: e.target.checked })}
              />
              Keep workspaces after failed jobs
            </label>
          </div>
        </div>
        <p className="idx-muted">
          These values are stored in SQLite and apply to newly claimed jobs without a restart.
        </p>
      </div>

      <div className="idx-card">
        <div className="idx-card-header">
          <div className="idx-card-title">Storage configurations</div>
          <Button
            text="Add storage"
            primary
            onClick={() => {
              setStorageForm(emptyStorage());
              setPanelMode("storage");
            }}
          />
        </div>
        <div className="idx-hint">
          Storage is loaded from the <code>S3_*</code> variables in <code>/etc/indexer/.env</code> on
          every startup. When <code>S3_BUCKET</code> is set, the stored source and destination values
          are overwritten on restart.
        </div>
        <table className="idx-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Role</th>
              <th>Bucket</th>
              <th>Endpoint</th>
              <th>Region</th>
              <th>Prefix</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {storage.map((item) => (
              <tr key={item.id}>
                <td>{item.name}</td>
                <td>{item.role}</td>
                <td>{item.bucket}</td>
                <td className="idx-break">{item.endpoint || "AWS default"}</td>
                <td>{item.region}</td>
                <td>{item.prefix || "-"}</td>
                <td className="idx-row-actions">
                  <Button
                    text="Test"
                    subtle
                    onClick={() =>
                      runAction(() => api.testStorage(item.id), `Storage ${item.name} reachable`)
                    }
                  />
                  <Button
                    text="Edit"
                    subtle
                    onClick={() => {
                      setStorageForm({ ...item, secret_key: "" });
                      setPanelMode("storage");
                    }}
                  />
                  <Button
                    text="Delete"
                    subtle
                    danger
                    onClick={() => {
                      if (window.confirm(`Delete storage configuration ${item.name}?`)) {
                        runAction(() => api.deleteStorage(item.id), `Deleted ${item.name}`).then(() =>
                          api.storage().then((data) => setStorage(data.storage))
                        );
                      }
                    }}
                  />
                </td>
              </tr>
            ))}
            {storage.length === 0 ? (
              <tr>
                <td colSpan={7} className="idx-empty">
                  No storage configurations yet.
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  );

  const body = () => {
    switch (tab) {
      case "jobs":
        return renderJobs();
      case "discovery":
        return renderDiscovery();
      case "logs":
        return renderLogs();
      case "api":
        return renderApi();
      case "settings":
        return renderSettings();
      default:
        return renderOverview();
    }
  };

  return (
    <Page className="idx-page">
      <div className="idx-shell">
        <div className="idx-masthead">
          <div>
            <div className="idx-product">Spaza · Airplay</div>
            <div className="idx-title">Olaf Daily Indexer</div>
            <div className="idx-subtitle">
              {stats ? `v${stats.version} · Olaf ${stats.olaf_version}` : "Loading service information"}
            </div>
          </div>
          <div className="idx-masthead-actions">
            <Button text="Refresh" onClick={() => { refreshJobs(); refreshStats(); }} />
            <Button text="New job" primary onClick={() => setPanelMode("new-job")} />
          </div>
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
          className="idx-tabs"
          selectedTabId={tab}
          onSelectedTabChanged={setTab}
          tabSize={TabSize.Compact}
        >
          <Tab id="overview" name="Overview" />
          <Tab id="jobs" name="Jobs" />
          <Tab id="discovery" name="Discovery" />
          <Tab id="logs" name="Logs" />
          <Tab id="api" name="API" />
          <Tab id="settings" name="Settings" />
        </TabBar>

        {body()}
      </div>

      {panelMode === "new-job" ? (
        <Panel
          onDismiss={closePanel}
          titleProps={{ text: "Create indexing job" }}
          description="Queue a manual rebuild for a stream and date."
          footerButtonProps={[
            { text: "Cancel", onClick: closePanel },
            { text: "Create job", onClick: submitNewJob },
          ]}
        >
          <div className="idx-panel-body">
            <Field label="Stream ID">
              <input value={newJobStream} onChange={(e) => setNewJobStream(e.target.value)} placeholder="e.g. radio-one" />
            </Field>
            <Field label="Date">
              <input type="date" value={newJobDate} onChange={(e) => setNewJobDate(e.target.value)} />
            </Field>
            <label className="idx-check">
              <input type="checkbox" checked={newJobForce} onChange={(e) => setNewJobForce(e.target.checked)} />
              Force rebuild even when a completed index exists
            </label>
            <p className="idx-muted">
              An existing active job for the same stream and date is returned instead of creating a duplicate.
            </p>
          </div>
        </Panel>
      ) : null}

      {panelMode === "job" && selectedJob ? (
        <Panel
          onDismiss={closePanel}
          contentClassName="idx-panel-content"
          titleProps={{ text: `${selectedJob.stream_id} · ${selectedJob.date}` }}
          description={`Job ${selectedJob.id} · ${selectedJob.source}`}
          footerButtonProps={[
            { text: "Close", onClick: closePanel },
            selectedJob.status === "failed"
              ? {
                  text: "Retry",
                  onClick: () =>
                    runAction(() => api.retryJob(selectedJob.id), `Retried job ${selectedJob.id}`),
                }
              : {
                  text: "Cancel",
                  onClick: () =>
                    runAction(() => api.cancelJob(selectedJob.id), `Cancellation requested for ${selectedJob.id}`),
                },
          ]}
        >
          <TabBar
            className="idx-panel-tabs"
            selectedTabId={panelTab}
            onSelectedTabChanged={(id) => setPanelTab(id as JobPanelTab)}
            tabSize={TabSize.Compact}
          >
            <Tab id="overview" name="Overview" />
            <Tab id="timeline" name="Timeline" />
            <Tab id="logs" name="Logs" />
          </TabBar>

          {panelTab === "overview" ? (
            <div className="idx-panel-body">
              <Definition label="Status">
                <Status status={selectedJob.status} />
              </Definition>
              <Definition label="Stage">{selectedJob.stage || "-"}</Definition>
              <Definition label="Progress">{Math.round((selectedJob.progress || 0) * 100)}%</Definition>
              <Definition label="Recordings">
                {selectedJob.recordings_indexed} indexed / {selectedJob.recordings_total} discovered
              </Definition>
              <Definition label="Attempts">{selectedJob.attempts}</Definition>
              <Definition label="Created">{formatTime(selectedJob.created_at)}</Definition>
              <Definition label="Started">{formatTime(selectedJob.started_at)}</Definition>
              <Definition label="Completed">{formatTime(selectedJob.completed_at)}</Definition>
              {selectedJob.build_id ? <Definition label="Build">{selectedJob.build_id}</Definition> : null}
              {selectedJob.next_attempt_at ? (
                <Definition label="Next attempt">{formatTime(selectedJob.next_attempt_at)}</Definition>
              ) : null}
              {selectedJob.error ? (
                <Definition label="Error">
                  <span className="idx-error-text">{selectedJob.error}</span>
                </Definition>
              ) : null}
              <div className="idx-summary-grid">
                <Metric label="Indexed" value={summary.recordings_indexed || 0} tone="success" />
                <Metric label="Failed" value={summary.recordings_failed || 0} tone="error" />
                <Metric label="Duration" value={formatDuration(summary.total_recorded_duration_ms)} tone="info" />
                <Metric label="Gaps" value={summary.gap_count || 0} tone="warning" />
                <Metric label="Overlaps" value={summary.overlap_count || 0} tone="warning" />
              </div>
              {attempts.length > 0 ? (
                <div className="idx-attempts">
                  <div className="idx-card-title">Attempts</div>
                  <table className="idx-table">
                    <thead>
                      <tr>
                        <th>Attempt</th>
                        <th>Status</th>
                        <th>Started</th>
                        <th>Finished</th>
                        <th>Error</th>
                      </tr>
                    </thead>
                    <tbody>
                      {attempts.map((attempt, index) => (
                        <tr key={index}>
                          <td>{String(attempt.attempt)}</td>
                          <td>{String(attempt.status)}</td>
                          <td>{formatTime(String(attempt.started_at || ""))}</td>
                          <td>{formatTime(String(attempt.finished_at || ""))}</td>
                          <td className="idx-error-text">{String(attempt.error || "")}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : null}
            </div>
          ) : null}

          {panelTab === "timeline" ? (
            <div className="idx-panel-body">
              {!timeline ? (
                <Spinner label="Loading timeline" />
              ) : (
                <>
                  <Definition label="Origin">{formatTime(timeline.timeline_origin)}</Definition>
                  <Definition label="Timezone">{timeline.timezone}</Definition>
                  <Definition label="Duration">{formatDuration(timeline.timeline_duration_ms)}</Definition>
                  {(() => {
                    type Row = { kind: "recording"; start: number; recording: Recording } | { kind: "gap"; start: number; gap: Gap };
                    const rows: Row[] = [
                      ...timeline.recordings.map((recording) => ({
                        kind: "recording" as const,
                        start: recording.timeline_start_ms || 0,
                        recording,
                      })),
                      ...(timeline.gaps || []).map((gap) => ({
                        kind: "gap" as const,
                        start: gap.timeline_start_ms,
                        gap,
                      })),
                    ].sort((a, b) => a.start - b.start);
                    return (
                      <div className="idx-table-wrap">
                        <table className="idx-table">
                          <thead>
                            <tr>
                              <th>#</th>
                              <th>Resource</th>
                              <th>Start</th>
                              <th>Duration</th>
                              <th>Status</th>
                              <th>Object</th>
                            </tr>
                          </thead>
                          <tbody>
                            {rows.map((row, index) =>
                              row.kind === "gap" ? (
                                <tr key={`gap-${index}`} className="idx-gap-row">
                                  <td>gap</td>
                                  <td>Missing recording</td>
                                  <td>{formatDuration(row.gap.timeline_start_ms)}</td>
                                  <td>{formatDuration(row.gap.duration_ms)}</td>
                                  <td>
                                    <Status status="warning" label="missing" />
                                  </td>
                                  <td>{row.gap.reason}</td>
                                </tr>
                              ) : (
                                <tr key={`${row.recording.resource_id || row.recording.object_key}-${index}`}>
                                  <td>{index + 1}</td>
                                  <td>{row.recording.resource_id || "-"}</td>
                                  <td>
                                    {row.recording.start_time
                                      ? formatTime(row.recording.start_time)
                                      : formatDuration(row.recording.timeline_start_ms)}
                                  </td>
                                  <td>{formatDuration(row.recording.duration_ms)}</td>
                                  <td>
                                    <Status
                                      status={
                                        row.recording.status === "indexed"
                                          ? "completed"
                                          : row.recording.status === "failed"
                                            ? "failed"
                                            : "queued"
                                      }
                                      label={row.recording.status}
                                    />
                                  </td>
                                  <td className="idx-break">{row.recording.object_key}</td>
                                </tr>
                              )
                            )}
                          </tbody>
                        </table>
                      </div>
                    );
                  })()}
                  <div className="idx-muted">
                    Showing {timeline.recordings.length} of {timeline.total} recordings and{" "}
                    {(timeline.gaps || []).length} gaps
                  </div>
                </>
              )}
            </div>
          ) : null}

          {panelTab === "logs" ? (
            <div className="idx-panel-body">
              <LogList entries={jobLogs} empty="No logs recorded for this job yet." />
            </div>
          ) : null}
        </Panel>
      ) : null}

      {panelMode === "storage" ? (
        <Panel
          onDismiss={closePanel}
          titleProps={{ text: storageForm.id ? `Edit storage: ${storageForm.name}` : "Add storage configuration" }}
          footerButtonProps={[
            { text: "Cancel", onClick: closePanel },
            { text: "Save", onClick: saveStorage },
          ]}
        >
          <div className="idx-panel-body">
            <Field label="Name">
              <input
                value={storageForm.name}
                onChange={(e) => setStorageForm({ ...storageForm, name: e.target.value })}
                placeholder="source"
              />
            </Field>
            <Field label="Role">
              <select
                value={storageForm.role}
                onChange={(e) => setStorageForm({ ...storageForm, role: e.target.value })}
              >
                <option value="source">Source</option>
                <option value="destination">Destination</option>
              </select>
            </Field>
            <Field label="Bucket">
              <input value={storageForm.bucket} onChange={(e) => setStorageForm({ ...storageForm, bucket: e.target.value })} />
            </Field>
            <Field label="Endpoint">
              <input
                value={storageForm.endpoint}
                onChange={(e) => setStorageForm({ ...storageForm, endpoint: e.target.value })}
                placeholder="https://s3.example.com (empty for AWS)"
              />
            </Field>
            <Field label="Region">
              <input value={storageForm.region} onChange={(e) => setStorageForm({ ...storageForm, region: e.target.value })} />
            </Field>
            <Field label="Access key">
              <input
                value={storageForm.access_key}
                onChange={(e) => setStorageForm({ ...storageForm, access_key: e.target.value })}
              />
            </Field>
            <Field label="Secret key">
              <input
                type="password"
                value={storageForm.secret_key || ""}
                onChange={(e) => setStorageForm({ ...storageForm, secret_key: e.target.value })}
                placeholder={storageForm.id ? "leave empty to keep current secret" : ""}
              />
            </Field>
            <Field label="Prefix (optional)">
              <input value={storageForm.prefix} onChange={(e) => setStorageForm({ ...storageForm, prefix: e.target.value })} />
            </Field>
            <label className="idx-check">
              <input
                type="checkbox"
                checked={storageForm.path_style}
                onChange={(e) => setStorageForm({ ...storageForm, path_style: e.target.checked })}
              />
              Path-style addressing
            </label>
          </div>
        </Panel>
      ) : null}
    </Page>
  );
}

function Field(props: { label: string; children: React.ReactNode }) {
  return (
    <label className="idx-field">
      <span className="idx-field-label">{props.label}</span>
      {props.children}
    </label>
  );
}

function JobTable(props: { jobs: Job[]; onOpen: (job: Job) => void }) {
  return (
    <div className="idx-table-wrap">
      <table className="idx-table idx-job-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Stream</th>
            <th>Date</th>
            <th>Status</th>
            <th>Progress</th>
            <th>Recordings</th>
            <th>Source</th>
            <th>Created</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          {props.jobs.map((job) => (
            <tr key={job.id} onClick={() => props.onOpen(job)}>
              <td>{job.id}</td>
              <td>{job.stream_id}</td>
              <td>{job.date}</td>
              <td>
                <Status status={job.status} label={job.stage ? `${job.status} · ${job.stage}` : job.status} />
              </td>
              <td>
                <div className="idx-progress">
                  <div className="idx-progress-bar" style={{ width: `${Math.round((job.progress || 0) * 100)}%` }} />
                </div>
                <span className="idx-progress-label">{Math.round((job.progress || 0) * 100)}%</span>
              </td>
              <td>
                {job.recordings_indexed} / {job.recordings_total}
              </td>
              <td>{job.source}</td>
              <td>{formatTime(job.created_at)}</td>
              <td>{formatTime(job.updated_at)}</td>
            </tr>
          ))}
          {props.jobs.length === 0 ? (
            <tr>
              <td colSpan={9} className="idx-empty">
                No jobs yet.
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  );
}

function LogList(props: { entries: LogEntry[]; empty: string }) {
  if (props.entries.length === 0) {
    return <div className="idx-empty">{props.empty}</div>;
  }
  return (
    <div className="idx-log-list">
      {props.entries.map((entry, index) => (
        <div className={`idx-log-entry level-${entry.level}`} key={`${entry.time}-${index}`}>
          <span className="idx-log-time">{formatTime(entry.time)}</span>
          <span className="idx-log-level">{entry.level}</span>
          {entry.job_id ? <span className="idx-log-job">#{entry.job_id}</span> : null}
          {entry.stage ? <span className="idx-log-stage">{entry.stage}</span> : null}
          <span className="idx-log-message">{entry.message}</span>
        </div>
      ))}
    </div>
  );
}
