# Upstream v0.3.5 comparison and preservation research

## Snapshot

- Fork remote: `origin` → `Guciliang/91`; upstream remote: `upstream` → `nianzhibai/91`.
- Fork `main` and `origin/main`: `ff002d6673f0de97b9081ea2d29f125d471f7a80`.
- Previous merged upstream baseline / merge base: `915c6e9e800c73a85a585621932ccb78894dc7b3`.
- Upstream `main` and release tag `v0.3.5`: `c5e358c61decc4566f3c0793bdf4a458db5a1394`.
- Divergence from the merge base: 23 upstream-only commits and 20 fork-only commits. The incoming upstream diff is 228 files, +18,969 / -6,760 lines. Eighteen changed paths overlap between sides.
- The merge comparison used `git merge-tree --write-tree HEAD upstream/main`; it did not change the index or working tree and reported four content conflicts.

## Release-level change groups

The v0.3.5 tag message highlights crawler v3, drive detail state changing from polling to SSE, retrying failed directories at the end of scans, fixing a missing-card issue when navigating back from playback, and general drive-request/reliability improvements. The commit/file history adds these implementation areas:

1. **Crawler execution and durable tasks:** replace the v2 one-shot event stream with a v3 command/response protocol and persistent crawler tasks; add feeds, import revisions, task finalization/time-limit behavior, durable Telegram upload cleanup, and portable crawler finishing tests.
2. **Drive resource and status synchronization:** add shared drive-generation actions, versioned snapshots, drive events/SSE, shared frontend resources, and less idle polling; retain scan/generation lifecycle, cancellation, and upload behavior.
3. **Catalog and generated media:** retry failed directories after scans; repair reserved color metadata; backfill missing durations; report combined scan skips/cleaned videos; expand catalog migrations and tests.
4. **Frontend navigation and presentation:** unify Android back navigation, prevent hover feedback on touch devices, improve retained listing/grid synchronization, update app icons, and refine unavailable-drive/empty-state layouts.
5. **Operations and build reliability:** make Telegram upload cleanup recoverable, keep routine requests out of logs, and include frontend build scripts in the Docker frontend stage.

## Crawler protocol boundary (scope is only this repository)

- Fork v0.3.4 code currently accepts `crawler.v1` and `crawler.v2`; v2 scripts receive a job JSON and emit JSON event lines. See `backend/docs/CRAWLER_PROTOCOL.md` and `backend/internal/drives/scriptcrawler/metadata.go` on the fork tip.
- The admin UI/API imports a script by upload or URL, validates it, and stores it in the server data directory's `crawler-scripts/`. It retains the source URL for an explicit refresh action; crawler execution uses the stored script. No crawler script source is tracked in this repository.
- Upstream v0.3.5 introduces only `crawler.v3`, explicitly rejects older protocol declarations, and defines a persistent process reading `discover` / `resolve` / `stop` commands from stdin and writing typed responses to stdout. `CRAWLER_FEEDS` is also supported. The URL-import workflow remains present.
- Accept the upstream v3 contract in this repository. Do not update, inspect, or migrate the separate crawler-script project or deployed data in this task. Report only the compatibility precondition: imported scripts must speak v3; changes to their external source belong to a separate task.

## Direct conflicts from read-only merge simulation

1. **`backend/cmd/server/drives.go`**
   - Fork-only behavior in the merge: per-drive proxy propagation, Crypt wrapping across supported drives, WebDAV `.strm` outside-root retry/configuration, and drive capability classification.
   - Incoming behavior: crawler v3 task setup and progress, generation/status notifications, duration-backfill lifecycle, revised graceful stop/cancel boundaries, and drive event integration.
   - Resolution requirement: retain both sets of behavior. In particular, preserve proxy/Crypt/STRM construction while integrating v3 crawler configuration, event notifications, and generation ownership/cancellation.

2. **`backend/go.mod`**
   - Fork dependency differences include Quark/Rclone support and upgraded `x/crypto`, `x/net`, and `x/sys`; vendored metadata must continue to include the fork's required modules.
   - Upstream adds/promotes dependencies for v3/task, event, and drive-view functionality, including `google/uuid` and its dependency graph.
   - Resolution requirement: combine the module graph intentionally and keep `go.sum` and `vendor/modules.txt` consistent. Do not use a broad vendor regeneration as an unreviewed shortcut.

