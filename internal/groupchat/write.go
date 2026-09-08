package groupchat

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"time"
)

type writerAdmission struct {
	token chan struct{}
	refs  int // holder plus waiters, including canceled callers until they leave
}

var writerAdmissions = struct {
	sync.Mutex
	databases map[*sql.DB]*writerAdmission
}{databases: make(map[*sql.DB]*writerAdmission)}

// AcquireWriteAdmission queues before taking a pooled connection. Release is
// idempotent and must run only after the transaction and connection are closed. SQLite's busy handler does not
// provide writer fairness: several waiting BEGINs can occupy the whole pool,
// starving both readers and a writer repeatedly overtaken by inbox projection.
// Admissions are shared by independent Store/Dispatcher instances for this DB;
// idle entries are removed so test/temporary databases are not retained forever.
func AcquireWriteAdmission(ctx context.Context, db *sql.DB) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	writerAdmissions.Lock()
	admission := writerAdmissions.databases[db]
	if admission == nil {
		admission = &writerAdmission{token: make(chan struct{}, 1)}
		writerAdmissions.databases[db] = admission
	}
	admission.refs++
	writerAdmissions.Unlock()
	unref := func() {
		writerAdmissions.Lock()
		admission.refs--
		if admission.refs == 0 {
			delete(writerAdmissions.databases, db)
		}
		writerAdmissions.Unlock()
	}
	select {
	case admission.token <- struct{}{}:
		release := sync.OnceFunc(func() { <-admission.token; unref() })
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		unref()
		return nil, ctx.Err()
	}
}

// WithWriteTransaction coordinates conversation storage and durable inbox
// projection. The callback runs exactly once inside BEGIN IMMEDIATE; callbacks
// must use the provided connection and must not nest another admission.
// SQLite still arbitrates writers from other processes/subsystems normally.
func WithWriteTransaction(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) error {
	release, err := AcquireWriteAdmission(ctx, db)
	if err != nil {
		return err
	}
	defer release()
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, rollbackErr := c.ExecContext(cleanup, "ROLLBACK"); rollbackErr != nil {
			// Never return a potentially open transaction to the shared pool.
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err = fn(c); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	committed = err == nil
	return err
}
