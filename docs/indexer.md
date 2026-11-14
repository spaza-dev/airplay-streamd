# Spaza OLAF Indexer
## MVP Development Specification

**Version:** 1.0  
**Status:** Development baseline  
**Backend:** Go  
**Frontend:** React + TypeScript  
**UI framework:** Azure DevOps UI (`azure-devops-ui`)  
**Database:** SQLite  
**Indexing engine:** OLAF  
**Storage:** S3-compatible object storage

---

# 1. Project overview

Spaza OLAF Indexer is a standalone indexing service that reads recorded audio chunks from S3-compatible object storage, processes them using OLAF, produces self-contained LMDB indexes, and uploads the completed indexes to a separate destination bucket.

The service must support:

- Multiple recording streams.
- Multiple recording chunks per indexing job.
- Daily stream indexing.
- Individual asset/session indexing.
- Concurrent indexing jobs.
- Persistent job queuing and recovery.
- Independent source and destination storage configurations.
- A web-based operations interface.
- Index validation, archiving and publication.
- Job progress, logs, retries and operational monitoring.

The platform is an indexing and archival service. It is not responsible for recording audio, managing broadcast sources, matching fingerprints against catalogs, or administering royalties.

## 1.1 Core processing flow

```text
                 SOURCE STORAGE
                spaza-recordings
                       |
                       v
                Read stream metadata
                   meta.json
                       |
                       v
               Discover recording chunks
                       |
                       v
                 Create indexing job
                       |
                       v
                 SQLite job queue
                       |
                       v
                Go worker scheduler
                       |
                       v
                Download audio chunks
                       |
                       v
                Prepare logical audio
                       |
                       v
                  OLAF indexing
                       |
                       v
                  LMDB database
                       |
                       v
                Validate LMDB output
                       |
                       v
                 Archive index
                  index.tar.zst
                       |
                       v
                Upload to destination
                  S3-compatible
                       |
                       v
               Publish manifest.json
                       |
                       v
                  INDEX COMPLETE
```

Each job must have an isolated working directory, OLAF configuration, cache directory and LMDB database.

# 2. Technology and implementation principles

## 2.1 Backend

Use Go.

Recommended dependencies:

| Purpose | Technology |
|---|---|
| HTTP server and API | Go `net/http` |
| S3 storage | AWS SDK for Go v2 |
| Database | SQLite |
| SQLite driver | `modernc.org/sqlite` |
| Configuration | YAML |
| Logging | `log/slog` |
| Audio preparation | FFmpeg / ffprobe |
| Compression | Zstandard |
| Process execution | Go `os/exec` |

Keep the backend as one executable.

The HTTP server, scheduler, workers, queue and storage integrations run within the same process.

Do not introduce microservices, message brokers, Redis, Kubernetes or a separate database server for the MVP.

## 2.2 Frontend

Use:

- React
- TypeScript
- Vite
- `azure-devops-ui`
- Azure DevOps component styling and design patterns.

The frontend is built separately and its compiled static assets are served by the Go application.

The UI must use the Azure DevOps UI library as its actual component system, not merely imitate the visual appearance with generic HTML elements.

## 2.3 Implementation principles

1. Keep the implementation straightforward.
2. Minimize the number of source files.
3. Avoid unnecessary abstractions and design patterns.
4. Use SQLite as the only persistent application database.
5. Use S3-compatible storage for input recordings and published indexes.
6. Use OLAF for fingerprint extraction and LMDB management.
7. Do not implement fingerprinting algorithms in Go.
8. Never expose storage credentials to the frontend.
9. Never let the frontend communicate directly with S3 or OLAF.
10. Make jobs recoverable after application restarts.

# 3. Storage architecture

Storage configuration is an important part of this application.

The source and destination are two independent storage connections.

They may use:

- The same provider and credentials.
- The same provider but different credentials.
- Different providers.
- Different endpoints.
- Different buckets.
- Different regions.

The implementation must not assume that source and destination share credentials, endpoints or even the same S3 provider.

## 3.1 Independent storage configuration

Use separate `source` and `destination` configuration sections.

Example:

```yaml
storage:
  source:
    endpoint: "https://source-storage.example.com"
    region: "auto"
    bucket: "spaza-recordings"
    access_key_id_env: "SOURCE_S3_ACCESS_KEY"
    secret_access_key_env: "SOURCE_S3_SECRET_KEY"
    session_token_env: ""
    force_path_style: true
    use_tls: true

  destination:
    endpoint: "https://destination-storage.example.com"
    region: "auto"
    bucket: "spaza-indexes"
    access_key_id_env: "DESTINATION_S3_ACCESS_KEY"
    secret_access_key_env: "DESTINATION_S3_SECRET_KEY"
    session_token_env: ""
    force_path_style: true
    use_tls: true
```

