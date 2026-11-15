# Olaf Daily Indexer

A single Go service that builds one validated Olaf fingerprint index per stream and
local date from timestamped recordings in S3-compatible storage. It also produces an
authoritative daily timeline describing recording order, offsets, gaps, and overlaps.

The service provides:

- A SQLite-backed job queue shared by manual, API, recorder-event, and discovery
  initiation paths.
- A bounded worker pool with retries, cancellation, attempts, and logs.
- Automatic discovery of completed recording dates.
- Versioned index publication through an immutable `builds/{build_id}/` prefix.
- A React web UI and REST API with OpenAPI documentation.
- systemd deployment scripts for install, update, and rollback.

The authoritative technical specification is in `../docs/indexer.md`. Local planning
documents are in `docs/implementation-plan.md` and `docs/task-list.md`.

---

## 1. How it works

```text
Source S3/R2
  {stream}/recordings/YYYY/MM/DD/HHMMSS.ext
                    |
                    v
        list -> download -> ffprobe -> timeline
                    |
                    v
       isolated Olaf cache -> one daily LMDB
                    |
                    v
        validate -> upload immutable build
                    |
                    v
Destination S3/R2
  {stream}/indexes/YYYY/MM/DD/meta.json
  {stream}/indexes/YYYY/MM/DD/builds/{build_id}/data.mdb
  {stream}/indexes/YYYY/MM/DD/builds/{build_id}/lock.mdb
  {stream}/indexes/YYYY/MM/DD/builds/{build_id}/timeline.json
```

The date-level `meta.json` is the completion pointer and is uploaded last. A failed
rebuild never invalidates a previously published build.

### Timeline behavior

- Recording timestamps come from `HHMMSS.ext` filenames and are interpreted in the
  stream timezone.
- The timezone is read from `{stream}/meta.json` at `metadata.timezone`, falling back
  to `default_timezone` in the deployment config.
- `timeline_origin` is local midnight converted to UTC.
- Durations come from `ffprobe`, never from filenames.
- Gaps and overlaps are preserved as explicit timeline data.
- A recording crossing midnight stays with the job for its start date.
- Publication is refused when a discovered recording is missing, unreadable, or not
  represented in the Olaf database.

---

## 2. Requirements

- Debian or Ubuntu with systemd. The installer uses `apt` and `dpkg`.
- Go 1.27.4 or newer to build the backend. `scripts/build.sh` prefers
  `$HOME/.local/go/bin/go`, then `go` from `PATH`.
- Node.js 20.19 or newer to build the frontend; Node.js 24 is installed when neither
  Node nor npm is present.
- Olaf CLI, tested with `2026.10.19`, available at the path in `config.yaml`
  (default `/usr/local/bin/olaf`).
- `ffprobe` at the path in `config.yaml` (default `/usr/bin/ffprobe`).
- S3-compatible storage. Cloudflare R2, MinIO, and AWS S3 are supported through the
  AWS SDK.
- `ffmpeg` is generally required by Olaf and by generated test audio.

The installer checks for every dependency and installs only what is missing:
`ffmpeg`/`ffprobe`, CA certificates, timezone data, `curl`, `tar`, `xz-utils`, Go,
Node.js, and Olaf. Existing tools are left in place and validated. Olaf is installed
from the pinned `v3.3.3` Linux release with SHA-256 verification.

Check the dependencies:

```bash
olaf --version
ffprobe -version
go version
node --version
```

`olaf --version` prints the version and command list. The service rejects startup if
the Olaf version cannot be determined or if the configured binaries are missing.

---

## 3. Build

From the `indexer/` directory:

```bash
./scripts/build.sh
```

This installs frontend dependencies, builds the Vite bundle into `web/dist`, embeds
the bundle in the Go binary, and writes `dist/indexer`.

Useful variants:

```bash
./scripts/build.sh --skip-web      # reuse an existing web/dist
VERSION=1.2.3 ./scripts/build.sh   # set the reported version
go test ./...
go vet ./...
```

Run the binary:

```bash
./dist/indexer version
```

For a local run, copy `config.yaml` to a writable location and adjust
`database.path`, `database.workspace`, and `logging.file` first:

```bash
cp config.yaml /tmp/indexer-config.yaml
# edit /tmp/indexer-config.yaml
./dist/indexer serve --config /tmp/indexer-config.yaml --env ./.env
```

The frontend is available at `http://127.0.0.1:8788/` by default.

### Frontend development

```bash
cd web
npm install
npm run dev
```

Vite serves the UI on port `5174` and proxies `/api`, `/openapi.json`, and `/docs` to
`127.0.0.1:8788`.

---

## 4. Configuration

Deployment settings are split between a YAML file and an environment file.

### 4.1 `.env`

The environment file is the source of truth for S3/R2 storage. For an installed
service it is `/etc/indexer/.env`. For local runs it is the file passed with `--env`
or `./.env` beside `config.yaml`.

```env
# Admin API authentication. Leave empty only behind an authenticating proxy.
INDEXER_ADMIN_TOKEN=

# Shared token for recorder completion events.
INDEXER_EVENT_TOKEN=

# Source storage.
S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
S3_REGION=auto
S3_BUCKET=spaza-recordings
S3_ACCESS_KEY_ID=...
S3_SECRET_ACCESS_KEY=...
S3_PREFIX=
S3_PATH_STYLE=false

# Optional destination overrides. Empty values fall back to the source values.
S3_DEST_ENDPOINT=
S3_DEST_REGION=
S3_DEST_BUCKET=
S3_DEST_ACCESS_KEY_ID=
S3_DEST_SECRET_ACCESS_KEY=
S3_DEST_PREFIX=
S3_DEST_PATH_STYLE=
```

Behavior details:

- When `S3_BUCKET` is set, startup creates or updates the `source` and `destination`
  storage records in SQLite. They are overwritten from `.env` on every restart.
- When `S3_BUCKET` is empty, existing storage records are left alone and can be
  managed through the UI or `/api/storage`.
- Source and destination may use the same bucket or different buckets, endpoints,
  credentials, regions, prefixes, and path-style settings.
- For Cloudflare R2 use the S3 endpoint
  `https://<account-id>.r2.cloudflarestorage.com` and region `auto`. Set
  `S3_PATH_STYLE=true` when your R2 or proxy setup requires path-style addressing.
- Credentials are stored in SQLite but are never returned by the API. The API masks
  access keys and omits secret keys.

### 4.2 `config.yaml`

```yaml
server:
  listen: "127.0.0.1:8788"
  admin_token: "${INDEXER_ADMIN_TOKEN}"
  event_token: "${INDEXER_EVENT_TOKEN}"
  shutdown_grace_seconds: 30

database:
  path: "/var/lib/indexer/indexer.db"
  workspace: "/var/lib/indexer/jobs"

olaf:
  binary: "/usr/local/bin/olaf"
  threads: 4
  timeout_minutes: 240

ffprobe:
  binary: "/usr/bin/ffprobe"
  timeout_seconds: 60

workers:
  max_active_jobs: 1
  downloads_per_job: 4
  uploads_per_job: 4
  job_timeout_minutes: 360
  retry_initial_seconds: 60
  retry_max_seconds: 3600
  max_attempts: 3

default_timezone: "Africa/Nairobi"

logging:
  level: "info"
  file: "/var/log/indexer/indexer.log"
```

`${VAR}` placeholders are expanded from the process environment and the loaded
`.env`. The default config path is `/etc/indexer/config.yaml`; the default env path is
`.env` beside that config.

### 4.3 Runtime settings

Worker and cleanup behavior can be changed without a restart from the UI under
**Settings**, or through `GET/PUT /api/settings`:

```json
{
  "max_active_jobs": 1,
  "retry_initial_seconds": 60,
  "retry_max_seconds": 3600,
  "max_attempts": 3,
  "keep_failed_workspaces": true,
  "min_free_disk_gb": 1,
  "builds_retained": 3
}
```

These values are stored in SQLite and apply to newly claimed jobs. Discovery is also
runtime configuration: it is persisted in SQLite and applied on the next scan without
a service restart.

---

## 5. Installation

The installer installs all missing dependencies, creates a dedicated `indexer` system
user, and installs the binary, configuration, environment template, directories, and
systemd unit. If `dist/indexer` is missing, it builds both the frontend and backend
automatically.

```bash
sudo ./scripts/install.sh
```

