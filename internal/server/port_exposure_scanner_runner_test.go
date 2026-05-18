package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// port_exposure_scanner.go — scanExposures + runPortExposureScanner.
//
// diffAndEmit / emitPortEvent / formatPort / shortContainerID are
// already 100% covered by sibling tests. These two zero-coverage
// methods are the DB query + the run-loop wrapper.
// ---------------------------------------------------------------------------

// ---- scanExposures ----

func TestScanExposures_EmptyTable_ReturnsEmptyMap(t *testing.T) {
	db := openTestDB(t)
	got, err := scanExposures(context.Background(), db)
	if err != nil {
		t.Fatalf("scanExposures: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestScanExposures_ReturnsActiveOnly_DropsRevokedAndExpired(t *testing.T) {
	// The WHERE clause is `status = 'ACTIVE'`. REVOKED and EXPIRED
	// rows must NOT appear — the diff loop would otherwise emit
	// network.port_closed events for them on every restart.
	db := openTestDB(t)
	wsID := "ws_scan_active"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, wsID); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', ?, 'C', 'c1')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents
		(id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('agent-a', ?, 'crew-1', 'A', 'a', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	seed := func(id, status string, port int) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO port_exposures
			(id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
			VALUES (?, ?, 'crew-1', 'agent-a', ?, 'ct-x', '10.0.0.1', ?, ?, ?)`,
			id, wsID, "tok-"+id, port, status, now); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("active-1", "ACTIVE", 8001)
	seed("active-2", "ACTIVE", 8002)
	seed("revoked-1", "REVOKED", 8003)
	seed("expired-1", "EXPIRED", 8004)

	got, err := scanExposures(context.Background(), db)
	if err != nil {
		t.Fatalf("scanExposures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (only ACTIVE)", len(got))
	}
	for _, dropID := range []string{"revoked-1", "expired-1"} {
		if _, ok := got[dropID]; ok {
			t.Errorf("non-ACTIVE row %q appeared in scan output", dropID)
		}
	}
	for _, keepID := range []string{"active-1", "active-2"} {
		if _, ok := got[keepID]; !ok {
			t.Errorf("ACTIVE row %q missing from scan output", keepID)
		}
	}
}

func TestScanExposures_PopulatesAllSnapshotFields(t *testing.T) {
	// Verify every field of exposureSnapshot is populated correctly.
	// A regression that lost agent_id (the NullString path) or
	// container_port would silently break the WS payload downstream.
	db := openTestDB(t)
	wsID := "ws_scan_fields"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, wsID); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-f', ?, 'F', 'cf')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	// Seed an agent row so the FK reference succeeds.
	if _, err := db.Exec(`INSERT INTO agents
		(id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('agent-1', ?, 'crew-f', 'A', 'a', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO port_exposures
		(id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
		VALUES ('exp-1', ?, 'crew-f', 'agent-1', 'tok-full', 'ct-full-abc', '10.0.0.5', 9000, 'ACTIVE', ?)`,
		wsID, now); err != nil {
		t.Fatalf("seed exposure: %v", err)
	}

	got, err := scanExposures(context.Background(), db)
	if err != nil {
		t.Fatalf("scanExposures: %v", err)
	}
	snap, ok := got["exp-1"]
	if !ok {
		t.Fatal("exp-1 missing from snapshot")
	}
	if snap.WorkspaceID != wsID {
		t.Errorf("WorkspaceID = %q", snap.WorkspaceID)
	}
	if snap.CrewID != "crew-f" {
		t.Errorf("CrewID = %q", snap.CrewID)
	}
	if snap.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1 (NullString → string)", snap.AgentID)
	}
	if snap.ContainerID != "ct-full-abc" {
		t.Errorf("ContainerID = %q", snap.ContainerID)
	}
	if snap.ContainerPort != 9000 {
		t.Errorf("ContainerPort = %d, want 9000", snap.ContainerPort)
	}
}

func TestScanExposures_QueryError_BubblesUp(t *testing.T) {
	// Drop the table after opening so the query returns a real
	// "no such table" error. Pin that the wrapping doesn't swallow
	// the failure — runPortExposureScanner relies on this to log
	// transient DB failures at debug instead of silently looping.
	db := openTestDB(t)
	if _, err := db.Exec(`DROP TABLE port_exposures`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	_, err := scanExposures(context.Background(), db)
	if err == nil {
		t.Fatal("expected error after dropping table")
	}
}

// ---- runPortExposureScanner ----

func TestRunPortExposureScanner_NilDB_NoOpReturnsImmediately(t *testing.T) {
	// Source guard: nil db → return immediately. Pin that the runner
	// doesn't start a ticker, doesn't panic, and doesn't leak a
	// goroutine — useful for headless / minimal test wiring.
	done := make(chan struct{})
	go func() {
		runPortExposureScanner(context.Background(), nil, nil, nil)
		close(done)
	}()
	select {
	case <-done:
		// returned immediately as expected
	case <-time.After(1 * time.Second):
		t.Fatal("runPortExposureScanner with nil db did not return immediately")
	}
}

func TestRunPortExposureScanner_RespectsContextCancel(t *testing.T) {
	// With a real DB and a cancellable context, the runner must exit
	// promptly on cancel. The initial scan runs synchronously then
	// the loop sleeps on the ticker; ctx.Done in the select unblocks
	// it on the next tick.
	db := openTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runPortExposureScanner(ctx, db, nil, nil)
		close(done)
	}()

	// Wait a short moment for the initial scan, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runPortExposureScanner did not exit within 2s of ctx cancel")
	}
}

func TestRunPortExposureScanner_NilJournal_DoesNotCrash(t *testing.T) {
	// Source comment: "may be nil in which case we still run the loop
	// (cheap) but skip emits". diffAndEmit early-returns on nil
	// journal. Pin that the runner doesn't crash on a nil emitter
	// even when ACTIVE exposures change between scans (the diff loop
	// would otherwise call emitPortEvent on a nil interface).
	db := openTestDB(t)
	wsID := "ws_scan_nilj"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', ?)`, wsID, wsID); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-nj', ?, 'N', 'nj')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents
		(id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('agent-nj', ?, 'crew-nj', 'A', 'a', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO port_exposures
		(id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
		VALUES ('exp-nj', ?, 'crew-nj', 'agent-nj', 'tok-nj', 'ct-nj', '10.0.0.7', 7000, 'ACTIVE', ?)`,
		wsID, now); err != nil {
		t.Fatalf("seed exposure: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("runPortExposureScanner with nil journal panicked: %v", r)
			}
		}()
		runPortExposureScanner(ctx, db, nil, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runner with nil journal did not exit in 2s")
	}
}

// Compile-time guard: ensure scanExposures' return type stays
// map[string]exposureSnapshot — a refactor changing the snapshot type
// would surface here at build time rather than at a downstream caller.
func TestScanExposures_ReturnTypeStable(t *testing.T) {
	db := openTestDB(t)
	var got map[string]exposureSnapshot
	var err error
	got, err = scanExposures(context.Background(), db)
	if err != nil {
		t.Fatalf("scanExposures: %v", err)
	}
	_ = got
}

// Sentinel: keeps the errors import warm for later additions to this
// file without re-importing.
var _ = errors.New
