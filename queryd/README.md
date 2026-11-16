# queryd

`queryd` is an asynchronous, batch query service for the broadcast archive produced by
`streamd` and `indexer`. Clients upload one or more audio clips together with a scope of
streams and dates. `queryd` discovers the matching daily Olaf indexes in S3, downloads
them, runs `olaf query`, maps the results back to archive recordings, and reports matches
through the API and optional progressive callbacks.

It is designed for one host and one SQLite database. Heavy temporary data is removed when
a job reaches a terminal state; job metadata, matches, logs, and callback delivery history
are retained according to the configured retention period.

- Web UI: `http://127.0.0.1:8790/`
- OpenAPI document: `GET /openapi.json`
- Swagger UI: `GET /docs`
- Source documentation: [requirements](docs/requirements.md),
  [implementation plan](docs/implementation-plan.md), and
  [archive/index contract](../docs/indexer.md)

## Features

- Upload up to 20 audio files per job by default.
- Select streams by `all`, explicit IDs, or country.
- Select a single date or a date range, with an optional daily time window in stream-local
  time.
- Discover only indexes whose daily `meta.json` is marked `status: complete`.
- Download indexes with bounded parallelism and a job size cap.
- Run each index through an isolated Olaf home, database, and cache directory.
- Map Olaf references to archive `resource_id` values and absolute broadcast occurrence
  times.
- Progressive `job.matches` and `job.progress` callbacks per index.
- Durable queue with retry, pause, resume, cancel, and restart recovery.
- Admin bearer-token API, signed callbacks, job logs, callback delivery history, stats,
  health, and readiness endpoints.
- Embedded Azure DevOps-style React UI.
- Installer, hardened systemd unit, build script, and rollback-capable update script.

## How it works

```text
Client
  |
  | POST /api/jobs (files + scope)
  v
queryd API ---> SQLite queue
  |
  v
Planner ---> S3 archive indexes/YYYY/MM/DD/meta.json
  |
  v
Downloader ---> local job workspace (data.mdb, lock.mdb, timeline.json)
  |
  v
Olaf query (isolated per index)
  |
  v
Timeline mapper + date/time-window filter
  |
  +--> matches table
  +--> job.progress / job.matches callbacks
  |
  v
Cleanup uploads and index files; retain metadata
```

## Requirements

### Runtime

- Linux on `x86_64` or `aarch64`.
- An S3-compatible bucket containing the archive written by `indexer`.
- A local `olaf` executable, version 2026.10.19 or compatible.
- `ffmpeg`/`ffprobe` for the current Olaf CLI.
- `tzdata` for stream-local time calculations.
- Free disk in the configured workspace. The default guard refuses work below 10 GB free.

### Build

- Go 1.27.4 or newer, as declared by `go.mod`.
- Node.js 20.19 or newer and npm. Node is only needed to build the embedded frontend.

The installer provisions missing build and runtime dependencies on Ubuntu/Debian.

### Archive prerequisites

The service expects this layout under the configured bucket and prefix:

```text
<stream>/meta.json
<stream>/indexes/YYYY/MM/DD/meta.json
<stream>/indexes/YYYY/MM/DD/builds/<build_id>/data.mdb
<stream>/indexes/YYYY/MM/DD/builds/<build_id>/lock.mdb
<stream>/indexes/YYYY/MM/DD/builds/<build_id>/timeline.json
<stream>/recordings/...
```

The daily `meta.json` is the completion pointer and must have `status: "complete"` plus a
build prefix or build ID. Incomplete or missing indexes are skipped. A job can therefore
complete with zero indexes when the requested streams or dates have not been indexed.

`<stream>/meta.json` supplies `timezone` and `metadata.country`, both optional depending on
the scope. If no index or stream timezone is available, `default_timezone` is used.

## Installation

### Quick install from source

Run as root from the `queryd` directory:

```bash
sudo ./scripts/install.sh --build
```

The installer:

1. Installs `ffmpeg`, `ca-certificates`, `tzdata`, `curl`, `tar`, and `xz-utils` when
   missing.
2. Installs Go at the version required by `go.mod` when no suitable toolchain is present.
3. Installs Node.js when missing and Node is needed for the build.
4. Installs Olaf from its GitHub release when `olaf` is not available.
5. Builds the frontend and backend.
6. Creates the `queryd` system user, directories, configuration, and environment file.
7. Installs and enables the `queryd` systemd unit.

