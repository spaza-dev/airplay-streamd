# Olaf Daily Indexer
## Technical Design and Implementation Specification

**Project:** Spaza Airplay – Olaf Daily Indexer  
**Purpose:** Implementation guide for coding agents  
**Backend:** Go  
**Database:** SQLite  
**Fingerprinting:** Olaf CLI  
**Index format:** LMDB  
**Storage:** S3-compatible object storage  
**Frontend:** React + TypeScript + `azure-devops-ui`

---

# 1. Project Overview

Build a lightweight service that creates daily Olaf fingerprint indexes from timestamped recordings stored in S3-compatible object storage.

The application must:

1. Discover recordings belonging to a stream and date.
2. Create one indexing job per stream/date.
3. Build a chronological stream timeline from the recording chunks.
4. Download and validate the recordings.
5. Generate Olaf fingerprints.
6. Store the fingerprints in one daily LMDB database.
7. Preserve the mapping between recording timestamps, logical timeline positions, and Olaf reference offsets.
8. Upload the completed daily index and its metadata.
9. Maintain job history, logs, statistics, and operational status.
10. Support manual, API, recorder-triggered, and automatically discovered jobs.
11. Allow automatic discovery to be configured through the web interface.

The application is a purpose-built indexing service, not a general workflow platform.

## 1.1 Core principles

- One job represents one stream and one calendar date.
- One daily job produces one daily LMDB index.
- All recording chunks for that date belong to one logical chronological stream.
- The stream timeline is authoritative for reconstructing broadcast timestamps.
- Missing intervals must be represented explicitly.
- Do not silently concatenate non-adjacent recordings.
- Use one isolated Olaf workspace per daily job.
- Use SQLite for durable job management.
- Use bounded worker concurrency.
- Never modify or delete source recordings.
- Support independent source and destination storage configurations.
- Publish indexes only after successful validation.

## 1.2 Out of scope

The application does not:

- Record live streams.
- Manage BMAS nodes.
- Maintain music catalogs.
- Execute recognition queries against completed indexes.
- Manage royalty or usage reporting.
- Delete source recordings.
- Provide the downstream query service.

Its responsibility ends when a complete, validated daily index is published.

---

# 2. Technology Stack

| Component | Technology |
|---|---|
| Backend | Go |
| HTTP server | Go standard library or lightweight router |
| Database | SQLite |
| Fingerprinting | Olaf CLI |
| Index storage | LMDB |
| Object storage | S3-compatible API |
| Frontend | React + TypeScript |
| UI components | `azure-devops-ui` |
| API documentation | OpenAPI 3.x + Swagger UI |
| Deployment | Linux systemd service |

Keep dependencies and source files to a minimum.

Do not introduce Kubernetes, Redis, Kafka, RabbitMQ, or a separate microservice architecture.

---

# 3. Architecture

The application is a single deployable service.

```text
                  WEB BROWSER
                       |
                       v
             +----------------------+
             |      Web UI          |
             |   azure-devops-ui    |
             +----------------------+
                       |
                       v
             +----------------------+
             |       Go API         |
             |                      |
             | Jobs                 |
             | Discovery            |
             | Timeline details     |
             | Storage settings     |
             | Stats and logs       |
             +----------------------+
                       |
                       v
             +----------------------+
             |       SQLite         |
             |                      |
             | Jobs                 |
             | Job attempts         |
             | Discovery config     |
             | Discovery runs       |
             | Logs                 |
             +----------------------+
                       |
                       v
             +----------------------+
             |   Job Scheduler      |
             |                      |
             | Manual/API events    |
             | Recorder events      |
             | Automatic discovery  |
             +----------------------+
                       |
                       v
             +----------------------+
             |  Bounded Workers     |
             +----------------------+
                       |
                       v
             +----------------------+
             |   Daily Job          |
             |                      |
             | Discover recordings  |
             | Build timeline       |
             | Download chunks      |
             | Olaf cache           |
             | Store fingerprints   |
             | Validate index       |
             | Upload artifacts     |
             +----------------------+
                  |           |
                  v           v
             Source S3     Olaf / LMDB
                               |
                               v
                         Destination S3
```

All job initiation mechanisms must feed the same SQLite job queue.

The discovery engine must never execute Olaf directly.

---

# 4. Object Storage

## 4.1 Source recordings

The source bucket uses this convention:

```text
spaza-recordings/
└── {stream_id}/
    ├── meta.json
    ├── recordings/
    │   └── YYYY/
    │       └── MM/
    │           └── DD/
    │               ├── 000000.ogg
    │               ├── 000500.ogg
    │               ├── 001000.ogg
    │               └── ...
    └── transcripts/
```

Recording object key:

```text
{stream_id}/recordings/YYYY/MM/DD/HHMMSS.{extension}
```

All source types may use chunked recordings, including radio, television, YouTube, Twitch, podcasts, and other sources.

Timestamps are derived from the filenames and interpreted using the stream's configured timezone.

## 4.2 Destination indexes

Indexes are stored alongside recordings:

