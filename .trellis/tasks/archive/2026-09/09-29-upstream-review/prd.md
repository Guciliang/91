# Integrate upstream into the fork without losing local features

## Goal

Prepare a safe integration of `nianzhibai/91` upstream changes into `Guciliang/91` while preserving every active fork-specific feature and all existing user work. The user requires local functionality to remain available even when upstream has refactored the same subsystem. Also fix the inherited SQLite write-contention bug exposed by the uncached full backend suite, without weakening the concurrency regression test.

## Confirmed baseline

- `origin` is `Guciliang/91`; `upstream` is `nianzhibai/91`.
- Fork `main` is at `bebfe3a`; `origin/main` points to the same commit.
- Fetched `upstream/main` is `915c6e9`; the common ancestor is `bbf3477`.
- The branches have 19 fork-only commits and 50 upstream-only commits. The incoming upstream diff from the common ancestor is 343 files (+21,778 / -5,172 lines).
- A read-only three-way merge simulation identifies four content conflicts: `README.md`, `backend/internal/crawlerupload/migrator.go`, `backend/internal/drives/onedrive/driver.go`, and `backend/internal/drives/p115/driver.go`. Other overlapping paths auto-merge but require behavior review.
- Existing worktree changes include modified `.gitattributes` and untracked `.agents/`, `.pi/`, and `.trellis/` files. They must remain intact and must not be staged or committed as part of this integration without explicit agreement.

## Confirmed policy and requirements

The user chose to keep the fork's functionality and integrate upstream behavior. The final plan was reviewed and approved, authorizing product/source edits and the merge. The user later approved two local commits: `ad600a0` for the upstream merge and `99166cc` for the SQLite contention fix. Push remains unauthorized.

- Integrate all of `upstream/main` through a normal merge; do not replace the fork branch with upstream or selectively omit incoming commits.
- Do not silently drop an active fork feature. If upstream replaces an implementation, migrate the existing behavior into the new architecture and add regression coverage.
- Distinguish live behavior from unreferenced or explicitly retired code by checking call sites, migrations, and tests.
- Preserve all pre-existing dirty and untracked user work. Do not stash, clean, reset, or stage it implicitly.
- The final planning summary was reviewed and explicitly approved before implementation. The user separately approved the two local commits; pushing still requires explicit authorization.

## Scope

**In scope:** all upstream changes through the reviewed `upstream/main` snapshot; the four direct conflicts; auto-merged backend, frontend, and deployment overlaps; the complete local-feature inventory and its regression checks; repair of the inherited SQLite writer-contention bug with safe bounded retries and uncached backend validation; protection of the existing worktree.

**Out of scope:** pushing the integration; staging or committing pre-existing `.gitattributes`, `.agents/`, `.pi/`, or untracked `.trellis/` work.

Detailed feature evidence, conflict resolutions, and validation mapping are recorded in `research/upstream-diff.md`.

## Acceptance criteria

- [x] Every active fork-specific feature in the feature inventory is mapped to its merged implementation and a regression check; no feature is lost as a conflict-resolution shortcut.
- [x] Each of the four direct conflicts combines compatible upstream behavior with the fork's active behavior.
- [x] Material auto-merge risks and upstream-retired/relocated code are reviewed semantically and documented.
- [x] The complete upstream snapshot is integrated; relevant backend and frontend checks pass, with the Docker Compose check accurately documented as unavailable because Docker is not installed.
- [x] Concurrent SQLite catalog writes retry transient `SQLITE_BUSY` safely at complete operation/transaction boundaries, with bounded context-aware waiting and regression coverage; the real scanner/metadata concurrency test passes uncached repeatedly without reducing concurrency or expectations.
- [x] Existing dirty/untracked work remains unchanged and is excluded from the merge commit unless separately authorized.
- [x] No source-code edit or merge occurs before approval of the final plan; no commit or push occurs without separate approval.

## Scope decision

The user explicitly added the shared, inherited SQLite write-contention bug to this task and authorized its repair. Evidence and remediation analysis are in `research/scanner-sqlite-busy.md` and `research/scanner-sqlite-busy-remediation.md`; the implemented fix and verification are recorded in the former and in the execution status. The merge and fix are committed locally as `ad600a0` and `99166cc`; do not push without separate authorization.