If no `dist/queryd` exists, the installer builds automatically even without `--build`.

Installer options:

| Option | Description |
| --- | --- |
| `--build` | Build the frontend and backend before installing. |
| `--binary PATH` | Install a specific backend binary instead of `dist/queryd`. |
| `--olaf-version TAG` | Olaf release tag to install when missing. Default `v3.3.3`. |
| `--olaf-binary PATH` | Install a local Olaf binary instead of downloading a release. |
| `--prefix PATH` | Install directory. Default `/opt/queryd`. |
| `--config-dir PATH` | Configuration directory. Default `/etc/queryd`. |
| `--state-dir PATH` | State, database, and workspace directory. Default `/var/lib/queryd`. |
| `--log-dir PATH` | Log directory. Default `/var/log/queryd`. |
| `--user NAME` | Service user. Default `queryd`. |
| `--no-start` | Install and enable the unit without starting it. |

Installed paths:

```text
/opt/queryd/queryd                  binary
/etc/queryd/config.yaml             configuration
/etc/queryd/.env                    secrets and S3 settings
/var/lib/queryd/queryd.db           SQLite database
/var/lib/queryd/jobs/               temporary job workspaces
/var/log/queryd/queryd.log          application log
```

The installer never overwrites an existing `config.yaml` or `.env`.

### Configure S3 and secrets

Edit `/etc/queryd/.env`:

```bash
sudo editor /etc/queryd/.env
```

Example:

```dotenv
QUERYD_ADMIN_TOKEN=replace-with-a-long-random-token
QUERYD_CALLBACK_SECRET=replace-with-a-callback-signing-secret

S3_ENDPOINT=https://s3.example.com
S3_REGION=auto
S3_BUCKET=airplay-archive
S3_ACCESS_KEY=replace-me
S3_SECRET_KEY=replace-me
S3_PREFIX=prod/
S3_PATH_STYLE=true
```

For AWS S3, leave `S3_ENDPOINT` empty and set the real region. For MinIO and other
S3-compatible stores, set the endpoint and usually keep `S3_PATH_STYLE=true`. An empty
prefix means the bucket root. Do not add a leading slash to `S3_PREFIX`.

The S3 identity only needs read access:

- `s3:ListBucket` on the bucket, scoped to the archive prefix if possible.
- `s3:GetObject` on the archive prefix.

If `S3_ACCESS_KEY` and `S3_SECRET_KEY` are both empty, the AWS SDK default credential chain
may be used, but static credentials in the environment file are the simplest deployment
for non-AWS stores.

`QUERYD_ADMIN_TOKEN` protects every `/api/*` endpoint except health. If it is empty, API
authentication is disabled. Keep the default `127.0.0.1` listener unless an authenticating
reverse proxy is in front of the service.

Optional callback signing is configured with `QUERYD_CALLBACK_SECRET`. The configuration
loader expands `${NAME}` placeholders from the environment, so an unset variable becomes an
empty value and disables the corresponding feature.

### Start and verify

```bash
sudo systemctl restart queryd
sudo systemctl status --no-pager queryd
curl -fsS http://127.0.0.1:8790/api/health
curl -fsS http://127.0.0.1:8790/api/health/ready
```

Without S3 configuration the service still starts in a degraded mode: `/api/health` returns
200, `/api/health/ready` returns 503, and job creation returns 503.

Open the UI at `http://127.0.0.1:8790/`. Enter the admin token in the **Settings** tab if
one is configured.

### Manual build and run

For development or a non-systemd deployment:

```bash
cd queryd
./scripts/build.sh
./dist/queryd version
./dist/queryd serve --config ./config.yaml --env ./.env
```

`--skip-web` builds only the Go binary and requires `web/dist/index.html` to already exist:

```bash
./scripts/build.sh --skip-web
```

The build script uses `$GO`, then `$HOME/.local/go/bin/go`, then `go` from `PATH`. Set
`GO=/path/to/go` to force a toolchain. The binary embeds the frontend and OpenAPI document.

Without a command, `queryd` runs `serve`. The default configuration path when `--config` is
omitted is `/etc/queryd/config.yaml`; the default environment path is `.env` in the same
directory. `--env` overrides that path.