```text
spaza-recordings/
└── {stream_id}/
    ├── meta.json
    ├── recordings/
    │   └── YYYY/MM/DD/
    │       └── ...
    ├── indexes/
    │   └── YYYY/MM/DD/
    │       ├── data.mdb
    │       ├── lock.mdb
    │       └── meta.json
    └── transcripts/
```

The destination may be the same bucket as the source or a different bucket.

Source and destination must have independently configurable:

- Endpoint.
- Credentials.
- Bucket.
- Region.
- Prefix.
- Path-style setting.

Do not assume shared credentials or storage infrastructure.

## 4.3 Publication

Publish in this order:

1. Upload the LMDB database files.
2. Verify successful uploads.
3. Upload timeline and required supporting metadata.
4. Upload `meta.json` last.
5. Set `status` to `complete`.
6. Mark the job completed in SQLite.

A downstream reader must not consider an index complete until it finds valid completion metadata.

Do not rely on object-store directory rename operations.

---

# 5. Job Model

## 5.1 Definition

One job represents:

**One stream ID + one date.**

Example:

```json
{
  "stream_id": "capital-fm-ke",
  "date": "2026-11-14",
  "force": false
}
```

All recording chunks belonging to this stream/date are processed as one logical daily stream.

The job produces one daily LMDB index and one authoritative stream timeline.

## 5.2 Initiation sources

| Source | Description |
|---|---|
| Manual | Web interface |
| API | REST API |
| Recorder | Recording-complete notification |
| Scheduler | Automatic discovery |

Store the initiation source for audit purposes.

## 5.3 Job lifecycle

```text
queued
  |
  v
downloading
  |
  v
caching
  |
  v
storing
  |
  v
validating
  |
  v
uploading
  |
  v
completed
```

Additional terminal states:

- `failed`
- `cancelled`

Persist the current stage and progress in SQLite.

## 5.4 Deduplication

Prevent two active jobs for the same stream/date.

When `force=false`:

- Return an existing active job.
- Return or reference the completed job if its index already exists.
- Do not rebuild an existing completed index unnecessarily.

When `force=true`:

- Create an explicit rebuild request.
- Preserve previous job history.
- Do not run two builds for the same stream/date simultaneously.
- Publish the replacement only after successful validation.

A failed forced rebuild must not invalidate an existing completed index.

## 5.5 Retry and cancellation

Preserve attempt history, errors, and logs.

Queued jobs can be cancelled immediately.

Running jobs should terminate their child processes safely, stop further work, clean up temporary files, and persist their cancelled state.

Never delete source recordings or a previously completed index during cancellation.

---

# 6. Automatic Job Discovery

Automatic discovery identifies recording dates that are eligible for indexing and submits them to the shared job queue.

## 6.1 Initiation mechanisms

Support:

1. Manual web job creation.
2. REST API job creation.
3. Recorder completion notifications.
4. Scheduled automatic discovery.

All four mechanisms must use the same job creation and deduplication logic.

## 6.2 Discovery configuration

The Discovery page must allow operators to configure:

| Setting | Purpose |
|---|---|
| Enabled | Enable or disable automatic discovery |
| Scan interval | Frequency of discovery |
| Source storage | Storage connection to scan |
| Stream scope | All streams or selected streams |
| Date scope | Which recording dates to examine |
| Lookback period | Number of previous days to inspect |
| Readiness strategy | How recording completeness is determined |
| Grace period | Time since last recording arrival |
| Minimum recordings | Optional minimum chunk count |
| Minimum duration | Optional minimum accumulated audio duration |
| Maximum jobs per scan | Limit enqueueing |
| Automatic enqueue | Whether eligible dates become jobs |
| Dry run | Discover without creating jobs |

## 6.3 Readiness strategies

Support:

**Completion event:** A recorder notifies the indexer that a stream/date is complete. The indexer still verifies that the expected objects are present.

**Grace period:** The scanner checks the latest recording object's last-modified time and considers the date ready after the configured period without new recordings.

The current date must not automatically be treated as complete.

## 6.4 Discovery processing

For each scan:

1. Read the enabled discovery configuration.
2. List stream/date recording prefixes.
3. Identify eligible dates.
4. Check the configured readiness conditions.
5. Check whether a completed index already exists.
6. Check whether a job is already queued or running.
7. Enqueue eligible jobs.
8. Record scan results.

Discovery must be idempotent.

Only one discovery scan may run at a time.

Configuration changes must be persisted immediately and applied without restarting the application.

## 6.5 Discovery configuration example

```json
{
  "enabled": true,
  "scan_interval_minutes": 30,
  "source_storage": "source",
  "stream_scope": "all",
  "selected_streams": [],
  "date_scope": "previous_days",
  "lookback_days": 7,
  "readiness": {
    "strategy": "grace_period",
    "grace_period_minutes": 30,
    "minimum_recordings": 1,
    "minimum_duration_seconds": 0
  },
  "automatic_enqueue": true,
  "max_jobs_per_scan": 100,
  "dry_run": false
}
```

