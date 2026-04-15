package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var instanceMemCounter atomic.Uint64

func TestIsInstanceOwner_GatesOnEnv(t *testing.T) {
	t.Setenv(InstanceOwnerEmailEnv, "")
	if IsInstanceOwner("anyone@example.com") {
		t.Error("empty env must block every caller")
	}
	t.Setenv(InstanceOwnerEmailEnv, "admin@example.com")
	if !IsInstanceOwner("admin@example.com") {
		t.Error("exact match should pass")
	}
	if !IsInstanceOwner("ADMIN@example.com") {
		t.Error("case-insensitive match should pass")
	}
	if !IsInstanceOwner(" admin@example.com ") {
		t.Error("padded email should be trimmed")
	}
	if IsInstanceOwner("intruder@example.com") {
		t.Error("different email must be rejected")
	}
}

func TestEnsureInstanceHostname_Idempotent(t *testing.T) {
	db := newInstanceConfigDB(t)
	ctx := context.Background()
	host, _ := os.Hostname()

	if err := EnsureInstanceHostname(ctx, db); err != nil {
		t.Fatalf("first call: %v", err)
	}
	got := CurrentInstanceHostname(ctx, db)
	if got != host {
		t.Fatalf("hostname: got %q want %q", got, host)
	}
	// Second call must be a no-op.
	if err := EnsureInstanceHostname(ctx, db); err != nil {
		t.Fatalf("second call: %v", err)
	}
	got2 := CurrentInstanceHostname(ctx, db)
	if got2 != host {
		t.Fatalf("hostname changed on idempotent call: %q", got2)
	}
}

func TestIsCrossInstanceRestore_SameHost(t *testing.T) {
	db := newInstanceConfigDB(t)
	ctx := context.Background()
	if err := EnsureInstanceHostname(ctx, db); err != nil {
		t.Fatalf("EnsureInstanceHostname: %v", err)
	}

	host, _ := os.Hostname()
	m := &Manifest{SourceInstance: Instance{Hostname: host}}
	if IsCrossInstanceRestore(ctx, db, m) {
		t.Error("same-host restore should NOT be flagged as cross-instance")
	}
}

func TestIsCrossInstanceRestore_DifferentHost(t *testing.T) {
	db := newInstanceConfigDB(t)
	ctx := context.Background()
	if err := EnsureInstanceHostname(ctx, db); err != nil {
		t.Fatalf("EnsureInstanceHostname: %v", err)
	}

	m := &Manifest{SourceInstance: Instance{Hostname: "somewhere-else"}}
	if !IsCrossInstanceRestore(ctx, db, m) {
		t.Error("different-host restore must be flagged as cross-instance")
	}
}

func TestIsCrossInstanceRestore_EmptySourceErrorsOnSide(t *testing.T) {
	db := newInstanceConfigDB(t)
	ctx := context.Background()
	if err := EnsureInstanceHostname(ctx, db); err != nil {
		t.Fatalf("EnsureInstanceHostname: %v", err)
	}

	m := &Manifest{SourceInstance: Instance{Hostname: ""}}
	if !IsCrossInstanceRestore(ctx, db, m) {
		t.Error("empty-source manifest must be treated as cross-instance (fail-safe)")
	}
}

// newInstanceConfigDB builds an in-memory sqlite DB with the
// instance_config table shaped like migration v50.
func newInstanceConfigDB(t *testing.T) *sql.DB {
	t.Helper()
	// Shared-cache in-memory DSN so pooled connections see a single
	// DB (plain ":memory:" is per-connection on modernc.org/sqlite).
	name := fmt.Sprintf("crewship-instance-test-%d", instanceMemCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE instance_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    hostname TEXT NOT NULL DEFAULT '',
    installed_at TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO instance_config (id, hostname) VALUES (1, '');
`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestAllowInstanceBackup_EnforcesWindow(t *testing.T) {
	ResetInstanceBackupLimiter()
	ok, _ := AllowInstanceBackup("u-1")
	if !ok {
		t.Fatal("first attempt must be allowed")
	}
	ok, retry := AllowInstanceBackup("u-1")
	if ok {
		t.Fatal("second attempt in window must be rejected")
	}
	if retry <= 0 || retry > time.Hour+time.Second {
		t.Errorf("retry-after out of range: %v", retry)
	}
	// Different user must be independent.
	if ok2, _ := AllowInstanceBackup("u-2"); !ok2 {
		t.Error("per-user isolation broken")
	}
}

func TestShouldRotateAuthKeysOnRestore(t *testing.T) {
	db := newInstanceConfigDB(t)
	ctx := context.Background()
	if err := EnsureInstanceHostname(ctx, db); err != nil {
		t.Fatalf("EnsureInstanceHostname: %v", err)
	}
	host, _ := os.Hostname()

	cases := []struct {
		name   string
		m      *Manifest
		expect bool
	}{
		{"non-instance scope", &Manifest{Scope: ScopeWorkspace, SourceInstance: Instance{Hostname: host}}, false},
		{"instance same host", &Manifest{Scope: ScopeInstance, SourceInstance: Instance{Hostname: host}}, false},
		{"instance diff host", &Manifest{Scope: ScopeInstance, SourceInstance: Instance{Hostname: "other"}}, true},
		{"instance unknown host", &Manifest{Scope: ScopeInstance}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldRotateAuthKeysOnRestore(ctx, db, c.m); got != c.expect {
				t.Errorf("got %v want %v", got, c.expect)
			}
		})
	}
}