### Update and rollback

```bash
sudo ./scripts/update.sh --build
```

The update script backs up the current binary under `/var/lib/queryd/backups`, installs the
new binary, restarts the service, and restores the previous binary if the service fails to
become active. Keep the newest five backups. Use `--binary PATH` to install a prebuilt
binary or `--no-restart` to replace it without restarting.

Database migrations run automatically on startup.

## Web UI

The embedded UI has these areas:

- **Overview**: service state, job counters, storage, disk, and recent jobs.
- **New Query**: upload files, build a scope, set a callback URL, and submit.
- **Jobs**: filter and browse jobs, open job details, pause/resume/cancel.
- **Logs**: global recent job logs with level and text filters.
- **API**: endpoint summary and link to the OpenAPI document.
- **Settings**: admin token stored locally in the browser and runtime limits.

A job panel contains **Summary**, **Progress**, **Matches**, **Logs**, and **Callback
deliveries** tabs.

The static UI itself is not authenticated. All data calls are protected by the admin token.
Put the service behind HTTPS and an authenticating reverse proxy before exposing it beyond
localhost.

## API

### Authentication

Send the admin token as either:

```http
Authorization: Bearer <token>
```

or:

```http
X-Admin-Token: <token>
```

`GET /api/health` and `GET /api/health/ready` are always public. If
`server.admin_token` is empty, all endpoints are public.

### Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/jobs` | Create a query job using multipart form data. |
| `GET` | `/api/jobs` | List jobs. Query: `status`, `limit`, `offset`. |
| `GET` | `/api/jobs/{id}` | Job detail with files and indexes. |
| `GET` | `/api/jobs/{id}/matches` | Matches. Query: `file_id`, `stream_id`, `limit`, `offset`. |
| `GET` | `/api/jobs/{id}/logs` | Job logs. Query: `level`, `search`, `limit`, `offset`. |
| `GET` | `/api/jobs/{id}/callbacks` | Callback delivery attempts. Query: `limit`. |
| `GET` | `/api/jobs/logs` | Global recent logs. Query: `level`, `search`, `limit`, `offset`. |
| `POST` | `/api/jobs/{id}/pause` | Pause a queued, planning, downloading, or querying job. |
| `POST` | `/api/jobs/{id}/resume` | Resume a paused job. |
| `POST` | `/api/jobs/{id}/cancel` | Cancel a job and remove temporary artifacts. |
| `GET` | `/api/stats` | Service, job, storage, disk, and limit statistics. |
| `GET` | `/api/health` | Liveness and version information. |
| `GET` | `/api/health/ready` | Readiness: database, S3 configuration, workspace. |
| `GET` | `/openapi.json` | OpenAPI 3 document. |
| `GET` | `/docs` | Swagger UI. It loads Swagger assets from unpkg. |

Pagination limits: jobs default 100, maximum 500; matches default 500, maximum 2000; logs
default 300, maximum 2000; callback deliveries default 100, maximum 500.

`GET /api/jobs` includes a `counts` object with counts for every job status. Stats include
`jobs`, `active_jobs`, `max_active_jobs`, `indexes_queried`, `matches_total`, storage
settings, limits, and disk free/total bytes.

### Create a job

`POST /api/jobs` is `multipart/form-data` with:

- `files`: one or more audio files. `files[]` is also accepted.
- `scope`: JSON object.
- `callback_url`: optional absolute `http` or `https` URL.

Example:

```bash
SCOPE='{
  "streams": {"mode": "ids", "ids": ["radio-one"]},
  "dates": {"mode": "date", "date": "2026-11-15"},
  "daily_time_window": {"start": "18:00", "end": "22:00"}
}'

curl -sS \
  -H "Authorization: Bearer $QUERYD_ADMIN_TOKEN" \
  -F "files=@clip-1.wav" \
  -F "files=@clip-2.mp3" \
  -F "scope=$SCOPE" \
  -F "callback_url=https://example.com/hooks/queryd" \
  http://127.0.0.1:8790/api/jobs
```

Response, HTTP 201:

