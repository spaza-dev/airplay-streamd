import * as React from "react";
import { Page } from "azure-devops-ui/Page";
import { Tab, TabBar, TabSize } from "azure-devops-ui/Tabs";
import { Button } from "azure-devops-ui/Button";
import { Panel } from "azure-devops-ui/Panel";
import { Spinner, SpinnerOrientation, SpinnerSize } from "azure-devops-ui/Spinner";
import { MessageBar, MessageBarSeverity } from "azure-devops-ui/MessageBar";
import { api, getToken, setToken } from "./api";
import {
  CallbackDelivery,
  Job,
  JobDetail,
  LogEntry,
  Match,
  Stats,
} from "./types";

type PanelTab = "summary" | "progress" | "matches" | "logs" | "callbacks";

const JOB_STATUSES = [
  "queued",
  "planning",
  "downloading",
  "querying",
  "paused",
  "completed",
  "failed",
  "cancelled",
];

const ENDPOINTS = [
  ["POST", "/api/jobs", "Create a multipart query job with files, scope, callback_url"],
  ["GET", "/api/jobs", "List jobs and status counts"],
  ["GET", "/api/jobs/{id}", "Job detail with files and indexes"],
  ["GET", "/api/jobs/{id}/matches", "Mapped archive matches"],
  ["GET", "/api/jobs/{id}/logs", "Job logs"],
  ["GET", "/api/jobs/{id}/callbacks", "Callback delivery attempts"],
  ["POST", "/api/jobs/{id}/pause", "Pause a running or queued job"],
  ["POST", "/api/jobs/{id}/resume", "Resume a paused job"],
  ["POST", "/api/jobs/{id}/cancel", "Cancel a job and clean temporary files"],
  ["GET", "/api/stats", "Service, storage, limit, and disk statistics"],
  ["GET", "/api/health", "Health check"],
  ["GET", "/api/health/ready", "Readiness check"],
];

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

function formatDuration(seconds: number): string {
  if (!seconds || seconds <= 0) return "-";
  const total = Math.floor(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return h > 0 ? `${h}h ${m}m` : m > 0 ? `${m}m ${s}s` : `${s}s`;
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
    case "done":
      return "success";
    case "failed":
      return "error";
    case "cancelled":
    case "skipped":
      return "muted";
    case "queued":
    case "planning":
      return "info";
    default:
      return "running";
  }
}

function Status(props: { status: string; label?: string }) {
  return (
    <span className={`q-status ${statusTone(props.status)}`}>
      <span className="q-dot" />
      {props.label || props.status}
    </span>
  );
}

function Definition(props: { label: string; children: React.ReactNode }) {
  return (
    <div className="q-definition">
      <div className="q-definition-label">{props.label}</div>
      <div className="q-definition-value">{props.children}</div>
    </div>
  );
}

function Metric(props: { label: string; value: React.ReactNode; tone?: string; hint?: string }) {
  return (
    <div className={`q-metric ${props.tone || "info"}`}>
      <div className="q-metric-value">{props.value}</div>
      <div className="q-metric-label">{props.label}</div>
      {props.hint ? <div className="q-metric-hint">{props.hint}</div> : null}
    </div>
  );
}

function Field(props: { label: string; children: React.ReactNode; hint?: string }) {
  return (
    <label className="q-field">
      <span>{props.label}</span>
      {props.children}
      {props.hint ? <span className="q-muted">{props.hint}</span> : null}
    </label>
  );
}

function LogList(props: { entries: LogEntry[]; empty: string }) {
  if (!props.entries.length) return <div className="q-empty">{props.empty}</div>;
  return (
    <div className="q-loglist">
      {props.entries.map((entry, index) => (
        <div className="q-logrow" key={`${entry.id || entry.time || index}`}>
          <span className={`q-loglevel ${entry.level}`}>{entry.level}</span>
          <span className="q-logtime">{formatTime(entry.created_at || entry.time)}</span>
          {entry.stage ? <span className="q-logstage">{entry.stage}</span> : null}
          <span className="q-logmessage">{entry.message}</span>
        </div>
      ))}
    </div>
  );
}

