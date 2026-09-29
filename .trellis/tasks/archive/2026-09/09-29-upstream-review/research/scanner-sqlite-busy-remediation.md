# SQLite `SQLITE_BUSY` remediation research

## Scope and conclusion

This is a read-only investigation of the inherited failure in `TestConcurrentScansPreserveTagsWithMetadataWrites`; no product files or tests were edited. The observed contention is between independent SQLite writers on the same catalog file, not a Go data race. WAL permits concurrent readers but SQLite still admits one writer at a time. The current catalog relies on a finite five-second busy timeout and starts important read/modify/write paths with an immediate writer reservation, but it has no application-level retry once SQLite returns a busy result.

The safest narrow fix is **bounded, context-aware retry around complete, idempotent catalog write operations**, after rolling back and releasing the failed transaction/connection. It should include scan admission, automatic tag replacement, and the metadata update path used by the concurrency regression test. Do not retry an individual statement while a transaction may be partially applied, and do not add a process-local mutex as the correctness mechanism: separate `Catalog` instances and processes must continue coordinating through SQLite.

Combining scan admission and auto-tag reconciliation can reduce scan-side writer acquisitions, but it is **not a drop-in safe refactor under current contracts**. The current APIs have different transaction and error boundaries, and scanner behavior deliberately retains an inserted video when tag reconciliation fails. A combined transaction can preserve that behavior only with a deliberately designed partial-outcome mechanism (for example, a savepoint plus an explicit admission/tag result); otherwise it changes visible behavior by rolling back the video or misreporting it as added. The first/minimal remediation should therefore retry the existing complete operations independently. Consider combining them later only with a focused API redesign and regression tests for both the success and tag-failure semantics.

## Relevant source paths and current write API boundaries

### Scanner call path

- `backend/internal/scanner/scanner.go`: `Scan` performs discovery and then `reconcile`; discovery itself is read-only, and file reconciliation checks `ctx.Err()` between files.
- `backend/internal/scanner/reconcile.go:42-88`: each file checks tombstones, obtains `MatchTagAssignments`, looks up an existing row, then chooses existing-row reconciliation or insertion.
- `backend/internal/scanner/reconcile.go:191-250`: `insertNew` calls `Catalog.InsertScannedVideo`, and after successful admission separately calls `Catalog.ReplaceAutoVideoTags` if assignments exist. Tagging failure is recorded as `IssueTags`, but the insertion remains committed; `Stats.Added`, `NewVideos`, and `OnNewVideo` still reflect the admitted row. A duplicate/non-insertion increments `Duplicates` and does not tag or dispatch a new-video callback.
- Existing rows also have competing writes: `reconcileExisting` calls `UpdateVideoMeta` when metadata differs and later `ReplaceAutoVideoTags`; duplicate audit recording can write via `RecordScannedDuplicate`.
- `MatchTagAssignments` is a read/matching operation for the reported test; its input tag is created before the scan goroutines start.

### Catalog operations directly implicated

- `backend/internal/catalog/scan_duplicates.go:16-72`, `InsertScannedVideo`: reserves a pooled `sql.Conn`, issues `BEGIN IMMEDIATE`, checks source identity and duplicate candidates, records a duplicate outcome if needed, and inserts through `upsertVideoRow` otherwise. A deferred rollback is attempted unless commit succeeds. The duplicate decision and insertion are intentionally in one writer transaction, including cross-`Catalog` coordination. The `seenFileIDs` rule permits replacement when same-drive candidates are stale.
- `backend/internal/catalog/tag_assignments.go:328-345`, `ReplaceAutoVideoTags`: starts a transaction, invokes `replaceAutoVideoTagsTx`, keeps manual/source assignments according to policy, synchronizes the legacy `videos.tags` JSON representation on change, and commits. `replaceAutoVideoTagsTx` checks `tags_manual` inside its transaction, avoiding a stale pre-transaction manual-lock decision. No-op changes do not sync JSON or commit writes.
- `backend/internal/catalog/catalog.go:699-800`, `UpdateVideoMeta`: builds a single `UPDATE videos` statement for the patch. This is the 240-write competing workload in the test. `TagsSet` additionally calls `SetAutoVideoTags`, so callers using that option actually have a two-operation contract; the regression workload only sets `DurationSeconds` and does one statement.
- `backend/internal/catalog/catalog.go:225-245`, `UpsertVideo`: does a pre-read, executes `upsertVideoRow`, and can separately initialize manual or matched automatic tags. It is not the scanner admission API; do not replace scan duplicate admission with this upsert because conflict-on-ID updates existing rows.
- `backend/internal/catalog/catalog.go:247-255`, `videoRowExecer` / `upsertVideoRow`: generic helper currently accepts an `ExecContext` interface and can be reused in a transaction without taking ownership of it.
- `backend/internal/catalog/scan_duplicates.go:78-82`, `RecordScannedDuplicate`: currently records through the DB executor outside the scan-admission transaction for an existing row. Its implementation can invoke multi-statement duplicate-record logic; if included in an application retry, its whole write unit must have atomic/idempotent semantics reviewed first.
- `backend/internal/catalog/scan_presence.go`: scan-presence reconciliation uses a transaction and updates/deletes miss state. It may overlap other catalog writes during a scan but is not the failing path in this test.