To force a fresh frontend and backend build:

```bash
sudo ./scripts/install.sh --build
```

To install an already-built binary without building:

```bash
sudo ./scripts/install.sh --binary ./dist/indexer
```

Options:

```text
--build              Build the frontend and backend before installing
--binary PATH        Install a specific binary (default: dist/indexer or a fresh build)
--olaf-version TAG   Olaf release to install when missing (default: v3.3.3)
--olaf-binary PATH   Install a specific Olaf binary instead of downloading a release
--prefix PATH        Install directory (default: /opt/indexer)
--config-dir PATH    Configuration directory (default: /etc/indexer)
--state-dir PATH     State and database directory (default: /var/lib/indexer)
--log-dir PATH       Log directory (default: /var/log/indexer)
--user NAME          Service user (default: indexer)
--no-start           Install and enable but do not start
```

Installed paths:

```text
/opt/indexer/indexer                    service binary
/etc/indexer/config.yaml                deployment config
/etc/indexer/.env                       secrets and S3/R2 config
/etc/systemd/system/indexer.service     systemd unit
/var/lib/indexer/indexer.db             SQLite database
/var/lib/indexer/jobs/{job_id}/         job workspaces
/var/lib/indexer/backups/               previous binaries from updates
/var/log/indexer/indexer.log            application log
```

The installer preserves an existing `config.yaml` and `.env`.

After installation, configure storage and start the service:

```bash
sudoedit /etc/indexer/.env
sudo systemctl restart indexer
sudo systemctl status indexer
journalctl -u indexer -f
```

The service runs as the unprivileged `indexer` user with systemd hardening:
read-only system paths, a private temporary directory, no new privileges, and write
access only to the state and log directories.

---

## 6. Updating

```bash
sudo ./scripts/update.sh --build
```

The updater installs the new binary atomically, restarts the service, checks that it
is active, and restores the previous binary if startup fails. Configuration and data
are preserved. Backups are kept in `/var/lib/indexer/backups`; the five most recent
are retained.

Options:

```text
--build              Build before updating
--binary PATH        Install a specific binary
--prefix PATH        Install directory (default: /opt/indexer)
--state-dir PATH     Backup directory base (default: /var/lib/indexer)
--no-restart         Replace the binary without restarting
```

Always inspect the service after an update:

```bash
systemctl status indexer
journalctl -u indexer -n 100 --no-pager
curl -fsS http://127.0.0.1:8788/api/health
curl -fsS http://127.0.0.1:8788/api/health/ready
```

---

## 7. Usage

### 7.1 Web UI

Open `http://127.0.0.1:8788/` or the configured listen address. If
`INDEXER_ADMIN_TOKEN` is set, enter it under **Settings**.

Tabs:

- **Overview**: job counts, active workers, storage readiness, discovery summary,
  disk usage, and recent jobs.
- **Jobs**: filter jobs by status or stream, create a manual job, and open the job
  drawer.
- **Discovery**: edit scan scheduling and readiness rules, run a scan immediately,
  and review scan history.
- **Logs**: search job logs and service logs by level, job, and message text.
- **API**: links to `/docs` and `/openapi.json`.
- **Settings**: admin token, runtime worker settings, and storage records.

The job drawer has three sections:

- **Overview**: status, stage, progress, recording counts, errors, attempts, and
  timeline summary.
- **Timeline**: recording and gap rows in chronological order.
- **Logs**: per-job processing logs.

### 7.2 Create a job

From the UI: **Jobs → New job**, then enter the stream ID and date.

From the API:

```bash
curl -X POST http://127.0.0.1:8788/api/jobs \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $INDEXER_ADMIN_TOKEN" \
  -d '{"stream_id":"capital-fm-ke","date":"2026-11-14","force":false}'
```

`force=false` returns an existing active or completed job instead of duplicating it.
`force=true` creates a rebuild job but never runs two builds for the same stream/date
at once. The replacement is published only after successful validation.

Job lifecycle:

```text
queued -> downloading -> caching -> storing -> validating -> uploading -> completed
                                                             -> failed
                                                             -> cancelled
```

### 7.3 Recorder completion events

If `INDEXER_EVENT_TOKEN` is set, send it in `X-Event-Token` (or as a bearer token):

