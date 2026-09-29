# Upstream/fork diff review

## Snapshot

- Fork remote: `origin` → `Guciliang/91`; original remote: `upstream` → `nianzhibai/91`.
- Local branch: `main` at `bebfe3a` (`chore: record journal`); `origin/main` is also `bebfe3a`.
- Fetched upstream ref: `upstream/main` at `915c6e9` (`Unify page footers and reduce bottom spacing`).
- Merge base: `bbf3477a049ae2686c380003b712e41ea5ee0f36`.
- Divergence: 19 fork-only commits and 50 upstream-only commits.
- Upstream diff from the common ancestor: 343 files, +21,778 / -5,172 lines.
- The upstream changes are broad: Telegram ingestion/admin/deployment/configuration, scanner and read retry behavior, crawler upload outcomes, drive upload/playback reliability, data-path and backup changes, frontend navigation/player changes, and build/release workflows. The merge scope is all of `upstream/main`, not a selective cherry-pick.

## Confirmed preservation policy

The user selected a preservation-first merge and later explicitly approved the final plan: integrate upstream while retaining all active fork functionality. No local feature may be discarded to make a textual merge easier. Replaced implementations may be migrated to the upstream architecture if their behavior remains available and regression-checked. The merge and SQLite fix are committed locally as `ad600a0` and `99166cc`; push remains unauthorized.

## Fork feature inventory and preservation evidence

The inventory below combines fork-only commit subjects, paths present on the fork but absent from current upstream, and current call sites/tests. Vendored dependencies supporting Quark are part of the feature and must remain buildable; do not regenerate or prune `backend/vendor` as a shortcut.

| Feature/behavior to preserve | Evidence in the fork | Integration/verification requirement |
|---|---|---|
| Quark Crypt decryption, range streaming/seek support, cache/prefetch and seek diagnostics, plus crawler uploads | Commits `32441a6`, `7e60c3a`; `backend/internal/drives/quark/crypt*.go`, `plaintext_request.go`, `backend/internal/proxy/proxy.go`, crawler upload adapter; focused Quark crypt/cache/prefetch/upload/transport tests | Adapt the Quark upload adapter to the upstream crawler-upload interface while keeping per-crawler persisted outcomes and file validation. Preserve the fork's `requireAssetsReady` behavior through upstream `assetBlockReason`: require a ready fingerprint (or sampled hash), and when previews are enabled require a ready/equivalent preview; pending/failed assets remain blocked outcomes. The upstream `execution.go` path and tests cover this gate. Keep vendored modules consistent and run Quark, crawlerupload, proxy, and full backend tests. |
| Per-drive Crypt/proxy support across configured providers | Commit `4ad3201`; `ProxyURL`/crypt configuration passed through drive construction, admin API/UI and provider HTTP clients; private transports support HTTP(S)/SOCKS5 and avoid leaking a drive proxy to another drive | Verify configuration round-trips through config → API → admin form → driver, all relevant API/stream/upload clients use the correct transport, and upstream request retry/security semantics remain intact. |
| WebDAV/local `.strm` playback, including configurable root handling and authenticated target URLs | Commits `f8b823e`, `968a7fd`, `83b0b9f`, `3bc57d0`; `backend/internal/drives/strm.go`, WebDAV/local-storage drivers and tests | Keep parsing, root boundaries, authenticated URL behavior, and media playback available; cover redirects and local/WebDAV cases. |
| Failure-driven refresh for preview generation when a WebDAV/direct media URL expires or redirects | Commit `cca89ac`; preview generator and `backend/internal/preview/ffmpeg_test.go` refresh/credential-boundary tests | Preserve bounded refresh/retry behavior and ensure credentials are not leaked to redirect targets. |
| Browser User-Agent continuity across pass-through redirects | Commit `d294051`; `backend/internal/proxy/proxy.go` and redirect/stream tests | Preserve the browser UA where the provider binds its download URL to that UA, without forwarding drive credentials to a redirect target. |
| Decrypted Quark playback, same-origin byte-range relay, and seek prioritization/diagnostics | `PlaintextRangeProvider`, plaintext request context, `/api/stream-seek*`, proxy handlers, Quark seek capability; `backend/internal/preview/plaintext_proxy_test.go`, proxy/API tests and `tests/videoSeekPriority.test.ts` | Keep byte-range semantics, cancellation, seek-priority API behavior and diagnostics while integrating upstream relay/player changes. The triple-screen `tripleScreenRelay=1`/`proxy.allow_forced_relay` behavior is present on both tips; verify the local plaintext path remains compatible with it. |
| Cross-platform preview-asset directory writer lease | Generic `asset_directory_lease.go` and test are on both tips; fork adds `asset_directory_lease_unix.go` and `asset_directory_lease_windows.go`; the simulated merge retains the lease acquisition/close lifecycle in `main.go` | Preserve Unix and Windows locking implementations and startup/shutdown lifecycle; run normal tests and a Windows cross-compile. |
| Chinese GBK/GB18030 subtitle decoding | Fork commit `a836a32` explicitly retained `golang.org/x/text v0.28.0` and its vendor manifest entries; `backend/internal/api/subtitle_encoding.go` and tests use the package | Current upstream now contains the same module version, vendor packages, decoder, and tests. Keep them present and run `go test ./internal/api`; no fork-only code adaptation is expected. |
| Admin toast copy behavior | Fork `src/admin/ToastContext.tsx` and `tests/adminToast.test.ts` make toast text copyable. Upstream replaces them with shared `src/components/ToastContext.tsx`/`Toast.tsx`, which still provides a dedicated copy button, two-visible-toast cap, and a replacement `tests/toast.test.ts`. | Use the upstream shared toast architecture and verify existing admin call sites still expose copy functionality; do not keep a duplicate provider unless needed for behavior. |
| Fork documentation and frontend conventions | Fork README changes (`bdfeee9`); tracked `.trellis/spec/frontend/*` and `.trellis/workspace/Guciliang/*` are absent from upstream | Combine README sections and retain committed project specs/journal. Never include the currently untracked Trellis setup files in a merge commit without explicit agreement. |

