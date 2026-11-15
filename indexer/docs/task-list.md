# Olaf Daily Indexer — Task List

**Status legend:** `[ ]` pending · `[~]` in progress · `[x]` done · `[!]` blocked

**Implementation status:** all implementation tasks are complete. Automated unit tests cover timeline, configuration, and settings validation. Integration, live-storage, and install/update verification remain before final acceptance.

## Phase 0 — Foundation

- [x] **T0.1** Initialize Go module and dependencies.
- [x] **T0.2** Add deployment configuration and `.env.example`.
- [x] **T0.3** Implement configuration loading and validation.
- [x] **T0.4** Implement lifecycle, logging, health, and graceful shutdown.
- [x] **T0.5** Implement SQLite initialization and embedded migrations.
- [x] **T0.6** Implement independent source/destination S3 clients.
- [x] **T0.7** Verify Olaf per-job isolation, identifiers, and offsets.

## Phase 1 — Job queue

- [x] **T1.1** Job creation and active-job deduplication.
- [x] **T1.2** Forced rebuild handling.
- [x] **T1.3** Transactional worker claim and bounded pool.
- [x] **T1.4** Retry with exponential backoff.
- [x] **T1.5** Cancellation and process-group termination.
- [x] **T1.6** Startup recovery of interrupted jobs.
- [x] **T1.7** Attempt and log history.

## Phase 2 — Timeline

- [x] **T2.1** Recording object listing and extension filtering.
- [x] **T2.2** Timezone-aware timestamp parsing.
- [x] **T2.3** Bounded-concurrency download and size verification.
- [x] **T2.4** `ffprobe` duration and format inspection.
- [x] **T2.5** Chronological ordering and duplicate timestamps.
- [x] **T2.6** Logical offsets, gaps, overlaps, midnight/DST handling.
- [x] **T2.7** Versioned `timeline.json` and checksum.
- [x] **T2.8** Timeline validation.
- [x] **T2.9** Timeline API summary/detail.
- [x] **T2.10** Timeline unit tests.

## Phase 3 — Olaf processing

- [x] **T3.1** Isolated workspace and Olaf config.
- [x] **T3.2** Safe subprocess runner.
- [x] **T3.3** `olaf cache --with-ids`.
- [x] **T3.4** `olaf store_cached`.
- [x] **T3.5** Validate LMDB and resource mapping.
- [ ] **T3.6** Integration tests with fake and real Olaf.

## Phase 4 — Publication

- [x] **T4.1** Immutable build prefix and build ID.
- [x] **T4.2** Upload and verify LMDB/timeline artifacts.
- [x] **T4.3** Generate and upload pointer `meta.json` last.
- [x] **T4.4** Preserve previous index on failed rebuild.
- [x] **T4.5** Remote build retention.
- [ ] **T4.6** Publication failure tests.

## Phase 5 — Discovery

- [x] **T5.1** Discovery configuration persistence and validation.
- [x] **T5.2** Stream enumeration and timezone lookup.
- [x] **T5.3** Date scope and lookback.
- [x] **T5.4** Grace-period readiness.
- [x] **T5.5** Completion-event readiness and idempotency.
- [x] **T5.6** Minimum recordings/duration checks.
- [x] **T5.7** Completed-index and active-job checks.
- [x] **T5.8** Dry run, maximum jobs per scan, scan history.
- [ ] **T5.9** Discovery tests.

## Phase 6 — API and UI

- [x] **T6.1** Job, timeline, discovery, event, stats, logs, health endpoints.
- [x] **T6.2** Storage configuration endpoints with secret masking.
- [x] **T6.3** Authentication for admin and recorder routes.
- [x] **T6.4** OpenAPI specification and Swagger UI.
- [x] **T6.5** Frontend scaffold and API client.
- [x] **T6.6** Overview, Jobs, Discovery, Logs, API, Settings views.
- [x] **T6.7** Job drawer with Overview, Timeline, Logs.
- [x] **T6.8** Embedded frontend build.

## Phase 7 — Deployment

- [x] **T7.1** `scripts/build.sh`.
- [x] **T7.2** `scripts/indexer.service`.
- [x] **T7.3** `scripts/install.sh`.
- [x] **T7.4** `scripts/update.sh`.
- [x] **T7.5** Script syntax checks and documentation.

## Phase 8 — Verification

- [x] **T8.1** `go test ./...` and `go vet ./...`.
- [x] **T8.2** Frontend production build.
- [ ] **T8.3** End-to-end fixture job with gaps and overlaps.
- [~] **T8.4** Real Olaf index and offset mapping verification.
- [ ] **T8.5** Discovery idempotency verification.
- [ ] **T8.6** Install/update preservation check.
- [~] **T8.7** Final acceptance review.

## Verification notes

- `go test ./...`, `go vet ./...`, and the production frontend build pass.
- The service starts with a temporary configuration; `/api/health`, `/api/stats`,
  `/api/discovery/config`, the embedded UI, and `/openapi.json` respond correctly.
- Olaf `2026.10.19` was verified with `help`, `config`, `cache --with-ids`,
  `store_cached`, `stats --verbose`, and `query`; identifiers appear in statistics and
  query output.
- Remaining manual checks: a complete fixture job through source/destination S3,
  publication-failure recovery, discovery idempotency, and install/update rollback.