```json
{
  "job": {
    "id": 42,
    "status": "queued",
    "stage": "queued",
    "progress": 0,
    "scope": "{\"streams\":{\"mode\":\"ids\",\"ids\":[\"radio-one\"]},...}",
    "callback_url": "https://example.com/hooks/queryd",
    "total_files": 2,
    "total_indexes": 0,
    "processed_indexes": 0,
    "total_matches": 0,
    "attempts": 0,
    "created_at": "2026-11-16T00:32:55Z",
    "updated_at": "2026-11-16T00:32:55Z"
  }
}
```

Accepted upload extensions: `.mp3`, `.wav`, `.flac`, `.ogg`, `.opus`, `.m4a`, `.aac`,
`.wma`, `.aiff`, and `.aif`. Validation is by extension, not by content type. Files are
stored under the job workspace as `<file_id>.<extension>`.

### Scope options

All streams, one date:

```json
{
  "streams": {"mode": "all"},
  "dates": {"mode": "date", "date": "2026-11-15"}
}
```

Specific streams, date range, and an evening window:

```json
{
  "streams": {"mode": "ids", "ids": ["radio-one", "radio-two"]},
  "dates": {"mode": "range", "from": "2026-11-01", "to": "2026-11-03"},
  "daily_time_window": {
    "start": "18:00",
    "end": "22:00",
    "basis": "stream_local",
    "match": "overlap"
  }
}
```

Streams filtered by country from `<stream>/meta.json` `metadata.country`:

```json
{
  "streams": {"mode": "country", "country": "KE"},
  "dates": {"mode": "date", "date": "2026-11-15"}
}
```

Scope rules:

- `streams.mode` is `all`, `ids`, or `country`.
- `dates.mode` is `date` or `range`; ranges may not exceed 366 days.
- `daily_time_window` is optional. `basis` must be `stream_local` and `match` must be
  `overlap`; omitted values default to those values.
- The window is evaluated in the stream timezone. Both `start == end` (all day) and windows
  crossing midnight are handled.
- Queryd selects index dates from `from - 1 day` through `to` and then filters matches by
  the requested local date. The extra previous index date catches recordings that start
  before midnight and continue into the requested day.

### Poll a job

```bash
curl -sS -H "Authorization: Bearer $QUERYD_ADMIN_TOKEN" \
  http://127.0.0.1:8790/api/jobs/42
```

The response contains `job`, `files`, and `indexes`. Poll `progress`, `processed_indexes`,
and `total_indexes` to follow execution. Terminal statuses are `completed`, `failed`, and
`cancelled`.

List jobs:

```bash
curl -sS -H "Authorization: Bearer $QUERYD_ADMIN_TOKEN" \
  "http://127.0.0.1:8790/api/jobs?status=completed&limit=20&offset=0"
```

### Read matches

```bash
curl -sS -H "Authorization: Bearer $QUERYD_ADMIN_TOKEN" \
  "http://127.0.0.1:8790/api/jobs/42/matches?limit=100"
```

Example match:

```json
{
  "id": 1,
  "job_id": 42,
  "job_index_id": 7,
  "file_id": 3,
  "file_name": "clip-1.wav",
  "query_path": "/var/lib/queryd/jobs/42/uploads/3.wav",
  "resource_id": "radio-one-20261115-180000",
  "stream_id": "radio-one",
  "date": "2026-11-15",
  "recording_start_time": "2026-11-15T18:00:00Z",
  "occurrence_time": "2026-11-15T18:00:00.152Z",
  "reference_start_ms": 152,
  "reference_stop_ms": 19200,
  "match_count": 618,
  "score": 0,
  "timeline_start_ms": 64800152,
  "timeline_end_ms": 64819200,
  "created_at": "2026-11-16T00:36:20Z"
}
```

Field meanings:

- `resource_id`: archive recording identifier from the timeline.
- `occurrence_time`: absolute UTC start of the matched audio in the broadcast.
- `reference_start_ms` / `reference_stop_ms`: match offsets from the recording start.
- `timeline_start_ms` / `timeline_end_ms`: offsets in the daily stream timeline.
- `date`: the index date, which may differ from the local date of a midnight-crossing
  occurrence.
- `stream_id`: the stream whose index produced the match.

Job file paths are not exposed in job detail responses. Match records include the
`query_path` used during execution, which may refer to a workspace that has since been
cleaned up.

