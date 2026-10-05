# Upstream v0.3.5 integration implementation plan

## Approval gates

- Planning is not implementation approval. Do not call `task.py start`, modify product/source files, or begin the live merge until the user explicitly approves the final planning summary.
- At finish, do not commit or push unless the user explicitly authorizes those actions. Push only the reviewed merge commit to `origin/main`; never force-push.

## Ordered execution checklist

### 1. Preflight and protect the worktree

- Confirm active task status, `HEAD`, `origin/main`, `upstream/main`, tag `v0.3.5`, and merge base. Stop if upstream moved beyond the reviewed release or origin changed; re-plan against the new snapshot.
- Record `git status --short`, every dirty/untracked path, and hashes of pre-existing user files. Confirm `origin/main` is still the expected fork tip.
- Confirm the reviewed `git merge-tree` result still names the four conflicts. Do not stash, clean, reset, or stage with `git add -A`.

### 2. Begin the approved integration

- Start a normal merge with `git merge --no-commit --no-ff upstream/main`.
- Resolve only after inspecting all four conflicts and the semantic overlap list in `research/upstream-v0.3.5-diff.md`.
- If new conflicts appear, add them to the preservation review. If any resolution would lose an active fork behavior or dirty user work, stop and ask rather than choosing a side wholesale.

### 3. Integrate and review affected boundaries

- Resolve drive runtime construction and configuration so Crypt, per-drive proxies, STRM, crawler v3, drive events, snapshots, and generation lifecycle all remain consistent.
- Resolve dependency/vendor changes while retaining Quark/Rclone modules and upstream v3 dependencies.
- Preserve transaction-boundary SQLite retries and the unchanged concurrency regression test while merging notifications, catalog migrations, and new persistent crawler task state.
- Review the Quark crawler-upload adapter against the new v3 importer/task contract. Verify per-video outcomes, retries, cancellation, duplicate reconciliation, and local-file validation.
- Review all 18 shared paths plus auto-merged changes affecting playback, previews, fingerprints, drive admin resources, Telegram cleanup, and deployment/build behavior.
- Keep the root README and all pre-existing `.gitattributes`, `.agents/`, `.pi/`, and `.trellis/` work out of the integration unless separately authorized.

### 4. Validate incrementally

- Focused backend checks for `internal/drives/scriptcrawler`, `internal/crawlerupload`, `internal/catalog`, drive-generation/API snapshots, preview, fingerprint, and Quark/proxy behavior.
- Repeat the unchanged scanner regression test uncached: `go test -count=1 -run '^TestConcurrentScansPreserveTagsWithMetadataWrites$' ./internal/scanner` from `backend/`; run the full backend suite `go test -count=1 ./...` and `go build ./cmd/server`.
- Cross-compile the server for Windows if platform-specific files are affected: `GOOS=windows GOARCH=amd64 go test -c ./cmd/server -o /tmp/server.test.exe`.
- Run focused frontend tests for drive configuration/resources, crawler status, player/navigation, then `npm run verify` from repository root with the supported Node version.
- Run `git diff --check` and inspect database migration/restore tests. Report any environment-limited check without substituting a weaker test.

### 5. Final integration review (before any commit)

- Re-run full relevant tests after final conflict/overlap changes.
- Review staged integration paths against the full upstream change set and fork feature inventory; inspect `git diff --cached --check` and `git diff --check`.
- Verify protected dirty/untracked files are unchanged and unstaged using the saved baseline. Confirm no external crawler repository or deployed script/data was touched.
- Present the merge diff and test results for user review. Stop for explicit commit authorization.

### 6. Commit and push only if separately authorized

- Stage only the reviewed merge paths; never stage protected user work.
- Create the normal merge commit using the repository's imperative, sentence-case commit convention and document actual tests.
- Push to `origin/main` only after explicit push authorization and confirming the remote has not advanced. If it has advanced, stop and request a new integration plan; do not force-push.

## Rollback points

- Before starting the live merge: no source changes exist; simply defer or revise planning.
- Before the merge commit: abort an unsafe live merge with `git merge --abort`, then compare all protected paths/hashes with the preflight baseline.
- After a merge commit: do not reset or rewrite shared history; discuss a reviewed revert if rollback is needed. Never push an unreviewed or unauthorized rollback.