### Commit-level completeness audit

All 13 non-merge commits unique to the fork were reviewed: Quark/Crypt (`32441a6`, `7e60c3a`), vendor/build support (`a836a32`), multi-drive Crypt/proxy (`4ad3201`), WebDAV `.strm` and authenticated URL/root handling (`f8b823e`, `968a7fd`, `83b0b9f`, `3bc57d0`), preview refresh (`cca89ac`), README (`bdfeee9`), redirect User-Agent (`d294051`), frontend specs (`7906826`), and the journal (`bebfe3a`). The subtitle vendor requirement from `a836a32` is now also present upstream, as noted in the inventory.

The six fork-only merge commits were checked with `git show --remerge-diff --stat`: `c306172` and `1df8886` have no resolution-only diff; custom resolution changes in `c131ef7`, `50074b3`, `3c4fc05`, and `1157739` map to the Quark/drive/proxy, STRM/asset-lease, preview, User-Agent, and vendoring items already listed above. No additional independent feature category was found in those resolution-only changes.

### Branch-only code that upstream retires or relocates

- `backend/internal/catalog/tag_classification.go` contains the active `removeAutomaticTaggingArtifacts` migration plus an unreferenced `classifyAllTagsAddOnly` helper. Upstream moves the active migration to `tag_retirement_migration.go` and expands cleanup of retired settings. Preserve the active data-migration outcome; do not carry the unused add-only helper merely because it was in the old file.
- `backend/cmd/server/tag_cluster_helpers.go` supports title-cluster code in the fork's tag-maintenance file. The live `propagateTagsAcrossTitleClusters` method is a no-op, the algorithm is marked retired, and no runtime caller invokes it. Upstream replaces the old maintenance file. This is currently test/retired code, not an active product feature; verify the new tag-maintenance tests and do not silently remove any live tag behavior.
- These cases are documented explicitly so deletion/rename in the merge is evaluated semantically, not treated as proof of feature loss or as a reason to preserve dead code indefinitely.