Olaf can report several overlapping alignment candidates for the same query file and
archive recording. Queryd collapses each overlapping cluster to the candidate with the
highest `match_count`. Non-overlapping occurrences are retained, so the same song played
twice in one recording produces two matches rather than one.

### Logs and callbacks

```bash
curl -sS -H "Authorization: Bearer $QUERYD_ADMIN_TOKEN" \
  "http://127.0.0.1:8790/api/jobs/42/logs?level=error&limit=100"

curl -sS -H "Authorization: Bearer $QUERYD_ADMIN_TOKEN" \
  http://127.0.0.1:8790/api/jobs/42/callbacks
```

Global logs are available at `GET /api/jobs/logs`. Log records contain `level`, `stage`,
`message`, and `created_at`.

### Pause, resume, cancel

```bash
curl -sS -X POST -H "Authorization: Bearer $QUERYD_ADMIN_TOKEN" \
  http://127.0.0.1:8790/api/jobs/42/pause

curl -sS -X POST -H "Authorization: Bearer $QUERYD_ADMIN_TOKEN" \
  http://127.0.0.1:8790/api/jobs/42/resume

curl -sS -X POST -H "Authorization: Bearer $QUERYD_ADMIN_TOKEN" \
  http://127.0.0.1:8790/api/jobs/42/cancel
```

A queued job can be paused immediately. Pausing an active job stops it at the next safe
point and keeps downloaded indexes for resume. Cancelling removes temporary uploads and
indexes. Pausing or cancelling a terminal job is a no-op. Resuming a job that is not paused
returns HTTP 409.

## Callbacks

If `callback_url` is set, queryd sends JSON POST requests with this envelope:

```json
{
  "event": "job.matches",
  "job_id": 42,
  "timestamp": "2026-11-16T00:36:20Z",
  "data": {}
}
```

Events:

| Event | Data |
| --- | --- |
| `job.started` | `{"total_files": 2}` |
| `job.progress` | `{"processed_indexes": 1, "total_indexes": 3, "total_matches": 5, "progress": 0.516}` |
| `job.matches` | `{"index": {"id": 7, "stream_id": "radio-one", "date": "2026-11-15", "build_id": "build-1", "matches": 1}, "matches": [...]}` |
| `job.paused` | No data. |
| `job.resumed` | No data. |
| `job.completed` | `{"total_indexes": 3, "total_matches": 5}` |
| `job.failed` | `{"error": "..."}` |
| `job.cancelled` | No data. |

Request headers:

```http
Content-Type: application/json
User-Agent: queryd/<version>
X-Queryd-Event: job.matches
X-Queryd-Signature: sha256=<hex hmac>
```

`X-Queryd-Signature` is present when `callbacks.signing_secret` /
`QUERYD_CALLBACK_SECRET` is configured. It is an HMAC-SHA256 of the raw request body. A
successful receiver should return any 2xx status. Deliveries are retried with exponential
backoff up to `callbacks.max_attempts`, capped at 30 seconds between attempts. Callback
failures never fail the job; inspect them through `/api/jobs/{id}/callbacks`.

Python verification example:

```python
import hashlib
import hmac

expected = "sha256=" + hmac.new(
    secret.encode("utf-8"), raw_body, hashlib.sha256
).hexdigest()

if not hmac.compare_digest(expected, request.headers["X-Queryd-Signature"]):
    raise ValueError("invalid callback signature")
```

## Configuration

Configuration is YAML. Every `${NAME}` placeholder is expanded from the process
environment after the environment file is loaded. Fields omitted from YAML keep their
defaults.

```yaml
server:
  listen: "127.0.0.1:8790"
  admin_token: "${QUERYD_ADMIN_TOKEN}"
  shutdown_grace_seconds: 30

database:
  path: "/var/lib/queryd/queryd.db"
  workspace: "/var/lib/queryd/jobs"

limits:
  max_upload_files: 20
  max_upload_mb: 200
  max_total_upload_mb: 1000
  max_indexes_per_job: 500
  max_download_gb: 50
  min_free_disk_gb: 10

workers:
  max_active_jobs: 1
  downloads_per_job: 4
  olaf_threads: 2
  job_timeout_minutes: 360
  retry_initial_seconds: 30
  retry_max_seconds: 1800
  max_attempts: 3

olaf:
  binary: "/usr/local/bin/olaf"
  target_sample_rate: 16000
  min_match_count: 20
  max_results: 500
  timeout_minutes: 60

callbacks:
  timeout_seconds: 15
  max_attempts: 8
  signing_secret: "${QUERYD_CALLBACK_SECRET}"

retention:
  jobs_days: 30
  logs_days: 30

default_timezone: "Africa/Nairobi"

logging:
  level: "info"
  file: "/var/log/queryd/queryd.log"
```

