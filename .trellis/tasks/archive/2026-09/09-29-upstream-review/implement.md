# Execution plan

## Entry gate

- [x] Present the final plan to the user and wait for a subsequent explicit approval to begin implementation.
- [x] Only after approval, run `python3 ./.trellis/scripts/task.py start .trellis/tasks/09-29-upstream-review` and follow the active-task workflow.
- [x] Recheck that the intended refs are still `HEAD=main@bebfe3a` (or document any movement), `upstream/main=915c6e9` (or update the review), and confirm the branch is still the intended merge target.

## Ordered integration checklist

### 1. Protect the current worktree and start an uncommitted merge

- [x] Record `git status --short` and the contents/status of pre-existing dirty paths: `.gitattributes`, `.agents/`, `.pi/`, and untracked `.trellis/` setup. Do not stage, stash, clean, or copy these into the merge.
- [x] Confirm none of the current untracked paths collides with an upstream-tracked path. If the user worktree has changed or Git cannot safely merge, stop and ask; do not make it clean implicitly.
- [x] Start the merge without creating a commit: `git merge --no-commit --no-ff upstream/main`.
- [x] Record the actual merge base/result and conflict list. Expected direct conflicts are `README.md`, `backend/internal/crawlerupload/migrator.go`, `backend/internal/drives/onedrive/driver.go`, and `backend/internal/drives/p115/driver.go`.

### 2. Resolve direct conflicts without dropping active behavior

- [x] `README.md`: combine fork feature/deployment guidance with upstream additions; check for contradictory data-path or deployment instructions.
- [x] `crawlerupload/migrator.go`: preserve upstream per-crawler outcomes/error persistence and validation/reconciliation; adapt the Quark Crypt uploader to the new upstream adapter/interface.
- [x] `onedrive/driver.go`: retain upstream Graph retries, session offset reconciliation and rate limits; configure the new API/upload clients with the per-drive proxy; ensure preauthenticated uploads do not receive Graph bearer credentials.
- [x] `p115/driver.go`: retain upstream SDK context/retry/pick-code/HLS behavior and apply the per-drive proxy to SDK, HLS, and stream client paths.
- [x] For any newly appearing conflict, add it to the feature matrix and inspect both sides and the merge base before resolving.

### 3. Review the full merge and preserve the feature matrix

- [x] Review all auto-merged backend and frontend overlaps listed in `research/upstream-diff.md`; do not assume a textual auto-merge is behaviorally correct.
- [x] Verify config → API → admin form → driver propagation of Crypt and per-drive `ProxyURL` settings.
- [x] Verify Quark crypt range requests, seek/cancellation, cache/prefetch diagnostics and crawler uploads remain operational with the upstream proxy/relay and upload abstractions.
- [x] Verify WebDAV/local `.strm` parsing/playback, preview link refresh, credential-safe redirects and browser User-Agent continuity.
- [x] Verify `tripleScreenRelay=1`/`proxy.allow_forced_relay` remains compatible with Quark plaintext serving and normal playback remains redirect-based where intended.
- [x] Verify frontend seek priority/reporting, drive form behavior, and shared-toast copy action. Verify the active tag-retirement migration is preserved; accept retirement of tag code only after confirming there are still no runtime call sites and the replacement tests cover active behavior.
- [x] Verify the generic asset lease lifecycle remains intact and the fork's Unix/Windows lock files are retained.
- [x] Review `go.mod`, `go.sum`, `vendor/modules.txt`, and Quark vendored sources together; avoid broad vendor regeneration.

### 4. Add/update targeted regression coverage as needed

- [x] Add or adapt tests for Quark Crypt crawler uploads under upstream per-crawler success/failure/cancellation outcomes.
- [x] Test OneDrive API/upload proxy routing, bounded retries/session reconciliation, and absence of Graph authorization on preauthenticated upload requests.
- [x] Test p115 SDK/download/HLS proxy routing alongside retry, cancellation, pick-code reuse and HLS fallback.
- [x] Keep tests for Crypt/Proxy admin configuration, `.strm`, Quark plaintext ranges/seek, preview refresh and safe redirects, User-Agent continuity, toast copy, and asset lease platform behavior.
- [x] Update tests only where upstream deliberately changes the implementation while retaining the same user-visible feature; document the preserved behavior in the review.

### 5. Fix inherited catalog writer contention

- [x] Add a narrowly scoped SQLite busy-code classifier that handles extended `SQLITE_BUSY_*` results without matching unrelated errors.
- [x] Add bounded, context-aware retry only at complete safe write boundaries; prove the failed transaction is rolled back and its connection released before replay.
- [x] Cover scan admission, auto-tag replacement and the metadata UPDATE transaction, preserving duplicate/manual-tag and `TagsSet` semantics. The metadata UPDATE uses explicit rollback-before-replay because its thumbnail timestamp expression is non-idempotent if replayed after a partial commit.
- [x] Add deterministic unit/integration coverage for retry success, exhaustion, non-busy errors, cancellation, transaction cleanup, and no partial/duplicate writes. Keep the concurrency regression unchanged.
- [x] Keep SQLite as the cross-instance/process writer arbiter; do not add a process-local mutex as the fix.

### 6. Run validation

From `backend/`:

```bash
go test ./internal/api ./internal/drives/quark ./internal/crawlerupload ./internal/drives/onedrive ./internal/drives/p115 ./internal/proxy ./internal/preview ./cmd/server
go test -count=1 ./... # force the scanner concurrency regression to run uncached
go build ./cmd/server
GOOS=windows GOARCH=amd64 go test -c ./cmd/server -o /tmp/server.test.exe
```