## Direct conflicts from read-only merge simulation

`git merge-tree --write-tree --messages HEAD upstream/main` reports exactly four content conflicts. It writes no index or working-tree changes. The conflict hunks were also inspected using `git merge-file` against the common ancestor.

1. `README.md`
   - The overlapping hunk is fork deployment/data-directory text adjacent to the upstream rename of `## 数据存放位置` to `## 数据默认存放位置`.
   - The remainder also needs a content-level review so local Crypt/proxy/`.strm` notes coexist with upstream Telegram, data-directory, and sponsor material.
   - Resolution: retain local feature/deployment documentation, adopt upstream additions and terminology, and verify paths against the actual deployment configuration. The Compose file bind-mounts host-relative `./data/` to `/opt/video-site-91/data/` in the container, so the merged README now labels host-relative paths and the standard `/root/video-site-91/data/` example instead of implying that absolute path is universal. The one-click systemd install uses a separate `/opt/video-site-91/config.yaml` path.

2. `backend/internal/crawlerupload/migrator.go`
   - The direct hunk is the fork's `quarkUploadDriver` adapter field versus upstream's concrete `*quark.Driver` plus refactored `existingUploadCache` shape.
   - Upstream also changes task outcomes to be persisted per crawler, returns failures to orchestration, and strengthens local-file validation/reconciliation.
   - Resolution: adapt the Quark Crypt uploader to the new upstream interface/outcome model; retain upstream persistence, validation, cooldown, and error behavior. Add targeted tests for successful Quark upload, duplicate reconciliation, and failed/cancelled per-video outcomes.

3. `backend/internal/drives/onedrive/driver.go`
   - Four direct hunks overlap: imports, driver client fields, constructor setup, and sending a preauthenticated upload request.
   - Fork behavior uses a configured per-drive proxy for API and stream requests. Upstream adds `readretry`, a scoped-proxy-aware Graph client and a separate bounded `uploadClient`, plus upload-session reconciliation/retries and rate-limit handling.
   - Resolution: construct upstream Graph/upload clients with the fork's configured transport; retain upstream retry/session logic. Preauthenticated upload URLs must use the configured transport without receiving the Graph bearer token. Test redirect, retry, and proxy routing explicitly.

4. `backend/internal/drives/p115/driver.go`
   - Two direct hunks overlap: imports and the `Driver` client fields. Fork stores separate proxy-aware API/stream clients; upstream switches SDK calls to `newSDKClient(ctx)` and adds `readretry`, scoped transport, bounded HLS handling/cache, and timeout/error behavior.
   - Resolution: thread the configured proxy through the new SDK-client factory and HLS/stream clients without bypassing context-bound retries, pick-code reuse, fallback, or request limits. Test API, download, HLS and direct playback requests with proxy configured and unset.

## Additional auto-merge and behavior risks

- `git merge-tree` auto-merges the other overlapping paths: server drive construction; admin drive API/config/tests; drive interfaces/providers; crawler-independent preview; proxy and scoped-proxy code; admin drive UI/API; `VideoPlayer`; and admin-drive form tests. Review the merged data flow end-to-end, especially per-drive `ProxyURL`/Crypt configuration, Quark plaintext range serving, seek-priority API calls, browser User-Agent forwarding, forced same-origin relay and upstream redirects.
- The local and upstream tips both contain the triple-screen relay flag and forced-relay configuration. It is not a fork-only feature, but the local Quark plaintext relay and proxy changes must not break it.
- Upstream replaces the fork's admin toast module, but the new shared toast retains copy actions; verify tests and all admin imports after migration.
- Upstream moves the active tag-retirement migration and removes only unreferenced/retired tag helpers. Confirm behavior through migration/retag tests before accepting the deletion.
- The local Quark additions include vendored dependency changes. Merge `go.mod`, `go.sum`, and `vendor/modules.txt` consistently; do not use `go mod vendor` as an unreviewed bulk rewrite.

## Worktree and toolchain constraints

