# streamd — Stream Capture Service
## Development Specification

## 1. Overview

`streamd` is a lightweight, standalone Linux service for recording multiple internet streams concurrently, splitting recordings into configurable chunks, and uploading completed chunks to S3-compatible object storage.

It is designed to capture radio, television, HLS, direct media URLs, YouTube, Twitch, and other sources supported by FFmpeg or yt-dlp.

The application is a recorder and uploader. It does not perform audio recognition, fingerprinting, media analytics, or rights management.

### Core principles

- One Go executable.
- Runs as a Linux systemd service.
- Supports concurrent stream recording.
- Uses FFmpeg and yt-dlp for capture.
- Records in configurable chunks.
- Uploads asynchronously through a persistent SQLite queue.
- Supports S3-compatible storage.
- Uses YAML for configuration and `.env` for secrets.
- Provides a compact web dashboard and CLI.
- Keeps the implementation simple and self-contained.

## 2. Technology

| Component | Technology |
|---|---|
| Backend | Go |
| Recording | FFmpeg |
| Web stream extraction | yt-dlp |
| Object storage | S3-compatible API |
| Storage SDK | AWS SDK for Go v2 |
| Local upload queue | SQLite |
| Configuration | YAML |
| Secrets | Environment file |
| Frontend | React + TypeScript |
| UI library | azure-devops-ui |
| Frontend build | Vite |
| HTTP server | Go net/http |
| Service manager | systemd |

The frontend is compiled into static assets and embedded in the Go executable. Node.js and npm are not required on the production server.

## 3. Project structure

Keep the codebase compact.

```text
streamd/
├── main.go
├── cli.go
├── recorder.go
├── storage.go
├── web.go
├── config.yaml
├── go.mod
├── go.sum
├── web/
│   ├── package.json
│   ├── package-lock.json
│   ├── vite.config.ts
│   ├── index.html
│   └── src/
│       ├── main.tsx
│       ├── App.tsx
│       └── styles.scss
└── scripts/
    ├── install.sh
    └── update.sh
```

### Backend responsibilities

- `main.go`: application entry point, configuration loading, logging, initialization, shutdown and CLI dispatch.
- `cli.go`: CLI commands and communication with the running daemon.
- `recorder.go`: stream manager, recording workers, FFmpeg/yt-dlp execution, chunk rotation and reconnection.
- `storage.go`: S3 client, metadata publishing, SQLite upload queue, upload workers, retry handling and local cleanup.
- `web.go`: HTTP server, embedded frontend, dashboard API and control endpoints.

## 4. Configuration

Use a single YAML configuration file for global settings and stream definitions. Keep credentials in `.env`.

### Example `config.yaml`

```yaml
server:
  listen: "127.0.0.1:8787"
  socket: "/run/streamd.sock"

recording:
  chunk_seconds: 300
  output_dir: "/var/lib/streamd/chunks"

  ffmpeg_path: "/usr/bin/ffmpeg"
  yt_dlp_path: "/usr/local/bin/yt-dlp"

  reconnect_delay_seconds: 10
  max_reconnect_delay_seconds: 300

  audio:
    codec: "libopus"
    container: "ogg"
    bitrate: "96k"
    sample_rate: 48000
    channels: 2
    vbr: true

  video:
    enabled: false

uploader:
  enabled: true

  queue_db: "/var/lib/streamd/state/uploads.db"

  concurrency: 4

  retry:
    initial_interval_seconds: 30
    max_interval_seconds: 3600
    max_attempts: 0

  cleanup_after_upload: true

storage:
  endpoint: "${S3_ENDPOINT}"
  region: "${S3_REGION}"
  bucket: "${S3_BUCKET}"

  access_key_id: "${S3_ACCESS_KEY_ID}"
  secret_access_key: "${S3_SECRET_ACCESS_KEY}"

  force_path_style: false

logging:
  level: "info"
  file: "/var/log/streamd/streamd.log"

streams:
  - id: "capital-fm-ke"
    name: "Capital FM Kenya"
    type: "radio"
    url: "https://example.com/stream"
    enabled: true

    chunk_seconds: 300

    audio:
      codec: "libopus"
      container: "ogg"
      bitrate: "96k"

    metadata:
      country: "Kenya"
      city: "Nairobi"
      language: "English"
      frequency: "98.4 FM"
      operator: "Example Media"

  - id: "youtube-channel"
    name: "Example YouTube Channel"
    type: "youtube"
    url: "https://www.youtube.com/@example"
    enabled: false

    metadata:
      country: "Kenya"
      platform: "YouTube"
      channel: "Example Channel"
```

The configuration above illustrates the intended structure. The implementation must validate supported FFmpeg codecs, containers, bitrate values and stream-specific overrides.