Focused SQLite/scanner checks from `backend/`:

```bash
go test -count=2 ./internal/scanner -run '^TestConcurrentScansPreserveTagsWithMetadataWrites$'
go test -count=1 ./internal/catalog -run '^(TestIsSQLiteBusyClassifiesPrimaryAndExtendedCodes|TestRetrySQLiteBusyPolicy|TestImmediateWrite)'
go vet ./internal/catalog ./internal/scanner
```

From repository root (Node >=22.12.0):

```bash
node --import tsx --test tests/adminDriveForm.test.ts tests/videoSeekPriority.test.ts tests/tripleScreen.test.ts tests/toast.test.ts
npm run verify
```

Also run:

```bash
git diff --cached --check
git diff --check
docker compose config --quiet
```

If `npm run verify` is blocked only by registry/network failure in `npm audit`, run `npm run lint`, `npm test`, and `npm run build` separately and report the audit limitation. If Docker Compose is unavailable, report that check as not run; do not claim it passed.

### 7. Final review and separately authorized commits

- [x] Review `git diff --cached` as the proposed integration result and `git diff` separately as pre-existing unstaged work. Confirm only authorized merge paths are staged.
- [x] Compare post-merge `git status --short` and protected dirty paths with the pre-merge baseline; verify no `.gitattributes`, `.agents/`, `.pi/`, or existing/untracked `.trellis/` work was altered or staged.
- [x] Confirm every active feature in `research/upstream-diff.md` has a passing regression check or a documented environment limitation for unavailable checks.
- [x] Summarize the merge diff, test results, and remaining risks for user review. The user separately authorized the two local commits; do not push without separate authorization.

## Stop and rollback points

- Stop before merge if the branch/ref or dirty-worktree preconditions changed unexpectedly.
- Stop before accepting a conflict resolution if it removes an active feature without a verified replacement.
- Stop before commit if any required test fails, a regression is unresolved, the feature inventory is incomplete, or protected local work differs from baseline.
- If the user chooses to abort an in-progress merge, inspect the merge state first, then use `git merge --abort` (never `reset --hard` or `git clean`) and verify pre-existing dirty files remain intact.

## Current execution status

- The user approved the implementation plan. The merge was completed locally in `ad600a0` (`Merge upstream main while preserving fork features`), followed by the separately authorized SQLite fix commit `99166cc` (`Retry transient SQLite catalog write contention`). No push has been made.
- The four planned conflicts and the auto-merged feature surfaces were reviewed. Local Crypt/proxy, Quark crawler upload and range playback, `.strm`, preview refresh, User-Agent, seek priority, asset lease, toast copy, and tag retirement behavior remain mapped to tests in `research/upstream-diff.md`.
- The Go 1.23-only `testing.T.Chdir` mismatch was fixed in `backend/internal/drives/scriptcrawler/crawler_test.go` by using the repository's existing `os.Chdir`/`t.Cleanup` pattern; the focused package test passes.
- The SQLite bug fix adds bounded retry for primary/extended `SQLITE_BUSY` codes at complete transaction boundaries in scan admission, automatic-tag replacement, and metadata UPDATE. Failed attempts roll back and release the connection before replay; scanner/tag operation boundaries and the existing `TagsSet` follow-up semantics remain unchanged. Unit tests cover base/extended busy detection, cancellation, exhaustion, non-busy errors, commit/rollback cleanup, and a non-idempotent thumbnail timestamp mutation.
- The unchanged scanner concurrency regression passes twice uncached (`go test -count=2 ./internal/scanner -run '^TestConcurrentScansPreserveTagsWithMetadataWrites$'`); its catalog helper tests pass. The complete backend suite passes uncached (`go test -count=1 ./...`). `go build ./cmd/server`, the Windows server test cross-compile, and `go vet ./internal/catalog ./internal/scanner` also pass.
- Frontend focused tests pass (72/72); `npm run lint` and `npm run build` pass. After placing Debian `iproute2`/`libmnl0` under `/tmp/91-ss` and adding that local binary/library path to the environment, `npm run verify` passes: audit found 0 vulnerabilities, all 862 tests pass, and the build succeeds. No system package installation or repository change was needed for `ss`. The user manually verified Docker playback; Docker Compose configuration validation was unavailable because the Docker CLI is not installed.
- Before the two commits, `git diff --cached --check` and `git diff --check` passed. The user-approved SQLite fix was committed separately from the merge. `.gitattributes`, `.agents/`, `.pi/`, and pre-existing `.trellis/` setup remain uncommitted and excluded; the 93 protected pre-merge files still match the baseline.
- Spec review (Phase 3.3): the active Trellis spec layer is frontend-only, and adding a backend layer would require modifying the user's untracked Trellis bootstrap/configuration, which this merge must leave untouched. The Go 1.23 test-compatibility contract is recorded in project memory and task research; creating a backend spec layer should be a separately authorized Trellis-configuration change.
- The user explicitly expanded the task to repair inherited SQLite writer contention. The repair and uncached backend validation pass, and frontend verification passes with the temporary `ss` utility. The merge and repair are committed locally as `ad600a0` and `99166cc`; the user manually confirmed Docker playback works. Push remains unauthorized. The remaining finish steps are to archive this task and record the session journal.
