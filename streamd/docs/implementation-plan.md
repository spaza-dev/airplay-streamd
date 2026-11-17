# streamd — Implementation Plan

**Status:** Complete
**Updated:** 2026-11-14
**Source spec:** `../../docs/streamd-requirements.md` (repository root)

## 1. Goal

Build `streamd`, a single-binary Go service that concurrently records internet streams with FFmpeg/yt-dlp, splits output into timed chunks, uploads chunks asynchronously to S3-compatible storage through a persistent SQLite queue, and exposes a Unix-socket CLI plus an embedded React dashboard.

## 2. Confirmed decisions

- Go toolchain: bootstrap **go1.27.4** into `~/.local/go` (no sudo).
- SQLite driver: **`modernc.org/sqlite`** (pure Go, no cgo/glibc dependency).
- Frontend: **React 16.14**, TypeScript, Vite, `azure-devops-ui` (peer constraint `^16.8.1`).
- Build Node: **nvm use 24** for every npm task; Node is build-time only.
- Recording: one long-lived FFmpeg **segment muxer** process per stream.
- Uploader tests: **real R2/S3** using `.env` at the repository root; isolated `streamd-test` stream/prefix.
- Deployment now: **user-mode daemon** for testing; systemd `install.sh`/`update.sh` prepared for later sudo execution.

## 3. Architecture

```text
                    ┌──────────────────────────────┐
  CLI (unix sock) → │  Go daemon                   │ → TCP / unix HTTP API + embedded UI
                    │                              │
                    │  StreamManager               │
                    │   └─ worker per stream       │
                    │        ├─ yt-dlp resolve     │
                    │        ├─ FFmpeg segmenter   │
                    │        └─ finalize chunk     │
                    │              ↓               │
                    │  SQLite upload queue         │
                    │   └─ upload worker pool ─────┼→ S3/R2 (chunks + meta.json)
                    │                              │
                    │  Logger + runtime state      │
                    └──────────────────────────────┘
```

Recording and uploading are independent. Recording never waits on storage.

## 4. Configuration

### 4.1 Loading

1. Load `.env` (default: same directory as config; overridable with `--env`).
2. Read YAML config (default `/etc/streamd/config.yaml`; `--config` override).
3. Expand `${VAR}` with `os.ExpandEnv`.
4. Apply defaults, validate service configuration.
5. Never log expanded secrets.
6. Open the SQLite stream store; YAML never defines streams.

### 4.2 Model

- `server`: `listen`, `socket`.
- `recording`: chunk seconds, output dir, ffmpeg/yt-dlp paths, reconnect delays, audio defaults, video defaults.
- `uploader`: enabled, queue DB, concurrency, retry policy, cleanup flag.
- `storage`: endpoint, region, bucket, keys, `force_path_style`.
- `logging`: level, file.
- `metrics`, `capacity`: sampling and capacity policy.
- `database`: `path` to the SQLite stream store.
- `discovery`: Radio Browser enable flag, base URL, timeout, cache, result cap.

Stream records live in SQLite, not YAML: `id`, `source`, `name`, `type`, `url`, `enabled`, optional `chunk_seconds`, optional `audio`/`video`, dynamic `metadata`, optional full `radio` station, timestamps.

### 4.3 Validation

- Stream ID: UUID; Radio Browser stations use `stationuuid`; custom streams get UUID v4; uniqueness; safe for paths/object keys.
- Type: `radio|tv|hls|direct|youtube|twitch|web`.
- URL scheme allowlist: `http, https, rtmp, rtmps, rtsp, udp, mms, srt`.
- Codec/container pairs: `libopus→ogg`, `aac→m4a/aac`, `libmp3lame→mp3`, `copy` only with compatible container.
- Bitrate: `^\d+k$`; sample rates/channels from allowlists; chunk seconds > 0.
- Storage values required only when `uploader.enabled`.

### 4.4 Persistence

Stream add/update/remove writes to SQLite at `database.path` (`PRAGMA user_version=1`). `config.yaml` is read-only service configuration; CLI and API never edit it directly.

## 5. Runtime state

Atomic JSON file `<queue_db_dir>/runtime.json`:

```json
{
  "stopped": ["stream-id"],
  "meta_created_at": {"stream-id": "2026-11-14T10:00:00Z"}
}
```

- `stopped` makes manual stops survive restarts.
- `meta_created_at` preserves `meta.json` `created_at` across updates.

## 6. Recording engine (`recorder.go`)

### 6.1 Worker lifecycle