This is the application discovery configuration, not an Olaf configuration file.

---

# 7. Stream Timeline

## 7.1 Purpose

The stream timeline is a core artifact of every daily indexing job.

It describes how the original timestamped recording chunks fit together chronologically to represent the stream during the indexed date.

It must preserve:

- Original recording start timestamps.
- Actual recording durations.
- Chronological order.
- Logical timeline positions.
- Missing recording intervals.
- Overlapping recording intervals.
- Mapping to Olaf reference resources and offsets.

The timeline enables the query service to translate an Olaf match into the original broadcast occurrence timestamp.

It is not merely a list of recording filenames.

## 7.2 One logical stream per daily job

All chunks for a stream/date belong to one logical daily stream.

The indexing model is:

```text
Stream: capital-fm-ke
Date:   2026-11-14

One daily job
    |
    +-- Recording chunk 00:00
    +-- Recording chunk 00:05
    +-- Recording chunk 00:10
    +-- Recording chunk 00:15
    +-- ...
    |
    +-- One Olaf indexing session/workflow
    |
    +-- One daily LMDB database
    |
    +-- One authoritative stream timeline
```

Do not create an independent daily database for every five-minute recording.

Do not create a separate job for each chunk.

All chunks must be processed into the same daily LMDB database.

The implementation may process or cache individual input files internally, but they must belong to the same daily job and database.

## 7.3 Timeline construction

Construct the timeline before fingerprinting.

The process:

1. List all recording objects under the stream/date prefix.
2. Extract the nominal start timestamp from each filename.
3. Sort recordings chronologically.
4. Download the recordings.
5. Determine actual durations using `ffprobe` or equivalent.
6. Validate the recording metadata.
7. Calculate each recording's logical timeline position.
8. Identify gaps and overlaps.
9. Persist the completed timeline manifest.
10. Use that timeline when generating the Olaf index.

Do not assume that every recording is exactly five minutes long.

Actual duration must be used.

## 7.4 Logical timeline offsets

Every recording receives a logical position within the daily timeline.

Use milliseconds or another consistent high-resolution time unit.

Suggested recording entry:

```json
{
  "resource_id": "capital-fm-ke-20261114-000000",
  "object_key": "capital-fm-ke/recordings/2026/11/14/000000.ogg",
  "start_time": "2026-11-14T00:00:00Z",
  "duration_ms": 300000,
  "timeline_start_ms": 0,
  "timeline_end_ms": 300000,
  "olaf_reference_id": "capital-fm-ke-20261114-000000",
  "status": "indexed"
}
```

The logical timeline position represents where that recording belongs in the chronological daily stream.

For recordings that are contiguous:

```text
Chunk A:
    timeline_start_ms = 0
    timeline_end_ms   = 300000

Chunk B:
    timeline_start_ms = 300000
    timeline_end_ms   = 600000

Chunk C:
    timeline_start_ms = 600000
    timeline_end_ms   = 900000
```

For a missing interval, the next recording must retain its actual chronological position.

Example:

```text
00:00 - 00:05  Recording A
00:05 - 00:10  Recording B
00:10 - 00:15  Missing
00:15 - 00:20  Recording C
```

The logical timeline must represent the gap:

```text
Recording A: 0       - 300000 ms
Recording B: 300000  - 600000 ms
Gap:         600000  - 900000 ms
Recording C: 900000  - 1200000 ms
```

Do not shift Recording C backwards to close the gap.

Do not treat non-adjacent recordings as contiguous.

The timeline must preserve actual elapsed time.

## 7.5 Gaps

A gap represents an interval in which no recording is available.

Possible causes:

- Recorder interruption.
- Source outage.
- Network failure.
- Missing object.
- Delayed upload.
- Incomplete recording.

Represent gaps explicitly.

Example:

```json
{
  "start_time": "2026-11-14T00:10:00Z",
  "end_time": "2026-11-14T00:15:00Z",
  "timeline_start_ms": 600000,
  "timeline_end_ms": 900000,
  "duration_ms": 300000,
  "reason": "missing_recording"
}
```

If the cause is unknown, use `unknown`.

Never invent a cause.

The indexing pipeline must not silently bridge missing intervals.

## 7.6 Overlaps

Overlaps occur when two recordings cover some of the same wall-clock interval.

For example:

```text
Recording A: 00:00:00 - 00:05:00
Recording B: 00:04:55 - 00:09:55
```

The timeline must preserve both original recording intervals and flag the overlap.

Do not silently change original timestamps or discard an overlapping recording.

The implementation must define and test a consistent logical-position policy for overlaps.

Where multiple source recordings cover the same wall-clock time, the timeline should retain the original interval information so the query service can resolve the corresponding occurrence without losing provenance.

## 7.7 Daily boundaries and timezone

The job date is interpreted using the stream's configured timezone.

Store canonical timestamps in UTC.

Preserve the original timezone identifier, such as:

```text
Africa/Nairobi
```

The timeline must handle:

- Day boundaries.
- Timezone conversion.
- Daylight-saving changes where applicable.
- Recordings whose actual duration crosses midnight.
- Chunks whose filename timestamp and measured duration disagree.

A daily job should not silently discard audio that crosses a calendar boundary. The implementation must preserve the original chunk's complete interval and explicitly define how boundary-crossing recordings are assigned to daily jobs.

## 7.8 Timeline and Olaf mapping

The timeline is the bridge between Olaf's internal reference positions and real broadcast time.

For every indexed recording, maintain:

```text
Olaf reference ID
    -> timeline entry
        -> original object key
        -> original recording start time
        -> actual duration
        -> logical timeline offset
```

The implementation must verify the exact identifier and offset semantics returned by the installed Olaf version.

If Olaf returns a match against a recording resource with an offset within that resource:

```text
absolute_occurrence_time =
    recording_start_time + reference_offset
```

If Olaf returns a position on a logical daily timeline:

```text
absolute_occurrence_time =
    timeline_origin + logical_timeline_offset
```

These are two possible mapping paths, not interchangeable assumptions.

The implementation must determine which position Olaf actually returns and use the corresponding mapping.

The final query-service interface must be able to resolve an Olaf match to:

- Stream ID.
- Original recording object.
- Original UTC occurrence timestamp.
- Stream-local occurrence timestamp.
- Match duration, where available.

Do not use temporary local paths as permanent identifiers.

### Important Olaf verification

The implementation must verify:

- How stable resource IDs are assigned.
- Whether IDs survive `cache` and `store_cached`.
- Whether reference offsets are per resource or relative to a larger input.
- Whether the cache workflow supports custom identifiers.
- How multiple input files are represented in the resulting LMDB database.

Olaf's `--with-ids` support must not be assumed to work identically in every command.

Do not implement timestamp conversion until the actual Olaf result semantics have been verified.

## 7.9 Timeline file format

Store the authoritative timeline as a separate file:

```text
{stream_id}/indexes/YYYY/MM/DD/
    data.mdb
    lock.mdb
    timeline.json
    meta.json
```

`timeline.json` contains the recording inventory, logical positions, gaps, overlaps, and Olaf reference mapping.

Keep `meta.json` relatively small. It should identify the timeline file, its format version, and its checksum.

This is a required index artifact, not an optional diagnostic file.

Suggested structure:

```json
{
  "version": 1,
  "stream_id": "capital-fm-ke",
  "date": "2026-11-14",
  "timezone": "Africa/Nairobi",
  "timeline_origin": "2026-11-14T00:00:00Z",
  "timeline_duration_ms": 86400000,
  "recordings": [
    {
      "resource_id": "capital-fm-ke-20261114-000000",
      "object_key": "capital-fm-ke/recordings/2026/11/14/000000.ogg",
      "start_time": "2026-11-14T00:00:00Z",
      "duration_ms": 300000,
      "timeline_start_ms": 0,
      "timeline_end_ms": 300000,
      "olaf_reference_id": "capital-fm-ke-20261114-000000",
      "status": "indexed"
    }
  ],
  "gaps": [],
  "overlaps": [],
  "summary": {
    "recordings_discovered": 288,
    "recordings_indexed": 288,
    "recordings_failed": 0,
    "total_recorded_duration_ms": 86400000,
    "total_gap_duration_ms": 0
  }
}
```

The example assumes a full 24-hour recording day. Actual timeline duration and counts must be calculated from the recordings.

The implementation must not assume that a date contains 86,400 seconds in every timezone.

Use a versioned schema so that downstream query services can identify the timeline format.

## 7.10 Timeline validation

Before publishing, verify:

- Every discovered recording has a timeline entry.
- Every successfully indexed recording has a valid Olaf reference mapping.
- Start timestamps are valid.
- Durations are positive.
- Timeline positions are consistent.
- Gaps are represented explicitly.
- Overlaps are represented explicitly.
- No recording is accidentally omitted.
- The timeline summary matches the recording inventory.
- The timeline checksum matches the uploaded file.

An indexing job must not be marked completed if its timeline is missing or invalid.

---

# 8. Job Processing Pipeline

The complete pipeline is:

```text
1. Validate job
2. Discover recording objects
3. Create isolated workspace
4. Download recordings
5. Probe actual durations
6. Build chronological stream timeline
7. Validate timeline
8. Generate Olaf fingerprint cache
9. Store fingerprints in one daily LMDB
10. Validate LMDB
11. Validate Olaf reference mapping
12. Generate final timeline.json
13. Upload LMDB files
14. Upload timeline.json
15. Upload meta.json last
16. Mark job completed
17. Clean local workspace
```

## 8.1 Workspace

```text
/var/lib/olaf-indexer/
├── indexer.db
└── jobs/
    └── {job_id}/
        ├── audio/
        ├── cache/
        ├── index/
        ├── timeline.json
        └── manifest.json
```

Each daily job has its own workspace.

Never share an Olaf cache or LMDB database between jobs.

## 8.2 Download

