package catalog

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func sqliteBusyError(t *testing.T, db *sql.DB) error {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	_, err = db.ExecContext(context.Background(), `BEGIN IMMEDIATE`)
	if err == nil {
		t.Fatal("second writer unexpectedly acquired the lock")
	}
	return err
}

func TestIsSQLiteBusyClassifiesPrimaryAndExtendedCodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE item (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	baseBusy := sqliteBusyError(t, db)
	if !isSQLiteBusy(baseBusy) {
		t.Fatalf("base busy error %v was not classified", baseBusy)
	}

	reader, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM item`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO item DEFAULT VALUES`); err != nil {
		t.Fatal(err)
	}
	_, snapshotErr := reader.Exec(`INSERT INTO item DEFAULT VALUES`)
	_ = reader.Rollback()
	if snapshotErr == nil {
		t.Fatal("stale WAL snapshot unexpectedly promoted to a writer")
	}
	if !isSQLiteBusy(snapshotErr) {
		t.Fatalf("extended busy error %v was not classified", snapshotErr)
	}
	var sqliteErr interface{ Code() int }
	if !errors.As(snapshotErr, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_BUSY_SNAPSHOT {
		t.Fatalf("snapshot error code = %v, want SQLITE_BUSY_SNAPSHOT", snapshotErr)
	}

	lockedLike := errors.New("not a SQLite busy result")
	if isSQLiteBusy(lockedLike) || isSQLiteBusy(errors.New("database is locked")) {
		t.Fatal("non-SQLite or SQLITE_LOCKED-like errors must not be classified as busy")
	}
}

func TestRetrySQLiteBusyPolicy(t *testing.T) {
	busyDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "busy.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer busyDB.Close()
	if _, err := busyDB.Exec(`CREATE TABLE item (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	busyErr := sqliteBusyError(t, busyDB)

	t.Run("success after busy", func(t *testing.T) {
		calls, waits := 0, 0
		policy := busyRetryPolicy{attempts: 3, wait: func(context.Context, time.Duration) error {
			waits++
			return nil
		}}
		got, err := retrySQLiteBusy(context.Background(), policy, func() (int, error) {
			calls++
			if calls < 3 {
				return 0, busyErr
			}
			return 42, nil
		})
		if err != nil || got != 42 || calls != 3 || waits != 2 {
			t.Fatalf("got=%d err=%v calls=%d waits=%d", got, err, calls, waits)
		}
	})

	t.Run("exhaustion", func(t *testing.T) {
		calls := 0
		policy := busyRetryPolicy{attempts: 2, wait: func(context.Context, time.Duration) error { return nil }}
		_, err := retrySQLiteBusy(context.Background(), policy, func() (int, error) {
			calls++
			return 0, busyErr
		})
		if !errors.Is(err, busyErr) || calls != 2 {
			t.Fatalf("error=%v calls=%d, want final busy error after two attempts", err, calls)
		}
	})

	t.Run("non-busy is not retried", func(t *testing.T) {
		calls := 0
		wantErr := errors.New("permanent failure")
		policy := busyRetryPolicy{attempts: 3, wait: func(context.Context, time.Duration) error { return nil }}
		_, err := retrySQLiteBusy(context.Background(), policy, func() (int, error) {
			calls++
			return 0, wantErr
		})
		if !errors.Is(err, wantErr) || calls != 1 {
			t.Fatalf("error=%v calls=%d, want immediate permanent error", err, calls)
		}
	})

	t.Run("cancellation during wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		waiting := make(chan struct{})
		var calls atomic.Int32
		policy := busyRetryPolicy{attempts: 3, wait: func(ctx context.Context, _ time.Duration) error {
			close(waiting)
			<-ctx.Done()
			return ctx.Err()
		}}
		done := make(chan error, 1)
		go func() {
			_, err := retrySQLiteBusy(ctx, policy, func() (int, error) {
				calls.Add(1)
				return 0, busyErr
			})
			done <- err
		}()
		<-waiting
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) || calls.Load() != 1 {
			t.Fatalf("error=%v calls=%d, want cancellation after one attempt", err, calls.Load())
		}
	})

	t.Run("canceled before first attempt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		policy := busyRetryPolicy{attempts: 2, wait: func(context.Context, time.Duration) error { return nil }}
		_, err := retrySQLiteBusy(ctx, policy, func() (int, error) {
			calls++
			return 1, nil
		})
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Fatalf("error=%v calls=%d, want canceled without operation", err, calls)
		}
	})
}

func TestImmediateWriteRollsBackCommitFailureAndKeepsConnectionUsable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commit-failure.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE parent (id INTEGER PRIMARY KEY);
CREATE TABLE child (parent_id INTEGER, FOREIGN KEY(parent_id) REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}

	_, err = withImmediateWrite(context.Background(), db, func(conn *sql.Conn) (struct{}, error) {
		_, err := conn.ExecContext(context.Background(), `INSERT INTO child(parent_id) VALUES (99)`)
		return struct{}{}, err
	})
	if err == nil {
		t.Fatal("deferred foreign-key violation unexpectedly committed")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM child`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed commit left partial child rows: count=%d err=%v", count, err)
	}
	if _, err := withImmediateWrite(context.Background(), db, func(conn *sql.Conn) (struct{}, error) {
		_, err := conn.ExecContext(context.Background(), `INSERT INTO parent(id) VALUES (99)`)
		return struct{}{}, err
	}); err != nil {
		t.Fatalf("connection unusable after failed commit rollback: %v", err)
	}
}

