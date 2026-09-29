# Concurrent scanner SQLite `SQLITE_BUSY` investigation

## Finding

The reported failure is a **real SQLite writer-contention failure**, not a tag-matching correctness failure and not evidence that the test itself has a faulty expected result. The relevant transaction and test implementation are present unchanged at `HEAD` and `upstream/main`; this is **not introduced by the current upstream merge**. The regression test and the attempted lock fix were introduced together by upstream commit `7dea499` (“Prevent SQLite lock conflicts during concurrent scans”), which is an ancestor of both branch tips. Thus the failing scenario is a **shared, pre-merge behavior**: the upstream-added concurrency test exposes a remaining robustness gap in the shared writer-admission strategy.

The available evidence supports writer queue/timeout exhaustion as the root-cause category: one catalog is subjected to 6 concurrent reconciliations plus 240 metadata update statements. Each newly inserted scan file takes a writer reservation, then tag assignment is persisted separately in another transaction; the background goroutine issues another 240 updates. These are hundreds of serialized SQLite writes. A 5-second connection-level busy timeout does not guarantee forward progress when the writer is repeatedly contended or the test process is under whole-suite CPU/I/O load. The test passing in isolation but failing under package concurrency, twice, is consistent with a timeout-budget/contention issue. The failure is returned from real catalog writes, so merely serializing this test with `-p=1`, relying on cache, or disabling vet would conceal rather than fix the reliability gap.

This is a root-cause **category**, not a claim that the exact lock-holder duration has been measured. No per-operation timing or SQLite connection diagnostics were included in the supplied failure report, so it is not possible to prove whether the 5 seconds are consumed by one long transaction, accumulated lock wait, or another SQLite lock transition. The evidence does rule out a missing WAL/busy-timeout setting and the classic deferred read-to-write snapshot-upgrade case as the primary explanation: WAL, 5-second busy timeout, and immediate transaction mode are set, and scan admission also explicitly uses `BEGIN IMMEDIATE`.

## Relevant call and transaction paths

1. `backend/internal/scanner/scanner.go:89-100`: `Scan` performs discovery, then `reconcile`; it launches no catalog-write worker pool itself.
2. `backend/internal/scanner/reconcile.go:42-88`: each discovered file is checked, tag assignments are matched, existing rows are looked up, then new rows route to `insertNew`.
3. `backend/internal/scanner/reconcile.go:191-250`: `insertNew` calls `Catalog.InsertScannedVideo` at line 225 and, when assignments exist, separately calls `Catalog.ReplaceAutoVideoTags` at line 238. This means an inserted/tagged scan item can require two distinct writer acquisitions/commits.
4. `backend/internal/catalog/scan_duplicates.go:16-68`: `InsertScannedVideo` reserves a pooled `sql.Conn`, issues explicit `BEGIN IMMEDIATE` at line 28, reads identity/duplicate candidates and writes the video row, then commits at line 64. It correctly avoids a deferred read transaction becoming a stale snapshot before promotion.
5. `backend/internal/catalog/tag_assignments.go:328-345`: `ReplaceAutoVideoTags` starts a transaction, evaluates and applies automatic tag changes, synchronizes tags JSON and commits. The manual-tag check occurs inside this transaction (`:364-367`), avoiding a stale pre-transaction decision.
6. `backend/internal/catalog/catalog.go:56-77`: `Catalog.Open` creates a `database/sql` pool with `_pragma=journal_mode(WAL)`, `_pragma=busy_timeout(5000)`, and `_txlock=immediate` at line 60. The comments at lines 57-59 explain the intended immediate-reservation protection. The timeout is finite and does not add application-level retry after SQLite returns `SQLITE_BUSY`.
7. `backend/internal/catalog/catalog.go:699+` (current merged file; `HEAD` starts this method at line 668): `UpdateVideoMeta` constructs a single `UPDATE videos ...` statement and executes through `c.db.ExecContext`; the tested patch sets `DurationSeconds`. This is a separate writer competing with scanner admission/tag transactions.
8. `backend/internal/scanner/concurrent_scan_test.go:108-199`: the test creates six scans, each with 20 distinct tagged files (`:124-150`), releases all scans together, and at the same time runs `scanCount*filesPerScan*2` = 240 metadata writes (`:159-170`). It expects all 120 rows and their tag assignments to persist (`:173-199`). The test's use of barriers intentionally creates concurrent source discovery; all writes then converge on one SQLite database.

`MatchTagAssignments` at `backend/internal/catalog/tag_matching.go:209-...` is predominantly a read/match path for this test. The label is ensured before workers start (`concurrent_scan_test.go:116-118`), so every video is not creating a new tag definition. The principal write amplification is the separate row-admission and tag-assignment operations per video, interleaved with the 240 metadata updates—not concurrent tag-definition creation.

## Branch comparison and provenance

