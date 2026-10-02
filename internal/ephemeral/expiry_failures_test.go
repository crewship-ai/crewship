package ephemeral

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestSweepPartialFailureOnlyAnnouncesCommittedExpirations(t *testing.T) {
	db := setupTestDB(t)
	ws, crew := seedWorkspaceAndCrew(t, db)
	db.SetMaxOpenConns(1)
	for _, id := range []string{"blocked-one", "blocked-two", "healthy"} {
		seedEphemeral(t, db, ws, crew, id, "2000-01-01T00:00:00Z", nil)
	}
	if _, err := db.Exec(`CREATE TEMP TRIGGER fail_expiry BEFORE UPDATE OF expired_at ON agents WHEN OLD.id LIKE 'blocked-%' BEGIN SELECT RAISE(ABORT,'storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	events := &recordingBroadcaster{}
	n, err := SweepExpiredAgents(t.Context(), db, nil, events, nil)
	if n != 1 || err == nil {
		t.Fatalf("partial result = %d, %v", n, err)
	}
	if got := events.Events(); len(got) != 1 || got[0]["id"] != "healthy" {
		t.Fatalf("announced uncommitted expirations: %+v", got)
	}
	for _, id := range []string{"blocked-one", "blocked-two", "healthy"} {
		var expired bool
		if err := db.QueryRow(`SELECT expired_at IS NOT NULL FROM agents WHERE id=?`, id).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired != (id == "healthy") {
			t.Fatalf("wrong committed state for %s", id)
		}
	}
	if _, err := db.Exec(`DROP TRIGGER fail_expiry`); err != nil {
		t.Fatal(err)
	}
	n, err = SweepExpiredAgents(t.Context(), db, nil, events, time.Now)
	if n != 2 || err != nil {
		t.Fatalf("retry should expire only previously failed rows: %d, %v", n, err)
	}
}

func TestSweepUnavailableAndMalformedStorageNeverExpiresAnAgent(t *testing.T) {
	db := setupTestDB(t)
	ws, crew := seedWorkspaceAndCrew(t, db)
	seedEphemeral(t, db, ws, crew, "due", "2000-01-01T00:00:00Z", nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if n, err := SweepExpiredAgents(ctx, db, nil, nil, nil); n != 0 || err == nil {
		t.Fatalf("cancelled sweep = %d, %v", n, err)
	}
	db.SetMaxOpenConns(1)
	// A malformed storage projection must abort the read, not use partially
	// scanned agent identities for the expiry write.
	if _, err := db.Exec(`CREATE TEMP VIEW agents AS SELECT id, workspace_id, crew_id, NULL AS name, parent_lead_id, expires_at, ephemeral, expired_at, deleted_at, status FROM main.agents`); err != nil {
		t.Fatal(err)
	}
	if n, err := SweepExpiredAgents(t.Context(), db, nil, nil, nil); n != 0 || err == nil {
		t.Fatalf("malformed sweep = %d, %v", n, err)
	}
	var expired bool
	if err := db.QueryRow(`SELECT expired_at IS NOT NULL FROM main.agents WHERE id='due'`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired {
		t.Fatal("malformed sweep changed the agent")
	}
}

type sweepLogChannel chan string

func (c sweepLogChannel) Write(p []byte) (int, error) {
	select {
	case c <- string(p):
	default:
	}
	return len(p), nil
}

func TestSweeperReportsStorageFailure(t *testing.T) {
	db := setupTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	logs := make(sweepLogChannel, 8)
	StartExpirySweeper(ctx, db, nil, nil, time.Millisecond, slog.New(slog.NewTextHandler(logs, nil)))
	select {
	case line := <-logs:
		if !strings.Contains(line, "sweep error") || !strings.Contains(line, "database is closed") {
			t.Fatalf("failure not actionable: %s", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sweeper silently swallowed database failure")
	}
}
