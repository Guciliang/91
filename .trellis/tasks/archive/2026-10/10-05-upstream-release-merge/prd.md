# Merge upstream v0.3.5 while preserving fork features

## Goal

Integrate the latest upstream release into `Guciliang/91` without losing fork-specific functionality or the user's existing local work, after discussing and approving the plan.

## Confirmed baseline

- `origin` is `Guciliang/91`; `upstream` is `nianzhibai/91`.
- Fork `main` and `origin/main` are at `ff002d6`; the working tree already contains user changes that must remain untouched unless separately authorized.
- The prior upstream integration base is `915c6e9`. The latest upstream release is `v0.3.5` at `c5e358c`, and `upstream/main` points to that exact tagged commit.
- Since the merge base, upstream has 23 commits and the fork has 20 commits. The upstream-side diff contains 228 changed files (+18,969 / -6,760 lines).
- A read-only merge-tree simulation reports four direct conflicts: `backend/cmd/server/drives.go`, `backend/go.mod`, `backend/internal/api/admin_drives.go`, and `backend/internal/catalog/scan_duplicates.go`. Eighteen other changed paths are modified on both sides and need semantic review.
- The previous integration inventory documents fork functionality including Quark Crypt and crawler uploads, per-drive Crypt/proxy configuration, WebDAV/local `.strm`, WebDAV preview-link refresh, browser User-Agent continuity, decrypted Quark range playback and seek behavior, cross-platform asset locking, frontend additions, and the SQLite busy-retry fix. These must be checked against the release changes, not assumed safe because a textual merge succeeds.
- This repository does not track standalone crawler `.py` sources. The admin supports uploading a file or importing it from a URL; URL import downloads and validates the file, then stores it under the server data directory's `crawler-scripts/` folder. The source URL is retained for a separate manual refresh action; crawler runs use the stored local script rather than automatically re-downloading the URL.
- The existing implementation accepts `crawler.v1` and `crawler.v2`; its v2 script is launched with a job file and emits events to stdout. Upstream v0.3.5 requires `crawler.v3`, rejects older protocol declarations, and changes the script contract to a persistent command/response session (`discover`, `resolve`, `stop`) with optional feed metadata. Therefore v3 is a script/backend protocol change, not merely a different URL-import workflow. The actual remote crawler project or configured script URL is not identified in this repository.

## Requirements

- Review and integrate upstream changes through the exact `v0.3.5` release snapshot; do not omit upstream changes merely to simplify conflicts.
- Preserve all active fork-specific behavior and its regression coverage, including behavior moved or replaced by upstream architectural changes.
- Preserve the existing SQLite contention remediation and its regression guarantees while integrating upstream catalog/scan changes.
- Protect all pre-existing dirty and untracked user work. Do not stash, clean, reset, or implicitly stage/commit it.
- Do not modify product/source code until the user explicitly approves the final plan. Do not commit or push without explicit authorization.
- Keep this task limited to the `91` repository: integrate and validate upstream's `crawler.v3` contract and preserve the URL-import workflow. Do not modify or assess the separate crawler-script project.

## Acceptance criteria

- [ ] The approved upstream v0.3.5 snapshot is fully integrated; the selected integration method and exact commit are recorded.
- [ ] Each active fork feature has an evidence-backed mapping to its resulting implementation and regression validation; no feature is dropped as a conflict-resolution shortcut.
- [ ] The four known direct conflicts and all material overlapping changes are reviewed semantically, including crawler execution/upload, drive configuration and resource synchronization, and SQLite retry behavior.
- [ ] Existing dirty/untracked work remains intact and excluded from integration commits unless separately authorized.
- [ ] Backend/frontend and platform-specific checks appropriate to changed behavior pass, and any unavailable checks or known upgrade steps are explicitly reported.
- [ ] The v3 script contract and any separately required external crawler-source update are documented clearly.
- [ ] Source edits, commits, and pushes occur only after the required explicit user authorization.

## Out of scope unless explicitly added

- Deploying the release or changing production data/configuration.
- Integrating upstream commits newer than `v0.3.5`.
- Editing, inspecting, or migrating the separate crawler-script project or deployed crawler files. Only the `91` repository is in scope.