### Stream configuration

Each stream should support:

- `id`: stable unique identifier.
- `name`: display name.
- `type`: source type.
- `url`: source URL, where applicable.
- `enabled`: whether to start automatically on daemon startup.
- `chunk_seconds`: optional stream-specific chunk duration.
- `audio`: optional encoding overrides.
- `video`: optional video configuration.
- `metadata`: arbitrary key-value metadata.

Metadata must be dynamic. Do not impose a fixed schema.

Examples include country, city, language, genre, frequency, operator, platform, channel, and other user-defined attributes.

### Supported source types

- `radio`
- `tv`
- `hls`
- `direct`
- `youtube`
- `twitch`
- `web`

Use FFmpeg directly for supported media URLs. Use yt-dlp to resolve sources requiring extraction before passing the resulting media URL to FFmpeg.

## 5. Recording engine

The recording engine manages multiple independent stream workers.

Each worker should:

1. Resolve the source URL when necessary.
2. Start FFmpeg.
3. Record for the configured chunk duration.
4. Finalize the chunk.
5. Register the completed chunk with the uploader.
6. Continue recording the next chunk.
7. Reconnect when the source becomes unavailable.

Recording must not wait for an upload to finish.

### Chunk handling

Each completed chunk is a separate recording file.

Use a predictable filename based on the chunk start time:

```text
YYYYMMDDHHMMSS.ext
```

For example:

```text
20261114100000.ogg
20261114100500.ogg
20261114101000.ogg
```

Use the same timestamp for the recording's deterministic object key.

### Audio configuration

Support global defaults and per-stream overrides.

The application should:

- Default to audio-only recording.
- Allow optional video recording for relevant sources.
- Support stream copy when the source codec and container are compatible with the requested output.
- Otherwise encode using the configured profile.
- Avoid unnecessary re-encoding.
- Translate structured configuration into FFmpeg arguments rather than accepting arbitrary command strings.

The initial proposed audio profile is Opus in Ogg, 96 kbps VBR, 48 kHz, stereo. This should be validated against downstream playback and OLAF compatibility before being treated as a production default.

Example overrides:

- Talk radio: mono, lower bitrate.
- Music radio: stereo, higher bitrate.
- Video streams: explicitly enabled video capture.

## 6. S3 uploader

The uploader is an independent subsystem. It must never block recording workers while waiting for remote storage.

### 6.1 Responsibilities

The uploader must:

- Maintain a persistent SQLite upload queue.
- Accept completed chunks from recording workers.
- Upload chunks asynchronously.
- Support concurrent uploads.
- Retry failed uploads.
- Recover pending uploads after service restarts.
- Delete local chunks only after successful upload confirmation, when cleanup is enabled.
- Publish and update stream metadata.
- Use deterministic object keys to make retries idempotent.

### 6.2 S3 destination structure

Use one root directory per stream.

Each stream has exactly one `meta.json` file at its root. Recording chunks are organized by date.

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
└── twitch-live/
    ├── meta.json
    └── recordings/
        └── 2026/
            └── 11/
                └── 14/
                    └── 100000.ogg
```

Object key pattern:

```text
{stream_id}/recordings/YYYY/MM/DD/HHMMSS.{extension}
```

The stream ID must remain stable. Renaming a stream should not silently change its storage identity.

### 6.3 Stream metadata

One `meta.json` per stream. Do not create daily metadata snapshots or per-recording metadata files.

Example:

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

Requirements:

- Metadata is dynamic key-value data.
- Include the stable stream ID and descriptive stream information.
- Include creation and update timestamps.
- Do not include storage credentials, access tokens or other secrets.
- Do not expose private source URLs unless explicitly intended.
- Upload metadata when a stream is registered.
- Update the same object whenever stream metadata changes.
- Do not create multiple metadata files for the same stream.

Metadata updates should be atomic from the application's perspective: serialize the current stream definition and upload it as a replacement of the existing object.

### 6.4 SQLite upload queue

Use SQLite as the durable local record of pending uploads.

Database location:

```text
/var/lib/streamd/state/uploads.db
```

The queue must survive service restarts and unexpected shutdowns.

A queue entry represents one completed recording chunk.

Suggested schema:

```sql
CREATE TABLE uploads (
    id INTEGER PRIMARY KEY AUTOINCREMENT,

    stream_id TEXT NOT NULL,

    local_path TEXT NOT NULL,
    object_key TEXT NOT NULL,

    file_size INTEGER NOT NULL,

    status TEXT NOT NULL DEFAULT 'pending',

    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,

    last_error TEXT,

    created_at TEXT NOT NULL,
    uploaded_at TEXT
);

CREATE INDEX idx_uploads_status_next_attempt
ON uploads(status, next_attempt_at);