- List only objects for the requested stream/date.
- Filter supported audio formats.
- Preserve original object keys.
- Download using bounded concurrency.
- Validate successful downloads.
- Record download errors.
- Never delete or modify source recordings.

## 8.3 Audio inspection

Use `ffprobe` or equivalent to determine:

- Actual duration.
- Audio format.
- Sample rate.
- Channel count.
- Readability.

Do not infer duration from filenames.

Do not assume all chunks have the same duration.

## 8.4 Olaf cache

Use Olaf's cache workflow to separate fingerprint extraction from LMDB insertion.

Conceptually:

```bash
olaf cache --threads N <audio-files>
olaf store_cached
```

Each daily job must use its own cache and database configuration.

The implementation must verify the installed Olaf version's configuration selection, cache directory, database directory, and resource ID behaviour.

Capture stdout, stderr, and exit codes.

Use safe subprocess execution through Go. Do not concatenate user input into shell commands.

## 8.5 One daily LMDB

All recordings belonging to a stream/date must be indexed into one daily LMDB database.

The internal processing may involve multiple files and fingerprint extraction operations, but the output must remain one daily database.

Do not create one LMDB database per recording chunk.

Do not create one independent indexing job per chunk.

## 8.6 Validation

Before upload, verify:

- Olaf completed successfully.
- LMDB files exist.
- The database contains fingerprints.
- The expected recording resources are represented.
- The timeline is valid.
- The Olaf reference mapping is complete.
- The index metadata can be generated.

Do not publish an empty, corrupt, or incomplete index as completed.

## 8.7 Cleanup

After successful publication:

- Remove local audio.
- Remove Olaf cache.
- Remove temporary LMDB workspace.
- Retain job logs and metadata in SQLite.

On failure, retain sufficient diagnostic information for troubleshooting, subject to the configured cleanup policy.

---

# 9. Index Metadata

Each completed index must contain `meta.json`.

Example:

```json
{
  "status": "complete",
  "stream_id": "capital-fm-ke",
  "date": "2026-11-14",
  "created_at": "2026-11-15T02:30:00Z",
  "completed_at": "2026-11-15T02:42:00Z",
  "timezone": "Africa/Nairobi",
  "olaf_version": "verified-version",
  "fingerprint_config": {},
  "recordings": {
    "discovered": 288,
    "downloaded": 288,
    "indexed": 288,
    "failed": 0
  },
  "timeline": {
    "file": "timeline.json",
    "version": 1,
    "checksum": "sha256:..."
  },
  "index": {
    "format": "lmdb",
    "files": [
      "data.mdb",
      "lock.mdb"
    ]
  }
}
```

The metadata must identify the index, its timeline, and the fingerprinting configuration used to build it.

Use actual Olaf version and configuration values.

---

# 10. Concurrency and Resource Management

Provide configurable limits for:

- Maximum active daily jobs.
- Concurrent downloads per job.
- Olaf threads per job.
- Concurrent uploads per job.
- Retry attempts.
- Retry delay.
- Job timeout.

Use a bounded worker pool.

Do not start hundreds of simultaneous Olaf processes simply because hundreds of jobs are queued.

Each active daily job must have an isolated workspace.

Collect real CPU, memory, disk, and throughput statistics where available.

Apply configuration changes safely without abruptly interrupting active jobs.

---

# 11. SQLite Database Design

Use SQLite as the durable application database.

## 11.1 `jobs`

Suggested fields:

- id
- stream_id
- date
- status
- stage
- source
- force
- progress
- recordings_total
- recordings_downloaded
- recordings_indexed
- error
- created_at
- started_at
- completed_at
- updated_at

Enforce appropriate uniqueness and lookup indexes.

## 11.2 `job_attempts`

- id
- job_id
- attempt_number
- status
- started_at
- completed_at
- error
- workspace_path

## 11.3 `job_logs`

- id
- job_id
- level
- stage
- message
- created_at

## 11.4 `discovery_config`

- id
- enabled
- config_json
- updated_at

## 11.5 `discovery_runs`

- id
- started_at
- completed_at
- status
- streams_examined
- dates_examined
- eligible_dates
- jobs_created
- jobs_skipped
- errors

## 11.6 `storage_configs`

- id
- name
- role
- endpoint
- bucket
- region
- access_key
- secret_key
- prefix
- path_style
- updated_at

Protect credentials and never return secret values through normal API responses.

## 11.7 `app_settings`

- key
- value
- updated_at

Use this for non-secret runtime settings.

The stream timeline itself belongs with the daily index in object storage. SQLite only needs job-level summary information and processing state, not a second permanent copy of every timeline.

---

# 12. Backend Structure

Keep the Go application small.

```text
olaf-indexer/
├── main.go
├── jobs.go
├── discovery.go
├── timeline.go
├── storage.go
├── api.go
├── database.go
├── go.mod
├── web/
│   ├── package.json
│   └── src/
│       └── App.tsx
└── migrations/
    └── 001_initial.sql
```

### Responsibilities

