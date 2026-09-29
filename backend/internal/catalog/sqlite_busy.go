package catalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	busyRetryAttempts = 3
	busyRetryBaseWait = 10 * time.Millisecond
	busyRetryMaxWait  = 100 * time.Millisecond
)

type busyRetryPolicy struct {
	attempts int
	wait     func(context.Context, time.Duration) error
}

var defaultBusyRetryPolicy = busyRetryPolicy{
	attempts: busyRetryAttempts,
	wait:     waitForRetry,
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
}

func retrySQLiteBusy[T any](ctx context.Context, policy busyRetryPolicy, operation func() (T, error)) (T, error) {
	var zero T
	if policy.attempts < 1 {
		return zero, errors.New("catalog: SQLite busy retry requires at least one attempt")
	}
	if policy.wait == nil {
		return zero, errors.New("catalog: SQLite busy retry requires a wait function")
	}

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := operation()
		if err == nil || !isSQLiteBusy(err) || attempt == policy.attempts {
			return value, err
		}
		wait := busyRetryBaseWait << (attempt - 1)
		if wait > busyRetryMaxWait {
			wait = busyRetryMaxWait
		}
		if err := policy.wait(ctx, wait); err != nil {
			return zero, err
		}
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func withImmediateWriteRetry[T any](ctx context.Context, db *sql.DB, policy busyRetryPolicy, operation func(*sql.Conn) (T, error)) (T, error) {
	return retrySQLiteBusy(ctx, policy, func() (T, error) {
		return withImmediateWrite(ctx, db, operation)
	})
}

// withImmediateWrite owns the connection and transaction for one whole attempt.
// In particular, a failed COMMIT is followed by ROLLBACK before the connection
// can return to database/sql's pool.
func withImmediateWrite[T any](ctx context.Context, db *sql.DB, operation func(*sql.Conn) (T, error)) (value T, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return value, err
	}
	defer conn.Close()

	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return value, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if _, rollbackErr := conn.ExecContext(context.Background(), `ROLLBACK`); rollbackErr != nil {
			// database/sql will discard a connection whose Raw callback returns
			// driver.ErrBadConn, preventing a possibly open SQLite transaction
			// from being reused if rollback itself could not be confirmed.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	value, err = operation(conn)
	if err != nil {
		return value, err
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return value, err
	}
	committed = true
	return value, nil
}