CREATE INDEX idx_uploads_stream
ON uploads(stream_id);
```

Suggested statuses:

- `pending`: waiting to be uploaded.
- `uploading`: currently being uploaded.
- `failed`: temporarily failed and waiting for retry.
- `completed`: successfully uploaded.

The uploader should use SQLite transactions when claiming queue entries so multiple uploader workers cannot upload the same queue item simultaneously.

### 6.5 Upload lifecycle

The expected lifecycle is:

1. FFmpeg finishes a chunk.
2. The recording worker confirms that the file is finalized.
3. The worker registers the file in SQLite.
4. An uploader worker claims a pending entry.
5. The worker uploads the file to its deterministic S3 object key.
6. The uploader confirms successful completion.
7. The SQLite entry is marked completed.
8. The local recording is deleted if `cleanup_after_upload` is enabled.

Recording and uploading operate independently. If the network or S3 service is unavailable, completed chunks remain on local storage until they can be uploaded.

### 6.6 Retry behaviour

Failed uploads must be retried automatically.

Use exponential backoff:

```text
30 seconds
60 seconds
120 seconds
240 seconds
...
Maximum: 3600 seconds
```

The retry count should be configurable.

A value of `0` for `max_attempts` means unlimited retries.

The uploader should:

- Persist retry state in SQLite.
- Record the last error.
- Persist the next eligible retry time.
- Avoid retrying continuously when the storage service is unavailable.
- Resume retries after service restart.
- Use the same object key for every retry.

A failed upload must never cause the original local recording to be deleted.

### 6.7 Concurrent uploads

Use a configurable worker pool.

Initial default:

```yaml
uploader:
  concurrency: 4
```

Workers independently claim eligible queue entries. The configured concurrency should limit simultaneous uploads, not recording workers.

### 6.8 Startup recovery

On startup:

- Open the SQLite queue.
- Find entries left in `uploading` state by a previous process.
- Return those entries to a retryable state.
- Verify that pending local files still exist.
- Resume eligible uploads.
- Preserve completed queue records or remove them according to a deliberate retention policy.

The uploader must not assume that an interrupted upload failed before any remote data was written. Deterministic object keys allow safe re-uploading.

### 6.9 Queue cleanup

Completed queue records may eventually be pruned to prevent unlimited SQLite growth.

This cleanup must not remove pending, uploading or retryable entries.

The initial implementation can retain completed records. Queue pruning can be introduced as a configurable maintenance operation later.

### 6.10 No per-chunk metadata sidecars

Do not create JSON sidecar files beside individual recordings.

The uploader queue stores the information it needs to transfer a chunk. The stream-level `meta.json` provides the descriptive metadata for downstream consumers.

If detailed recording properties become necessary later, they can be added as queue columns or collected by a separate indexing process without introducing sidecar files.

## 7. Local filesystem layout

```text
/var/lib/streamd/
├── chunks/
│   ├── capital-fm-ke/
│   │   └── 2026-11-14/
│   │       ├── 100000.ogg
│   │       └── 100500.ogg
│   └── ntv-ke/
│       └── 2026-11-14/
│           └── 100000.ogg
│
└── state/
    └── uploads.db
```

The SQLite database is the source of truth for upload state. Do not maintain a separate JSON upload queue.

Local chunks must remain available until their corresponding uploads are confirmed successful.

## 8. Stream state and management

Distinguish between configuration and runtime state.

- `enabled`: whether a stream should automatically start when the service boots.
- `running`: whether its recording worker is currently active.
- `stopped`: whether it has been manually stopped.
- `error`: whether it has encountered an unrecoverable or currently active recording error.

A manual stop should persist across daemon restarts. The service must not automatically restart a manually stopped stream just because `enabled` is true.

The application should support:

- Add stream.
- Update stream.
- Remove stream.
- Start stream.
- Stop stream.
- Restart stream.
- View stream status.
- View stream logs.

A stream's metadata update should also trigger an update of its S3 `meta.json`.

## 9. CLI

The CLI should communicate with the running daemon over a Unix domain socket.

Socket:

```text
/run/streamd.sock
```

Example commands:

```bash
streamd serve

streamd status

streamd stream list
streamd stream show capital-fm-ke
streamd stream add
streamd stream update capital-fm-ke
streamd stream remove capital-fm-ke

streamd stream start capital-fm-ke
streamd stream stop capital-fm-ke
streamd stream restart capital-fm-ke

streamd logs
streamd logs capital-fm-ke