**main.go**
- Load configuration.
- Initialize SQLite and storage.
- Start workers and discovery.
- Start HTTP server.
- Handle shutdown.

**jobs.go**
- Job creation and deduplication.
- Queue and worker management.
- Processing lifecycle.
- Retry and cancellation.

**discovery.go**
- Discovery configuration.
- Scheduled scans.
- Readiness checks.
- Automatic enqueueing.
- Discovery history.

**timeline.go**
- Recording inventory.
- Timestamp extraction.
- Duration handling.
- Logical timeline construction.
- Gap and overlap detection.
- Olaf reference mapping.
- Timeline validation and serialization.

**storage.go**
- S3-compatible clients.
- Object listing.
- Downloads and uploads.
- Object metadata.

**api.go**
- REST API.
- Request validation.
- Response serialization.
- OpenAPI and Swagger UI.

**database.go**
- SQLite initialization.
- Migrations.
- Transactions and queries.

**App.tsx**
- Main web application.
- Navigation tabs.
- Tables, forms, drawers, and dialogs.

Avoid unnecessary package fragmentation.

---

# 13. REST API

All routes should use consistent JSON responses.

## 13.1 Jobs

| Method | Endpoint | Description |
|---|---|---|
| POST | `/api/jobs` | Create job |
| GET | `/api/jobs` | List jobs |
| GET | `/api/jobs/{id}` | Job details |
| POST | `/api/jobs/{id}/retry` | Retry job |
| POST | `/api/jobs/{id}/cancel` | Cancel job |

## 13.2 Timeline

| Method | Endpoint | Description |
|---|---|---|
| GET | `/api/jobs/{id}/timeline` | Get timeline for a job |
| GET | `/api/jobs/{id}/timeline/summary` | Get timeline summary |

The timeline endpoint should return the generated timeline for the job, including gaps, overlaps, and recording mappings.

For large timelines, support pagination or a summary/detail option.

Do not make the browser download the full timeline merely to display a summary.

## 13.3 Discovery

| Method | Endpoint | Description |
|---|---|---|
| GET | `/api/discovery/config` | Get configuration |
| PUT | `/api/discovery/config` | Update configuration |
| POST | `/api/discovery/run` | Run discovery |
| GET | `/api/discovery/runs` | List scan history |
| GET | `/api/discovery/runs/{id}` | Scan details |

## 13.4 Recorder events

`POST /api/events/recording-complete`

## 13.5 Statistics and health

| Method | Endpoint |
|---|---|
| GET | `/api/stats` |
| GET | `/api/logs` |
| GET | `/api/health` |
| GET | `/api/health/ready` |

## 13.6 Storage configuration

| Method | Endpoint |
|---|---|
| GET | `/api/storage` |
| POST | `/api/storage` |
| PUT | `/api/storage/{id}` |
| POST | `/api/storage/{id}/test` |

## 13.7 API documentation

- `/openapi.json`
- `/docs`

Maintain an OpenAPI 3.x specification and Swagger UI.

---

# 14. Web UI

## 14.1 Design direction

Use the actual `azure-devops-ui` package.

The interface must be:

- Professional and minimal.
- Light theme.
- Boxed layout.
- No sidebar.
- Horizontal navigation tabs.
- Compact tables.
- Subtle borders.
- Right-side detail drawers.
- Dialogs for short forms.
- Small, clear status indicators.

Avoid gradients, oversized cards, excessive rounding, and decorative charts.

## 14.2 Main navigation

```text
Overview | Jobs | Discovery | Logs | API | Settings
```

## 14.3 Overview

Display:

- Service status.
- Worker status.
- Discovery status.
- Storage connection status.
- Queued, active, completed, and failed jobs.
- Recent jobs.
- Latest discovery run.
- Timeline processing summary.

## 14.4 Jobs

Use an Azure DevOps-style table.

Columns:

| Column | Description |
|---|---|
| Stream | Stream ID |
| Date | Recording date |
| Status | Job status |
| Stage | Processing stage |
| Recordings | Indexed / total |
| Gaps | Number of detected gaps |
| Overlaps | Number of detected overlaps |
| Progress | Overall progress |
| Duration | Processing duration |
| Source | Initiation source |
| Created | Creation time |

Clicking a row opens a right-side drawer.

### Job details drawer

Use tabs within the drawer:

**Overview**
- Job information.
- Status.
- Current stage.
- Attempts.
- Errors.
- Processing duration.

**Timeline**
- Timeline origin and timezone.
- Total timeline duration.
- Recorded duration.
- Missing duration.
- Recording count.
- Gap count.
- Overlap count.
- Chronological recording inventory.

**Logs**
- Job logs.
- Olaf stdout/stderr.
- Errors.

The timeline view should provide a chronological table with:

- Start time.
- End time.
- Duration.
- Recording object.
- Timeline start/end offsets.
- Status.

Gaps should appear as explicit entries, not disappear from the table.

A simple horizontal timeline visualization may be added if it remains compact and useful. The chronological table is the authoritative UI representation.