These are independent connection configurations.

The environment variable names are references, not credential values. Actual credentials must be supplied through environment variables or a protected secrets mechanism.

If both connections use the same credentials, the operator can point both references to the same environment variables.

If they use different credentials, the operator supplies separate values.

The application must initialize two independent S3 clients:

- `SourceClient`
- `DestinationClient`

Do not reuse one client and mutate its credentials or endpoint between operations.

## 3.2 Storage connection capabilities

The source connection requires:

- List objects.
- Get objects.
- Read object metadata.
- Read object size and modification information.

The destination connection requires:

- Put objects.
- Get objects for verification.
- Head objects.
- List objects where necessary.
- Multipart upload support for large archives.

The application should validate each connection independently at startup and provide a connection-test action in Settings.

A source connection test should verify access to the configured source bucket.

A destination connection test should verify that the configured destination bucket is accessible and writable.

Do not require destination write permissions on the source connection.

Do not require source read permissions on the destination connection.

## 3.3 Source recordings structure

The source bucket follows this structure:

```text
s3://spaza-recordings/
│
├── capital-fm-ke/
│   ├── meta.json
│   └── recordings/
│       └── 2026/
│           └── 11/
│               └── 14/
│                   ├── 100000.ogg
│                   ├── 100500.ogg
│                   └── 101000.ogg
│
├── ntv-ke/
│   ├── meta.json
│   └── recordings/
│       └── 2026/
│           └── 11/
│               └── 14/
│                   ├── 100000.ogg
│                   └── 100500.ogg
│
└── youtube-example/
    ├── meta.json
    └── recordings/
        └── 2026/
            └── 11/
                └── 14/
                    ├── 100000.ogg
                    └── 100500.ogg
```

Object key pattern:

```text
{stream_id}/recordings/YYYY/MM/DD/HHMMSS.{extension}
```

**Important:** All source types use this chunk-based storage convention.

Radio, television, YouTube, podcasts, Twitch and other sources may all have multiple timestamped recording chunks.

Do not implement separate chunk discovery logic for each source type.

The indexer must use the same generic object discovery mechanism.

## 3.4 Stream metadata

Example `meta.json`:

```json
{
  "id": "capital-fm-ke",
  "name": "Capital FM Kenya",
  "type": "radio",
  "format": "ogg",
  "created_at": "2026-11-14T10:00:00Z",
  "updated_at": "2026-11-14T10:00:00Z",
  "metadata": {
    "country": "Kenya",
    "city": "Nairobi",
    "language": "English",
    "frequency": "98.4 FM",
    "operator": "Example Media"
  }
}
```

The indexer must:

1. Read metadata from the stream root.
2. Validate that `id` matches the stream directory.
3. Preserve the complete metadata object.
4. Avoid hardcoding source-specific metadata fields.
5. Include a metadata snapshot in the generated index manifest.

The `type` field is descriptive. It must not determine how chunks are discovered or processed.

# 4. Destination index structure

The destination bucket is organized by stream and indexing scope.

Recommended structure:

```text
s3://spaza-indexes/
│
├── capital-fm-ke/
│   ├── meta.json
│   │
│   ├── daily/
│   │   └── 2026/
│   │       └── 11/
│   │           ├── 13/
│   │           │   ├── index.tar.zst
│   │           │   └── manifest.json
│   │           │
│   │           └── 14/
│   │               ├── index.tar.zst
│   │               └── manifest.json
│   │
│   └── assets/
│       └── {asset_id}/
│           ├── index.tar.zst
│           └── manifest.json
│
├── ntv-ke/
│   ├── meta.json
│   ├── daily/
│   └── assets/
│
└── youtube-example/
    ├── meta.json
    ├── daily/
    └── assets/
```

## 4.1 Indexing scopes

The indexer supports two indexing scopes.

### Daily indexing

One index per stream per calendar day.

Example:

```text
capital-fm-ke/daily/2026/11/14/
```

This applies to any source type when the requested indexing unit is a day.

### Asset indexing

One index per logical asset.

Examples:

- A YouTube video.
- A podcast episode.
- A Twitch session.
- Another identifiable recording session.

Example:

```text
youtube-example/assets/video-12345/
```

An asset can consist of multiple timestamped chunks.

The application must never assume that all chunks within a date folder belong to one asset.

Asset grouping must be established using explicit job metadata or a recording manifest. Do not infer asset boundaries from source type or filename alone.