3. **`backend/internal/api/admin_drives.go`**
   - Fork adds WebDAV `.strm` outside-root state to drive-list responses.
   - Upstream extracts typed drive config/runtime/stats DTOs and adds versioned snapshot responses.
   - Resolution requirement: retain the WebDAV-specific field in the new DTO and preserve incoming snapshot/resource behavior and frontend contract.

4. **`backend/internal/catalog/scan_duplicates.go`**
   - Fork wraps complete immediate write transactions in bounded, context-aware SQLite busy retries.
   - Upstream emits drive-event notifications after successful catalog writes.
   - Resolution requirement: preserve rollback/connection-release-before-retry semantics and emit notifications only after committed writes; keep the contention regression test unchanged.

## All shared changed paths

The 18 paths changed by both branches are:

- `backend/cmd/server/drives.go` — direct conflict.
- `backend/go.mod` — direct conflict.
- `backend/internal/api/admin_drive_operations.go`.
- `backend/internal/api/admin_drives.go` — direct conflict.
- `backend/internal/api/admin_test.go`.
- `backend/internal/catalog/catalog.go`.
- `backend/internal/catalog/scan_duplicates.go` — direct conflict.
- `backend/internal/crawlerupload/migrator.go`.
- `backend/internal/drives/scriptcrawler/crawler_test.go`.
- `backend/internal/fingerprint/worker.go`.
- `backend/internal/preview/ffmpeg.go`.
- `backend/internal/preview/ffmpeg_test.go`.
- `backend/internal/preview/worker_test.go`.
- `src/admin/DrivesPage.tsx`.
- `src/admin/api.ts`.
- `src/admin/drive/constants.ts`.
- `src/components/VideoPlayer.tsx`.
- `tests/adminDriveForm.test.ts`.

These are semantic review points even if Git auto-merges them.

## Fork behavior preservation source

Use the prior approved feature inventory in `.trellis/tasks/archive/2026-09/09-29-upstream-review/research/upstream-diff.md` as the detailed baseline for Quark Crypt/decryption/cache/range support and crawler uploads; per-drive Crypt/proxy; STRM; preview-link refresh; redirect User-Agent; plaintext playback/seek APIs; cross-platform asset-directory leases; toast behavior; and documentation. Add the subsequent `99166cc` SQLite busy-retry fix to that inventory. Do not treat upstream file deletion or relocation as evidence that a live behavior is retired; check call sites, data migrations, and tests.

## Worktree protection and integration boundary

- Before any approved live merge, record `HEAD`, remote tips, merge base, and a hash/status baseline of every current dirty or untracked user path.
- Existing local `.gitattributes`, `.agents/`, `.pi/`, and `.trellis/` content is user work. Do not stash, clean, reset, broadly stage, or include it in the integration commit without separate authorization.
- The current source worktree remained unchanged during remote fetch and merge-tree research. Upstream changes do not include the current untracked `.trellis/` planning/bootstrap paths.
- No source edit, live merge, commit, or push is authorized until the user approves the final plan. A later merge abort must use `git merge --abort`, followed by verification of all protected paths.

## Validation mapping for the planned execution

- **Crawler v3 and fork uploads:** crawler/scriptcrawler and crawlerupload tests; verify task persistence, cancellation/finalization, feed/config data flow, import/update behavior, and Quark upload outcomes.
- **Drives/SSE/resources:** API and app-server drive-generation/snapshot tests; frontend drive resource/detail/form tests; check STRM and Crypt/proxy round trips.
- **SQLite/catalog:** uncached repeated `TestConcurrentScansPreserveTagsWithMetadataWrites`, retry/rollback tests, and catalog package/full backend tests.
- **Media and platform behavior:** preview/fingerprint/duration tests; Windows cross-compile for platform-specific lease/runtime files where applicable.
- **Frontend:** targeted admin-drive/player/navigation/resource tests, then project verification/lint/type check/build using the repository's supported Node version.
- **Integration/worktree:** `git diff --check`, full `go test -count=1 ./...`, `go build ./cmd/server`, frontend `npm run verify`, and a final comparison against the saved dirty/untracked baseline. Report unavailable checks accurately.