### Other catalog write boundaries that can contend

Every catalog write shares SQLite's single writer slot, even when it writes a different table. Relevant runtime writers include:

- Scanner and related paths: `InsertScannedVideo`, `ReplaceAutoVideoTags`, `UpdateVideoMeta`, `RecordScannedDuplicate`, scan-presence reconciliation and scan result persistence.
- Metadata/background workers: direct `UpdateVideoMeta` calls; other direct video updates such as `UpdatePreview`, `HideVideo`, and fingerprint/status updates; these can overlap scans even when they are not the reproduction's exact writer.
- Tag/catalog mutations: `EnsureTag`, `SetAutoVideoTags`/`ReplaceAutoVideoTags`, tag CRUD and tag-reconciliation operations.
- Other subsystems: crawler upload result/remote upload job updates, Telegram receipts/local-file state, login/session/user operations, drive status/configuration, and other catalog transactions. Their table separation does not eliminate writer contention.

This is not a recommendation to wrap every SQL statement mechanically. Transactional operations must be retried at their full logical boundary, and direct single-statement mutations are candidates only when repeating the statement is safe under their API contract. Multi-step non-transactional public APIs (notably `UpdateVideoMeta` with `TagsSet`, and `UpsertVideo`) need special treatment rather than retrying only one internal statement or replaying partially completed workflows without idempotence analysis. First rollout should target the confirmed scan and metadata write paths; further expansion should be selected based on reliability requirements and verified API-level atomicity.

## Transaction, trigger, and semantic constraints

1. The scan admission transaction must continue taking the SQLite writer reservation **before** its duplicate reads. The explicit `BEGIN IMMEDIATE` exists specifically to avoid a stale WAL read snapshot followed by failed promotion, and must remain at the start of each fresh attempt.
2. `upsertVideoRow` is intentionally parameterized so scanner insertion and restore can reuse the row statement inside an existing transaction. For a combined insert/tag transaction, duplicate lookup, duplicate audit writes, video insertion (including `AFTER INSERT` triggers), tag assignment, tag JSON synchronization, and final commit would all need a shared transaction executor/query interface. Today `findScannedVideoDuplicate` needs `QueryContext`, while `replaceAutoVideoTagsTx` requires `*sql.Tx`; current helpers are therefore not directly composable with the explicit-`Conn` transaction.
3. Video insert/update triggers maintain canonical/representative state (`tag_migrations.go`, trigger-install SQL around `maintain_video_canonical_after_insert` and `maintain_video_representatives_after_insert`). The transaction refactor must not skip or reorder the ordinary SQL insert that fires them.
4. The tags JSON column is synchronized from `video_tags` by `syncVideoTagsJSONTx`; source/manual tags and the `tags_manual` protection must not be overwritten. Keep the current “unknown assignment label is skipped” and no-op behavior from `replaceAutoVideoTagsTx`.
5. Combining operations into one all-or-nothing transaction would change current error semantics: on non-busy tag error, current scanner keeps the video and records an `IssueTags`; whole-transaction rollback would discard the row, while committing after an arbitrary partially failed tag operation could leave partial tag state. A savepoint could isolate tag writes and preserve the insertion, but the API would have to explicitly report `inserted=true` and a tag error separately. Returning only `(bool, error)` cannot naturally communicate both successful admission and nonfatal tag failure. Do not silently conflate these states.
6. Existing duplicate semantics must not change: same ID/source is not re-admitted or overwritten; live duplicates record an outcome atomically and are skipped; stale same-drive duplicate candidates are ignored according to the snapshot's seen-file IDs; other-drive candidates remain valid duplicates.

## `modernc.org/sqlite` error typing and busy detection