## 4.2 Index archive

Each completed job produces:

```text
index.tar.zst
manifest.json
```

The archive contains the complete LMDB database directory.

The archive must be self-contained and restorable.

The application must close OLAF and release LMDB resources before archiving the database.

Do not upload an open or actively modified LMDB directory.

The archive process must preserve the LMDB files and directory structure required to restore and open the database.

## 4.3 Index manifest

Example:

```json
{
  "schema_version": 1,
  "index_id": "capital-fm-ke-2026-11-14",
  "stream": {
    "id": "capital-fm-ke",
    "name": "Capital FM Kenya",
    "type": "radio",
    "format": "ogg",
    "metadata": {
      "country": "Kenya",
      "city": "Nairobi",
      "language": "English",
      "frequency": "98.4 FM",
      "operator": "Example Media"
    }
  },
  "scope": "daily",
  "period": {
    "date": "2026-11-14"
  },
  "source": {
    "bucket": "spaza-recordings",
    "prefix": "capital-fm-ke/recordings/2026/11/14/",
    "chunks": 288,
    "total_bytes": 123456789
  },
  "index": {
    "engine": "olaf",
    "format": "lmdb"
  },
  "archive": {
    "filename": "index.tar.zst",
    "compression": "zstd",
    "size_bytes": 88000000,
    "checksum_sha256": "..."
  },
  "created_at": "2026-11-15T00:15:00Z"
}
```

All values must be generated from actual processing results.

The manifest must also record:

- Exact input object keys.
- Input object sizes and available ETags/version IDs.
- Detected missing intervals.
- OLAF version.
- Relevant indexing configuration.
- Processing start and completion timestamps.
- Validation result.
- Archive checksum.

The example is illustrative; the implementation should define and document a stable manifest schema.

## 4.4 Publishing an index

Use this sequence:

1. Complete fingerprinting.
2. Close OLAF and LMDB.
3. Validate the database.
4. Create `index.tar.zst`.
5. Calculate archive checksum.
6. Upload the archive to a temporary object key.
7. Verify successful upload and object metadata.
8. Publish the archive under its final key.
9. Upload `manifest.json` last.

The presence of the final manifest indicates that the index has been published successfully.

A failed upload must not create an index that appears complete.

Use deterministic destination keys so retries do not create duplicate published indexes.

# 5. Indexing engine and OLAF

OLAF is the fingerprinting engine.

Go is responsible for orchestration, job management, storage, process execution and operational monitoring.

Go must not reimplement OLAF's fingerprinting functionality.

## 5.1 OLAF execution

For the initial implementation, use the OLAF CLI through Go's `os/exec`.

Each job must have its own:

- Working directory.
- OLAF configuration.
- Cache directory.
- LMDB database directory.
- Temporary audio preparation directory.

Example:

```text
/var/lib/olaf-indexer/
│
└── jobs/
    └── {job_id}/
        ├── config/
        │   └── olaf.json
        ├── cache/
        ├── db/
        ├── input/
        ├── output/
        └── logs/
```

Do not allow concurrent jobs to write into the same OLAF cache or LMDB directory.

## 5.2 Chunk processing

The application must discover and sort input chunks chronologically.

It must not blindly treat multiple independent audio files as one continuous recording.

The audio preparation implementation must preserve the intended logical timeline.

Before finalizing the production indexing pipeline, perform a technical validation of:

- OLAF's supported input mechanisms.
- Whether multiple input files preserve a common timeline.
- Whether cache extraction across files preserves the required fingerprint relationships.
- How timestamps are represented in the LMDB index.
- How gaps between chunks should be handled.
- Whether concatenation or streaming input is the appropriate method.

Do not assume that separate OLAF REST store requests share fingerprint state or preserve a global timeline.

For the initial implementation, prefer a clearly validated and reproducible audio preparation process over an optimization that has not been tested.

Missing chunks must be detected and recorded. Never silently join non-adjacent chunks as though they were continuous.

## 5.3 OLAF validation

The implementation must verify:

- OLAF exits successfully.
- The expected LMDB directory exists.
- Database files are present.
- The database can be reopened or queried after indexing.
- The archive can be restored.
- The restored database passes validation.

If the output is invalid, the job must fail and must not publish an index.

## 5.4 OLAF configuration

Example application configuration:

```yaml
olaf:
  binary: "/usr/local/bin/olaf"
  config_template: "/etc/olaf-indexer/olaf.json"
  threads_per_job: 2
  cache_enabled: true
  validate_after_indexing: true
```

