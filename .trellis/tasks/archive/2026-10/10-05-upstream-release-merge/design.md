# Upstream v0.3.5 integration design

## Goal and integration strategy

Integrate the complete `nianzhibai/91` `v0.3.5` snapshot (`c5e358c`) into fork `Guciliang/91` with a normal three-way merge, retaining fork-only behavior and history. Do not cherry-pick selected upstream features, reset to upstream, rebase, or accept an upstream version of a conflict file wholesale as a shortcut.

The merge base is the prior upstream tip `915c6e9`; the incoming side is 23 upstream commits. The fork adds 20 commits after that base. This is one coupled repository-integration deliverable: catalog/schema changes, crawler execution/upload, drive generation, backend snapshots/events, admin UI, and player changes must be validated as a coherent whole rather than split into independent tasks.

## Boundaries and data flow

### Crawler scripts and uploads

Adopt upstream's `crawler.v3` runtime, durable task model, feed metadata, import revisions, cleanup, and result persistence inside `91`. Keep script URL/file import and the local server-side script snapshot flow. The imported Python source is external/user-provided data and is not tracked in this repository; no external script repository or deployed script/config will be edited in this task. Clearly document that scripts supplied to the v0.3.5 runtime must implement v3.

The fork's Quark crawler-upload adapter and per-video persisted upload outcomes must continue to work with the v3 crawler-task/importer lifecycle. Preserve file validation, cancellation, duplicate reconciliation, and upload error reporting.

### Drive configuration and playback

Combine upstream drive snapshots, SSE/status events, resource generation, duration backfill, and task-lifecycle behavior with fork drive configuration and runtime behavior. The config-to-runtime path must retain per-drive `ProxyURL`, Crypt wrappers, WebDAV/local STRM settings, and authenticated/range playback. Preserve Quark plaintext range/seek behavior, browser User-Agent continuity, and the existing redirect-vs-same-origin relay boundary.

Admin drive API DTOs and frontend resource/detail consumers must keep fork's WebDAV `strmAllowOutsideRoot` setting while adopting upstream typed snapshots and shared resources. Keep updates consistent through config decoding, API serialization, admin form, driver construction, and event-driven status display.

### Catalog and dependencies

Preserve the fork's complete SQLite busy-retry transaction wrapper and regression tests while adding upstream drive-event notifications, catalog changes, new crawler tables, and migrations. Retry only a fully rolled-back complete operation after releasing its connection; notify only after a successful committed change. Do not weaken the existing scanner concurrency test.

Combine the Go dependency graphs deliberately, retaining Quark/Rclone and current fork module versions as appropriate alongside upstream task/event dependencies. Keep `go.mod`, `go.sum`, and `vendor/modules.txt` consistent; avoid wholesale vendor regeneration unless the reviewed dependency graph requires it.

### Frontend and deployment

Integrate upstream's admin resource/SSE changes, crawler v3 controls, navigation behavior, and layout/build changes while retaining fork-specific UI behavior and project files. Validate API response shape and component expectations across the backend/frontend boundary. Keep the root README and all user/untracked Trellis files protected from implicit changes.

## Conflict resolution contracts

- `backend/cmd/server/drives.go`: retain fork Crypt/proxy/STRM configuration, and upstream v3 task setup, drive-event/generation notifications, duration backfill, and graceful/forced task cancellation semantics.
- `backend/go.mod`: merge both dependency graphs and synchronize lock/vendor metadata; do not discard Quark/Rclone requirements or upstream v3 dependencies.
- `backend/internal/api/admin_drives.go`: preserve the WebDAV STRM outside-root API field while adopting upstream DTO/snapshot behavior.
- `backend/internal/catalog/scan_duplicates.go`: combine retry-at-transaction-boundary with notifications after successful writes; preserve rollback/connection-release ordering and concurrency guarantees.

Review all 18 shared paths listed in `research/upstream-v0.3.5-diff.md` even when Git auto-merges them. Specifically inspect `crawlerupload/migrator.go`, catalog transactions/migrations, scriptcrawler test/runtime changes, drive API/UI data flow, preview/fingerprint workers, and VideoPlayer behavior.

## Compatibility and migration

- The release intentionally changes the crawler interface from v1/v2 to v3. Follow upstream's v3-only behavior; do not add a compatibility shim that contradicts the release or modify external scripts here.
- Existing imported scripts/data are not in the source tree. Report that v3-compatible remote scripts are required and leave any deployed script migration to a separate request.
- Preserve SQLite data through upstream's migration path and add confidence through existing migration/restore/catalog tests. Do not reset or recreate user databases.
- Keep the backend's declared Go toolchain compatibility and frontend Node requirements; do not raise minimum versions only to accommodate local tools.

## Worktree safety, approval, and rollback

No live merge or source edit begins before the user approves this final plan and the task is activated. Before merge, record the exact commit/remote tips, dirty/untracked path list, and hashes for pre-existing user files. Do not stash, clean, reset, broadly stage, or include pre-existing files.

Use a no-commit normal merge so the user can review the full integration before any commit. If merge resolution becomes unsafe or a fork behavior cannot be preserved, stop before commit; abort an active merge with `git merge --abort` and verify protected work against the recorded baseline. Commit and push remain separate explicit authorization gates; never force-push.
