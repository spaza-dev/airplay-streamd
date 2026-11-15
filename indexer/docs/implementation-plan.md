# Olaf Daily Indexer — Implementation Plan

**Service:** `indexer`
**Source spec:** `../../docs/indexer.md`
**Status:** Implementation complete; integration verification pending
**Updated:** 2026-11-15

## 1. Goal

Build a single Go service that creates one validated daily Olaf fingerprint index per
stream and date from timestamped recordings in S3-compatible storage, together with an
authoritative chronological stream timeline. Jobs may be created manually, through the
API, by recorder notifications, or by automatic discovery.

## 2. Confirmed decisions

- New sibling directory `indexer/`, Go module `indexer`.
- Go 1.27.4 from `~/.local/go`; Node 24 via nvm for frontend builds.
- SQLite driver: `modernc.org/sqlite`.
- Frontend: React 16.14, TypeScript, Vite, `azure-devops-ui` 2.282.0.
- Stream timezone is read from `{stream_id}/meta.json` at `metadata.timezone`. If it is
  absent or invalid, `default_timezone` from deployment configuration is used.
- Forced rebuilds publish immutable `builds/{build_id}/` artifacts. The date-level
  `meta.json` is the completion pointer and is always uploaded last. It includes
  bucket-relative paths for the metadata, build directory, and every build artifact.
- S3/R2 source and destination settings are configured in `.env` (`S3_*`, with optional
  `S3_DEST_*` overrides) and applied to SQLite on every startup. The UI/API can still
  inspect them; they are overwritten at restart when `S3_BUCKET` is set.

## 3. Verified Olaf behaviour

Installed version: **2026.10.19**.

- Configuration is loaded from `$HOME/.olaf/olaf_config.json`, then from
  `olaf_config.json` beside the executable.
- Each job gets its own `HOME`, database folder, and cache folder.
- `olaf cache --with-ids` accepts `audio_file identifier` pairs.
- `olaf store_cached` indexes every cache entry in the configured cache folder.
- Query results contain the resource identifier in `path` and per-resource offsets in
  `reference_start` / `reference_stop` seconds.
- Absolute occurrence time: `recording_start + reference_start`.
- `olaf stats --verbose` is used to cross-check the indexed resource count.

## 4. Architecture

```text
Web UI / REST / recorder event / scheduler
                    |
                    v
              SQLite job queue
                    |
                    v
          bounded worker pool
                    |
                    v
 daily job: list -> download -> ffprobe -> timeline -> Olaf -> validate -> upload
              |                                             |
              v                                             v
         source S3                                  destination S3
```

All job initiation paths use the same creation and deduplication logic.

## 5. Project layout

```text
indexer/
├── main.go          lifecycle and startup/shutdown
├── config.go        deployment configuration and validation
├── database.go      SQLite initialization, migrations, queries
├── jobs.go          queue, deduplication, workers, retry, cancellation
├── timeline.go      inventory, durations, gaps, overlaps, mapping
├── storage.go       source/destination S3 clients
├── olaf.go          isolated Olaf workspace and subprocess execution
├── discovery.go     scheduled scans, readiness, recorder events
├── api.go           REST API, OpenAPI, Swagger UI, embedded web assets
├── migrations/      embedded SQL migrations
├── web/             React + TypeScript + azure-devops-ui
├── scripts/         build.sh, install.sh, update.sh, indexer.service
├── config.yaml      deployment configuration template
└── .env.example     secrets template
```

Runtime paths:

```text
/var/lib/indexer/
├── indexer.db
└── jobs/{job_id}/
    ├── audio/
    ├── cache/
    ├── index/
    ├── olaf-home/.olaf/olaf_config.json
    ├── timeline.json
    └── manifest.json
```

## 6. Object layout

Source:

```text
{stream_id}/meta.json
{stream_id}/recordings/YYYY/MM/DD/HHMMSS.{ext}
```

Destination:

```text
{stream_id}/indexes/YYYY/MM/DD/meta.json
{stream_id}/indexes/YYYY/MM/DD/builds/{build_id}/data.mdb
{stream_id}/indexes/YYYY/MM/DD/builds/{build_id}/lock.mdb
{stream_id}/indexes/YYYY/MM/DD/builds/{build_id}/timeline.json
```

Publication order:

1. Upload LMDB files and `timeline.json` to the immutable build prefix.
2. Verify uploaded objects exist and have expected sizes.
3. Upload the date-level `meta.json` last.
4. Mark the job completed only after the pointer upload succeeds.

A failed forced rebuild leaves the previous `meta.json` and build untouched.

## 7. Timeline rules

- The job date is the local calendar date of each recording's filename timestamp.
- `timeline_origin` is local midnight converted to UTC.
- Timeline offsets are wall-clock offsets from the origin, so gaps and overlaps are
  preserved rather than compressed.
- Actual duration comes from `ffprobe`, never from the filename.
- Gaps cover uncovered intervals inside the daily window (including leading and
  trailing gaps) and are recorded explicitly.
- Overlapping recordings are both retained and indexed.
- A recording crossing midnight stays with the job matching its start date and keeps
  its full interval.
- Publish is refused if any discovered recording is missing a valid timeline entry or
  Olaf mapping.

## 8. Job lifecycle

```text
queued -> downloading -> caching -> storing -> validating -> uploading -> completed
                                                       \-> failed / cancelled
```

- One active job per stream/date is enforced by a partial unique index.
- `force=false` returns an existing active or completed job.
- `force=true` creates a rebuild job without interrupting an active build.
- Attempts and logs are retained across retries.
- Running jobs are cancelled through a context and process-group termination.

## 9. Discovery

Configuration is stored as JSON in `discovery_config` and applied without restart.
Supported readiness strategies:

- `grace_period`: latest recording object must be older than the configured grace.
- `completion_event`: a recorder event exists for the stream/date and objects are
  present.

Scans are idempotent, serialized, respect dry-run and maximum jobs per scan, skip
existing completed indexes and active jobs, and record results in `discovery_runs`.

## 10. API and UI

API routes follow `docs/indexer.md` section 13. Request/response JSON is consistent.
Storage secrets are write-only through the API. Admin routes can be protected with a
bearer token; recorder events use a separate token.

The UI uses horizontal tabs only: Overview, Jobs, Discovery, Logs, API, Settings.
Jobs use a compact table and a right-side drawer with Overview, Timeline, and Logs.
Gaps appear as explicit rows in the timeline table.

## 11. Reliability and security

- Bounded job/download/Olaf/upload concurrency.
- Exponential retry with configurable maximum attempts.
- Startup recovery of interrupted jobs.
- Disk-space checks and partial-file cleanup.
- Safe subprocess argument vectors only; no shell interpolation.
- ID, date, timezone, object-key, and path validation.
- Credentials never returned by the API and never logged.
- Graceful shutdown with a configurable active-job grace period.

## 12. Deployment

`scripts/build.sh` builds the frontend and Go binary using the local Go 1.27.4 and
Node 24. `scripts/install.sh` installs dependencies, service user, directories, binary,
configuration, and systemd units while preserving existing state. `scripts/update.sh`
performs a health-checked graceful replacement with rollback.

Installed paths:

```text
/opt/indexer/indexer
/etc/indexer/config.yaml
/etc/indexer/.env
/var/lib/indexer/
/var/log/indexer/
```