```bash
curl -X POST http://127.0.0.1:8788/api/events/recording-complete \
  -H 'Content-Type: application/json' \
  -H "X-Event-Token: $INDEXER_EVENT_TOKEN" \
  -d '{"event_id":"capital-fm-ke:2026-11-14","stream_id":"capital-fm-ke","date":"2026-11-14"}'
```

The event is stored idempotently. The service verifies that recordings exist, then
creates a job. If a discovery scan is running, the endpoint returns `409`; retry the
event shortly.

### 7.4 Discovery

Discovery finds eligible stream/date pairs and feeds the same job queue. Configure it
under **Discovery** or with `PUT /api/discovery/config`.

Important fields:

- `source_storage`: storage record name, normally `source`.
- `stream_scope`: `all` or `selected`.
- `date_scope`: `previous_days` or `explicit`. The current local date is never
  auto-completed.
- `readiness.strategy`:
  - `grace_period`: the newest recording must be older than the grace period.
  - `completion_event`: a recorder event must exist for the stream/date.
- `minimum_recordings` and `minimum_duration_seconds`.
- `automatic_enqueue`, `dry_run`, and `max_jobs_per_scan`.

A scan skips dates with an existing active job or a completed remote index. Every scan
is recorded in `discovery_runs`, including errors.

Run a scan immediately:

```bash
curl -X POST http://127.0.0.1:8788/api/discovery/run \
  -H "Authorization: Bearer $INDEXER_ADMIN_TOKEN"
```

### 7.5 REST API

Common endpoints:

| Method | Endpoint | Purpose |
|---|---|---|
| GET | `/api/health` | Process health |
| GET | `/api/health/ready` | Database and storage configuration readiness |
| GET | `/api/stats` | Job, discovery, disk, and version statistics |
| GET/POST | `/api/jobs` | List or create jobs |
| GET | `/api/jobs/{id}` | Job details and attempts |
| POST | `/api/jobs/{id}/retry` | Retry a failed or cancelled job |
| POST | `/api/jobs/{id}/cancel` | Cancel or request cancellation |
| GET | `/api/jobs/{id}/timeline` | Paginated timeline detail |
| GET | `/api/jobs/{id}/timeline/summary` | Lightweight timeline summary |
| GET/PUT | `/api/discovery/config` | Read or update discovery config |
| POST | `/api/discovery/run` | Run a scan now |
| GET | `/api/discovery/runs` | Scan history |
| GET | `/api/discovery/runs/{id}` | Scan detail |
| POST | `/api/events/recording-complete` | Recorder event ingestion |
| GET | `/api/logs` | Job and service logs |
| GET/PUT | `/api/settings` | Runtime worker settings |
| GET/POST | `/api/storage` | List or create storage records |
| PUT/DELETE | `/api/storage/{id}` | Update or delete a storage record |
| POST | `/api/storage/{id}/test` | Test bucket connectivity |

Swagger UI is at `/docs`; the OpenAPI document is `/openapi.json`.

Admin authentication applies to all `/api/*` routes except health and the recorder
event route. Health and readiness are always unauthenticated.

### 7.6 Remote layout

```text
{stream}/meta.json
{stream}/recordings/YYYY/MM/DD/HHMMSS.{ext}
{stream}/indexes/YYYY/MM/DD/meta.json
{stream}/indexes/YYYY/MM/DD/builds/{build_id}/data.mdb
{stream}/indexes/YYYY/MM/DD/builds/{build_id}/lock.mdb
{stream}/indexes/YYYY/MM/DD/builds/{build_id}/timeline.json
```

Supported audio extensions: `.ogg`, `.opus`, `.mp3`, `.m4a`, `.aac`, `.wav`, `.flac`,
`.mp4`, `.mkv`, `.webm`, `.wv`, `.ape`, `.wma`, `.raw`.

The published `meta.json` contains the build ID and prefix, recording counts, timeline
checksum, Olaf version, and fingerprint configuration. The timeline checksum is
verified against the uploaded file before publication completes.

### 7.7 Local workspaces

Each job runs in `/var/lib/indexer/jobs/{job_id}` with separate `audio`, `cache`,
`index`, and `olaf-home` directories. Olaf cache and LMDB databases are never shared
between jobs.

