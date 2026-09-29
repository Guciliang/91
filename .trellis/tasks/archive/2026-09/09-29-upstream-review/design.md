# Upstream integration design

## Goal and governing decision

Integrate the complete `upstream/main` snapshot (`915c6e9`) into fork `main` (`bebfe3a`) using a normal three-way merge. Preserve all active fork-specific functionality and all existing user work. Upstream behavior that replaces a fork implementation must be adapted into the new architecture rather than accepted as a feature deletion.

The user approved the preservation policy and final plan before implementation. The upstream merge was committed locally as `ad600a0`; the SQLite contention fix was committed separately as `99166cc`. No push is included.

## Scope boundary

This is one atomic branch-integration deliverable, not independently shippable subsystem work: crawler migration, drive transports, preview/proxy behavior, and frontend playback share interfaces and must be validated together. Use one Trellis task rather than children that would imply independent merge branches.

Merge all changes through `upstream/main`; do not rebase, reset to upstream, or cherry-pick only selected commits. Preserve the fork's existing commit history. Keep the root README in scope because it is one of the four merge conflicts and contains fork feature documentation.

## Merge and worktree strategy

1. After implementation is authorized, recheck `HEAD`, `upstream/main`, the merge base, and `git status --short`. Record the pre-merge dirty-path list and ensure no upstream path now collides with any untracked user file.
2. Do not stash, clean, reset, stage, or commit the pre-existing `.gitattributes`, `.agents/`, `.pi/`, or untracked `.trellis/` work. If Git refuses to merge safely because of dirty or untracked content, stop and ask rather than moving it implicitly.
3. Start the merge with `git merge --no-commit --no-ff upstream/main`. The merge result may stage upstream integration paths, but must not create a commit.
4. Resolve the four known conflicts and review all auto-merged behavior against the feature inventory in `research/upstream-diff.md`.
5. Before any commit, inspect staged merge changes separately from unstaged pre-existing work, rerun `git status --short`, and verify the original dirty paths are unchanged and unstaged.

The read-only merge simulation has exactly four content conflicts. If the authorized live merge produces additional conflicts, inspect and add them to the preservation review; do not choose upstream wholesale for convenience.

## Conflict integration boundaries

### `README.md`

Combine the fork's Crypt, per-drive proxy, `.strm`, and deployment/data guidance with upstream Telegram, data-directory, and sponsor sections. Keep one accurate description for each deployment/data behavior and do not discard fork feature documentation.

### `backend/internal/crawlerupload/migrator.go`

Keep upstream's per-crawler outcome persistence, explicit error propagation, local-file validation, reconciliation, and cooldown behavior. Replace the fork's old concrete Quark adapter shape with an adapter compatible with the new upstream crawler-upload interface so Quark Crypt uploads continue to work. Preserve cancellation and duplicate-detection behavior; add focused tests for successful, duplicate, failed, and cancelled Quark upload outcomes.

### `backend/internal/drives/onedrive/driver.go`

Retain the upstream Graph request retry/session-reconciliation/rate-limit design and separate bounded upload client. Construct the API and upload clients with the fork's per-drive scoped proxy. Preauthenticated upload URLs must use the configured transport but must not receive a Graph bearer token. Preserve the distinction between pass-through redirect behavior and proxy transport behavior.

### `backend/internal/drives/p115/driver.go`

Retain the upstream context-aware SDK factory, read retries, pick-code reuse, HLS bounds/cache/fallback, and timeout/error behavior. Thread the configured per-drive proxy through `newSDKClient(ctx)` and the applicable HLS/stream transports. Preserve request cancellation and keep each client role (API, download stream, HLS) explicit.

## Cross-layer integration boundaries

