package bbolt

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func tempDB(t *testing.T) *Provider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	p, err := New(path)
	if err != nil {
		t.Fatalf("new bbolt provider: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestSetAndGet(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()

	if err := p.Set(ctx, "runs", "run-1", []byte(`{"status":"running"}`)); err != nil {
		t.Fatal(err)
	}

	val, err := p.Get(ctx, "runs", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(val) != `{"status":"running"}` {
		t.Fatalf("unexpected value: %s", val)
	}
}

func TestGetMissingBucket(t *testing.T) {
	p := tempDB(t)
	val, err := p.Get(context.Background(), "nonexistent", "key")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatalf("expected nil, got %s", val)
	}
}

func TestGetMissingKey(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()
	if err := p.Set(ctx, "runs", "exists", []byte("data")); err != nil {
		t.Fatal(err)
	}

	val, err := p.Get(ctx, "runs", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatalf("expected nil, got %s", val)
	}
}

func TestDelete(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()
	if err := p.Set(ctx, "runs", "run-1", []byte("data")); err != nil {
		t.Fatal(err)
	}

	if err := p.Delete(ctx, "runs", "run-1"); err != nil {
		t.Fatal(err)
	}

	val, err := p.Get(ctx, "runs", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatalf("expected nil after delete, got %s", val)
	}
}

func TestDeleteMissingBucket(t *testing.T) {
	p := tempDB(t)
	if err := p.Delete(context.Background(), "nonexistent", "key"); err != nil {
		t.Fatal(err)
	}
}

func TestList(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()
	for _, kv := range []struct{ k, v string }{{"a", "1"}, {"b", "2"}, {"c", "3"}} {
		if err := p.Set(ctx, "runs", kv.k, []byte(kv.v)); err != nil {
			t.Fatal(err)
		}
	}

	result, err := p.List(ctx, "runs")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result))
	}
	if string(result["b"]) != "2" {
		t.Fatalf("expected '2', got '%s'", result["b"])
	}
}

func TestListByPrefix(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()
	for _, kv := range []struct{ k, v string }{{"crew-a:run-1", "1"}, {"crew-a:run-2", "2"}, {"crew-b:run-1", "3"}} {
		if err := p.Set(ctx, "runs", kv.k, []byte(kv.v)); err != nil {
			t.Fatal(err)
		}
	}

	result, err := p.ListByPrefix(ctx, "runs", "crew-a:")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result))
	}
}

func TestNewCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	path := filepath.Join(dir, "test.db")
	p, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("expected directory to be created")
	}
}

// holdLock opens path with its own bbolt handle and keeps the flock for the
// duration of the test, standing in for "another crewship instance is already
// running against this state.db".
func holdLock(t *testing.T, path string) {
	t.Helper()
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("hold lock on %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
}

// shortLockWait shrinks the production 10 s lock budget for the duration of a
// test. The tests still exercise the real probe → warn → wait → error path;
// they just do not spend ten seconds proving it.
func shortLockWait(t *testing.T) {
	t.Helper()
	prev := lockWait
	lockWait = 400 * time.Millisecond
	t.Cleanup(func() { lockWait = prev })
}

// B-04, observed in production: a second crewshipd on a host where one was
// already running blocked here forever. bolt.Open was called with Timeout: 0,
// which means "wait for the flock indefinitely", and nothing was logged — the
// last line the operator saw was "docker provider disabled", then silence for
// 25 s until a signal killed it. From outside it looked like a slow start; it
// never started at all.
//
// The guard timer is what makes this a test rather than a hang: a regression to
// Timeout: 0 fails the test instead of wedging CI.
func TestNewFailsFastWhenLocked(t *testing.T) {
	tests := []struct {
		name     string
		locked   bool
		wantErr  bool
		wantText []string
	}{
		{
			name:   "free file opens",
			locked: false,
		},
		{
			name:    "locked file returns a descriptive error",
			locked:  true,
			wantErr: true,
			// The error has to name the file and say who is likely holding
			// it — that is the whole difference from the silent hang.
			wantText: []string{"state.db", "lock"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shortLockWait(t)
			path := filepath.Join(t.TempDir(), "state.db")
			if tc.locked {
				holdLock(t, path)
			}

			type result struct {
				p   *Provider
				err error
			}
			done := make(chan result, 1)
			go func() {
				p, err := New(path)
				done <- result{p, err}
			}()

			select {
			case r := <-done:
				if r.p != nil {
					r.p.Close()
				}
				if tc.wantErr {
					if r.err == nil {
						t.Fatalf("New(%s) succeeded while the file was locked", path)
					}
					if !errors.Is(r.err, ErrLocked) {
						t.Errorf("error %v does not match ErrLocked", r.err)
					}
					for _, want := range tc.wantText {
						if !strings.Contains(r.err.Error(), want) {
							t.Errorf("error %q does not mention %q", r.err, want)
						}
					}
				} else if r.err != nil {
					t.Fatalf("New(%s) on a free file: %v", path, r.err)
				}
			case <-time.After(20 * time.Second):
				t.Fatalf("New(%s) did not return within 20s — it is blocking on the bbolt lock forever (B-04)", path)
			}
		})
	}
}

// The hang was invisible as well as unbounded. Before it blocks, the open must
// say on the default logger that it is waiting and for which file, so an
// operator watching the journal sees the cause instead of silence.
func TestNewLogsBeforeWaitingForLock(t *testing.T) {
	shortLockWait(t)
	path := filepath.Join(t.TempDir(), "state.db")
	holdLock(t, path)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p, err := New(path)
	if err == nil {
		p.Close()
		t.Fatal("New succeeded while the file was locked")
	}
	logged := buf.String()
	if !strings.Contains(logged, path) {
		t.Errorf("log output %q does not name the contended file %q", logged, path)
	}
	if !strings.Contains(strings.ToLower(logged), "wait") {
		t.Errorf("log output %q never says it is waiting for the lock", logged)
	}
}

// The production default is the value that actually protects a real start, and
// the failure mode it guards against is a regression to Timeout: 0 (wait
// forever) or to some multi-minute budget that is indistinguishable from a
// hang for an operator watching the journal.
func TestLockWaitIsBounded(t *testing.T) {
	if lockWait <= 0 {
		t.Fatalf("lockWait = %s — a non-positive wait is the block-forever bug (B-04)", lockWait)
	}
	if lockWait > 30*time.Second {
		t.Errorf("lockWait = %s — too long to be distinguishable from a hang", lockWait)
	}
	if lockProbe <= 0 || lockProbe >= lockWait {
		t.Errorf("lockProbe = %s must be a small positive slice of lockWait (%s)", lockProbe, lockWait)
	}
}