1. Manager starts enabled streams that are not manually stopped.
2. Worker resolves URL when type needs yt-dlp.
3. Starts FFmpeg segment muxer.
4. Reads finalized segment lines from `-segment_list pipe:1`.
5. Moves finalized files to `chunks/<id>/YYYY-MM-DD/HHMMSS.<ext>`.
6. Registers each chunk in SQLite and wakes uploaders.
7. On FFmpeg exit: log, back off (10s…300s), re-resolve, restart.
8. On graceful shutdown: SIGTERM process group, flush final segment, wait.

### 6.2 FFmpeg strategy

- Input: structured argv only; no shell.
- HTTP/HLS reconnection flags for direct sources.
- Audio-only default (`-vn`); optional video.
- `-c:a copy` when `audio.codec: copy`; otherwise encode with configured profile.
- Segment output:
  - incoming: `chunks/<id>/.incoming/%Y%m%d%H%M%S.<ext>`
  - `-f segment -segment_time N -reset_timestamps 1 -strftime 1 -segment_list pipe:1 -segment_list_type csv`
- Startup removes stale `.incoming` partials.

### 6.3 yt-dlp

`yt-dlp --no-playlist --no-warnings --no-progress -g -f bestaudio/best <url>` (video-enabled streams use `best`), 60s timeout, first URL used, stderr logged.

## 7. Storage and uploader (`storage.go`)

### 7.1 Object layout

- Metadata: `{stream_id}/meta.json`
- Chunks: `{stream_id}/recordings/YYYY/MM/DD/HHMMSS.{ext}`

### 7.2 SQLite queue

Use the spec schema plus indexes. Statuses: `pending`, `uploading`, `failed`, `completed`. Transactional claim with conditional update so workers cannot duplicate. Retry state persisted in `attempts`, `next_attempt_at`, `last_error`.

### 7.3 Upload lifecycle

1. Chunk finalized and registered.
2. Worker claims entry in a transaction.
3. `PutObject` with deterministic key, content type, content length.
4. Success → `completed` + `uploaded_at`; delete local file when cleanup enabled.
5. Failure → `failed` with exponential backoff (30s→3600s), unlimited when `max_attempts: 0`.
6. Never delete a local file after a failed upload.

### 7.4 Startup recovery

- `uploading` → `failed` with immediate retry.
- Missing local files → `failed`, no retry, clear error.
- Verify bucket/endpoint lazily; unavailable storage must not block recording.
- Completed rows retained initially.

### 7.5 Metadata

- Upload on stream registration and on metadata/config updates.
- One object per stream; replace atomically via `PutObject`.
- Include `id`, `name`, `type`, `format`, `created_at`, `updated_at`, dynamic `metadata`.
- Exclude URLs, credentials, tokens.

## 8. HTTP API (`web.go`)

- `GET /` embedded SPA (fallback to `index.html` for client routes).
- `GET /health`
- `GET /control/streams`, `GET /control/streams/{id}`
- `POST /control/streams` (add), `PUT /control/streams/{id}` (update), `DELETE /control/streams/{id}` (remove)
- `POST /control/streams/{id}/start|stop|restart`
- `GET /control/logs?stream=&level=&search=&limit=`
- `GET /control/logs/stream/{id}`
- `GET /control/uploader/status`
- `GET /control/uploader/queue?status=&limit=`
- `GET /control/discovery/radio?name=&country=&limit=...`
- `GET /control/discovery/radio/{stationuuid}`

JSON emits runtime `running`, `stopped`, `error`, `last_activity`. Uploader responses omit credentials and local paths. HTTP and Unix-socket listeners share one handler; default bind is `127.0.0.1:8787`.

## 9. CLI (`cli.go`)

`streamd serve`, `status`, `stream list|show|search|add|update|remove|start|stop|restart`, `logs [stream]`, `config validate`. Global flags: `--config`, `--env`, `--json`. Client dials the configured Unix socket; no separate recording/upload process. `stream add` accepts flags or `--radio-uuid`; stream YAML is no longer supported.

## 10. Frontend (`web/`)

- React 16.14 + TS + Vite + `azure-devops-ui`, ADO light theme.
- Centered content, max width ~1400px, dense layout, minimal decoration.
- **Streams view:** table with name/ID, type, status, enabled, last activity, start/stop/restart; search, status/type filters, pagination; uploader status strip.
- **Stream drawer (`Panel`):** Overview, Metadata, Logs tabs.
- **Logs view:** service and per-stream logs with search/level filter.
- Add/Edit/Delete stream dialogs; Radio Browser search with one-click add; polling refresh.
- Build output embedded with `//go:embed all:web/dist`.