The worker must generate a job-specific OLAF configuration using isolated paths.

Do not assume that the example configuration or CLI commands are valid without checking the installed OLAF version.

The implementation should include an initial OLAF integration test before developing the full worker pipeline.

# 6. Job queue and concurrency

SQLite is the persistent queue.

The queue must survive application restarts.

## 6.1 Job types

Support:

- `daily`
- `asset`

Each job must have a unique deterministic identity based on its logical scope.

For example:

```text
daily:capital-fm-ke:2026-11-14
asset:youtube-example:video-12345
```

The application must prevent duplicate active jobs for the same logical indexing unit.

Allow deliberate re-indexing through an explicit force/rebuild action.

## 6.2 Job states

Use these states:

```text
queued
discovering
downloading
preparing
indexing
validating
archiving
uploading
completed
failed
cancelled
```

The application should expose these states consistently through the API and UI.

## 6.3 Job progress

Track progress for each major processing stage.

Example:

| Stage | Progress information |
|---|---|
| Discovery | Objects discovered |
| Download | Chunks and bytes downloaded |
| Preparation | Audio preparation status |
| Indexing | OLAF execution status |
| Validation | Validation result |
| Archiving | Archive size and progress |
| Upload | Bytes uploaded |

Overall progress should be calculated from actual stage progress, not arbitrary increments.

If OLAF does not expose reliable fingerprinting progress, show its state as running rather than inventing a percentage.

## 6.4 Concurrency

Use a configurable maximum number of concurrent jobs.

Example:

```yaml
service:
  max_concurrent_jobs: 4
  job_timeout: "24h"
  workspace_dir: "/var/lib/olaf-indexer/jobs"

queue:
  max_attempts: 5
  retry_backoff: "30s"
  lease_timeout: "60s"
```

These are initial configuration examples, not guaranteed performance settings.

Benchmark CPU, memory, disk I/O and OLAF behavior before selecting production values.

Each worker must claim jobs atomically using SQLite transactions.

Use:

- SQLite WAL mode.
- Busy timeout.
- Job leases.
- Worker heartbeats.
- Retry backoff.
- Stale-job recovery.
- Graceful shutdown.

A worker restart must not leave jobs permanently stuck in a running state.

## 6.5 Retry behavior

A failed job should retain its error and processing history.

Retrying a job must reuse already completed stages when their outputs remain valid.

For example, an upload failure should not require fingerprinting the audio again if the completed LMDB archive is still available locally.

Use a configurable retry limit.

After the limit is reached, mark the job as failed and wait for manual intervention.

# 7. SQLite database

Keep the schema simple.

The minimum tables are:

## 7.1 `jobs`

Stores indexing jobs.

Suggested columns:

```text
id
job_key
stream_id
asset_id
scope
recording_date
status
priority
attempts
source_bucket
source_prefix
destination_bucket
destination_prefix
total_objects
processed_objects
total_bytes
processed_bytes
progress
worker_id
lease_expires_at
workspace
last_error
created_at
started_at
completed_at
updated_at
```

Store the effective source and destination bucket/prefix on each job. This ensures that a job retains its intended storage targets even if configuration changes later.

Do not store plaintext storage secrets in job records.

## 7.2 `job_sources`

Records the input objects associated with a job.

```text
id
job_id
object_key
size_bytes
etag
last_modified
status
downloaded_at
```

Use this table to track discovered and downloaded chunks.

## 7.3 `job_events`

Stores job history and operational events.

```text
id
job_id
level
component
message
created_at
```

## 7.4 `workers`

Stores local worker status.

```text
id
hostname
status
current_job_id
started_at
last_heartbeat
```

The service may run multiple workers inside one process. Worker records are for operational visibility and recovery, not a requirement for distributed execution.

Use SQLite migrations and indexes for job lookup, filtering, leases and event retrieval.

# 8. Go API

The frontend must communicate exclusively with the Go API.

Use JSON responses and consistent error formats.

## 8.1 Overview

```http
GET /api/overview
```

Returns:

- Service status.
- Queued jobs.
- Running jobs.
- Completed jobs today.
- Failed jobs.
- Active workers.
- Storage connection status.
- Recent activity.

## 8.2 Jobs

```http
GET    /api/jobs
GET    /api/jobs/{id}
POST   /api/jobs
POST   /api/jobs/{id}/retry
POST   /api/jobs/{id}/cancel
GET    /api/jobs/{id}/events
GET    /api/jobs/{id}/sources
```

Job creation should accept:

```json
{
  "scope": "daily",
  "stream_id": "capital-fm-ke",
  "date": "2026-11-14"
}
```