## 14.5 Discovery

Provide configuration controls for:

- Enable/disable.
- Scan interval.
- Source storage.
- Stream scope.
- Date scope.
- Lookback days.
- Readiness strategy.
- Grace period.
- Minimum recordings.
- Minimum duration.
- Maximum jobs per scan.
- Dry run.

Display recent discovery runs and their results.

## 14.6 Logs

Provide search and filters for:

- Log level.
- Job.
- Stage.
- Date.

Use pagination.

## 14.7 API

Show Swagger UI or a link to `/docs`.

## 14.8 Settings

Provide sections for:

- Source and destination storage.
- Worker concurrency.
- Olaf configuration.
- Retry policy.
- Workspace and cleanup.
- Logging and retention.

Mask credentials.

## 14.9 UI implementation rules

- Use `azure-devops-ui` components wherever suitable.
- Use its tables, tabs, panels, dialogs, and status controls.
- Keep the initial frontend simple.
- Use API calls for persisted data.
- Show loading, empty, success, and error states.
- Refresh active jobs periodically or use server-sent events if simple.
- Do not store operational state only in browser memory.

---

# 15. Configuration Management

Separate deployment configuration from runtime configuration.

## Deployment configuration

Examples:

- HTTP listen address.
- SQLite database path.
- Workspace directory.
- Olaf executable path.
- Authentication secrets.
- Logging configuration.

## Runtime configuration

Managed through the UI and stored in SQLite:

- Source storage.
- Destination storage.
- Discovery configuration.
- Worker concurrency.
- Retry settings.
- Cleanup settings.

Validate all runtime configuration before saving.

Do not expose secrets through API responses.

---

# 16. Scheduler and Worker Behaviour

The scheduler must:

- Claim queued jobs atomically.
- Respect concurrency limits.
- Start daily job processing.
- Detect stale jobs.
- Recover interrupted jobs.
- Respect cancellation requests.

On startup, recover interrupted jobs by starting a clean attempt where safe.

Preserve previous attempt logs.

Use worker leases or equivalent ownership checks so that two workers cannot claim the same job.

Run Olaf as a child process with captured output, safe arguments, cancellation support, and recorded exit codes.

---

# 17. Error Handling

Categorize errors:

| Category | Examples |
|---|---|
| Storage | Authentication, network, missing object |
| Download | Partial or corrupt file |
| Audio | Unsupported format, invalid file |
| Timeline | Invalid timestamp, inconsistent duration |
| Olaf | Process failure, configuration error |
| Index | Missing or corrupt LMDB |
| Mapping | Missing or invalid Olaf resource mapping |
| Upload | Permission, timeout, incomplete publication |
| System | Disk full, memory pressure, restart |

Retry transient failures with exponential backoff.

Do not retry permanent failures indefinitely.

Do not mark an index completed if timeline validation or reference mapping fails.

---

# 18. Security

- Protect administrative endpoints.
- Authenticate recorder events.
- Never expose storage credentials.
- Validate all input.
- Prevent path traversal.
- Avoid shell command interpolation.
- Use HTTPS in production.
- Restrict access to SQLite and workspace directories.
- Do not allow browser access to Olaf or S3 directly.

---

# 19. Deployment

Suggested Linux paths:

```text
/opt/olaf-indexer/
    olaf-indexer

/etc/olaf-indexer/
    config.env

/var/lib/olaf-indexer/
    indexer.db
    jobs/

/var/log/olaf-indexer/
    application.log
```

Run as a dedicated service account under systemd.

On startup:

1. Load configuration.
2. Verify Olaf availability.
3. Initialize SQLite.
4. Apply migrations.
5. Initialize storage clients.
6. Recover interrupted jobs.
7. Start workers.
8. Start discovery.
9. Start HTTP server.

On shutdown:

1. Stop accepting new work.
2. Stop new job claims.
3. Stop discovery scans.
4. Allow active jobs a configurable grace period.
5. Cancel remaining jobs safely.
6. Persist state.
7. Close dependencies.

---

# 20. Implementation Phases

## Phase 1: Foundation

- Go application.
- SQLite and migrations.
- Deployment configuration.
- Independent source/destination storage.
- Health endpoints.

## Phase 2: Job queue

- Job creation.
- Deduplication.
- Worker claiming.
- Retry and cancellation.
- Logs and job history.

## Phase 3: Stream timeline

- Recording object discovery.
- Timestamp parsing.
- Actual duration probing.
- Chronological ordering.
- Logical timeline positions.
- Gap and overlap detection.
- Timeline JSON generation.
- Timeline validation.
- Timeline API and job details UI.

**Acceptance criterion:** Given a set of timestamped recording chunks, the application produces a correct chronological timeline with explicit gaps and overlaps.

## Phase 4: Olaf processing

- Verify Olaf CLI and configuration.
- Verify isolated cache workflow.
- Verify stable resource IDs.
- Build one daily LMDB per job.
- Validate index.
- Validate reference mapping.
- Publish index and timeline.

