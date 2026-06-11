package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func slowRegistryPage() []byte {
	page := registryListResponse{
		Servers: []registryEntryEnvelope{
			{
				Server: registryServerEntry{
					Name:        "example.com/slow-server",
					Title:       "Slow Server",
					Description: "served after a long stall",
					Packages: []registryPackage{{
						RegistryType: "npm",
						Identifier:   "@example/slow-server",
						Transport:    registryTransport{Type: "stdio"},
					}},
				},
				Meta: registryEntryMeta{Official: registryOfficialMeta{Status: "active", IsLatest: true}},
			},
		},
	}
	b, _ := json.Marshal(page)
	return b
}

// TestSyncMCPRegistry_DoesNotBlockWritersDuringFetch pins the #653
// fix: the sync must not hold a SQLite write transaction while it is
// waiting on the network. Pre-fix, BeginTx happened before the first
// page fetch, so a hung registry endpoint blocked every other writer
// in the process (observed live on dev1: 10+ minutes of
// "database is locked" across the journal writer, pipeline_runs
// projection, and the port-expose purger).
func TestSyncMCPRegistry_DoesNotBlockWritersDuringFetch(t *testing.T) {
	db := setupTestDB(t)

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRegistry := func() { releaseOnce.Do(func() { close(release) }) }

	var entered sync.Once
	fetchStarted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered.Do(func() { close(fetchStarted) })
		<-release // simulate a hung registry
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(slowRegistryPage())
	}))
	// Cleanup order matters (LIFO): the hung handler must be released
	// BEFORE srv.Close() waits on in-flight requests — otherwise a
	// failing assertion below turns into a hanging test instead of a
	// failing one. srv.Close registered first = runs last.
	t.Cleanup(srv.Close)
	t.Cleanup(releaseRegistry)
	prev := mcpRegistryURL
	mcpRegistryURL = srv.URL
	t.Cleanup(func() { mcpRegistryURL = prev })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	syncErr := make(chan error, 1)
	go func() { syncErr <- SyncMCPRegistry(context.Background(), db, logger) }()

	select {
	case <-fetchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("registry fetch never started")
	}

	// While the registry hangs mid-fetch, an unrelated write must go
	// through promptly. Pre-fix this blocks on the sync's open write
	// tx until the deadline trips.
	wctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := db.ExecContext(wctx,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws_lock_probe', 'Lock Probe', 'ws-lock-probe')`,
	); err != nil {
		t.Fatalf("concurrent write blocked while registry fetch was in flight: %v", err)
	}

	releaseRegistry()
	if err := <-syncErr; err != nil {
		t.Fatalf("sync: %v", err)
	}

	// The restructured sync must still land the fetched rows.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mcp_registry_servers WHERE name = 'example.com/slow-server'`).Scan(&count); err != nil {
		t.Fatalf("count synced: %v", err)
	}
	if count != 1 {
		t.Errorf("synced rows: got %d, want 1", count)
	}
}

// TestSyncMCPRegistry_DefaultDeadline pins the second half of #653:
// callers that pass a context with no deadline (the boot-time worker)
// must still be bounded — a registry that hangs forever must not pin
// the sync goroutine for the lifetime of the process.
func TestSyncMCPRegistry_DefaultDeadline(t *testing.T) {
	db := setupTestDB(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	prev := mcpRegistryURL
	mcpRegistryURL = srv.URL
	t.Cleanup(func() { mcpRegistryURL = prev })
	prevTimeout := mcpRegistrySyncTimeout
	mcpRegistrySyncTimeout = 100 * time.Millisecond
	t.Cleanup(func() { mcpRegistrySyncTimeout = prevTimeout })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := time.Now()
	err := SyncMCPRegistry(context.Background(), db, logger)
	if err == nil {
		t.Fatal("expected deadline error from hung registry")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("sync did not respect the default deadline (took %v)", elapsed)
	}
}