Asset example:

```json
{
  "scope": "asset",
  "stream_id": "youtube-example",
  "asset_id": "video-12345",
  "source_objects": [
    "youtube-example/recordings/2026/11/14/100000.ogg",
    "youtube-example/recordings/2026/11/14/100500.ogg"
  ]
}
```

The asset example uses explicitly supplied input objects. An asset manifest or another reliable discovery mechanism can be added later.

## 8.3 Streams

```http
GET /api/streams
GET /api/streams/{id}
GET /api/streams/{id}/recordings
GET /api/streams/{id}/indexes
```

The API should discover stream metadata from the source bucket and recording availability from the recordings prefixes.

## 8.4 Indexes

```http
GET /api/indexes
GET /api/indexes/{id}
GET /api/indexes/{id}/manifest
```

Only successfully published indexes should appear in the main index catalogue.

## 8.5 Workers and logs

```http
GET /api/workers
GET /api/logs
GET /api/health
```

Use Server-Sent Events (SSE) for live job and log updates if required.

Polling is acceptable for the initial MVP if it keeps the implementation simpler.

## 8.6 Settings

```http
GET  /api/settings
PUT  /api/settings
POST /api/settings/storage/source/test
POST /api/settings/storage/destination/test
POST /api/settings/olaf/test
```

Never return plaintext credentials in GET responses.

Settings updates must validate configuration before applying it.

Changes to storage configuration must not silently redirect already-running jobs.

# 9. Web interface — Azure DevOps UI

This is a mandatory design requirement.

The application must adopt the Azure DevOps UI look and feel throughout the interface.

Use the actual React components from:

https://www.npmjs.com/package/azure-devops-ui

Repository:

https://github.com/Microsoft/azure-devops-ui-public

Reference:

https://developer.microsoft.com/en-us/azure-devops/components

The Azure DevOps UI component system includes patterns for page structure, tables, tabs, panels, filters, status, dialogs, notifications and other operational controls.

Use these components as the foundation rather than recreating them with generic HTML and custom CSS.

## 9.1 Overall layout

The application must have:

- A charcoal application header.
- A horizontal tab navigation.
- A light neutral page background.
- Bordered, boxed content sections.
- Compact Azure DevOps-style tables.
- Right-side detail drawers.
- Azure DevOps-style command bars, filters, buttons and dialogs.

**There must be no sidebar.**

Primary navigation:

```text
┌──────────────────────────────────────────────────────────────┐
│ SPAZA  Indexer                              Service: Online   │
├──────────────────────────────────────────────────────────────┤
│ Overview   Jobs   Streams   Indexes   Workers   Logs   Settings│
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  Page title                                   Page actions    │
│  Description                                                 │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐  │
│  │                                                        │  │
│  │                  Boxed content                         │  │
│  │                                                        │  │
│  └────────────────────────────────────────────────────────┘  │
│                                                              │
└──────────────────────────────────────────────────────────────┘
```

## 9.2 Azure DevOps component adoption

Use the package's available components and patterns for these elements:

| Interface element | Azure DevOps UI |
|---|---|
| Application header | Header |
| Main page structure | Page |
| Horizontal navigation | Tabs / TabBar / Pivot pattern |
| Tables | Table |
| Filters | FilterBar / Filter |
| Command toolbar | HeaderCommandBar |
| Detail drawer | Panel / DetailsPanel |
| Inner drawer navigation | Tabs |
| Status indicators | Status / Pill |
| Progress | ProgressBar |
| Dialogs | Dialog |
| Notifications | MessageBar / Toast |
| Loading states | Spinner |
| Empty states | ZeroData |
| Buttons | Button |
| Form inputs | TextField, Dropdown, Checkbox |
| Icons | Azure DevOps Icon |

Confirm component exports and APIs against the installed package version. Do not invent component props or copy outdated examples without checking the installed TypeScript definitions.

## 9.3 Visual design requirements

Use this Spaza palette:

| Element | Color |
|---|---|
| Header | `#242322` |
| Main background | `#FAF9F8` |
| Box background | `#FFFFFF` |
| Secondary background | `#F4F3F2` |
| Border | `#EDEBE9` |
| Primary text | `#323130` |
| Secondary text | `#605E5C` |
| Accent | `#A85F34` |

Design requirements:

- Compact typography.
- Subtle separators and borders.
- Minimal corner rounding.
- No excessive card shadows.
- No gradients.
- No oversized dashboard cards.
- No intense orange active indicators.
- Consistent alignment and spacing.
- Information-dense tables.
- Clear visual distinction between primary and secondary actions.