- **Drive configuration:** preserve Crypt and `ProxyURL` from config decoding through `cmd/server/drives.go`, admin API serialization, and admin form submission to each supported drive client. Do not share mutable proxy transports between drives.
- **Playback and proxy:** preserve Quark plaintext range responses, `Range`/`HEAD` semantics, cancellation, User-Agent continuity and per-drive proxy selection. Keep the shared triple-screen same-origin contract (`tripleScreenRelay=1`, gated by `proxy.allow_forced_relay`) working with plaintext and redirecting streams. Ordinary playback should retain redirect behavior where intended.
- **Preview generation:** retain failure-driven signed-link refresh and ensure WebDAV credentials do not cross to redirect targets.
- **Frontend playback:** merge upstream player changes with local seek-priority/reporting requests; keep URLs same-origin, throttling and existing source priority behavior.
- **STRM:** preserve parsing, configured root boundaries, authenticated URL handling, and local-storage/WebDAV playback.
- **Asset lease:** keep the fork's Unix and Windows lock implementations and the server's acquire/close lifecycle. The generic lease exists on both tips; the platform files are fork-specific.
- **Toasts and tags:** migrate admin toast call sites to upstream's shared component while retaining the copy action. Preserve the active tag-retirement migration when it moves to the upstream file. The add-only classification helper has no callers, and title-cluster propagation is retired/no-op with no runtime caller; verify that status and updated upstream tests before accepting those source deletions.
- **Vendored modules:** keep Quark Crypt's dependency graph and vendor metadata buildable while integrating upstream dependency changes. Do not regenerate the full vendor tree without a reviewed reason.
- **Project metadata:** retain tracked fork Trellis frontend guidelines/workspace history and leave current untracked Trellis/bootstrap setup untouched.

`research/upstream-diff.md` is the source-of-truth feature matrix, conflict notes, current-tree evidence, and test mapping for implementation and checking.

## SQLite writer-contention bug remediation

The user has explicitly expanded this task to fix the pre-existing `SQLITE_BUSY` failure in `TestConcurrentScansPreserveTagsWithMetadataWrites`. The affected operations contend on SQLite's single writer slot; WAL and `_txlock=immediate` already protect read/modify/write correctness, but the 5-second busy timeout alone does not guarantee completion under concurrent load.

Keep the fix localized to catalog write boundaries used by scanning and its metadata competitors. Retry only transient SQLite busy result codes, including extended `SQLITE_BUSY_*` codes, and only after aborting/rolling back and releasing the prior transaction/connection. Retry waits must honor the operation context and a finite budget. Never retry arbitrary statements inside a partially applied transaction; preserve duplicate admission atomically, automatic/manual tag semantics, and `UpdateVideoMeta`'s existing separate tag-update contract. SQLite remains the coordination authority across separate Catalog instances/processes; do not substitute a process-local mutex or weaken/serialize the test.

The implementation follows the transaction-boundary analysis in `research/scanner-sqlite-busy-remediation.md`. `retrySQLiteBusy` classifies the primary SQLite result code (including extended `SQLITE_BUSY_*` codes) and uses three bounded attempts with context-aware backoff. `InsertScannedVideo` and `ReplaceAutoVideoTags` retry complete explicit transactions only after rollback and connection release. `UpdateVideoMeta` runs its SQL UPDATE in the same explicit transaction helper: this matters because the thumbnail timestamp expression increments a value, so retry is safe only after a failed attempt is proven rolled back. Its separate `TagsSet` follow-up remains separate, preserving the existing API contract. The scanner admission/tag operations are not combined; they have distinct partial-outcome semantics, and the retry-based fix passed the repeated concurrency regression and uncached full backend suite without changing scanner scheduling. Broader catalog-wide retries or merging those operations require separate evidence and design.

## Compatibility, security, and operational constraints

- Do not forward Graph authorization to preauthenticated upload URLs or provider credentials across redirect boundaries.
- Per-drive proxy settings must apply to the intended requests only; preserve timeouts, redirect policy, and context cancellation.
- Range/seek streaming must not break same-origin WebGL pixel reads or convert ordinary redirect playback into an unnecessary server relay.
- The backend module and CI target Go 1.23 (`go 1.23.0` / toolchain `go1.23.4`); the available Go 1.27 toolchain does not change that compatibility contract. The incoming scriptcrawler test used `testing.T.Chdir`, which is newer than the module target and rejected by `go vet`'s stdversion check. The integration adapts that test to the repository's existing `os.Chdir` plus `t.Cleanup` pattern rather than raising the project's Go minimum. Frontend requires Node >=22.12.0.
- The current worktree has tracked and untracked user content. Never resolve merge issues by `git clean`, broad checkout, reset, or an unapproved stash.

## Rollback and stopping conditions

- The merge and SQLite fix are committed locally as `ad600a0` and `99166cc`. Do not push during this task; the user has not authorized a push.
- If a conflict or regression cannot be resolved without losing a fork feature, stop before commit and document the exact behavior and evidence.
- If the user elects to abort after reviewing an unexpected result, inspect the merge state first, use `git merge --abort` rather than hard reset, then verify every pre-merge dirty path against the recorded baseline. Never remove the existing dirty worktree as part of rollback.
