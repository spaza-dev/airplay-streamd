# streamd — Task List

**Status legend:** `[ ]` pending · `[~]` in progress · `[x]` done · `[!]` blocked

## Phase D — Documentation (start first)

- [x] **D1** Write `docs/implementation-plan.md`.
- [x] **D2** Write `docs/task-list.md`.
- [x] **D3** Update status/notes as each phase completes.

## Phase 0 — Tooling and skeleton

- [x] **T0.1** Bootstrap go1.27.4 into `~/.local/go`; verify `go version`.
- [x] **T0.2** Initialize `go.mod` (`module streamd`).
- [x] **T0.3** Add `scripts/build.sh` (`nvm use 24`, `npm ci`, `npm run build`, `go build`).
- [x] **T0.4** Add `config.yaml`, `.env.example`, `.gitignore`.
- [x] **T0.5** Create backend skeleton files and health build.

## Phase 1 — Config, logging, lifecycle

- [x] **T1.1** Config structs, defaults, `.env` load, `${VAR}` expansion.
- [x] **T1.2** Validation: IDs, types, URL schemes, codecs, containers, bitrate, rates, channels, chunks.
- [x] **T1.3** Per-stream override merge.
- [x] **T1.4** Atomic config save for stream changes.
- [x] **T1.5** Levelled logger: stderr, file, in-memory ring buffer.
- [x] **T1.6** Runtime state persistence (manual stop, metadata timestamps).
- [x] **T1.7** Main lifecycle, signal handling, graceful shutdown, CLI dispatch.
- [x] **T1.8** Unit tests for config/validation/merge.

## Phase 2 — Recorder

- [x] **T2.1** Stream manager with start/stop/restart and manual-stop persistence.
- [x] **T2.2** yt-dlp resolver (timeout, structured args, error logging).
- [x] **T2.3** FFmpeg argv builder (audio encode/copy, video toggle, reconnect flags).
- [x] **T2.4** Segment muxer process with `segment_list` reader.
- [x] **T2.5** Finalize → move to dated dir → stat → enqueue.
- [x] **T2.6** Reconnect with exponential backoff and backoff reset.
- [x] **T2.7** Startup cleanup of `.incoming` partials.
- [x] **T2.8** Unit tests for argv, filename/date parsing, backoff.

## Phase 3 — Storage and uploader

- [x] **T3.1** S3 client (endpoint, region, path-style, R2-safe upload options).
- [x] **T3.2** SQLite schema + indexes + connection pragmas (WAL, busy timeout).
- [x] **T3.3** Transactional claim and status transitions.
- [x] **T3.4** Upload worker pool with configurable concurrency.
- [x] **T3.5** Exponential backoff and max-attempt handling.
- [x] **T3.6** Startup recovery + missing-file handling.
- [x] **T3.7** Cleanup-after-upload.
- [x] **T3.8** `meta.json` publisher (create/update, preserve `created_at`).
- [x] **T3.9** Unit/integration tests with fake S3 and temp SQLite.

## Phase 4 — HTTP API and CLI

- [x] **T4.1** HTTP/unix listeners sharing one handler.
- [x] **T4.2** Health, streams list/show/start/stop/restart.
- [x] **T4.3** Stream add/update/remove endpoints.
- [x] **T4.4** Logs endpoints with filters.
- [x] **T4.5** Uploader status/queue endpoints (sanitized).
- [x] **T4.6** SPA serving with index fallback.
- [x] **T4.7** CLI socket client and commands (`status`, `stream …`, `logs`, `config validate`, `serve`).
- [x] **T4.8** CLI/API smoke tests.

## Phase 5 — Frontend

- [x] **T5.1** Vite/React 16.14/TS/`azure-devops-ui` scaffold.
- [x] **T5.2** ADO-style layout, theme, max-width, navigation.
- [x] **T5.3** API client and types.
- [x] **T5.4** Streams table with search/filter/pagination/actions.
- [x] **T5.5** Uploader status strip.
- [x] **T5.6** Stream details drawer (Overview/Metadata/Logs).
- [x] **T5.7** Logs view with filters.
- [x] **T5.8** Add/Edit/Delete dialogs.
- [x] **T5.9** Production build + embedding verified.

## Phase 6 — Deployment scripts

- [x] **T6.1** `scripts/build.sh` finalized and documented.
- [x] **T6.2** `scripts/install.sh` (deps, user, dirs, binary, config/.env, systemd, permissions, start).
- [x] **T6.3** `scripts/update.sh` (stop, replace, preserve state, restart, verify).
- [x] **T6.4** systemd unit template and validation.
- [x] **T6.5** Shell syntax checks / dry-run review.

## Phase 7 — Verification

- [x] **T7.1** `go test ./...` and `go vet ./...` pass.
- [x] **T7.2** Frontend production build passes.
- [x] **T7.3** User-mode daemon starts with local config.
- [x] **T7.4** E2E recording: local source → multiple chunks.
- [x] **T7.5** E2E upload to real R2/S3 under `streamd-test`; verify keys + `meta.json`. *(verified against R2: recordings and `meta.json` uploaded; virtual-hosted style works)*
- [x] **T7.6** Retry/recovery: stop daemon with pending upload, restart, confirm completion. *(verified against R2 for `pending` and `uploading` rows; both completed after restart)*
- [x] **T7.7** Cleanup: local file removed only after successful upload.
- [x] **T7.8** CLI commands verified against daemon.
- [x] **T7.9** Dashboard/API verified in browser/curl.
- [x] **T7.10** Final acceptance-criteria review. *(all criteria verified locally and against real R2)*

## Phase 8 — Handoff

- [x] **T8.1** Keep plan/task list current.
- [x] **T8.2** Document user-mode run commands and systemd install steps.