- The backend imports the vendored `modernc.org/sqlite` driver (`backend/vendor/modernc.org/sqlite/sqlite.go`). Its public concrete error is `*sqlite.Error`, with `Code() int`; the driver error implementation constructs it with the SQLite result code (`conn.errstr`, around `sqlite.go:1420-1430`). Wrapped errors should be inspected with `errors.As`.
- The driver enables extended result codes for each connection during connect (`sqlite.go:854-860`, `extendedResultCodes(true)`). SQLite extended busy codes therefore may include `SQLITE_BUSY_RECOVERY` (261), `SQLITE_BUSY_SNAPSHOT` (517), and `SQLITE_BUSY_TIMEOUT` (773), in addition to primary `SQLITE_BUSY` (5). The vendor constants confirm their values (e.g. generated `lib/sqlite_linux_amd64.go`).
- A robust predicate should test the primary result-code byte, conceptually `sqliteErr.Code()&0xff == SQLITE_BUSY`, rather than equality with 5, so extended `BUSY_*` variants are included. Avoid string matching. Do not automatically broaden this to `SQLITE_LOCKED` (primary code 6): it is a distinct condition with different causes and retry assumptions.
- Existing catalog tests already use `errors.As(err, &sqliteErr)` and compare `sqliteErr.Code()` for the base busy error in `sqlite_transactions_test.go`; the driver currently exposes only `Code()`, not a `ExtendedCode()` method.

## Busy timeout and context behavior

- `Catalog.Open` in `backend/internal/catalog/catalog.go:56-77` opens SQLite with `journal_mode(WAL)`, `_pragma=busy_timeout(5000)`, and `_txlock=immediate`. The timeout is applied through modernc's connection pragmas; modernc sorts `busy_timeout` first when applying options (`vendor/modernc.org/sqlite/sqlite.go` around 897-907).
- `_txlock=immediate` makes ordinary `database/sql` write transactions reserve the writer at `BeginTx`; scan admission explicitly executes `BEGIN IMMEDIATE` on a reserved connection. This protects read-modify-write correctness but does not guarantee progress once the configured busy handler expires or SQLite returns busy without waiting.
- modernc's `ExecContext`/`QueryContext` paths check cancellation and install `interruptOnDone` (`sqlite.go` around 499-509 and 612-621), which invokes `sqlite3_interrupt`; there is an existing `TestCanceledSQLiteQueryDoesNotPoisonNextQuery` in `sqlite_cancellation_test.go`. Each attempt and any retry delay should use the original operation context. Retry wait must select on `ctx.Done()` and return `ctx.Err()` promptly. The finite busy timeout and retry budget compose: worst-case elapsed time can be several busy-timeout windows plus backoff, so choose a bounded overall attempt/deadline policy consciously; do not assume a retry loop alone makes the existing 20-second scanner test unlimited.
- A busy failure inside a transaction must not be retried by issuing the failed statement again in place. Roll back the full transaction and release its connection first; then begin a fresh immediate transaction and rerun the complete operation. For a commit-time busy result, ensure the failed transaction is rolled back/released before replay. Non-busy errors remain immediate failures.

## Assessment: combine scan admission and auto-tag assignment?

**Not safely by a small helper-call substitution.** The potential upside is fewer scan-side writer transitions: successful tagged insertions would move from an admission reservation/commit plus separate tag transaction to one reservation/commit. But today:

- insertion is implemented on `*sql.Conn` with an explicit SQL transaction, while auto-tagging accepts `*sql.Tx`;
- current `(bool, error)` admission and separate scanner issue accounting cannot represent “row admitted, tag step failed” as one combined atomic call;
- scanner behavior intentionally preserves a successfully inserted row when tag matching/reconciliation fails;
- query, tag, JSON synchronization and insert triggers all need to share one transaction; moving matching reads or calculation under the writer reservation would lengthen writer-lock duration if done carelessly.

A valid future combined API would need an explicit result shape for admission outcome and tag outcome, preserve the existing insertion-on-tag-error behavior (e.g. isolate tag changes behind a savepoint, roll back that savepoint on ordinary tag failure, commit the inserted row, and return the tag issue separately), and restart the **whole** operation on a busy error. It must preserve duplicate audit atomicity and tag manual/source semantics. This design is feasible, but its added result/error semantics and savepoint rollback behavior make it broader than the minimal targeted reliability correction. Avoid computing any new tag matching while holding the writer reservation; scanner already computes assignments before insert.

## Recommended bounded-retry boundary

