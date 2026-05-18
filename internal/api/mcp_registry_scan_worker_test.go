package api

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// mcp_registry.go — scanRegistryRow + StartRegistrySyncWorker lifecycle.
//
// SyncMCPRegistry + List + Search are partially covered by other tests;
// these fill in the row scanner (whose 0%-coverage masked the bool
// int-conversion contract for is_verified/is_featured) and the
// background worker's shutdown path (close(stop) must wind the
// goroutine down without leaking it past server shutdown).
// ---------------------------------------------------------------------------

func TestScanRegistryRow_ConvertsIntBoolsCorrectly(t *testing.T) {
	// scanRegistryRow reads is_verified / is_featured as INTEGER and
	// translates to bool via `!= 0`. Pin both true and false cases
	// across both columns — a regression to `== 1` would silently
	// flip the bool for any non-1 truthy value (rare but possible
	// for hand-edited DBs).
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO mcp_registry_servers (
		id, name, display_name, description, icon, transport, homepage_url, source_url,
		package_name, package_registry, command, endpoint, auth_type, env_vars_json,
		category, is_verified, trust_tier, is_featured, synced_at)
		VALUES
		('s1', 'anthropic-server', 'Anthropic', 'desc-1', 'icon-1', 'stdio', 'https://x', 'https://src',
		 'pkg', 'npm', 'npx pkg', '', 'none', '[]',
		 'productivity', 1, 'anthropic', 1, '2026-05-17T00:00:00Z'),
		('s2', 'community-server', 'Community', 'desc-2', 'icon-2', 'http', 'https://y', 'https://src2',
		 '', '', '', 'https://api/mcp', 'bearer', '[{"name":"TOKEN"}]',
		 'data', 0, 'community', 0, '2026-05-17T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, err := db.Query(`SELECT ` + registrySelectCols + ` FROM mcp_registry_servers ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var got []mcpRegistryServerRow
	for rows.Next() {
		r, err := scanRegistryRow(rows)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}

	// s1: anthropic-tier, featured, verified
	if got[0].ID != "s1" || got[0].Name != "anthropic-server" {
		t.Errorf("s1 identity wrong: %+v", got[0])
	}
	if !got[0].IsVerified {
		t.Error("s1 IsVerified = false; DB row has is_verified=1")
	}
	if !got[0].IsFeatured {
		t.Error("s1 IsFeatured = false; DB row has is_featured=1")
	}
	if got[0].TrustTier != "anthropic" {
		t.Errorf("s1 TrustTier = %q, want anthropic", got[0].TrustTier)
	}
	if got[0].Transport != "stdio" || got[0].PackageName != "pkg" {
		t.Errorf("s1 transport/pkg fields wrong: %+v", got[0])
	}

	// s2: community-tier, not featured, not verified
	if got[1].IsVerified {
		t.Error("s2 IsVerified = true; DB row has is_verified=0")
	}
	if got[1].IsFeatured {
		t.Error("s2 IsFeatured = true; DB row has is_featured=0")
	}
	if got[1].TrustTier != "community" {
		t.Errorf("s2 TrustTier = %q, want community", got[1].TrustTier)
	}
	// Remote-server fields populated; stdio-only fields empty.
	if got[1].Endpoint != "https://api/mcp" {
		t.Errorf("s2 Endpoint = %q", got[1].Endpoint)
	}
	if got[1].AuthType != "bearer" {
		t.Errorf("s2 AuthType = %q, want bearer", got[1].AuthType)
	}
}

func TestScanRegistryRow_NonOneTruthyValuesStillBool(t *testing.T) {
	// SQL semantics: is_verified is an INTEGER. A row hand-inserted
	// with is_verified=2 (or any non-zero) MUST still produce
	// IsVerified=true — the `!= 0` check is load-bearing here.
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO mcp_registry_servers (
		id, name, display_name, transport, env_vars_json,
		is_verified, trust_tier, is_featured, synced_at)
		VALUES ('s-truthy', 'truthy', 'Truthy', 'stdio', '[]',
		 7, 'anthropic', 99, '2026-05-17T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := db.Query(`SELECT ` + registrySelectCols + ` FROM mcp_registry_servers WHERE id = 's-truthy'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no row returned")
	}
	r, err := scanRegistryRow(rows)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !r.IsVerified {
		t.Errorf("IsVerified for is_verified=7 = false; the `!= 0` contract requires true")
	}
	if !r.IsFeatured {
		t.Errorf("IsFeatured for is_featured=99 = false; the `!= 0` contract requires true")
	}
}

// ---- StartRegistrySyncWorker ----

func TestStartRegistrySyncWorker_StopChannelHaltsWaiterBeforeFirstSync(t *testing.T) {
	// The worker delays 10 seconds before the first sync. Closing
	// `stop` during that window must cause the goroutine to exit
	// without ever hitting the network sync — pin that the select-on-
	// stop in the initial delay fires.
	db := setupTestDB(t)
	logger := newTestLogger()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartRegistrySyncWorker(db, logger, stop, &wg)

	// Close stop immediately. The goroutine should observe the
	// signal via the `select { case <-stop: return ... }` in the
	// initial 10s delay.
	close(stop)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		// graceful
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop within 2s of close(stop); initial-delay select branch may not be firing")
	}
}

func TestStartRegistrySyncWorker_StopChannelIdempotent(t *testing.T) {
	// Standard WaitGroup safety check — calling Add(1) inside the
	// worker AND Wait() outside it must not deadlock if stop fires
	// before the goroutine reaches Add. (Source calls wg.Add inside
	// the function, BEFORE spawning the goroutine, so Wait() is safe
	// even on immediate stop.) Pin that property here.
	db := setupTestDB(t)
	logger := newTestLogger()
	stop := make(chan struct{})
	close(stop) // pre-closed before StartRegistrySyncWorker runs

	var wg sync.WaitGroup
	StartRegistrySyncWorker(db, logger, stop, &wg)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pre-closed stop did not unblock wg.Wait() within 2s")
	}
}

func TestStartRegistrySyncWorker_MultipleStartCallsAllShutdownIndependently(t *testing.T) {
	// Each StartRegistrySyncWorker call adds 1 to its own wg and
	// spawns its own goroutine. Multiple instances (e.g. server
	// restart in tests) must each respect their own stop channel.
	db := setupTestDB(t)
	logger := newTestLogger()

	var wg sync.WaitGroup
	stops := []chan struct{}{
		make(chan struct{}),
		make(chan struct{}),
		make(chan struct{}),
	}
	for _, stop := range stops {
		StartRegistrySyncWorker(db, logger, stop, &wg)
	}
	// Close all three.
	for _, stop := range stops {
		close(stop)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("3 workers did not all stop within 2s")
	}
}