**Acceptance criterion:** One stream/date job produces a valid daily LMDB and a timeline that can translate Olaf match positions into exact recording timestamps.

## Phase 5: Automatic discovery

- Discovery configuration.
- Scheduled scanning.
- Readiness checks.
- Deduplication.
- Automatic enqueue.
- Scan history.
- Manual scan endpoint.

**Acceptance criterion:** Eligible recording dates are discovered and enqueued without duplicate jobs.

## Phase 6: Web UI

Implement:

- Overview.
- Jobs.
- Discovery.
- Logs.
- API.
- Settings.

Use the Azure DevOps design language and no sidebar.

**Acceptance criterion:** Operators can manage jobs, inspect timelines, configure automatic discovery, and monitor processing through the web interface.

## Phase 7: Reliability

- Bounded concurrency.
- Job recovery.
- Retry/backoff.
- Cancellation.
- Safe publication.
- Disk-space checks.
- Graceful shutdown.

## Phase 8: Deployment

- Build frontend assets.
- Package the Go binary.
- Create systemd service.
- Document installation.
- Document configuration and recovery.

---

# 21. Testing Requirements

## Timeline tests

- Chronologically ordered chunks.
- Out-of-order object listing.
- Missing five-minute chunk.
- Multiple consecutive missing chunks.
- Variable chunk duration.
- Overlapping recordings.
- Duplicate timestamps.
- Invalid filename timestamp.
- Filename timestamp versus actual media duration.
- Recording crossing midnight.
- Timezone conversion.
- Empty recording date.
- Failed download.
- Failed audio inspection.
- Missing Olaf resource mapping.
- Incorrect timeline offset.
- Timeline checksum mismatch.

## Job tests

- Manual job.
- API job.
- Recorder event.
- Duplicate submission.
- Forced rebuild.
- Retry.
- Cancellation.
- Restart recovery.

## Discovery tests

- Multiple streams.
- Multiple dates.
- Completed index detection.
- Active job detection.
- Grace period.
- Completion event.
- Dry run.
- Configuration update without restart.
- Scan overlap prevention.

## Storage tests

- Same source and destination bucket.
- Different buckets.
- Different credentials.
- Different endpoints.
- Authentication failure.
- Interrupted download.
- Interrupted upload.

## Olaf tests

- Successful cache.
- Successful daily LMDB build.
- Invalid audio.
- Process failure.
- Isolated concurrent job caches.
- Stable resource IDs.
- Query result timestamp mapping.
- Database validation.

## Publication tests

- Successful LMDB upload.
- Successful timeline upload.
- Metadata uploaded last.
- Failure during upload.
- Forced rebuild failure.
- Previous completed index remains available.

## UI tests

- Create and inspect jobs.
- Inspect timeline.
- View gaps and overlaps.
- Configure discovery.
- Run a scan.
- View scan history.
- Filter logs.
- Test storage connections.
- View Swagger.

---

# 22. Mandatory Implementation Constraints

1. One job = one stream/date.
2. One daily job = one daily LMDB.
3. One daily job = one authoritative stream timeline.
4. All chunks must be represented in chronological order.
5. Missing intervals must be explicit.
6. Do not silently concatenate non-adjacent recordings.
7. Preserve actual recording durations.
8. Preserve stable Olaf reference mappings.
9. Do not create one database per recording chunk.
10. Do not share cache or LMDB workspaces between daily jobs.
11. Do not upload audio recordings.
12. Do not delete or modify source recordings.
13. Do not assume source and destination share credentials.
14. Do not let the browser access Olaf or S3 directly.
15. Do not publish incomplete indexes.
16. Do not discard job history on retry.
17. Discovery configuration must be editable through the web UI.
18. Verify Olaf CLI flags and configuration keys before implementation.
19. Do not use temporary paths as permanent resource identifiers.
20. Use the actual `azure-devops-ui` package.
21. Do not add a sidebar.
22. Keep the codebase and dependencies small.

---

# 23. Definition of Done

The application is complete when:

- Operators can create daily indexing jobs manually.
- External systems can create jobs through the API.
- Recorders can send completion notifications.
- Automatic discovery identifies eligible recording dates.
- Discovery is configurable through the web interface.
- All initiation mechanisms use one durable queue.
- Duplicate jobs are prevented.
- Multiple jobs run within configured resource limits.
- Each daily job has an isolated Olaf workspace.
- All chunks are processed into one daily LMDB.
- Each daily job has a complete chronological stream timeline.
- Gaps and overlaps are preserved explicitly.
- Exact occurrence timestamps can be reconstructed from Olaf matches.
- The daily index and timeline are safely published.
- Source recordings remain untouched.
- Job, attempt, discovery, and log history are retained.
- The UI follows Azure DevOps conventions without a sidebar.
- Swagger documents the API.
- The application runs as a standalone Linux systemd service.

**Final architectural objective:** one small Go application, one SQLite database, one shared job queue, one configurable discovery engine, one isolated daily indexing workflow per stream/date, one daily LMDB, and one authoritative stream timeline per index.