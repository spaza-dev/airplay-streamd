# streamd — Stream Capture Service

`streamd` is a Linux service that continuously records internet audio/video streams, splits them into date-based chunks, and uploads those chunks asynchronously to S3-compatible object storage (Cloudflare R2 or AWS S3).

It provides:

- A foreground daemon that owns all recording and upload workers.
- A Unix-socket CLI and a localhost HTTP control API.
- An embedded React/TypeScript dashboard.
- A SQLite-backed upload queue with persisted retry state.
- Per-stream configuration overrides and stream metadata published to object storage.

The authoritative development specification is in [`docs/streamd-requirements.md`](docs/streamd-requirements.md). Implementation notes and task tracking are in [`streamd/docs/implementation-plan.md`](streamd/docs/implementation-plan.md) and [`streamd/docs/task-list.md`](streamd/docs/task-list.md). The related indexer specification is in [`docs/indexer.md`](docs/indexer.md).

---

## Contents

- [Features](#features)
- [Architecture](#architecture)
- [Repository layout](#repository-layout)
- [Requirements](#requirements)
- [Quick start (user mode)](#quick-start-user-mode)
- [Configuration](#configuration)
- [Recording behavior](#recording-behavior)
- [Storage layout](#storage-layout)
- [Upload queue and retries](#upload-queue-and-retries)
- [CLI usage](#cli-usage)
- [HTTP API](#http-api)
- [Web dashboard](#web-dashboard)
- [Installation](#installation)
- [Updating](#updating)
- [Operations](#operations)
- [Building from source](#building-from-source)
- [Testing](#testing)
- [Security](#security)
- [Troubleshooting](#troubleshooting)
- [Verification status](#verification-status)

---

## Features

- Concurrent recording of multiple streams, one worker per stream.
- Configurable chunk duration globally and per stream.
- FFmpeg segment muxer output with deterministic chunk names.
- Direct FFmpeg sources: radio, TV, HLS, direct URLs.
- `yt-dlp` source resolution for YouTube, Twitch, and generic web pages.
- Audio encoding/copying and optional video recording.
- Local chunks written to `.incoming`, then moved to a dated directory when finalized.
- SQLite upload queue with transactional claims and persisted retry backoff.
- Uploader worker pool with configurable concurrency.
- Startup recovery for interrupted uploads and missing local files.
- Optional cleanup of local chunks only after confirmed upload success.
- One `meta.json` per stream at its S3 root, replaced in place on metadata updates.
- No per-chunk metadata sidecar files.
- CLI commands for stream management and logs.
- A read-only public `/api/streams` API with optional bearer authentication and Swagger UI.
- Embedded dashboard with streams, filters, drawer details, logs, and uploader indicators.
- Systemd service and socket activation for `/run/streamd.sock`.
- Install and update scripts that preserve configuration, secrets, chunks, and the queue.

---

## Architecture

```text
┌──────────────────────────────────────────────────────────────┐
│ streamd daemon                                               │
│                                                              │
│  Config / Runtime state                                      │
│      │                                                       │
│      ├── Stream Manager ──► FFmpeg segment worker per stream │
│      │                         │                              │
│      │                         ├── .incoming/<timestamp>.ext │
│      │                         └── chunks/<id>/<date>/*.ext │
│      │                                      │                │
│      │                                      ▼                │
│      ├── SQLite upload queue ◄──── finalize + enqueue         │
│      │        │                                               │
│      │        ▼                                               │
│      └── Uploader worker pool ──► S3 / R2                      │
│                                                              │
│  HTTP + Unix socket API ◄──── CLI / browser dashboard         │
└──────────────────────────────────────────────────────────────┘
```

The daemon owns all recording and upload processes. CLI commands only send control requests; they never launch FFmpeg, yt-dlp, or uploads themselves.

---

## Repository layout

```text
.
├── README.md
├── docs/
│   ├── indexer.md
│   └── streamd-requirements.md
└── streamd/
    ├── main.go                 # config, logging, runtime state, lifecycle
    ├── recorder.go             # stream manager, FFmpeg/yt-dlp workers
    ├── storage.go              # SQLite queue, S3 uploader, metadata publisher
    ├── web.go                  # HTTP/Unix API and embedded frontend
    ├── cli.go                  # CLI client and commands
    ├── config_test.go
    ├── storage_test.go
    ├── config.yaml             # default system configuration
    ├── .env.example            # R2/S3 credential template
    ├── scripts/
    │   ├── build.sh            # frontend + Go build
    │   ├── install.sh          # system install and systemd setup
    │   └── update.sh           # preserve state and replace binary
    ├── web/                    # React + TypeScript + azure-devops-ui
    └── docs/
        ├── implementation-plan.md
        └── task-list.md
```

---

## Requirements

### Runtime

- Linux with systemd for service installation.
- FFmpeg with `libopus`, `libx264`/`libx265`, and the required muxers.
- `yt-dlp` for YouTube/Twitch/web sources.
- Read/write access to the configured chunk, state, and log directories.
- Network access to the configured sources and S3-compatible endpoint.

### Build

- Go `1.27.4` (the build script bootstraps it to `$HOME/.local/go` if needed).
- Node.js 24 via `nvm` for the frontend build.
- npm.

The service itself runs without CGO (`CGO_ENABLED=0`) because it uses the pure-Go `modernc.org/sqlite` driver.

### S3 / R2

- An S3-compatible endpoint.
- A bucket.
- An access key ID and secret access key.
- Region: `auto` for Cloudflare R2, or the AWS region for S3.

---

## Quick start (user mode)

User mode is useful for development and testing without root or systemd.

```bash
cd streamd

# Build the frontend and Go binary.
./scripts/build.sh

# Create a local configuration for a generated test stream.
mkdir -p /tmp/streamd-demo/{chunks,state,www}
cat > /tmp/streamd-demo/config.yaml <<'YAML'
server:
  listen: "127.0.0.1:8788"
  socket: "/tmp/streamd-demo/streamd.sock"
recording:
  chunk_seconds: 5
  output_dir: "/tmp/streamd-demo/chunks"
  ffmpeg_path: "/usr/bin/ffmpeg"
  yt_dlp_path: "/usr/local/bin/yt-dlp"
  reconnect_delay_seconds: 2
  max_reconnect_delay_seconds: 10
  audio:
    codec: "libopus"
    container: "ogg"
    bitrate: "64k"
    sample_rate: 48000
    channels: 2
    vbr: true
  video:
    enabled: false
uploader:
  enabled: false
  queue_db: "/tmp/streamd-demo/state/uploads.db"
logging:
  level: "debug"
  file: "/tmp/streamd-demo/streamd.log"
streams:
  - id: "demo"
    name: "Demo Stream"
    type: "direct"
    url: "http://127.0.0.1:8099/live.ogg"
    enabled: true
    chunk_seconds: 5
    metadata:
      purpose: "demo"
YAML

# Generate a looping live OGG source on port 8099.
ffmpeg -hide_banner -loglevel error \
  -f lavfi -i "sine=frequency=440:duration=30" \
  -c:a libopus -b:a 64k -ar 48000 -ac 2 \
  -y /tmp/streamd-demo/www/source.ogg

ffmpeg -hide_banner -loglevel error -re -stream_loop -1 \
  -i /tmp/streamd-demo/www/source.ogg \
  -c copy -f ogg -listen 1 http://127.0.0.1:8099/live.ogg &
```

Start the daemon in another terminal:

```bash
./streamd serve --config /tmp/streamd-demo/config.yaml
```

Inspect it:

```bash
./streamd status --config /tmp/streamd-demo/config.yaml
./streamd stream list --config /tmp/streamd-demo/config.yaml
./streamd logs --config /tmp/streamd-demo/config.yaml --limit 20
find /tmp/streamd-demo/chunks -type f
```

The dashboard is available at `http://127.0.0.1:8788/`.

The demo intentionally disables the uploader. See [Installation](#installation) for a production setup with S3/R2.

---

## Configuration

### File locations

| Purpose | Default |
|---|---|
| Configuration | `/etc/streamd/config.yaml` |
| Secrets | `/etc/streamd/.env` |
| Local chunks | `/var/lib/streamd/chunks` |
| SQLite queue | `/var/lib/streamd/state/uploads.db` |
| Runtime state | `/var/lib/streamd/state/runtime.json` |
| Service log | `/var/log/streamd/streamd.log` |
| Control socket | `/run/streamd.sock` |
| HTTP API | `127.0.0.1:8787` |

`streamd serve --env` defaults to `.env` next to the configuration file. Any `${VAR}` reference in the YAML is expanded from the process environment after `.env` is loaded.

### Example `config.yaml`

```yaml
server:
  listen: "127.0.0.1:8787"
  socket: "/run/streamd.sock"
  admin_token: "${STREAMD_ADMIN_TOKEN}"

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

  - id: "youtube-channel"
    name: "Example YouTube Channel"
    type: "youtube"
    url: "https://www.youtube.com/@example"
    enabled: false
    metadata:
      platform: "YouTube"
      channel: "Example Channel"
```

### Environment variables

Create `.env` with restricted permissions:

```bash
STREAMD_ADMIN_TOKEN=
S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
S3_REGION=auto
S3_BUCKET=your-bucket
S3_ACCESS_KEY_ID=
S3_SECRET_ACCESS_KEY=
```

`STREAMD_ADMIN_TOKEN` is optional. When set, it protects the read-only `/api/*` endpoints; when empty, those endpoints are unauthenticated. The existing `/control/*` CLI and dashboard endpoints keep their current localhost/socket access model.

For AWS S3, set `S3_ENDPOINT` to the regional endpoint, for example `https://s3.eu-west-1.amazonaws.com`, and set `S3_REGION` to the bucket region, for example `eu-west-1`. `streamd` requires a non-empty `storage.endpoint` whenever the uploader is enabled.

`force_path_style`:

- `false` uses virtual-hosted style (`https://bucket.endpoint/key`). This works with Cloudflare R2 and AWS S3.
- `true` uses path style (`https://endpoint/bucket/key`). Use this for S3-compatible servers that require path style.

### Server settings

| Key | Default | Description |
|---|---|---|
| `server.listen` | `127.0.0.1:8787` | TCP address for the dashboard and API. |
| `server.socket` | `/run/streamd.sock` | Unix socket for CLI/API clients. |
| `server.admin_token` | empty | Optional token for the read-only `/api/*` endpoints. Empty disables authentication. |

The HTTP server binds to localhost by default. Do not expose it publicly without a reverse proxy and authentication.

### Recording settings

| Key | Default | Description |
|---|---|---|
| `recording.chunk_seconds` | `300` | Default chunk duration. |
| `recording.output_dir` | `/var/lib/streamd/chunks` | Local chunk root. |
| `recording.ffmpeg_path` | `/usr/bin/ffmpeg` | FFmpeg executable. |
| `recording.yt_dlp_path` | `/usr/local/bin/yt-dlp` | yt-dlp executable. |
| `recording.reconnect_delay_seconds` | `10` | Initial reconnect delay. |
| `recording.max_reconnect_delay_seconds` | `300` | Maximum reconnect delay. |
| `recording.audio.*` | see above | Defaults for all streams. |
| `recording.video.*` | see above | Defaults for all streams. |

Reconnect delay grows exponentially from the initial value to the maximum after a recording process exits unexpectedly. A session that lasts longer than two minutes resets the delay.

### Audio options

Allowed codecs: `libopus`, `aac`, `libmp3lame`, `copy`.

| Container | Accepted codecs |
|---|---|
| `ogg` | `libopus`, `copy` |
| `opus` | `libopus`, `copy` |
| `m4a` | `aac`, `copy` |
| `aac` | `aac`, `copy` |
| `mp3` | `libmp3lame`, `copy` |

- `bitrate` must match `^[0-9]+k$`, for example `64k`, `96k`, `128k`.
- `sample_rate` must be one of `8000`, `16000`, `22050`, `24000`, `32000`, `44100`, `48000`, `96000`.
- `channels` must be `1` or `2`.
- `vbr` applies to `libopus` (`-vbr on` or `-vbr off`).
- With codec `copy`, bitrate/sample-rate/channel validation is skipped.

### Video options

Allowed codecs: `libx264`, `libx265`, `copy`.

| Container | Accepted codecs |
|---|---|
| `mp4` | `libx264`, `libx265`, `copy` |
| `mkv` | `libx264`, `libx265`, `copy` |
| `webm` | `libx264`, `libx265`, `copy` |

When video is disabled, FFmpeg receives `-vn` and the output uses the audio container. When video is enabled, the output uses the video container.

### Stream settings

| Key | Required | Description |
|---|---|---|
| `id` | yes | 1–64 characters: letters, digits, `_`, `-`; must start with a letter or digit. |
| `name` | no | Display name; defaults to `id`. |
| `type` | yes | `radio`, `tv`, `hls`, `direct`, `youtube`, `twitch`, `web`. |
| `url` | yes | Source URL. |
| `enabled` | no | Starts automatically when the daemon starts. |
| `chunk_seconds` | no | Per-stream chunk duration; `0` uses the global default. |
| `audio` | no | Per-stream audio overrides. |
| `video` | no | Per-stream video overrides. |
| `metadata` | no | Arbitrary key/value metadata published in `meta.json`. |

Allowed URL schemes: `http`, `https`, `rtmp`, `rtmps`, `rtsp`, `udp`, `mms`, `srt`. Types `youtube`, `twitch`, and `web` require an `http` or `https` URL.

Per-stream `audio` and `video` objects override only the fields provided; unspecified fields inherit the global recording defaults.

Stream IDs are used in filesystem paths and S3 keys. They are validated before use, and path traversal is rejected.

### Source types

| Type | Recorder |
|---|---|
| `radio`, `tv`, `hls`, `direct` | FFmpeg reads the URL directly. HTTP reconnect flags are added for supported inputs. |
| `youtube`, `twitch`, `web` | `yt-dlp -g` resolves a playable media URL, then FFmpeg records it. |

yt-dlp resolution uses a 60-second timeout and logs stderr on failure. Video-enabled streams request `best`; audio-only streams request `bestaudio/best`.

---

## Recording behavior

For each running stream, `streamd` starts one long-lived FFmpeg process using the segment muxer:

```text
ffmpeg ... -f segment \
  -segment_time <chunk_seconds> \
  -reset_timestamps 1 \
  -strftime 1 \
  -segment_list pipe:1 \
  -segment_list_type csv \
  -segment_format <format> \
  <output_dir>/<stream_id>/.incoming/%Y%m%d%H%M%S.<ext>
```

When FFmpeg closes a segment, it emits a CSV line on stdout. `streamd` then:

1. Resolves the finalized file in `.incoming`.
2. Moves it to `<output_dir>/<stream_id>/YYYY-MM-DD/HHMMSS.<ext>`.
3. Discards zero-byte segments.
4. Registers the file in the SQLite upload queue with an object key.
5. Updates the stream's last-activity time.

Chunk filenames use local time. If two segments share a timestamp, a `-1`, `-2`, ... suffix is added to both the local file and object key.

On graceful shutdown, the final segment is flushed and enqueued. On startup, leftover files in `.incoming` from a hard crash are removed.

---

## Storage layout

### Local filesystem

```text
<recording.output_dir>/
└── <stream_id>/
    ├── .incoming/
    │   └── YYYYMMDDHHMMSS.<ext>
    └── YYYY-MM-DD/
        └── HHMMSS.<ext>
```

### S3-compatible object layout

```text
<bucket>/
└── <stream_id>/
    ├── meta.json
    └── recordings/
        └── YYYY/
            └── MM/
                └── DD/
                    └── HHMMSS.<ext>
```

Rules:

- Exactly one metadata object per stream: `<stream_id>/meta.json`.
- Recording objects are date-based.
- No per-chunk sidecar files are generated.
- Metadata updates replace the same `meta.json` key.
- `created_at` is persisted in runtime state and preserved across metadata updates as long as that state file survives.
- The stream source URL is not published in metadata.

### `meta.json`

```json
{
  "id": "capital-fm-ke",
  "name": "Capital FM Kenya",
  "type": "radio",
  "format": "ogg",
  "created_at": "2026-11-14T09:08:00Z",
  "updated_at": "2026-11-14T09:08:32Z",
  "metadata": {
    "country": "Kenya",
    "city": "Nairobi"
  }
}
```

---

## Upload queue and retries

The upload queue is a SQLite database at `uploader.queue_db`.

Statuses:

| Status | Meaning |
|---|---|
| `pending` | Ready to be claimed. |
| `uploading` | Claimed by an uploader worker. |
| `failed` | Upload failed; retry time or permanent error. |
| `completed` | Upload succeeded. |

Behavior:

- A chunk is registered only after FFmpeg finalizes it.
- Claims are transactional and serialized inside the daemon.
- The queue stores attempts, next attempt time, last error, file size, object key, and timestamps.
- Retry delay starts at `initial_interval_seconds` and doubles up to `max_interval_seconds`.
- `max_attempts: 0` retries indefinitely.
- `max_attempts > 0` marks the item permanently failed once the attempt count reaches the limit.
- On startup, rows left in `uploading` are reset to `failed` with an immediate retry.
- Pending and retryable failed rows are checked for their local file; if it is missing, the row is permanently failed.
- Local chunks are deleted only after a confirmed successful upload, and only when `cleanup_after_upload: true`.

Runtime state is stored in `runtime.json` next to the queue database. It tracks manual stop flags and metadata `created_at` timestamps.

---

## CLI usage

Every command supports these flags:

| Flag | Default | Description |
|---|---|---|
| `--config` | `/etc/streamd/config.yaml` | Configuration file. |
| `--env` | `.env` next to config | Environment/secrets file. |
| `--json` | `false` | Emit JSON where supported. |

> **Flag order:** flags must appear before positional arguments. This is standard Go flag parsing. For example, use `streamd stream show --config /etc/streamd/config.yaml capital-fm-ke`, not `streamd stream show capital-fm-ke --config ...`.

### Service commands

```bash
streamd serve --config /etc/streamd/config.yaml --env /etc/streamd/.env
streamd status
streamd config validate
```

`serve` runs in the foreground. Use systemd for background operation.

### Stream commands

```bash
streamd stream list
streamd stream list --json

streamd stream show capital-fm-ke

streamd stream add \
  --id capital-fm-ke \
  --name "Capital FM Kenya" \
  --type radio \
  --url "https://example.com/stream" \
  --enabled \
  --metadata country=Kenya \
  --metadata city=Nairobi

streamd stream update --name "Capital FM Kenya (New)" capital-fm-ke
streamd stream update --enabled false capital-fm-ke
streamd stream update --chunk-seconds 600 capital-fm-ke

streamd stream start capital-fm-ke
streamd stream stop capital-fm-ke
streamd stream restart capital-fm-ke
streamd stream remove capital-fm-ke
```

Stream add/update flags:

| Flag | Description |
|---|---|
| `--id` | Stream ID (add only; positional ID also accepted). |
| `--name` | Display name. |
| `--type` | Source type. |
| `--url` | Source URL. |
| `--enabled` | Add: boolean flag. Update: literal `true` or `false`. |
| `--chunk-seconds` | Per-stream chunk duration. |
| `--file` | YAML stream definition, or `-` for stdin. |
| `--metadata` | Repeatable `key=value`. |

YAML stream definition example:

```bash
streamd stream add --id radio-one --file - <<'YAML'
id: radio-one
name: Radio One
type: radio
url: https://example.com/radio.mp3
enabled: true
chunk_seconds: 300
metadata:
  country: Kenya
YAML
```

The stream ID is required via `--id` or a positional argument, even when `--file` is used. If both are supplied, `--id` wins.

### Logs

```bash
streamd logs
streamd logs capital-fm-ke
streamd logs --level warn --limit 50
streamd logs --search "upload failed" --limit 20
```

Log flags: `--level` (`debug`, `info`, `warn`, `error`), `--search`, `--limit`.

### Configuration validation

```bash
streamd config validate
```

This loads the configuration and `.env`, then prints the validated streams and exits non-zero on error.

---

## HTTP API

The API is available on `server.listen` and the Unix socket. The dashboard uses the same endpoints.

All API responses are JSON. Unknown `/control/...` paths return `404`.

### Health

```http
GET /health
```

```json
{"status":"ok","uptime_seconds":42,"version":"dev"}
```

### Status

```http
GET /control/status
```

Returns version, uptime, stream count, and uploader status.

### Read-only public API

```http
GET /api/streams
```

Returns overview data for streams with `enabled: true`. The response intentionally omits `chunk_seconds`, metadata, uploader data, and queue state.

```json
{
  "streams": [
    {
      "id": "capital-fm-ke",
      "name": "Capital FM Kenya",
      "status": "running",
      "type": "radio",
      "url": "https://example.com/stream",
      "enabled": true,
      "last_activity": "2026-11-16T10:05:00Z",
      "audio": {
        "codec": "libopus",
        "container": "ogg",
        "bitrate": "96k",
        "sample_rate": 48000,
        "channels": 2,
        "vbr": true
      },
      "video": {
        "enabled": false,
        "codec": "libx264",
        "bitrate": "2500k",
        "container": "mp4"
      }
    }
  ]
}
```

`status` is one of `running`, `stopped`, `error`, or `idle`. An `error` field is included when the stream has an active recording error. `last_activity` is in-memory and resets when the daemon restarts.

Authentication is optional and controlled by `server.admin_token` (`STREAMD_ADMIN_TOKEN`). When configured, send either:

```bash
curl -H 'Authorization: Bearer <token>' http://127.0.0.1:8787/api/streams
curl -H 'X-Admin-Token: <token>' http://127.0.0.1:8787/api/streams
```

Swagger UI and the OpenAPI document are available at:

```text
GET /docs
GET /openapi.json
```

### Streams

```http
GET    /control/streams
GET    /control/streams/{id}
POST   /control/streams
PUT    /control/streams/{id}
DELETE /control/streams/{id}
POST   /control/streams/{id}/start
POST   /control/streams/{id}/stop
POST   /control/streams/{id}/restart
```

Add/update request body:

```json
{
  "id": "capital-fm-ke",
  "name": "Capital FM Kenya",
  "type": "radio",
  "url": "https://example.com/stream",
  "enabled": true,
  "chunk_seconds": 300,
  "audio": {
    "codec": "libopus",
    "container": "ogg",
    "bitrate": "96k",
    "sample_rate": 48000,
    "channels": 2,
    "vbr": true
  },
  "metadata": {
    "country": "Kenya"
  }
}
```

Unknown JSON fields are rejected with `400 Bad Request`. `audio` and `video` are optional.

Examples:

```bash
curl http://127.0.0.1:8787/control/streams
curl http://127.0.0.1:8787/control/streams/capital-fm-ke

curl -X POST http://127.0.0.1:8787/control/streams/capital-fm-ke/stop
curl -X POST http://127.0.0.1:8787/control/streams/capital-fm-ke/start
curl -X POST http://127.0.0.1:8787/control/streams/capital-fm-ke/restart
```

Via the Unix socket:

```bash
curl --unix-socket /run/streamd.sock http://streamd/health
curl --unix-socket /run/streamd.sock http://streamd/control/streams
```

### Logs

```http
GET /control/logs?stream=&level=&search=&limit=
GET /control/logs/stream/{id}?level=&search=&limit=
```

Example:

```bash
curl 'http://127.0.0.1:8787/control/logs?level=warn&limit=50'
```

### Uploader

```http
GET /control/uploader/status
GET /control/uploader/queue?status=&limit=
```

Status example:

```json
{
  "enabled": true,
  "concurrency": 4,
  "active": 1,
  "counts": {
    "pending": 2,
    "uploading": 1,
    "failed": 0,
    "completed": 120
  }
}
```

Queue responses expose operational fields only: queue ID, stream ID, local file basename, object key, size, status, attempts, next attempt time, last error, created time, uploaded time. Local absolute paths and credentials are not exposed.

---

## Web dashboard

The dashboard is embedded in the binary and served at `/`.

Main views:

- **Streams**: name, ID, type, recording state, enabled state, last activity, start/stop/restart actions, search, type/status filters, pagination.
- **Stream details drawer**: Overview, Metadata, Logs.
- **Logs**: service-wide or per-stream, with level and search filters.
- **Uploader status strip**: pending, uploading, failed/retrying, completed, active uploads.

The UI uses React 16.14, TypeScript, Vite, and `azure-devops-ui` with an Azure DevOps-inspired light layout.

---

## Installation

### Automated installation (Debian/Ubuntu + systemd)

The installer must run as root.

```bash
cd streamd
./scripts/build.sh
sudo ./scripts/install.sh
```

`install.sh` performs these steps:

1. Installs `ffmpeg`, `curl`, and CA certificates with `apt-get`.
2. Installs `yt-dlp` to `/usr/local/bin/yt-dlp` if missing.
3. Creates the `streamd` system user and group.
4. Creates `/opt/streamd`, `/etc/streamd`, `/var/lib/streamd`, and `/var/log/streamd`.
5. Installs the binary to `/opt/streamd/streamd`.
6. Creates `/etc/streamd/config.yaml` if missing.
7. Creates `/etc/streamd/.env` if missing, using `streamd/.env`, the repo-root `.env`, or `.env.example`.
8. Installs `streamd.service` and `streamd.socket`.
9. Sets permissions:
   - `/etc/streamd`: `0770 root:streamd`
   - `config.yaml`: `0660 root:streamd`
   - `.env`: `0640 root:streamd`
   - state and log directories: `0750 streamd:streamd`
10. Enables the socket and service, starts them, and verifies the service is active.

The service user can read `.env` and write `config.yaml` (for CLI/API stream changes). Users outside `root` and the `streamd` group cannot read the secrets.

If the installer copies `.env.example`, the uploader will fail validation until credentials are filled in. Edit the file and restart:

```bash
sudo editor /etc/streamd/.env
sudo systemctl restart streamd
```

Verify:

```bash
systemctl status streamd streamd.socket
curl http://127.0.0.1:8787/health
sudo -u streamd /opt/streamd/streamd status --config /etc/streamd/config.yaml
```

### Manual installation

Use this path for non-Debian systems or when you need custom paths.

1. Create the service user and directories:

```bash
sudo useradd --system --home-dir /var/lib/streamd --shell /usr/sbin/nologin streamd

sudo install -d -m 0755 /opt/streamd
sudo install -d -m 0770 -o root -g streamd /etc/streamd
sudo install -d -m 0750 -o streamd -g streamd \
  /var/lib/streamd /var/lib/streamd/chunks /var/lib/streamd/state /var/log/streamd
```

2. Install the binary and configuration:

```bash
sudo install -m 0755 streamd /opt/streamd/streamd
sudo install -m 0660 -o root -g streamd config.yaml /etc/streamd/config.yaml
sudo install -m 0640 -o root -g streamd .env /etc/streamd/.env
```

3. Install `streamd.socket` at `/etc/systemd/system/streamd.socket`:

```ini
[Unit]
Description=streamd control socket
PartOf=streamd.service

[Socket]
ListenStream=/run/streamd.sock
SocketMode=0660
SocketUser=streamd
SocketGroup=streamd
RemoveOnStop=true

[Install]
WantedBy=sockets.target
```

4. Install `streamd.service` at `/etc/systemd/system/streamd.service`:

```ini
[Unit]
Description=streamd stream capture service
After=network-online.target streamd.socket
Wants=network-online.target
Requires=streamd.socket

[Service]
Type=simple
User=streamd
Group=streamd
WorkingDirectory=/var/lib/streamd
ExecStart=/opt/streamd/streamd serve --config /etc/streamd/config.yaml --env /etc/streamd/.env
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
KillSignal=SIGTERM
LimitNOFILE=65536
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=/var/lib/streamd /var/log/streamd
EnvironmentFile=-/etc/streamd/.env

[Install]
WantedBy=multi-user.target
```

5. Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable streamd.socket streamd
sudo systemctl start streamd.socket
sudo systemctl restart streamd
sudo systemctl status streamd streamd.socket
```

### Socket activation

`streamd.socket` creates `/run/streamd.sock` owned by `streamd:streamd` with mode `0660` and passes the listening file descriptor to the service. This keeps the documented socket path while allowing the service to run as the unprivileged `streamd` user.

When `streamd` is started outside systemd, it creates the configured Unix socket itself. Use a writable path such as `/tmp/streamd.sock` for user-mode runs.

---

## Updating

```bash
cd streamd
./scripts/build.sh
sudo ./scripts/update.sh
```

`update.sh`:

1. Stops `streamd` gracefully.
2. Replaces `/opt/streamd/streamd`.
3. Leaves `/etc/streamd/config.yaml`, `/etc/streamd/.env`, local chunks, and the SQLite queue untouched.
4. Reloads systemd, starts the service, and verifies it is active.

Never delete or replace `uploads.db`, `runtime.json`, or pending files during an update.

---

## Operations

### Logs

```bash
journalctl -u streamd -f
tail -f /var/log/streamd/streamd.log
streamd logs --level warn --limit 100
```

Logs are written to stderr (captured by journald), to the configured log file, and to an in-memory ring buffer used by the dashboard/API.

### Start, stop, and restart

```bash
sudo systemctl start streamd
sudo systemctl stop streamd
sudo systemctl restart streamd
```

Stopping via systemd sends `SIGTERM`. The daemon stops recording workers, flushes final segments, closes the API, and closes the uploader database.

### Inspect the queue

```bash
sqlite3 /var/lib/streamd/state/uploads.db \
  'select status, count(*) from uploads group by status;'

sqlite3 /var/lib/streamd/state/uploads.db \
  'select id, stream_id, status, attempts, last_error from uploads order by id desc limit 20;'
```

### Backup

Stop the service before copying the SQLite database to get a consistent snapshot:

```bash
sudo systemctl stop streamd
sudo tar czf streamd-backup-$(date +%Y%m%d).tgz \
  /etc/streamd /var/lib/streamd
sudo systemctl start streamd
```

---

## Building from source

### Full build

```bash
cd streamd
./scripts/build.sh
```

`build.sh`:

- Uses `GO_BIN` (default `$HOME/.local/go/bin/go`).
- Downloads Go `1.27.4` to `$HOME/.local/go` if missing.
- Loads `nvm` and selects Node 24 if available.
- Runs `npm ci` when `web/package-lock.json` exists, otherwise `npm install`.
- Runs the TypeScript check and Vite production build.
- Builds the Go binary with `CGO_ENABLED=0`, `-trimpath`, stripped symbols, and an embedded version.

Useful overrides:

```bash
GO_VERSION=1.27.4 GO_BIN=/usr/local/go/bin/go VERSION=1.2.3 ./scripts/build.sh
```

The binary embeds `web/dist`, so always build the frontend before the Go binary. `scripts/build.sh` does this automatically.

### Frontend development

```bash
cd streamd/web
nvm use 24
npm install
npm run dev
```

Vite serves the UI on port `5173` and proxies `/control` and `/health` to `http://127.0.0.1:8787`.

### Go only

```bash
cd streamd
CGO_ENABLED=0 "$HOME/.local/go/bin/go" build -trimpath \
  -ldflags="-s -w -X main.version=dev" -o streamd .
```

---

## Testing

### Automated checks

```bash
cd streamd

"$HOME/.local/go/bin/go" vet ./...
"$HOME/.local/go/bin/go" test ./...
"$HOME/.local/go/bin/go" test -race ./...

cd web
nvm use 24
npm run build
```

Covered by the Go tests:

- Configuration loading, `${VAR}` expansion, `.env` loading, validation, and per-stream merge.
- Credential redaction when the configuration is saved.
- FFmpeg argument construction and source redaction.
- Reconnect backoff and upload retry timing.
- SQLite queue enqueue/claim/complete/fail/recover and concurrent queue operations.
- Chunk finalization, object-key generation, collision handling, and empty-chunk discard.
- Uploader success against an in-process fake S3, content types, cleanup, and `meta.json`.

### Local end-to-end check

Use the user-mode demo in [Quick start](#quick-start-user-mode), then:

```bash
find /tmp/streamd-demo/chunks -type f
./streamd status --config /tmp/streamd-demo/config.yaml
```

### Real S3 / R2 check

Use a dedicated test prefix/stream ID, for example `streamd-test`, and short chunks. Verify in your S3 console or with the AWS CLI:

```bash
aws --endpoint-url "$S3_ENDPOINT" s3 ls "s3://$S3_BUCKET/streamd-test/"
aws --endpoint-url "$S3_ENDPOINT" s3 cp \
  "s3://$S3_BUCKET/streamd-test/meta.json" -
```

Expected objects:

```text
streamd-test/meta.json
streamd-test/recordings/YYYY/MM/DD/HHMMSS.ogg
```

---

## Security

- S3/R2 credentials live in `.env`, never in `config.yaml` or API responses.
- When stream changes rewrite `config.yaml`, access keys are replaced with `${S3_ACCESS_KEY_ID}` and `${S3_SECRET_ACCESS_KEY}` placeholders. Real values remain only in memory and `.env`.
- Installed `.env` is `0640 root:streamd`; only root and the service group can read it.
- The HTTP API binds to `127.0.0.1` by default.
- The Unix socket is `0660`.
- `server.admin_token` optionally protects the read-only `/api/*` endpoints. When it is empty, `/api/streams` is public and exposes stream source URLs to anyone who can reach the listener.
- Stream IDs are validated with `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`.
- URL schemes are allow-listed.
- FFmpeg and yt-dlp are invoked with structured argument arrays; no shell is used.
- Stream IDs are used in paths and S3 keys only after validation.
- Use a reverse proxy with authentication if remote dashboard access is required.
- Keep the service, `.env`, and state directories out of version control.

If you suspect a credential leak, rotate the R2/S3 key, update `.env`, and restart the service.

---

## Troubleshooting

### `storage.access_key_id and storage.secret_access_key are required when the uploader is enabled`

The uploader is enabled but credentials are missing. Add them to `.env` or disable the uploader:

```yaml
uploader:
  enabled: false
```

### `dial unix /run/streamd.sock: connect: no such file or directory`

The service or socket unit is not running:

```bash
systemctl status streamd streamd.socket
sudo systemctl start streamd.socket
sudo systemctl restart streamd
```

### `permission denied` on `/run/streamd.sock`

The socket is owned by `streamd:streamd` with mode `0660`. Run the CLI as root, run it as `streamd`, or add your user to the `streamd` group and log in again.

### FFmpeg not found

Set the correct path:

```yaml
recording:
  ffmpeg_path: "/usr/bin/ffmpeg"
```

Verify with `ffmpeg -version`. Ensure the installed FFmpeg includes the codecs/muxers you configured.

### yt-dlp source resolution fails

- Verify `yt-dlp --version`.
- Update yt-dlp regularly.
- Check that the URL is supported and accessible from the server.
- For video-enabled streams, yt-dlp requests `best`; for audio-only it requests `bestaudio/best`.
- See the stream's FFmpeg/yt-dlp errors in the dashboard or `streamd logs <id>`.

### No chunks appear

Check:

```bash
streamd stream show --config /etc/streamd/config.yaml <id>
streamd logs --config /etc/streamd/config.yaml --level debug --limit 100
```

Common causes: source unreachable, wrong URL scheme, FFmpeg codec/container mismatch, or the stream is disabled/stopped.

### Uploads fail with 403/404

Check bucket, endpoint, region, keys, and path style. For S3-compatible servers that require path style:

```yaml
storage:
  force_path_style: true
```

For R2, `region: auto` and `force_path_style: false` work with virtual-hosted style. Path style is also supported.

### `.incoming` files remain after a crash

They are incomplete and are removed on the next daemon start. A graceful stop flushes the final segment before exit.

### Queue shows `failed` with `local file missing`

The uploader found a queue row whose local chunk no longer exists. It marks the row permanently failed and does not retry. Restore the file if you need to recover it, or remove the row after inspection.

### `database is locked`

Queue writes are serialized inside the daemon. If an external process holds a write transaction, wait or close it. The database uses WAL and a busy timeout.

### Metadata did not update

Metadata publishing happens asynchronously in the uploader. Check that the uploader is enabled and inspect logs for `published metadata` or `publish metadata failed`. The object key is always `<stream_id>/meta.json`.

### Service fails to start

```bash
journalctl -u streamd -n 100 --no-pager
streamd config validate --config /etc/streamd/config.yaml --env /etc/streamd/.env
```

Check `.env` permissions, configuration validation, port `8787`, and socket permissions.

---

## Verification status

The current implementation has been verified with:

- Unit and race tests for configuration, queue, uploader, finalization, retries, and recovery.
- A local live-source end-to-end run through FFmpeg segments, SQLite queue, uploads, cleanup, and metadata publication.
- Real Cloudflare R2 uploads under the `streamd-test/` prefix using virtual-hosted style (`force_path_style: false`).
- Real `meta.json` creation and replacement with preserved `created_at`.
- Real restart recovery for both `pending` and `uploading` queue rows.
- Local chunk cleanup after confirmed uploads.
- CLI and HTTP/Unix-socket API smoke tests.
- Embedded dashboard production build and asset serving.
- Systemd unit and socket activation validation.

Test objects created during R2 verification remain under the isolated `streamd-test/` prefix and can be deleted when no longer needed.
