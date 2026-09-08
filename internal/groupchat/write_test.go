package groupchat

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestWriteAdmissionSharedStoresLeavePoolAvailableAndHonorCancellation(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	first, second := New(db), New(db)
	release, err := AcquireWriteAdmission(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	const count = 8
	entered := make(chan struct{}, count)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	failures := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := first
			if i%2 == 1 {
				store = second
			}
			failures <- store.write(ctx, func(querier) error { entered <- struct{}{}; return nil })
		}(i)
	}
	// Observe admission membership, not a sleep: all writers must have queued
	// before asserting none owns a connection or executes SQL.
	deadline := time.After(5 * time.Second)
	for {
		writerAdmissions.Lock()
		queued := writerAdmissions.databases[db].refs
		writerAdmissions.Unlock()
		if queued == count+1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("writers did not reach admission")
		case <-time.After(time.Millisecond):
		}
	}
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Fatalf("queued writes occupy %d database connections", inUse)
	}
	var one int
	if err := db.QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("reader blocked: %d %v", one, err)
	}
	cancel()
	wg.Wait()
	close(failures)
	for err := range failures {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation: %v", err)
		}
	}
	if len(entered) != 0 {
		t.Fatal("canceled queued callbacks executed")
	}
	release()
	release() // explicit+defer release is supported for API continuations
	writerAdmissions.Lock()
	_, retained := writerAdmissions.databases[db]
	writerAdmissions.Unlock()
	if retained {
		t.Fatal("idle DB retained in writer registry")
	}
	if err := second.write(t.Context(), func(querier) error { return nil }); err != nil {
		t.Fatalf("next writer failed: %v", err)
	}
}

func TestWriteTransactionCanceledCallbackRollsBackBeforeConnectionReuse(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE admission_test(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	sentinel := errors.New("abort after mutation")
	calls := 0
	err := WithWriteTransaction(ctx, db, func(conn *sql.Conn) error {
		calls++
		if _, err := conn.ExecContext(ctx, "INSERT INTO admission_test VALUES('must roll back')"); err != nil {
			return err
		}
		cancel()
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("callback result=%v calls=%d", err, calls)
	}
	if err := WithWriteTransaction(t.Context(), db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(t.Context(), "INSERT INTO admission_test VALUES('committed')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM admission_test").Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback/reuse rows=%d err=%v", count, err)
	}
}

func TestWriteAdmissionDifferentDatabasesRemainIndependent(t *testing.T) {
	first, second := testutil.MigratedSQLDB(t), testutil.MigratedSQLDB(t)
	release, err := AcquireWriteAdmission(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := WithWriteTransaction(ctx, second, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatalf("unrelated DB blocked: %v", err)
	}
}