Use the library's styling conventions and theme utilities where possible. Avoid overriding its component styles globally in ways that break component behavior.

## 9.4 Main tabs

### Overview

Show:

- Queued jobs.
- Running jobs.
- Completed today.
- Failed jobs.
- Active processing jobs.
- Recent activity.
- Source and destination storage health.

Keep this page compact and operational.

### Jobs

This is the primary working page.

Command bar:

- New job.
- Retry.
- Cancel.
- Refresh.

Table columns:

| Column | Description |
|---|---|
| Job ID | Unique job identifier |
| Stream | Stream name |
| Scope | Daily or asset |
| Period / Asset | Date or asset ID |
| Status | Current state |
| Progress | Processing progress |
| Chunks | Processed / total |
| Duration | Elapsed time |
| Created | Job creation time |

Provide filters for:

- Status.
- Stream.
- Source type.
- Scope.
- Date range.
- Worker.

Clicking a row opens a right-side details panel.

The details panel contains tabs:

1. Summary.
2. Input.
3. Processing.
4. Output.
5. Events.

The panel should show stage progress, input chunks, missing intervals, errors, archive information and job history.

### Streams

Show streams discovered in the source bucket.

Table columns:

- Stream name.
- Stream ID.
- Source type.
- Format.
- Last recording.
- Available recording days.
- Index coverage.

Clicking a stream opens a details panel with:

- Stream metadata.
- Recording inventory.
- Available dates.
- Existing indexes.
- Missing index dates.
- Indexing history.

Provide actions for creating daily and asset indexing jobs.

### Indexes

Show successfully published indexes.

Table columns:

- Index ID.
- Stream.
- Scope.
- Period or asset.
- Archive size.
- OLAF version.
- Published date.
- Validation status.

The detail panel should expose:

- Manifest.
- Input inventory.
- Archive key.
- Checksum.
- Validation result.
- Archive retrieval information.

Do not show incomplete or failed uploads as published indexes.

### Workers

Show:

- Worker ID.
- Hostname.
- Status.
- Current job.
- CPU utilization.
- Memory usage.
- OLAF process status.
- Last heartbeat.

Also show configured maximum job concurrency.

### Logs

Provide a searchable event viewer.

Filters:

- Time range.
- Log level.
- Job ID.
- Worker.
- Component.

Components include:

- Scheduler.
- Queue.
- S3 downloader.
- Audio preparation.
- OLAF runner.
- LMDB validator.
- Archive.
- S3 uploader.

Clicking a job reference should open its detail panel.

### Settings

Use tabs inside Settings:

**General**
- Service name.
- Timezone.
- Workspace directory.
- Log level.

**Source storage**
- Endpoint.
- Region.
- Bucket.
- Access key.
- Secret key.
- Session token.
- Path-style option.
- TLS option.
- Test connection.

**Destination storage**
- Endpoint.
- Region.
- Bucket.
- Access key.
- Secret key.
- Session token.
- Path-style option.
- TLS option.
- Test connection.

Source and destination must have independent forms and independent connection tests.

The UI may provide an explicit "Use source credentials" convenience option, but copying credentials must be an intentional user action. Editing one connection must not automatically alter the other.

**OLAF**
- Binary path.
- Configuration.
- Threads per job.
- Cache settings.
- Validation settings.
- Test OLAF.

**Queue and workers**
- Maximum concurrent jobs.
- Maximum attempts.
- Retry delay.
- Lease timeout.
- Job timeout.

**Maintenance**
- Workspace cleanup.
- Retention policy.
- Disk usage.
- Health checks.

All secrets must be masked. Never return stored secrets through the API.

## 9.5 Drawers and dialogs

Use Azure DevOps-style panels for details.

Examples:

- Job details.
- Stream details.
- Index details.
- Worker details.

Use dialogs for:

- Creating jobs.
- Confirming cancellation.
- Confirming re-indexing.
- Destructive maintenance operations.

Do not navigate to a separate page for every record.

Keep the user in the current tab and open details in a panel.

## 9.6 Frontend simplicity

Keep the frontend small.

Use one main application component to manage the active tab and shared detail panel.

Avoid creating a separate file for every page, table, drawer or small component.

Use the Azure DevOps components directly wherever possible.

# 10. Application configuration

Example complete configuration:

```yaml
service:
  name: "Spaza OLAF Indexer"
  listen: "0.0.0.0:8080"
  timezone: "Africa/Nairobi"
  workspace_dir: "/var/lib/olaf-indexer"
  max_concurrent_jobs: 4
  job_timeout: "24h"

database:
  path: "/var/lib/olaf-indexer/indexer.db"
  journal_mode: "WAL"
  busy_timeout: "5s"

storage:
  source:
    endpoint: "https://source.example.com"
    region: "auto"
    bucket: "spaza-recordings"
    access_key_id_env: "SOURCE_S3_ACCESS_KEY"
    secret_access_key_env: "SOURCE_S3_SECRET_KEY"
    session_token_env: ""
    force_path_style: true
    use_tls: true

  destination:
    endpoint: "https://destination.example.com"
    region: "auto"
    bucket: "spaza-indexes"
    access_key_id_env: "DESTINATION_S3_ACCESS_KEY"
    secret_access_key_env: "DESTINATION_S3_SECRET_KEY"
    session_token_env: ""
    force_path_style: true
    use_tls: true

olaf:
  binary: "/usr/local/bin/olaf"
  config_template: "/etc/olaf-indexer/olaf.json"
  threads_per_job: 2
  cache_enabled: true
  validate_after_indexing: true

queue:
  max_attempts: 5
  retry_backoff: "30s"
  lease_timeout: "60s"

archive:
  format: "tar.zst"
  compression_level: 3

logging:
  level: "info"
  format: "json"
```

The configuration is an example. Validate actual supported fields and OLAF settings against the installed versions.

Credentials must not be written into the YAML file.

# 11. Minimal project structure

Keep the project intentionally small.

```text
olaf-indexer/
│
├── main.go
├── go.mod
├── config.yaml
│
├── internal/
│   ├── api.go
│   ├── database.go
│   ├── storage.go
│   ├── worker.go
│   └── olaf.go
│
├── migrations/
│   └── schema.sql
│
└── web/
    ├── package.json
    ├── index.html
    └── src/
        ├── main.tsx
        ├── App.tsx
        └── style.css
```

File responsibilities:

| File | Responsibility |
|---|---|
| `main.go` | Load configuration, initialize services, start HTTP server and workers |
| `api.go` | HTTP routes and API handlers |
| `database.go` | SQLite initialization, migrations and database operations |
| `storage.go` | Source discovery, downloads, destination uploads and S3 clients |
| `worker.go` | Queue scheduling, job execution, progress and retries |
| `olaf.go` | OLAF subprocess management, audio preparation integration and LMDB validation |
| `schema.sql` | Database schema |
| `App.tsx` | Main UI, horizontal tabs, page rendering, drawers and dialogs |
| `style.css` | Application theme and layout styling |
| `main.tsx` | React entry point |

Do not create extra packages or files unless there is a concrete reason.

# 12. Operational requirements

## 12.1 Deployment

The MVP should run on a Linux server as a systemd service.

Suggested locations:

```text
/opt/olaf-indexer/
/etc/olaf-indexer/
/var/lib/olaf-indexer/
```

Use:

- Application binary under `/opt`.
- Configuration under `/etc`.
- SQLite database and job workspaces under `/var/lib`.

The service must support:

- Automatic startup.
- Graceful shutdown.
- Log output to journald or structured log files.
- Health endpoint.
- Restart recovery.
- Configurable resource limits.

## 12.2 Disk management

The service downloads and prepares audio locally, so disk capacity must be monitored.

Show:

- Total disk capacity.
- Available disk space.
- Workspace usage.
- Active job workspace usage.
- Archive staging usage.

Do not delete job workspaces while a job is active.

Retain completed workspaces until the archive is safely published and the configured cleanup policy permits removal.

Retain failed workspaces for troubleshooting according to a configurable policy.

## 12.3 Security

- Do not expose S3 credentials to React.
- Do not log secrets.
- Validate all API input.
- Restrict the web interface to authorized operators.
- Use HTTPS when deployed beyond localhost or a trusted private network.
- Validate storage endpoint configuration.
- Protect settings updates.
- Do not expose arbitrary filesystem paths or unrestricted command execution through the API.
- Use safe process argument construction rather than shell interpolation.

# 13. Testing requirements

## 13.1 Storage tests

Test:

- Source and destination using identical credentials.
- Source and destination using different credentials.
- Different S3-compatible providers.
- Different endpoints and regions.
- Missing credentials.
- Invalid bucket.
- Insufficient source permissions.
- Insufficient destination permissions.
- Failed multipart upload.
- Interrupted upload and retry.

## 13.2 Recording discovery tests

Test:

- Radio stream with multiple chunks.
- TV stream with multiple chunks.
- YouTube recording with multiple chunks.
- Podcast recording with multiple chunks.
- Mixed audio extensions.
- Missing chunks.
- Duplicate or conflicting objects.
- Empty date prefix.
- Invalid metadata.
- Invalid timestamps.