func TestImmediateWriteDiscardsConnectionWhenRollbackCannotBeConfirmed(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "rollback-failure.db")+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE rollback_probe (id INTEGER PRIMARY KEY);
CREATE TRIGGER rollback_probe_abort BEFORE INSERT ON rollback_probe
BEGIN SELECT RAISE(ROLLBACK, 'force transaction rollback'); END;`); err != nil {
		t.Fatal(err)
	}
	pooled, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pooled.ExecContext(context.Background(), `CREATE TEMP TABLE connection_marker (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := pooled.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = withImmediateWrite(context.Background(), db, func(conn *sql.Conn) (struct{}, error) {
		_, err := conn.ExecContext(context.Background(), `INSERT INTO rollback_probe(id) VALUES (1)`)
		return struct{}{}, err
	})
	if err == nil {
		t.Fatal("rollback trigger did not fail the operation")
	}
	usable, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer usable.Close()
	var markerCount int
	if err := usable.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_temp_master WHERE name='connection_marker'`).Scan(&markerCount); err != nil || markerCount != 0 {
		t.Fatalf("connection with unconfirmed transaction was reused: marker count=%d err=%v", markerCount, err)
	}
	var count int
	if err := usable.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM rollback_probe`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback failure left partial writes or poisoned pool: count=%d err=%v", count, err)
	}
}

func TestImmediateWriteRetryRollsBackNonIdempotentMetadataMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	cat, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	if _, err := cat.db.Exec(`CREATE TABLE retry_probe (id INTEGER PRIMARY KEY, thumbnail_updated_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.db.Exec(`INSERT INTO retry_probe(id, thumbnail_updated_at) VALUES (1, NULL)`); err != nil {
		t.Fatal(err)
	}
	competitor, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer competitor.Close()

	attempts := 0
	policy := busyRetryPolicy{attempts: 2, wait: func(context.Context, time.Duration) error { return nil }}
	_, err = withImmediateWriteRetry(context.Background(), cat.db, policy, func(conn *sql.Conn) (struct{}, error) {
		attempts++
		if _, err := conn.ExecContext(context.Background(), `UPDATE retry_probe
SET thumbnail_updated_at=MAX(COALESCE(thumbnail_updated_at, 0) + 1, 100) WHERE id=1`); err != nil {
			return struct{}{}, err
		}
		if attempts == 1 {
			_, err := competitor.Exec(`INSERT INTO retry_probe(id, thumbnail_updated_at) VALUES (2, 0)`)
			if err == nil {
				return struct{}{}, errors.New("competitor unexpectedly wrote during immediate transaction")
			}
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("error=%v attempts=%d, want successful second attempt", err, attempts)
	}
	var updatedAt int
	if err := cat.db.QueryRow(`SELECT thumbnail_updated_at FROM retry_probe WHERE id=1`).Scan(&updatedAt); err != nil || updatedAt != 100 {
		t.Fatalf("retry_probe thumbnail_updated_at=%d error=%v, want one committed update", updatedAt, err)
	}
	if _, err := competitor.Exec(`INSERT INTO retry_probe(id, thumbnail_updated_at) VALUES (2, 0)`); err != nil {
		t.Fatalf("connection remained locked after retry: %v", err)
	}
}
