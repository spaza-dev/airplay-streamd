# queryd implementation plan

## Architecture

Single Go binary embedding the built React UI and OpenAPI document.

```
HTTP API / UI
     |
 JobManager (SQLite queue, bounded workers)
     |
 Planner -> S3 archive -> job_indexes
     |
 Downloader (parallel, disk/size guards)
     |
 Olaf runner (one index at a time, isolated HOME)
     |
 Timeline mapper + time-window filter
     |
 Matches + callback deliveries
     |
 Cleanup + retention
```

## Components

### Configuration and startup

- `config.go`: YAML plus `${ENV}` expansion and `.env` loading.
- `main.go`: flags, logger, S3 client, DB, recovery of interrupted jobs, HTTP server,
  signal handling, background job manager and retention loop.
- Default listen address `127.0.0.1:8790`.

### Persistence

SQLite migrations under `migrations/`. Tables:

- `jobs`: lifecycle, counters, scope, callback URL, retry and pause/cancel flags.
- `job_files`: original name, stored name, path, size.
- `job_indexes`: stream/date, build and object keys, status, bytes, matches.
- `matches`: mapped archive matches.
- `job_logs`: structured job logs.
- `callback_deliveries`: callback attempts and responses.
- `schema_migrations`.

Writer methods serialize through a mutex; reads use the same SQLite connection with WAL.

### Storage

`storage.go` mirrors the indexer S3 client:

- endpoint, region, bucket, static credentials, prefix, path style;
- list common prefixes, get bytes, download to file, head;
- all keys are relative to `S3_PREFIX`.

### Planning

`scope.go` validates the scope. `archive.go`:

- expands streams (`all`, `ids`, `country` via `<stream>/meta.json`);
- enumerates dates `from-1 .. to`;
- reads `<stream>/indexes/YYYY/MM/DD/meta.json`;
- accepts only `status: complete` with a build prefix;
- inserts idempotent `job_indexes` rows;
- enforces the index cap.

### Processing

`jobs.go` implements the queue and stages:

1. Write workspace, emit `job.started`.
2. Plan indexes.
3. Check free disk and sum index sizes.
4. Download `data.mdb`, optional `lock.mdb`, and `timeline.json` in parallel.
5. For each index, write an isolated Olaf config and HOME, then run
   `olaf query --threads N --format json <files>`.
6. Parse JSON, map resource IDs through `timeline.json`, apply date and daily-window
   filters, persist matches, emit `job.matches` and `job.progress`.
7. Mark complete, clean temporary artifacts, emit terminal callback.

Retries set failed indexes back to pending while keeping completed indexes. Pause keeps
downloads; cancel cleans up.

### Callbacks

`callbacks.go` signs and posts events with a bounded timeout. Delivery results are stored.
Callback errors are logged but do not fail jobs.

### API

`api.go` provides:

- `POST /api/jobs` (multipart: `files`, `scope`, optional `callback_url`)
- `GET /api/jobs`
- `GET /api/jobs/{id}`
- `GET /api/jobs/{id}/matches`
- `GET /api/jobs/{id}/logs`
- `GET /api/jobs/logs` (global recent logs)
- `POST /api/jobs/{id}/pause|resume|cancel`
- `GET /api/stats`
- `GET /api/health`, `/api/health/ready`
- `GET /openapi.json`, `/docs`
- embedded SPA

Admin bearer token applies to all `/api/*` paths except health.

### UI

React 16 + TypeScript + Vite + Azure DevOps UI, embedded into the binary. Tabs:

- Overview: job counters, service state, disk, recent jobs.
- New Query: file picker, scope builder, callback URL, submit.
- Jobs: filter, list, open job panel.
- Logs: job selector and log table.
- API: OpenAPI link and endpoint summary.
- Settings: admin token stored locally, readiness and runtime limits.

Job panel tabs: Summary, Progress, Matches, Logs, Callback deliveries.

### Scripts

- `scripts/build.sh`: frontend and static Go build.
- `scripts/install.sh`: dependency checks/install, build if needed, user/dirs/config/env,
  systemd unit.
- `scripts/update.sh`: build, backup, replace, restart, rollback on failure.
- `scripts/queryd.service`: hardened unit template.

## Testing

- Unit tests for scope validation, time-window matching, timeline mapping, and Olaf JSON
  parsing.
- `go test ./...` and `go vet ./...`.
- Frontend `npm run build` (TypeScript check plus Vite build).