| Section | Field | Description |
| --- | --- | --- |
| `server` | `listen` | Listen address. Default `127.0.0.1:8790`. |
| | `admin_token` | Bearer token for `/api/*` except health. Empty disables auth. |
| | `shutdown_grace_seconds` | Time allowed for active jobs before cancellation on shutdown. |
| `database` | `path` | SQLite file. Parent directory must be creatable/writable. |
| | `workspace` | Temporary job workspaces. Must have room for uploads and indexes. |
| `limits` | `max_upload_files` | Maximum files per job. |
| | `max_upload_mb` | Maximum size of each uploaded file. |
| | `max_total_upload_mb` | Maximum combined upload size per job. |
| | `max_indexes_per_job` | Maximum indexes a scope may resolve to. |
| | `max_download_gb` | Maximum total download size per job. |
| | `min_free_disk_gb` | Refuse work when free workspace disk is below this. |
| `workers` | `max_active_jobs` | Concurrent jobs. Keep at 1 for predictable Olaf CPU use. |
| | `downloads_per_job` | Parallel index downloads within one job. |
| | `olaf_threads` | Passed to `olaf query --threads`. |
| | `job_timeout_minutes` | Per-job timeout. |
| | `retry_initial_seconds` | Initial retry delay. |
| | `retry_max_seconds` | Maximum retry delay. |
| | `max_attempts` | Transient failure attempts. |
| `olaf` | `binary` | Path to the Olaf CLI. Checked at startup. |
| | `target_sample_rate` | Written into the per-index Olaf config. |
| | `min_match_count` | Written into the per-index Olaf config. |
| | `max_results` | Written into the per-index Olaf config. |
| | `timeout_minutes` | Timeout for one `olaf query`. |
| `callbacks` | `timeout_seconds` | HTTP client timeout per attempt. |
| | `max_attempts` | Delivery attempts per event. |
| | `signing_secret` | Optional HMAC secret. |
| `retention` | `jobs_days` | Delete terminal job metadata after this many days. `0` disables. |
| | `logs_days` | Prune logs after this many days. `0` disables. |
| `logging` | `level` | `debug`, `info`, `warn`, or `error`. |
| | `file` | Log file. Empty logs to stderr only. |

`default_timezone` is used when neither the index metadata nor stream metadata provides a
timezone.

## Job lifecycle

Statuses:

```text
queued -> planning -> downloading -> querying -> completed
   |          |             |            |
   |          +-------------+------------+--> failed
   |          +-------------+------------+--> cancelled
   +----------+-------------+------------+--> paused -> queued
```

- **queued**: waiting for a worker or retry delay.
- **planning**: resolving streams and reading daily index metadata.
- **downloading**: fetching index files with a disk and size guard.
- **querying**: running Olaf per index and mapping matches.
- **completed**: all indexes processed.
- **failed**: permanent error or retries exhausted.
- **cancelled**: cancelled by a client.
- **paused**: stopped by a client and retained for resume.

Transient failures retry with exponential backoff up to `workers.max_attempts`. Failed
indexes return to `pending`; indexes already completed are kept. Jobs that are executing
when the process stops are returned to `queued` on restart. A graceful shutdown lets jobs
run for `shutdown_grace_seconds` before interrupting them so they can be recovered.

After a terminal state, uploaded files and downloaded indexes are removed. The database
keeps job, file metadata, index metadata, matches, logs, and callback delivery records.
Retention pruning runs hourly.

## Operations

### Service management

```bash
sudo systemctl start queryd
sudo systemctl stop queryd
sudo systemctl restart queryd
sudo systemctl status --no-pager queryd
journalctl -u queryd -f
```

The service runs as the `queryd` user with `ProtectSystem=strict`, a private temporary
directory, no new privileges, and write access only to the state and log directories.
The process `HOME` is the state directory so Olaf can write its cache/config safely.