- Before this task the worktree already had a modified `.gitattributes` and untracked `.agents/`, `.pi/`, and `.trellis/` files. The upstream tree has no exact collision with the untracked paths and does not change `.gitattributes`, but recheck immediately before an approved merge. Do not stash, clean, stage, or commit this work without permission.
- The Trellis task adds planning files under `.trellis/tasks/09-29-upstream-review/`; these are planning artifacts, not product changes or commit authorization.
- The backend module/CI target Go 1.23 (`backend/go.mod`: `go 1.23.0`, toolchain `go1.23.4`; CI uses Go 1.23), even though the local toolchain is Go 1.27. The merged scriptcrawler test initially used `testing.T.Chdir` (added in Go 1.24), which `go vet` rejects under the module's Go 1.23 target. The integration replaces it with the existing `os.Chdir` plus `t.Cleanup` pattern in `backend/internal/drives/scriptcrawler/crawler_test.go`; the focused scriptcrawler package test passes.
- Frontend requires Node `>=22.12.0`; `package.json` provides `npm run verify` (audit, type check, tests, build).

## Validation mapping

- Review the final four conflict resolutions and all auto-merged overlap paths; compare them with this feature inventory.
- Backend from `backend/`: `go test ./...`, `go build ./cmd/server`, plus `GOOS=windows GOARCH=amd64 go test -c ./cmd/server -o /tmp/server.test.exe` for the platform-specific lease files.
- Frontend from repository root: focused tests for `adminDriveForm`, `videoSeekPriority`, toast and triple-screen behavior, then `npm run verify` with Node `>=22.12.0`.
- Run `git diff --check`; validate the affected Docker Compose configuration if the upstream deployment changes are part of the resulting diff and Docker Compose is available.
- Compare post-merge `git status` to the pre-merge baseline and confirm no existing dirty file was altered or staged.
## Executed integration and validation status

- The approved upstream merge is complete on `main` in commit `ad600a0` (merge parents `bebfe3a` and `915c6e9e800c73a85a585621932ccb78894dc7b3`). The separate SQLite contention fix is commit `99166cc`. Neither commit has been pushed.
- All four conflicts were resolved by combining local functionality with compatible upstream behavior. The fork feature inventory and auto-merged overlap review are recorded above; scoped behavior tests passed during the implementation check.
- The pre-existing `.gitattributes` modification remains unstaged and unchanged; `.agents/`, `.pi/`, and `.trellis/` remain untracked and excluded from the merge index. The scriptcrawler test compatibility adaptation is staged as part of the integration. A SHA-256 comparison confirms all 93 pre-existing protected files outside the active task directory still match their pre-merge baseline.
- Backend: the user explicitly expanded scope to fix the inherited SQLite writer-contention bug. The fix classifies primary/extended `SQLITE_BUSY` codes and retries only complete scan-admission, automatic-tag replacement, and metadata UPDATE transactions after rollback/connection release. The unchanged scanner concurrency test passes twice uncached; retry/rollback tests pass; the complete `go test -count=1 ./...` suite now passes. `go build ./cmd/server`, the Windows server test cross-compile, and `go vet ./internal/catalog ./internal/scanner` pass. Root cause and implementation evidence are in `research/scanner-sqlite-busy.md`.
- Frontend: the four focused drive-form, seek-priority, triple-screen, and toast test files pass (72 tests); `npm run lint` and `npm run build` pass. `npm run verify` passes with a temporary `ss` binary extracted from Debian `iproute2` plus its `libmnl0` dependency under `/tmp/91-ss` (no root/system installation or repository changes): audit found 0 vulnerabilities, all 862 tests pass, and the build succeeds. The user manually verified Docker playback; Docker Compose configuration validation was not run because the Docker CLI is unavailable.
- Before commit, `git diff --cached --check` and `git diff --check` passed. The backend contention issue is resolved and its uncached suite passes; complete frontend verification also passes using the temporary `/tmp/91-ss` utility. The user manually confirmed Docker playback works, although Docker Compose configuration validation was unavailable because the Docker CLI is not installed. The two local commits are `ad600a0` and `99166cc`; neither has been pushed.