- `git rev-parse HEAD` = `bebfe3a1308c3ed30b54e150d4670c7d9f64c222`; `upstream/main` = `915c6e9e800c73a85a585621932ccb78894dc7b3`; common ancestor = `bbf3477a049ae2686c380003b712e41ea5ee0f36`.
- `git merge-base --is-ancestor 7dea499 HEAD` and the equivalent check for `upstream/main` both succeed. `7dea499` adds the regression test and `_txlock=immediate` change together.
- The tracked test and duplicate-admission implementation exist in both branch trees and their file contents compare equal (`git diff HEAD upstream/main -- backend/internal/scanner/concurrent_scan_test.go backend/internal/catalog/scan_duplicates.go` is empty).
- The catalog DSN line is the same at both tips. Current merged `backend/internal/scanner/reconcile.go` adds ancestor-directory names to the assignment matching call relative to `HEAD`/upstream in merge-context diffs, but the test's fake source has no ancestor directory names, and the tested tag is a pre-created simple filename match. This change does not explain the observed lock error.
- Other changes in the merged catalog (including import migration and tag schema evolution) are not implicated by the reported code path. The conflict-free comparison points to an inherited problem in the shared implementation, not a merge-introduced scanner transaction change.

## Initial correction recommendation (pre-implementation)

Prefer fixing writer coordination at the transaction boundary rather than adding sleeps or weakening/removing the concurrency test:

1. **Reduce acquisitions first:** consider a catalog operation that performs scan admission plus the applicable automatic tag reconciliation in one `BEGIN IMMEDIATE` transaction. The current split in `insertNew` causes a release/reacquire window and roughly doubles scan-side write-lock acquisitions for the tested tagged inserts. Keep duplicate decision, video insertion and tag state coherent; preserve the existing manual-tag guard and outcome semantics.
2. **Add bounded retry for transient `SQLITE_BUSY` at safe operation boundaries.** Retry the whole idempotent catalog write unit only after rolling back/releasing its connection/transaction, with bounded backoff and context/deadline awareness; do not retry an arbitrary individual statement inside a partially executed transaction. This complements, rather than replaces, SQLite's busy timeout and handles lock contention that exhausts that timeout. Treat persistent errors distinctly and avoid unbounded retry.
3. Keep scanner scheduling concurrent. A test-only mutex, reducing scan count, making the test serial, increasing only the test timeout, or increasing the SQLite timeout without operational retry may make this workload pass but does not establish robust production behavior. A process-local writer mutex alone is also insufficient if multiple `Catalog` instances/processes access the same DB; SQLite-level locking and bounded retries remain necessary.

These are recommendations only. No product/source files were changed, and no tests were rerun as part of this read-only investigation.

## Implemented correction and validation

The user explicitly expanded the upstream integration task to include this inherited reliability bug and approved its repair. The chosen fix adds a narrow retry boundary without combining the scanner's existing admission and tag phases:

- `backend/internal/catalog/sqlite_busy.go` classifies `*modernc.org/sqlite.Error` via `errors.As` and checks the primary result-code byte, so primary and extended `SQLITE_BUSY_*` codes retry while `SQLITE_LOCKED` and unrelated errors do not.
- The policy makes at most three attempts, waits with bounded exponential backoff, and observes the operation context. Busy retries wrap complete explicit transactions for `InsertScannedVideo` and `ReplaceAutoVideoTags`; the failed transaction is rolled back and the connection released/discarded before retry.
- `UpdateVideoMeta` executes its SQL UPDATE inside that explicit transaction helper. This preserves correctness for its non-idempotent `thumbnail_updated_at` increment: a failed attempt is rolled back before replay. The existing separate `TagsSet` follow-up remains separate.
- Scanner admission and tag assignment were not combined because the scanner intentionally keeps an admitted video when tag reconciliation fails and reports a tag issue. Changing that outcome would require a broader result/savepoint redesign. No process-local mutex was added, and the scanner test's concurrency, counts, and assertions were not weakened.
- New catalog tests cover base/extended busy-code classification, retry success/exhaustion, non-busy errors, cancellation, failed-commit cleanup, rollback failure/connection discard, and rollback-before-retry for the timestamp increment.

Validation passed:

- `cd backend && go test -count=2 ./internal/scanner -run '^TestConcurrentScansPreserveTagsWithMetadataWrites$'` — both uncached repetitions pass; output: `ok ... 14.023s`.
- `cd backend && go test -count=1 ./internal/catalog -run '^(TestIsSQLiteBusyClassifiesPrimaryAndExtendedCodes|TestRetrySQLiteBusyPolicy|TestImmediateWrite)'` — pass (`1.879s`).
- `cd backend && go test -count=1 ./...` — the complete uncached backend suite passes, including the concurrency test.
- `go build ./cmd/server`, `GOOS=windows GOARCH=amd64 go test -c ./cmd/server -o /tmp/server.test.exe`, and `go vet ./internal/catalog ./internal/scanner` — pass.
- `git diff --check` and `git diff --cached --check` — pass.

Go 1.23 compatibility is retained; no use of `testing.T.Chdir` was introduced.
