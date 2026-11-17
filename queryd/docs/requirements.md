# queryd requirements

## Purpose

`queryd` is an asynchronous, batch query service for the broadcast archive produced by
`streamd` and `indexer`. A client submits one or more audio file URLs, or uploads clips
through the web UI, together with a scope (streams and dates). The service downloads the
files, resolves the matching daily Olaf indexes, downloads them, runs `olaf query`, reports
matches progressively through a callback, and removes the heavy temporary artifacts when
the job ends.

The service exposes only job-oriented APIs, statistics, and health checks. It does not
expose archive catalog browsing APIs.

## Scope of v1

- Accept up to `limits.max_upload_files` audio files per job, either as URLs or through the
  web UI multipart upload endpoint.
- Accept a JSON scope selecting streams and dates, with an optional daily time window.
- Run asynchronously. Jobs survive restarts and can be paused, resumed, or cancelled.
- Use S3 credentials from the environment; no storage administration API.
- Download completed daily indexes referenced by `meta.json`.
- Run the local `olaf query` CLI against each index.
- Map Olaf matches back to archive recordings and absolute occurrence times.
- Deliver progressive callbacks per index and terminal callbacks per job.
- Delete uploaded audio and downloaded indexes after the job reaches a terminal state.
- Retain job, file, index, match, log, and callback-delivery metadata.
- Provide a single-page Azure DevOps style web UI and an OpenAPI/Swagger endpoint.

## Input

### Input files

`POST /api/jobs` accepts an array of absolute `http` or `https` file URLs. The worker
downloads them asynchronously into the job workspace. The bundled web UI continues to use
`POST /api/jobs/upload` with multipart file uploads.

Accepted extensions: `.mp3`, `.wav`, `.flac`, `.ogg`, `.opus`, `.m4a`, `.aac`, `.wma`,
`.aiff`, `.aif`. For URL files the extension is derived from the URL path,
`Content-Disposition`, or `Content-Type`.

Each file is limited by `limits.max_upload_mb`; the combined files are limited by
`limits.max_total_upload_mb`. Files are stored in the job workspace and renamed to
`<file_id>.<ext>` before being passed to Olaf. URL downloads follow at most five redirects.
Unless `limits.allow_private_urls` is enabled, destinations resolving to private, loopback,
or link-local addresses are rejected.

### Scope

```json
{
  "streams": { "mode": "all" },
  "dates": { "mode": "range", "from": "2026-11-01", "to": "2026-11-03" },
  "daily_time_window": {
    "start": "18:00",
    "end": "22:00",
    "basis": "stream_local",
    "match": "overlap"
  }
}
```

- `streams.mode`: `all`, `ids`, or `country`.
- `streams.ids`: required for `ids`; stream identifiers.
- `streams.country`: required for `country`; compared with `<stream>/meta.json`
  `metadata.country`.
- `dates.mode`: `date` or `range`.
- `dates.date`, `dates.from`, `dates.to`: `YYYY-MM-DD`.
- `daily_time_window` is optional. `start` and `end` are `HH:MM`. The window is evaluated in
  the stream timezone (`basis: stream_local`) and a match is kept when its occurrence range
  overlaps the window (`match: overlap`). Windows that cross midnight are supported.

The service selects index dates from `from - 1 day` through `to` so recordings that start
before midnight and finish after midnight are considered. Matches are then filtered against
the requested local dates.

## Output

Each match contains:

- job, index, file, stream, date and resource identifiers;
- the matched archive `resource_id`;
- the original upload file name and stored query path;
- recording start time, absolute occurrence time, and reference offsets in milliseconds;
- timeline offsets within the daily timeline;
- Olaf match count and score when available.

Olaf can return multiple overlapping alignment hypotheses for the same uploaded file and
recording. Each overlapping cluster is reduced to the candidate with the highest match
count; distinct non-overlapping occurrences are kept separately.

## Callbacks

`callback_url` is optional. Events are JSON POSTs:

- `job.started`
- `job.progress`
- `job.matches`
- `job.paused`
- `job.resumed`
- `job.completed`
- `job.failed`
- `job.cancelled`

When `callbacks.signing_secret` is configured, requests include
`X-Queryd-Signature: sha256=<hex hmac>` over the raw request body. Delivery attempts,
response status, and errors are recorded. Callback failures never fail a job.

## Lifecycle

States: `queued`, `planning`, `downloading`, `querying`, `completed`, `failed`,
`cancelled`, `paused`.

- Transient failures retry with exponential backoff up to `workers.max_attempts`.
- Pause stops the active process and preserves downloaded indexes for resume.
- Cancel stops the active process and removes temporary artifacts.
- Cleanup removes uploaded audio and downloaded indexes after completion, failure,
  cancellation, or retention expiry.

## Operational requirements

- Single-node SQLite metadata database with WAL and a 10 second busy timeout.
- Disk guard: refuse work when free space is below `limits.min_free_disk_gb`.
- Download guard: refuse a job whose indexes exceed `limits.max_download_gb`.
- Index guard: refuse a job resolving to more than `limits.max_indexes_per_job`.
- Readiness requires the database and S3 configuration.
- Installer provisions dependencies (Go, Node.js, Olaf, ffmpeg, CA certificates) when
  missing, creates the service user, installs config and environment template, and enables
  the systemd unit.

## Non-goals for v1

- Distributed workers or Olaf load balancing.
- Per-request index sharding.
- Archive catalog APIs.
- In-browser playback.
- Multi-tenant authentication beyond the admin bearer token.