### Logs

- Application log: `/var/log/queryd/queryd.log`
- systemd journal: `journalctl -u queryd`
- Per-job structured logs: UI **Logs**, or `GET /api/jobs/{id}/logs`
- Global recent logs: `GET /api/jobs/logs`

### Backup

The only durable state is the SQLite database. Back it up while the service is stopped, or
use the SQLite backup command if `sqlite3` is installed:

```bash
sudo systemctl stop queryd
sudo cp /var/lib/queryd/queryd.db /var/backups/queryd-$(date -u +%Y%m%dT%H%M%SZ).db
sudo systemctl start queryd
```

S3 is the source of truth for archive indexes; queryd does not modify it. Callback delivery
and match history exist only in SQLite.

### Scaling

Run one queryd process per database and workspace. The queue, active-job limits, and
retention are local. To handle more load, increase `workers.max_active_jobs`,
`workers.downloads_per_job`, and `workers.olaf_threads` in line with available CPU, memory,
disk, and S3 bandwidth. Do not point multiple instances at the same SQLite file.

### Reverse proxy

The default listener is localhost-only. If remote clients need access, put queryd behind
HTTPS and authentication. Keep the admin token enabled and forward `Authorization`
unchanged. The UI is a static SPA and has no login screen of its own.

## Troubleshooting

### Readiness returns 503

`/api/health/ready` checks the database, workspace, and S3 configuration. Check
`S3_BUCKET`; an empty bucket deliberately disables job creation. Restart the service after
changing `/etc/queryd/.env`.

```bash
sudo systemctl show queryd -p EnvironmentFiles
sudo journalctl -u queryd -n 100 --no-pager
```

### Job creation returns 503

S3 is not configured. Set `S3_BUCKET` and the credentials and restart. `GET /api/health`
shows `"storage_configured": false` in this state.

### Job completes with zero indexes

The scope may be valid but no complete daily index exists. Queryd skips missing
`meta.json`, non-`complete` metadata, and metadata without a build prefix. Check the job
logs and verify the stream IDs, dates, bucket, and prefix. The API does not expose a catalog
listing endpoint by design.

### S3 access errors

Verify `ListBucket` and `GetObject` permissions on the full prefix. For MinIO or another
S3-compatible service, check the endpoint, region, and path-style setting. `S3_PREFIX` must
not start with a slash.

### Startup fails with an executable error

`olaf.binary` must point to an executable file. `queryd` also checks the Olaf version at
startup. Install Olaf with the installer or set `olaf.binary` to its real path.

### Jobs fail during query

Inspect the job's error log and the `debug` log for Olaf output. The most common causes are
an unsupported or corrupt upload, a missing `timeline.json`, an index built by an
incompatible Olaf version, or insufficient disk during download. Ensure `ffmpeg`/`ffprobe`
remain installed.

### Disk guard blocks work

`limits.min_free_disk_gb` is checked before downloads. Increase workspace space, remove old
job workspaces, or lower the guard. The max download guard uses metadata sizes, falling
back to a `HEAD` request when sizes are absent.

### Callback failures

Callbacks are best-effort and recorded in `/api/jobs/{id}/callbacks`. Check the target URL,
TLS trust, timeout, and HMAC secret. A non-2xx response or timeout is retried; it never
changes the job's terminal status.

### SQLite is locked or the service will not start

Only one queryd process may use a database and workspace. Check for a stale second process
or a manually started test instance. The database uses WAL mode and a 10 second busy
timeout.

## Development

```bash
cd queryd
./scripts/build.sh
go test ./...
go vet ./...
```

Frontend only:

```bash
cd web
npm ci
npm run build
```

The production binary embeds `web/dist`; build the frontend before a plain `go build`.
`scripts/build.sh` handles both. Unit tests cover scope validation, time-window overlap,
timeline mapping, and Olaf JSON parsing.

## Caveats

- Single-node only; the queue and workers are not distributed.
- No archive catalog or playback APIs.
- Authentication is a single admin token plus optional callback signing.
- The service reads S3; it never writes to the archive bucket.
- Query paths shown in match records may refer to deleted workspaces after cleanup.
- The Swagger UI page loads its assets from `unpkg.com`; `/openapi.json` works without
  external assets.