function JobTable(props: {
  jobs: Job[];
  onOpen: (job: Job) => void;
  onAction?: (job: Job, action: "pause" | "resume" | "cancel") => void;
}) {
  return (
    <div className="q-table-wrap">
      <table className="q-table q-job-table">
        <thead>
          <tr>
            <th>Job</th>
            <th>Status</th>
            <th>Stage</th>
            <th>Files</th>
            <th>Indexes</th>
            <th>Matches</th>
            <th>Created</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {props.jobs.map((job) => (
            <tr key={job.id} onClick={() => props.onOpen(job)}>
              <td>#{job.id}</td>
              <td>
                <Status status={job.status} />
              </td>
              <td>{job.stage}</td>
              <td>{job.total_files}</td>
              <td>
                {job.processed_indexes}/{job.total_indexes}
              </td>
              <td>{job.total_matches}</td>
              <td>{formatTime(job.created_at)}</td>
              <td>
                {props.onAction ? (
                  <div className="q-row-actions" onClick={(event) => event.stopPropagation()}>
                    {job.status === "paused" ? (
                      <Button text="Resume" subtle onClick={() => props.onAction && props.onAction(job, "resume")} />
                    ) : job.status === "queued" || job.status === "planning" || job.status === "downloading" || job.status === "querying" ? (
                      <Button text="Pause" subtle onClick={() => props.onAction && props.onAction(job, "pause")} />
                    ) : null}
                    {!["completed", "failed", "cancelled"].includes(job.status) ? (
                      <Button text="Cancel" subtle danger onClick={() => props.onAction && props.onAction(job, "cancel")} />
                    ) : null}
                  </div>
                ) : null}
              </td>
            </tr>
          ))}
          {props.jobs.length === 0 ? (
            <tr>
              <td colSpan={8} className="q-empty">
                No jobs yet.
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  );
}

export function App() {
  const [stats, setStats] = React.useState<Stats | null>(null);
  const [jobs, setJobs] = React.useState<Job[]>([]);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [notice, setNotice] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [submitting, setSubmitting] = React.useState(false);
  const [tab, setTab] = React.useState("overview");
  const [statusFilter, setStatusFilter] = React.useState("");

  const [streamMode, setStreamMode] = React.useState("all");
  const [streamIDs, setStreamIDs] = React.useState("");
  const [country, setCountry] = React.useState("");
  const [dateMode, setDateMode] = React.useState("range");
  const [date, setDate] = React.useState(localDate(-1));
  const [fromDate, setFromDate] = React.useState(localDate(-7));
  const [toDate, setToDate] = React.useState(localDate(-1));
  const [windowEnabled, setWindowEnabled] = React.useState(false);
  const [windowStart, setWindowStart] = React.useState("18:00");
  const [windowEnd, setWindowEnd] = React.useState("22:00");
  const [callbackURL, setCallbackURL] = React.useState("");
  const [uploadFiles, setUploadFiles] = React.useState<File[]>([]);
  const fileInput = React.useRef<HTMLInputElement | null>(null);

  const [selectedJobId, setSelectedJobId] = React.useState<number | null>(null);
  const [detail, setDetail] = React.useState<JobDetail | null>(null);
  const [panelTab, setPanelTab] = React.useState<PanelTab>("summary");
  const [matches, setMatches] = React.useState<Match[]>([]);
  const [matchTotal, setMatchTotal] = React.useState(0);
  const [jobLogs, setJobLogs] = React.useState<LogEntry[]>([]);
  const [callbacks, setCallbacks] = React.useState<CallbackDelivery[]>([]);

  const [logLevel, setLogLevel] = React.useState("");
  const [logSearch, setLogSearch] = React.useState("");
  const [allLogs, setAllLogs] = React.useState<LogEntry[]>([]);
  const [serviceLogs, setServiceLogs] = React.useState<LogEntry[]>([]);
  const [token, setTokenState] = React.useState(getToken());

  const refreshJobs = React.useCallback(async () => {
    try {
      const data = await api.jobs({ status: statusFilter || undefined, limit: 200 });
      setJobs(data.jobs || []);
      setCounts(data.counts || {});
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [statusFilter]);

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
    const timer = window.setTimeout(() => setNotice(""), 5000);
    return () => window.clearTimeout(timer);
  }, [notice]);

  const loadDetail = React.useCallback(async (id: number) => {
    try {
      const data = await api.job(id);
      setDetail(data);
      if (panelTab === "matches") {
        const matchData = await api.matches(id, 0, 1000);
        setMatches(matchData.matches || []);
        setMatchTotal(matchData.total || 0);
      } else if (panelTab === "logs") {
        const logs = await api.jobLogs(id);
        setJobLogs(logs.entries || []);
      } else if (panelTab === "callbacks") {
        const deliveries = await api.callbacks(id);
        setCallbacks(deliveries.deliveries || []);
      }
    } catch (e) {
      setError((e as Error).message);
    }
  }, [panelTab]);

  React.useEffect(() => {
    if (!selectedJobId) return;
    loadDetail(selectedJobId);
    const timer = window.setInterval(() => loadDetail(selectedJobId), 4000);
    return () => window.clearInterval(timer);
  }, [selectedJobId, loadDetail]);

  React.useEffect(() => {
    if (tab !== "logs") return;
    let active = true;
    const load = async () => {
      try {
        const data = await api.allLogs({ level: logLevel || undefined, search: logSearch || undefined });
        if (!active) return;
        setAllLogs(data.entries || []);
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
  }, [tab, logLevel, logSearch]);

  const runAction = async (action: () => Promise<unknown>, message: string) => {
    setBusy(true);
    try {
      await action();
      setNotice(message);
      setError("");
      await refreshJobs();
      await refreshStats();
      if (selectedJobId) await loadDetail(selectedJobId);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const jobAction = (job: Job, action: "pause" | "resume" | "cancel") => {
    const label = action === "pause" ? "Paused" : action === "resume" ? "Resumed" : "Cancelled";
    const call =
      action === "pause" ? api.pauseJob(job.id) : action === "resume" ? api.resumeJob(job.id) : api.cancelJob(job.id);
    runAction(() => call, `${label} job ${job.id}`);
  };

  const openJob = (job: Job) => {
    setSelectedJobId(job.id);
    setDetail(null);
    setMatches([]);
    setCallbacks([]);
    setJobLogs([]);
    setPanelTab("summary");
  };

  const closePanel = () => {
    setSelectedJobId(null);
    setDetail(null);
  };

  const submitJob = async () => {
    if (submitting) return;
    if (!uploadFiles.length) {
      setError("Select at least one audio file");
      return;
    }
    const scope: Record<string, unknown> = {
      streams:
        streamMode === "ids"
          ? { mode: "ids", ids: streamIDs.split(",").map((item) => item.trim()).filter(Boolean) }
          : streamMode === "country"
          ? { mode: "country", country: country.trim() }
          : { mode: "all" },
      dates:
        dateMode === "date"
          ? { mode: "date", date }
          : { mode: "range", from: fromDate, to: toDate },
    };
    if (windowEnabled) {
      scope.daily_time_window = {
        start: windowStart,
        end: windowEnd,
        basis: "stream_local",
        match: "overlap",
      };
    }
    const form = new FormData();
    uploadFiles.forEach((file) => form.append("files", file));
    form.append("scope", JSON.stringify(scope));
    if (callbackURL.trim()) form.append("callback_url", callbackURL.trim());
    setSubmitting(true);
    try {
      const result = await api.createJob(form);
      setNotice(`Created job ${result.job.id}`);
      setError("");
      setUploadFiles([]);
      if (fileInput.current) fileInput.current.value = "";
      setTab("jobs");
      await refreshJobs();
      await refreshStats();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  const saveToken = () => {
    setToken(token);
    setNotice("Admin token saved locally");
  };

  const recent = jobs.slice(0, 10);
  const countsSafe = counts || {};

  const renderOverview = () => (
    <div className="q-section">
      <div className="q-metrics">
        <Metric label="Queued" value={countsSafe.queued || 0} tone="info" />
        <Metric label="Active" value={stats ? stats.active_jobs : 0} tone="running" hint={stats ? `limit ${stats.max_active_jobs}` : ""} />
        <Metric label="Paused" value={countsSafe.paused || 0} tone="warning" />
        <Metric label="Completed" value={countsSafe.completed || 0} tone="success" />
        <Metric label="Failed" value={countsSafe.failed || 0} tone="error" />
        <Metric label="Matches" value={stats ? stats.matches_total : 0} tone="info" hint={stats ? `${stats.indexes_queried} indexes queried` : ""} />
        <Metric label="Disk free" value={stats ? formatBytes(stats.disk.free_bytes) : "-"} tone="info" hint={stats ? `of ${formatBytes(stats.disk.total_bytes)}` : ""} />
      </div>

      <div className="q-two-col">
        <div className="q-card">
          <div className="q-card-title">Service</div>
          <Definition label="Version">{stats ? stats.version : "-"}</Definition>
          <Definition label="Olaf">{stats ? stats.olaf_version : "-"}</Definition>
          <Definition label="Uptime">{stats ? formatDuration(stats.uptime_seconds) : "-"}</Definition>
          <Definition label="Storage">
            {stats && stats.storage.configured ? (
              <Status status="completed" label="configured" />
            ) : (
              <Status status="failed" label="missing" />
            )}
          </Definition>
          <Definition label="Bucket">{stats ? stats.storage.bucket || "-" : "-"}</Definition>
          <Definition label="Prefix">{stats ? stats.storage.prefix || "(root)" : "-"}</Definition>
        </div>
        <div className="q-card">
          <div className="q-card-title">Limits</div>
          <Definition label="Upload files">{stats ? stats.limits.max_upload_files : "-"}</Definition>
          <Definition label="File size">{stats ? `${stats.limits.max_upload_mb} MB` : "-"}</Definition>
          <Definition label="Indexes / job">{stats ? stats.limits.max_indexes_per_job : "-"}</Definition>
          <Definition label="Download cap">{stats ? `${stats.limits.max_download_gb} GB` : "-"}</Definition>
        </div>
      </div>

      <div className="q-card">
        <div className="q-card-header">
          <div className="q-card-title">Recent jobs</div>
          <div className="q-masthead-actions">
            <Button text="All jobs" subtle onClick={() => setTab("jobs")} />
            <Button text="New query" primary onClick={() => setTab("new")} />
          </div>
        </div>
        <JobTable jobs={recent} onOpen={openJob} />
      </div>
    </div>
  );

  const renderNew = () => (
    <div className="q-section">
      <div className="q-card">
        <div className="q-card-title">Upload audio</div>
        <input
          ref={fileInput}
          type="file"
          multiple
          accept=".mp3,.wav,.flac,.ogg,.opus,.m4a,.aac,.wma,.aiff,.aif"
          disabled={submitting}
          onChange={(event) => setUploadFiles(Array.from(event.target.files || []))}
        />
        <div className="q-muted q-top-gap">
          {uploadFiles.length
            ? uploadFiles.map((file) => `${file.name} (${formatBytes(file.size)})`).join(", ")
            : "Select one or more clips. Files are deleted after the job finishes."}
        </div>
      </div>

      <div className="q-card">
        <div className="q-card-title">Scope</div>
        <div className="q-form-grid">
          <Field label="Streams">
            <select value={streamMode} onChange={(event) => setStreamMode(event.target.value)}>
              <option value="all">All streams</option>
              <option value="ids">Specific stream ids</option>
              <option value="country">Country</option>
            </select>
          </Field>
          {streamMode === "ids" ? (
            <Field label="Stream ids" hint="Comma separated">
              <input value={streamIDs} onChange={(event) => setStreamIDs(event.target.value)} placeholder="radio-one, radio-two" />
            </Field>
          ) : null}
          {streamMode === "country" ? (
            <Field label="Country" hint="Matches stream metadata country">
              <input value={country} onChange={(event) => setCountry(event.target.value)} placeholder="KE" />
            </Field>
          ) : null}
          <Field label="Dates">
            <select value={dateMode} onChange={(event) => setDateMode(event.target.value)}>
              <option value="range">Date range</option>
              <option value="date">Single date</option>
            </select>
          </Field>
          {dateMode === "date" ? (
            <Field label="Date">
              <input type="date" value={date} onChange={(event) => setDate(event.target.value)} />
            </Field>
          ) : (
            <>
              <Field label="From">
                <input type="date" value={fromDate} onChange={(event) => setFromDate(event.target.value)} />
              </Field>
              <Field label="To">
                <input type="date" value={toDate} onChange={(event) => setToDate(event.target.value)} />
              </Field>
            </>
          )}
        </div>
        <label className="q-check">
          <input type="checkbox" checked={windowEnabled} onChange={(event) => setWindowEnabled(event.target.checked)} />
          Restrict to a daily time window in the stream timezone
        </label>
        {windowEnabled ? (
          <div className="q-form-grid">
            <Field label="Start">
              <input type="time" value={windowStart} onChange={(event) => setWindowStart(event.target.value)} />
            </Field>
            <Field label="End" hint="Windows crossing midnight are supported">
              <input type="time" value={windowEnd} onChange={(event) => setWindowEnd(event.target.value)} />
            </Field>
          </div>
        ) : null}
        <Field label="Callback URL (optional)" hint="Receives signed progressive JSON events">
          <input value={callbackURL} onChange={(event) => setCallbackURL(event.target.value)} placeholder="https://example.com/queryd/callback" />
        </Field>
        <div className="q-submit-row">
          <Button
            text={submitting ? "Submitting query…" : "Submit query"}
            primary
            disabled={submitting}
            onClick={submitJob}
          />
          {submitting ? (
            <Spinner
              size={SpinnerSize.small}
              orientation={SpinnerOrientation.row}
              label="Uploading files and creating job"
            />
          ) : null}
        </div>
      </div>
    </div>
  );

  const renderJobs = () => (
    <div className="q-section">
      <div className="q-toolbar">
        <label className="q-filter">
          Status
          <select value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)}>
            <option value="">All</option>
            {JOB_STATUSES.map((status) => (
              <option key={status} value={status}>
                {status}
              </option>
            ))}
          </select>
        </label>
        <Button text="Refresh" onClick={() => { refreshJobs(); refreshStats(); }} />
        <Button text="New query" primary onClick={() => setTab("new")} />
      </div>
      <div className="q-card">
        {loading ? <div className="q-empty">Loading jobs…</div> : <JobTable jobs={jobs} onOpen={openJob} onAction={jobAction} />}
      </div>
    </div>
  );

  const renderLogs = () => (
    <div className="q-section">
      <div className="q-toolbar">
        <label className="q-filter">
          Level
          <select value={logLevel} onChange={(event) => setLogLevel(event.target.value)}>
            <option value="">All</option>
            <option value="debug">debug</option>
            <option value="info">info</option>
            <option value="warn">warn</option>
            <option value="error">error</option>
          </select>
        </label>
        <label className="q-filter">
          Search
          <input value={logSearch} onChange={(event) => setLogSearch(event.target.value)} placeholder="text" />
        </label>
      </div>
      <div className="q-two-col q-logs-col">
        <div className="q-card">
          <div className="q-card-title">Job logs</div>
          <LogList entries={allLogs} empty="No job logs matched." />
        </div>
        <div className="q-card">
          <div className="q-card-title">Service log</div>
          <LogList entries={serviceLogs} empty="No service log entries." />
        </div>
      </div>
    </div>
  );

  const renderApi = () => (
    <div className="q-section">
      <div className="q-card">
        <div className="q-card-header">
          <div className="q-card-title">REST API</div>
          <div className="q-masthead-actions">
            <Button text="Open Swagger UI" primary onClick={() => window.open("/docs", "_blank")} />
            <Button text="OpenAPI JSON" onClick={() => window.open("/openapi.json", "_blank")} />
          </div>
        </div>
        <div className="q-table-wrap">
          <table className="q-table">
            <thead>
              <tr>
                <th>Method</th>
                <th>Path</th>
                <th>Description</th>
              </tr>
            </thead>
            <tbody>
              {ENDPOINTS.map(([method, path, description]) => (
                <tr key={method + path}>
                  <td><code>{method}</code></td>
                  <td><code>{path}</code></td>
                  <td>{description}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );

  const renderSettings = () => (
    <div className="q-section">
      <div className="q-card">
        <div className="q-card-title">Admin token</div>
        <p className="q-muted">Stored only in this browser and sent as a bearer token to the API.</p>
        <div className="q-inline">
          <input type="password" value={token} onChange={(event) => setTokenState(event.target.value)} placeholder="QUERYD_ADMIN_TOKEN" />
          <Button text="Save" primary onClick={saveToken} />
          <Button text="Clear" onClick={() => { setTokenState(""); setToken(""); }} />
        </div>
      </div>
      <div className="q-two-col">
        <div className="q-card">
          <div className="q-card-title">Storage</div>
          <Definition label="Configured">{stats && stats.storage.configured ? "Yes" : "No"}</Definition>
          <Definition label="Bucket">{stats ? stats.storage.bucket || "-" : "-"}</Definition>
          <Definition label="Endpoint">{stats ? stats.storage.endpoint || "(AWS default)" : "-"}</Definition>
          <Definition label="Prefix">{stats ? stats.storage.prefix || "(root)" : "-"}</Definition>
        </div>
        <div className="q-card">
          <div className="q-card-title">Runtime</div>
          <Definition label="Version">{stats ? stats.version : "-"}</Definition>
          <Definition label="Olaf">{stats ? stats.olaf_version : "-"}</Definition>
          <Definition label="Active jobs">{stats ? `${stats.active_jobs} / ${stats.max_active_jobs}` : "-"}</Definition>
          <Definition label="Uptime">{stats ? formatDuration(stats.uptime_seconds) : "-"}</Definition>
        </div>
      </div>
    </div>
  );

  const body = () => {
    switch (tab) {
      case "new":
        return renderNew();
      case "jobs":
        return renderJobs();
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

  const job = detail ? detail.job : jobs.find((item) => item.id === selectedJobId) || null;

  return (
    <Page className="q-page">
      <div className="q-shell">
        <div className="q-masthead">
          <div>
            <div className="q-product">Spaza · Airplay</div>
            <div className="q-title">Archive Query Service</div>
            <div className="q-subtitle">
              {stats ? `v${stats.version} · Olaf ${stats.olaf_version}` : "Loading service information"}
            </div>
          </div>
          <div className="q-masthead-actions">
            <Button text="Refresh" onClick={() => { refreshJobs(); refreshStats(); }} />
            <Button text="New query" primary onClick={() => setTab("new")} />
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

        <TabBar className="q-tabs" selectedTabId={tab} onSelectedTabChanged={setTab} tabSize={TabSize.Compact}>
          <Tab id="overview" name="Overview" />
          <Tab id="new" name="New Query" />
          <Tab id="jobs" name="Jobs" />
          <Tab id="logs" name="Logs" />
          <Tab id="api" name="API" />
          <Tab id="settings" name="Settings" />
        </TabBar>

        {body()}
      </div>

      {job ? (
        <Panel
          onDismiss={closePanel}
          contentClassName="q-panel-content"
          titleProps={{ text: `Job #${job.id}` }}
          description={`${job.status} · ${job.processed_indexes}/${job.total_indexes} indexes · ${job.total_matches} matches`}
          footerButtonProps={([
            { text: "Close", onClick: closePanel },
            job.status === "paused"
              ? { text: "Resume", onClick: () => jobAction(job, "resume") }
              : ["queued", "planning", "downloading", "querying"].includes(job.status)
              ? { text: "Pause", onClick: () => jobAction(job, "pause") }
              : null,
            !["completed", "failed", "cancelled"].includes(job.status)
              ? { text: "Cancel", danger: true, onClick: () => jobAction(job, "cancel") }
              : null,
          ].filter(Boolean)) as any}
        >
          <TabBar
            className="q-panel-tabs"
            selectedTabId={panelTab}
            onSelectedTabChanged={(id) => setPanelTab(id as PanelTab)}
            tabSize={TabSize.Compact}
          >
            <Tab id="summary" name="Summary" />
            <Tab id="progress" name="Progress" />
            <Tab id="matches" name="Matches" />
            <Tab id="logs" name="Logs" />
            <Tab id="callbacks" name="Callback deliveries" />
          </TabBar>

          {panelTab === "summary" ? (
            <div className="q-panel-body">
              <Definition label="Status"><Status status={job.status} /></Definition>
              <Definition label="Stage">{job.stage || "-"}</Definition>
              <Definition label="Progress">{Math.round((job.progress || 0) * 100)}%</Definition>
              <Definition label="Attempts">{job.attempts}</Definition>
              <Definition label="Files">{job.total_files}</Definition>
              <Definition label="Indexes">{job.processed_indexes} / {job.total_indexes}</Definition>
              <Definition label="Matches">{job.total_matches}</Definition>
              <Definition label="Callback">{job.callback_url || "-"}</Definition>
              <Definition label="Created">{formatTime(job.created_at)}</Definition>
              <Definition label="Started">{formatTime(job.started_at)}</Definition>
              <Definition label="Completed">{formatTime(job.completed_at)}</Definition>
              {job.error ? <Definition label="Error"><span className="q-error-text">{job.error}</span></Definition> : null}
              <div className="q-card-title q-top-gap">Uploaded files</div>
              <div className="q-table-wrap">
                <table className="q-table">
                  <thead>
                    <tr><th>File</th><th>Size</th><th>Type</th></tr>
                  </thead>
                  <tbody>
                    {(detail ? detail.files : []).map((file) => (
                      <tr key={file.id}>
                        <td>{file.original_name}</td>
                        <td>{formatBytes(file.size_bytes)}</td>
                        <td>{file.content_type || "-"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          ) : null}

          {panelTab === "progress" ? (
            <div className="q-panel-body">
              <div className="q-table-wrap">
                <table className="q-table">
                  <thead>
                    <tr><th>#</th><th>Stream</th><th>Date</th><th>Build</th><th>Status</th><th>Matches</th><th>Size</th></tr>
                  </thead>
                  <tbody>
                    {(detail ? detail.indexes : []).map((index) => (
                      <tr key={index.id}>
                        <td>{index.id}</td>
                        <td>{index.stream_id}</td>
                        <td>{index.date}</td>
                        <td className="q-break">{index.build_id || "-"}</td>
                        <td><Status status={index.status} /></td>
                        <td>{index.matches}</td>
                        <td>{formatBytes(index.size_bytes)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          ) : null}

          {panelTab === "matches" ? (
            <div className="q-panel-body">
              <div className="q-muted q-bottom-gap">{matchTotal} match(es)</div>
              <div className="q-table-wrap">
                <table className="q-table">
                  <thead>
                    <tr><th>File</th><th>Stream</th><th>Date</th><th>Occurrence</th><th>Resource</th><th>Ref start</th><th>Ref stop</th><th>Count</th><th>Score</th></tr>
                  </thead>
                  <tbody>
                    {matches.map((match) => (
                      <tr key={match.id}>
                        <td>{match.file_name}</td>
                        <td>{match.stream_id}</td>
                        <td>{match.date}</td>
                        <td>{formatTime(match.occurrence_time)}</td>
                        <td className="q-break">{match.resource_id}</td>
                        <td>{match.reference_start_ms} ms</td>
                        <td>{match.reference_stop_ms} ms</td>
                        <td>{match.match_count}</td>
                        <td>{match.score ? match.score.toFixed(2) : "-"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          ) : null}

          {panelTab === "logs" ? (
            <div className="q-panel-body">
              <LogList entries={jobLogs} empty="No logs recorded for this job yet." />
            </div>
          ) : null}

          {panelTab === "callbacks" ? (
            <div className="q-panel-body">
              <div className="q-table-wrap">
                <table className="q-table">
                  <thead>
                    <tr><th>Event</th><th>Attempt</th><th>Status</th><th>Success</th><th>Error</th><th>Time</th></tr>
                  </thead>
                  <tbody>
                    {callbacks.map((item) => (
                      <tr key={item.id}>
                        <td>{item.event}</td>
                        <td>{item.attempt}</td>
                        <td>{item.status_code || "-"}</td>
                        <td>{item.success ? "yes" : "no"}</td>
                        <td className="q-error-text q-break">{item.error || "-"}</td>
                        <td>{formatTime(item.created_at)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          ) : null}
        </Panel>
      ) : null}
    </Page>
  );
}