streamd config validate
```

The daemon owns the recording workers and uploader. CLI commands must not launch independent recording or upload processes.

## 10. Web dashboard

Build a compact, professional dashboard using React, TypeScript and `azure-devops-ui`.

The design should be inspired by Azure DevOps:

- Minimal and information-dense.
- Light theme.
- Centered content with a maximum width of approximately 1,400px.
- Avoid full-browser-width content.
- Use drawers for stream details.
- Use tabs where appropriate.
- Include search, pagination and filters.
- Avoid unnecessary rounded cards and decorative UI.

### Main views

**Streams**

- Stream name and ID.
- Source type.
- Recording status.
- Enabled/disabled state.
- Last activity.
- Start/stop controls.
- Search and pagination.

**Logs**

- Service logs.
- Stream-specific logs.
- Search and filtering.

### Stream details drawer

Suggested tabs:

- Overview
- Metadata
- Logs

The overview should show source details and recording state. The metadata tab should display dynamic key-value metadata.

The dashboard should also expose useful uploader information, such as pending upload count, active uploads, failed/retrying uploads and completed uploads. These are operational indicators, not a separate analytics system.

## 11. HTTP API

The Go HTTP server serves the embedded frontend and exposes internal control endpoints.

```text
GET  /
GET  /health

GET  /control/streams
GET  /control/streams/{id}

POST /control/streams/{id}/start
POST /control/streams/{id}/stop
POST /control/streams/{id}/restart

GET  /control/logs
GET  /control/logs/stream/{id}
```

Add uploader status endpoints:

```text
GET /control/uploader/status
GET /control/uploader/queue
```

The queue endpoint should expose operational information, not credentials or internal storage secrets.

The browser must communicate with the daemon's HTTP API. It must not communicate directly with FFmpeg, yt-dlp or S3.

## 12. Deployment

### Directory layout

```text
/opt/streamd/
└── streamd

/etc/streamd/
├── config.yaml
└── .env

/var/lib/streamd/
├── chunks/
└── state/
    └── uploads.db

/var/log/streamd/
└── streamd.log

/run/
└── streamd.sock

/etc/systemd/system/
└── streamd.service
```

Run the application under a dedicated Linux service user.

### systemd service

The service should:

- Start automatically on boot.
- Restart on unexpected failure.
- Run under the dedicated service account.
- Have access to its configuration, state, log and recording directories.
- Shut down gracefully, allowing workers to finalize their current state.
- Recover pending uploads on restart.

### Installation script

`scripts/install.sh` should:

1. Install required system dependencies, including FFmpeg.
2. Install yt-dlp.
3. Create the dedicated service user.
4. Create required directories.
5. Install the executable.
6. Create the configuration file if it does not exist.
7. Create the `.env` file if it does not exist.
8. Install the systemd unit.
9. Set directory ownership and permissions.
10. Enable and start the service.

### Update script

`scripts/update.sh` should:

1. Stop the service gracefully.
2. Replace the executable.
3. Preserve the existing configuration.
4. Preserve `.env`.
5. Preserve local chunks.
6. Preserve the SQLite upload queue.
7. Restart the service.
8. Verify that it is running.

Never overwrite or delete local pending recordings or the upload database during an application update.

## 13. Security

- Keep S3 credentials in `.env`.
- Do not expose credentials through the dashboard or HTTP API.
- Restrict permissions on configuration, secrets and state directories.
- Bind the control API to localhost by default.
- Use a reverse proxy and authentication if remote dashboard access is required.
- Validate stream IDs before using them in filesystem paths or object keys.
- Prevent path traversal.
- Do not allow arbitrary shell command execution through stream configuration.
- Use structured FFmpeg and yt-dlp arguments.

## 14. Out of scope

The following are not part of the initial implementation:

- Audio fingerprinting or OLAF integration.
- Media recognition.
- Analytics and reporting.
- Royalty calculations.
- Rights management.
- Distributed recording.
- Centralized multi-node orchestration.
- Database-backed user or organization management.
- Per-chunk metadata sidecar files.
- A separate microservice for uploads.

## 15. Initial acceptance criteria

The implementation is complete when:

1. `streamd` installs and runs as a systemd service.
2. Multiple streams can be recorded concurrently.
3. Streams can be configured independently.
4. FFmpeg records configurable chunks.
5. yt-dlp can resolve supported web sources.
6. Completed chunks are registered in SQLite.
7. Upload workers transfer chunks asynchronously.
8. Failed uploads are retried using persisted backoff state.
9. Pending uploads survive service restarts.
10. Local files are deleted only after confirmed successful uploads, when cleanup is enabled.
11. Each stream has one `meta.json` at its S3 root.
12. Metadata updates replace that same metadata object.
13. Recording objects follow the agreed date-based S3 structure.
14. No per-chunk sidecar files are generated.
15. The dashboard displays stream and uploader status.
16. CLI commands can manage streams and inspect logs.
17. Updating the application preserves configuration, secrets, recordings and the SQLite queue.