Successful jobs remove their workspaces after publication. Failed jobs retain the
workspace by default for diagnosis (`keep_failed_workspaces`). Downloaded source
objects are never modified or deleted.

---

## 8. Operations

### Health checks

```bash
curl -fsS http://127.0.0.1:8788/api/health
curl -fsS http://127.0.0.1:8788/api/health/ready
```

`/api/health/ready` checks SQLite and the presence of source/destination storage
configuration. It does not perform a remote S3 request.

### Logs

```bash
journalctl -u indexer -f
tail -f /var/log/indexer/indexer.log
```

Per-job logs are also stored in SQLite and visible under **Logs** or
`GET /api/logs?job_id={id}`.

### Backups

The important state is:

- `/var/lib/indexer/indexer.db`
- Published indexes and source recordings in object storage

SQLite uses WAL mode. Back up the database with SQLite's backup mechanism or while
the service is stopped:

```bash
sudo systemctl stop indexer
sudo cp /var/lib/indexer/indexer.db /var/backups/indexer-$(date +%F).db
sudo systemctl start indexer
```

### Disk usage

The service checks free space before downloading a job and rejects the job when the
configured reserve is insufficient. Failed workspaces can consume significant space
when `keep_failed_workspaces` is enabled.

---

## 9. Troubleshooting

### `required executable not available`

The path in `olaf.binary` or `ffprobe.binary` does not exist. Update `config.yaml` or
install the binary.

### `could not determine Olaf version`

Run:

```bash
olaf --version
```

If that fails, check the Olaf installation and the configured path.

### Readiness returns `503`

`/api/health/ready` requires SQLite plus configured source and destination storage.
Set `S3_BUCKET` and credentials in `/etc/indexer/.env`, then restart the service.

### Jobs fail with `no recordings found`

Check:

- The S3 bucket, prefix, and credentials.
- The stream ID.
- The date folder convention `{stream}/recordings/YYYY/MM/DD/`.
- The stream timezone in `{stream}/meta.json`.
- That filenames use `HHMMSS.ext`.

### Jobs fail with `insufficient disk space`

Increase storage under `/var/lib/indexer`, or lower `min_free_disk_gb` in runtime
settings. Large daily jobs download every chunk before indexing.

### Discovery skips a date

Common reasons:

- The grace period has not elapsed.
- `completion_event` readiness is configured but no event exists.
- The number of recordings or total duration is below the minimum.
- A completed index already exists.
- An active job exists for that stream/date.
- The scan is a dry run.

### Admin API returns `401`

Set the same token in the UI under **Settings** or send it as
`Authorization: Bearer <token>`. Tokens are configured in `/etc/indexer/.env` and
expanded into `config.yaml`.

### Update rollback

`scripts/update.sh` restores the previous binary when the service does not become
active. Inspect:

```bash
journalctl -u indexer -n 200 --no-pager
ls -1t /var/lib/indexer/backups
```

---

## 10. Security notes

- Bind to `127.0.0.1` and place the service behind a TLS reverse proxy when exposing
  the UI publicly.
- Set both `INDEXER_ADMIN_TOKEN` and `INDEXER_EVENT_TOKEN` outside trusted networks.
- Storage credentials are stored in SQLite but are not returned by the API. The
  database and `/etc/indexer/.env` are readable only by root and the service group.
- Olaf and ffprobe run as child processes with argument vectors only; no shell
  interpolation is used.
- The systemd unit uses `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, and
  a private temporary directory.

---

## 11. Uninstall

This removes the binary, configuration, database, logs, and workspaces:

```bash
sudo systemctl disable --now indexer
sudo rm -f /etc/systemd/system/indexer.service
sudo systemctl daemon-reload
sudo rm -rf /opt/indexer /etc/indexer /var/lib/indexer /var/log/indexer
sudo userdel indexer
```

Source recordings and published indexes remain in object storage.

---

## 12. Tests

```bash
go test ./...
go vet ./...
./scripts/build.sh
```

Unit tests cover recording key parsing, timezone-aware timeline construction, gaps
and overlaps, failed recordings, resource IDs, discovery configuration validation,
runtime settings validation, and environment expansion.

Remaining integration checks are tracked in `docs/task-list.md`.
