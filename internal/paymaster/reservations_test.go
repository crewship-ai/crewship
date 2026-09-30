package paymaster

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func reservationDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cost.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../database/migrations/20260930083100_restricted_cost_reservations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	return db, path
}
func reservationRequest() ReservationRequest {
	return ReservationRequest{Scope: Scope{WorkspaceID: "ws", AgentID: "agent", CrewID: "crew"}, PrincipalID: "human", AttemptID: "attempt", CredentialID: "key", Provider: "openai", Model: "gpt-5-mini", MaxInputTokens: 1000, MaxOutputTokens: 1000}
}
func reservationCap(t *testing.T, db *sql.DB, limit float64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('cap','ws','workspace','ws','month',?,'hard')`, limit); err != nil {
		t.Fatal(err)
	}
}
func TestReservationConcurrentConnections(t *testing.T) {
	db, path := reservationDB(t)
	reservationCap(t, db, .006)
	second, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, conn := range []*sql.DB{db, second} {
		wg.Add(1)
		go func(c *sql.DB) {
			defer wg.Done()
			<-start
			_, err := Reserve(context.Background(), c, reservationRequest())
			results <- err
		}(conn)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("wanted exactly one admitted provider request, got %d", successes)
	}
	var spent float64
	if err = db.QueryRow(`SELECT SUM(cost_usd) FROM cost_ledger`).Scan(&spent); err != nil || spent > .006 {
		t.Fatalf("overspend %.8f %v", spent, err)
	}
}
func TestReservationRestartUnknownAndChildrenCannotReset(t *testing.T) {
	db, path := reservationDB(t)
	reservationCap(t, db, .01)
	first, err := Reserve(t.Context(), db, reservationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err = Settle(t.Context(), db, first.ID, ReservationUsage{}); err != nil {
		t.Fatal(err)
	}
	if err = Settle(t.Context(), db, first.ID, ReservationUsage{Known: true, InputTokens: 1, OutputTokens: 1}); err == nil {
		t.Fatal("unknown debit later refunded")
	}
	db.Close()
	recovered, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	request := reservationRequest()
	request.AttemptID = "retry-child"
	request.PrincipalID = "other-human"
	request.Scope.AgentID = "other-agent"
	if _, err = Reserve(t.Context(), recovered, request); err == nil {
		t.Fatal("child/retry/restart reset shared budget")
	}
}
func TestReservationCrashPreservesPendingAndSettlement(t *testing.T) {
	db, path := reservationDB(t)
	reservationCap(t, db, .01)
	reserved, err := Reserve(t.Context(), db, reservationRequest())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	recovered, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, err = Reserve(t.Context(), recovered, reservationRequest()); err == nil {
		t.Fatal("crash refunded pending debit")
	}
	usage := ReservationUsage{Known: true, InputTokens: 100, OutputTokens: 100, CachedInputTokens: 50}
	if err = Settle(t.Context(), recovered, reserved.ID, usage); err != nil {
		t.Fatal(err)
	}
	if err = Settle(t.Context(), recovered, reserved.ID, usage); err != nil {
		t.Fatal("idempotent settle", err)
	}
	usage.OutputTokens = 0
	if err = Settle(t.Context(), recovered, reserved.ID, usage); err == nil {
		t.Fatal("conflicting repeated settle changed debit")
	}
	if _, err = Reserve(t.Context(), recovered, reservationRequest()); err != nil {
		t.Fatal("validated usage did not release unused debit", err)
	}
}
func TestReservationUnknownPricingMissingCapAndBadUsage(t *testing.T) {
	db, _ := reservationDB(t)
	if _, err := Reserve(t.Context(), db, reservationRequest()); err == nil {
		t.Fatal("missing cap admitted")
	}
	reservationCap(t, db, .02)
	request := reservationRequest()
	request.Model = "future-unpriced"
	if _, err := Reserve(t.Context(), db, request); err == nil {
		t.Fatal("fallback price admitted unknown model")
	}
	reserved, err := Reserve(t.Context(), db, reservationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE restricted_cost_reservations SET rate_input_per_m=0 WHERE id=?`, reserved.ID); err == nil {
		t.Fatal("price snapshot was mutable")
	}
	if err = Settle(t.Context(), db, reserved.ID, ReservationUsage{Known: true, InputTokens: 1001, OutputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	var state string
	var cost float64
	if err = db.QueryRow(`SELECT r.state,l.cost_usd FROM restricted_cost_reservations r JOIN cost_ledger l ON l.id=r.ledger_id WHERE r.id=?`, reserved.ID).Scan(&state, &cost); err != nil || state != "unknown" || cost < .005 {
		t.Fatalf("invalid usage refunded reservation %s %.8f %v", state, cost, err)
	}
}

func TestPendingReservationSurvivesWindowRollover(t *testing.T) {
	db, _ := reservationDB(t)
	reservationCap(t, db, .01)
	reserved, err := Reserve(t.Context(), db, reservationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE cost_ledger SET ts='2020-01-01T00:00:00Z' WHERE id=?`, reserved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = Reserve(t.Context(), db, reservationRequest()); err == nil {
		t.Fatal("calendar rollover refunded pending/crashed request")
	}
}

func TestReservationRequiresSharedCapAndRespectsNarrowerCaps(t *testing.T) {
	db, _ := reservationDB(t)
	if _, err := db.Exec(`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('agent-cap','ws','agent','agent','month',.1,'hard')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Reserve(t.Context(), db, reservationRequest()); err == nil {
		t.Fatal("agent-only cap can be reset by delegated target")
	}
	reservationCap(t, db, .1)
	if _, err := db.Exec(`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('crew-cap','ws','crew','crew','month',.001,'tiered')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Reserve(t.Context(), db, reservationRequest()); err == nil {
		t.Fatal("narrower tiered crew cap ignored")
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cost_ledger`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed admission left debit %d %v", rows, err)
	}
}