All source types must use the same generic discovery implementation.

## 13.3 Job tests

Test:

- Daily indexing.
- Asset indexing.
- Duplicate job prevention.
- Concurrent jobs.
- Job cancellation.
- Worker restart.
- Retry after download failure.
- Retry after OLAF failure.
- Retry after archive failure.
- Retry after upload failure.
- Re-indexing an existing index.

## 13.4 Index integrity tests

Test:

- LMDB database creation.
- LMDB close and reopen.
- Archive creation.
- Archive restoration.
- Checksum verification.
- Manifest correctness.
- Publication ordering.
- No published manifest for incomplete indexes.

# 14. Development phases

Build the platform in the following order.

## Phase 1 — Foundation

- Create the Go application.
- Load YAML configuration.
- Initialize SQLite.
- Implement independent source and destination S3 clients.
- Implement source and destination connection tests.
- Create the React application.
- Install and configure Azure DevOps UI.
- Implement the application header and horizontal tabs.
- Establish the boxed page layout and theme.

**Deliverable:** Running application with storage connection testing and the basic UI shell.

## Phase 2 — Source discovery

- Read stream metadata.
- Discover stream directories.
- Discover recording chunks.
- Sort chunks chronologically.
- Calculate object counts and sizes.
- Detect missing intervals.
- Display streams and recordings in the UI.

**Deliverable:** Functional Streams page and generic source inventory.

## Phase 3 — Job queue

- Create SQLite job schema.
- Implement job creation.
- Implement atomic job claiming.
- Implement worker scheduling.
- Implement status transitions.
- Implement retry and cancellation.
- Display jobs and job details.

**Deliverable:** Persistent job queue and functional Jobs page.

## Phase 4 — OLAF integration

- Validate OLAF executable and configuration.
- Create isolated job workspaces.
- Implement audio preparation.
- Implement OLAF execution.
- Capture process output and errors.
- Validate LMDB creation and reopening.
- Track job progress.

Before completing this phase, verify the actual OLAF behavior for processing sequential chunks and preserving the intended logical timeline.

**Deliverable:** A working end-to-end indexing job that produces a valid LMDB database.

## Phase 5 — Archiving and publishing

- Package LMDB into `index.tar.zst`.
- Calculate checksums.
- Upload to destination storage.
- Verify uploaded objects.
- Publish manifest last.
- Implement upload retry without re-indexing.
- Display published indexes.

**Deliverable:** Complete source-to-destination indexing pipeline.

## Phase 6 — Operations UI

- Complete Overview.
- Complete Streams.
- Complete Indexes.
- Complete Workers.
- Complete Logs.
- Complete Settings.
- Add drawers, filters, dialogs and notifications.
- Add live updates or polling.

**Deliverable:** Fully usable operations console.

## Phase 7 — Production readiness

- Test restart recovery.
- Test concurrent workloads.
- Test storage failures.
- Test large recording days.
- Test disk cleanup.
- Test archive restoration.
- Configure systemd.
- Document installation and operation.

**Deliverable:** Deployable MVP.

# 15. MVP acceptance criteria

The platform is considered functional when:

1. It can connect independently to source and destination S3-compatible buckets.
2. The two connections can use different credentials and endpoints.
3. It can read stream metadata from `meta.json`.
4. It can discover timestamped recording chunks for all source types.
5. It supports daily and asset indexing scopes.
6. It can create and persist indexing jobs in SQLite.
7. It can process multiple independent jobs concurrently.
8. Each job uses an isolated OLAF workspace and LMDB database.
9. It can create and validate a self-contained LMDB archive.
10. It can upload the archive and manifest to destination storage.
11. Failed uploads can be retried without repeating fingerprinting when the local archive is still available.
12. Jobs survive application restarts.
13. Operators can inspect jobs, streams, indexes, workers and logs.
14. The interface adopts Azure DevOps UI components and styling.
15. The interface uses horizontal tabs, boxed layouts and right-side detail panels, with no sidebar.
16. The application runs as a single Go service with a compiled React frontend.

---

# 16. Final implementation guidance

Prioritize a working indexing pipeline over a feature-rich dashboard.

The most important technical milestone is:

**Read chunks from source storage → prepare the logical recording → index with OLAF → produce a valid LMDB → archive → upload to destination storage → publish manifest.**

Once this pipeline works reliably, the rest of the platform is primarily job management and operational visibility.

Keep the code small, use the actual Azure DevOps UI components, and avoid introducing infrastructure that the MVP does not need.