1. Add a small internal catalog retry primitive (Go 1.23 compatible) that retries only classified SQLite busy errors, has a finite attempt/time budget and bounded backoff, and can be deterministically tested through an internal injected wait/policy seam. The production wait uses a timer and context select, not fixed sleeps.
2. Apply it around the **entire** scan-admission transaction in `InsertScannedVideo`: reserve connection, begin immediate, duplicate reads, duplicate recording or row insertion, commit. Every busy failure aborts/rolls back/releases before a new attempt. On a normal duplicate return `false,nil` without retry. Keep all source-identity and live/stale duplicate checks inside each attempt.
3. Apply it around the **entire** automatic tag replacement transaction in `ReplaceAutoVideoTags`; retry only after rollback and restart. This preserves manual-lock checks and avoids partial tag changes. No-op result remains `false,nil`.
4. Apply bounded retry at the `UpdateVideoMeta` logical API boundary for the test's idempotent single-statement metadata update. Be cautious with `TagsSet`, since that API performs another tagging call after the update and is not atomic today; either explicitly retry only the update statement before the second phase or design a dedicated API boundary, rather than replaying the whole function without considering tags' partial state.
5. Preserve SQLite as the writer arbiter across catalog instances/processes. Do not use an in-memory mutex/queue as the only serialization mechanism. Do not expand to arbitrary direct SQL operations until their multi-step idempotency/atomicity is reviewed.

This reduces busy failures without weakening duplicate admission or manual-tag semantics. It does not reduce writer acquisition count as much as a successful combined insert/tag transaction, but retries make eventual progress possible within the chosen finite budget while preserving current partial outcomes. If the reproduced load still exhausts the budget, measure attempt counts/wait duration and evaluate the explicit combined-transaction redesign rather than serializing tests or simply raising the timeout.

## Deterministic regression-test proposals (no arbitrary sleeps)

### Retry helper behavior

- Unit-test an injected operation that returns wrapped base `SQLITE_BUSY` and each extended `BUSY_*` code for the first N attempts, then succeeds. Assert attempt count, success value, and that non-busy errors are returned immediately. Also assert exhausted budget returns the final busy error and never exceeds the configured attempt count.
- Test the retry wait seam with channel-controlled fake waiting: after the operation signals that it returned busy and the wait hook signals entry, cancel the context; assert the helper exits with `context.Canceled` (or deadline error) without waiting on wall-clock sleeps. Verify cancellation before first attempt avoids invoking the operation.
- A small predicate unit test should ensure `Code()&0xff` identifies 5/261/517/773 as busy and does not treat primary `SQLITE_LOCKED` as busy.

### Catalog transaction retry/atomicity

- Use an internal per-attempt test hook or injected retry policy to force a first-attempt busy at a known transaction stage (including begin and, if practical, a write/commit boundary), then allow the next attempt. Assert only one video row / one duplicate record / one tag result is committed. Do not use a scheduler-dependent delay to “hope” the contention occurs.
- For cross-instance correctness, retain/extend `TestInsertScannedVideoCoordinatesConcurrentCatalogs` and assert duplicate admission remains single-winner after retries. Preserve `TestScannedVideoDuplicatePolicyPreservesLiveSources`, `TestInsertScannedVideoDoesNotOverwriteExistingSource`, rollback-trigger coverage, and connection-release-on-cancellation coverage.
- Add retry coverage to `ReplaceAutoVideoTags` while a manual tag lock is present and while assignments are successfully applied; assert manual assignments survive, auto assignment JSON/metadata is synchronized exactly once, and a failed busy attempt leaves no partial assignment.
- For production-path cancellation, use an explicit synchronization hook at retry-wait entry (or test an injected waiter) to cancel deterministically. Then assert no partially inserted video/tag state, writer connection released, and a subsequent operation succeeds. Existing `BeginWriteBarrier` tests provide a controlled cross-connection lock source, but the 5-second production timeout means a test should not simply wait for it to expire; inject a zero/short test retry policy or use a controlled retry hook.

### Workload regression

- Keep the real `TestConcurrentScansPreserveTagsWithMetadataWrites` expectations: all 120 rows, one expected auto tag per new video, all 240 metadata updates successful, no scan issues. Run uncached repeatedly and in the full backend suite only after targeted unit tests pass. Avoid altering its scan count, making it serial, or inserting sleeps.
- Verify Go 1.23 compatibility; the module target is Go 1.23 and tests must not use `testing.T.Chdir`.

## Validation/evidence limits

This report traces code and vendor source but does not rerun tests or measure lock-holder durations. It does not establish whether individual observed waits consumed the full five seconds, nor does it attribute a failure to any single transaction by timing. No source code, product tests, specs, or task files outside `research/` were changed.