## 11. Deployment

- User-mode test configuration under a temporary/home prefix.
- `scripts/build.sh`: `nvm use 24`, `npm ci`, `npm run build`, Go build.
- `scripts/install.sh`: ffmpeg, yt-dlp, service user, dirs, binary, preserve config/`.env`, systemd unit, permissions, enable/start.
- `scripts/update.sh`: graceful stop, replace binary, preserve config/`.env`/chunks/DB, restart, verify.
- systemd: dedicated user, restart on failure, graceful SIGTERM, `EnvironmentFile=-/etc/streamd/.env`.
- `streamd.socket` provides `/run/streamd.sock` with `SocketMode=0660`, so the dedicated user can serve the control socket without write access to `/run`; the daemon inherits the listener via socket activation and still creates the socket itself when run standalone.
- Never overwrite configuration, secrets, recordings, or the upload DB during updates.

## 12. Security

- Credentials only in `.env` (mode `0640`, group-readable by the service account); never returned by API/dashboard.
- Control API bound to localhost by default.
- Validate IDs and URL schemes; prevent path traversal.
- Structured argv only; reject unsafe schemes.
- Restrict state/log/config permissions.

## 13. Testing strategy

- **Unit:** config validation/merge, ID and path safety, timestamp parsing, object keys, backoff, queue claim/retry/recovery, FFmpeg argv, meta serialization.
- **Integration:** uploader against in-process fake S3; restart recovery; cleanup semantics.
- **E2E (real R2/S3):** local generated audio source → chunks → queue → upload → verify objects and `meta.json` under `streamd-test`; verify cleanup.
- **UI:** `npm run build`; serve embedded assets; exercise API and dashboard endpoints.
- **CLI:** status/list/show/start/stop/restart/logs/config validate against running daemon.
- **Quality gates:** `go test ./...`, `go vet ./...`, full build with embedded assets.

## 14. Risks and mitigations

- **Old system Go:** bootstrap local Go 1.27.4; `build.sh` verifies version.
- **azure-devops-ui older peer deps:** pin React 16.14 and use classic JSX runtime; `skipLibCheck`.
- **FFmpeg segment finalize detection:** use `segment_list` pipe; only register finalized lines; discard `.incoming` on startup.
- **Midnight directory rollover:** move finalized files into date directories after finalize.
- **S3 compatibility:** configurable endpoint/path-style; avoid unsupported checksum modes.
- **No passwordless sudo:** user-mode run now; scripts for later install.

## 15. Acceptance criteria mapping

| Spec criterion | Implementation |
|---|---|
| 1 systemd install | `install.sh` + generated unit |
| 2 concurrent streams | worker per stream |
| 3 independent config | per-stream overrides |
| 4 configurable chunks | segment muxer + `chunk_seconds` |
| 5 yt-dlp sources | resolver for youtube/twitch/web |
| 6 SQLite registration | chunk finalize → queue |
| 7 async uploads | worker pool |
| 8 persisted retry | attempts/next_attempt/last_error |
| 9 restart recovery | `uploading` reset + retries |
| 10 safe cleanup | delete only after success |
| 11 one meta.json | `{id}/meta.json` |
| 12 metadata replace | same-key `PutObject` |
| 13 date-based keys | `{id}/recordings/YYYY/MM/DD/HHMMSS.ext` |
| 14 no sidecars | no per-chunk JSON |
| 15 dashboard | Streams/Logs + uploader strip |
| 16 CLI management | socket CLI |
| 17 update preserves state | `update.sh` guarantees |

## 16. Operations

User-mode run (no root required):

```bash
cd streamd
./scripts/build.sh
./streamd serve --config /tmp/streamd-config.yaml --env /tmp/streamd.env
./streamd status --config /tmp/streamd-config.yaml --env /tmp/streamd.env
```

Systemd install/update (requires root):

```bash
cd streamd
./scripts/build.sh
sudo ./scripts/install.sh          # installs units, preserves existing config/.env/state
sudo ./scripts/update.sh           # graceful stop, replace binary, restart, verify
systemctl status streamd streamd.socket
```

Installed paths: binary `/opt/streamd/streamd`, config `/etc/streamd/config.yaml`, secrets `/etc/streamd/.env`, state `/var/lib/streamd`, logs `/var/log/streamd`, socket `/run/streamd.sock`. Real upload verification requires the repo-root `.env` (or `/etc/streamd/.env`) to contain the R2/S3 endpoint, bucket and credentials.
