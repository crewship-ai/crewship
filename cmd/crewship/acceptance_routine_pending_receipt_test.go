package main

// The built CLI reads real migrated receipts through the real authenticated API.
// A dispatcher error creates the failure; the CLI must expose it by stable ID.
import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type pendingAcceptanceRejector struct{}

func (pendingAcceptanceRejector) Run(context.Context, pipeline.RunInput) (*pipeline.RunResult, error) {
	return nil, errors.New("private executor detail must not reach the receipt")
}

func TestAcceptance_RoutinePendingReceipt_RealDispatchFailure(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	const ws = "cpendingreceipt000001"
	const token = "crewship_cli_pendingreceipt000000000000000000"
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,full_name) VALUES('pending-owner','pending@example.test','Owner')`, nil},
		{`INSERT INTO workspaces(id,name,slug) VALUES(?,'Pending','pending-receipt')`, []any{ws}},
		{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pending-member',?,'pending-owner','OWNER')`, []any{ws}},
		{`INSERT INTO cli_tokens(id,user_id,name,token_hash) VALUES('pending-token','pending-owner','test',?)`, []any{sha256HexToken(token)}},
		{`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('pending-recipe',?,'receipt-recipe','Receipt','{}','hash')`, []any{ws}},
	} {
		if _, err := db.ExecContext(t.Context(), seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	store := pipeline.NewPendingRunStore(db)
	for _, id := range []string{"dispatch-failed", "legacy-running"} {
		if _, _, err := store.Enqueue(t.Context(), pipeline.PendingRun{ID: id, WorkspaceID: ws, PipelineID: "pending-recipe", PipelineSlug: "receipt-recipe", FireAt: time.Now().Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	// Empty fired_run_id is also a legitimate in-flight legacy representation.
	if _, err := db.ExecContext(t.Context(), `UPDATE pending_runs SET status='fired' WHERE id='legacy-running'`); err != nil {
		t.Fatal(err)
	}
	dispatcher := pipeline.NewPendingRunDispatcher(store, pendingAcceptanceRejector{}, nil)
	dispatcher.Start(t.Context())
	t.Cleanup(dispatcher.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		pr, err := store.Get(t.Context(), ws, "dispatch-failed")
		if err != nil {
			t.Fatal(err)
		}
		if pr.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dispatcher did not record error: %+v", pr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	dispatcher.Stop()
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := runRoutineTriggerCLI(t, cfg, "routine", "pending", "get", "dispatch-failed", "-f", "json")
	if err != nil {
		t.Fatalf("get: %v\n%s", err, out)
	}
	var receipt pendingTriggerRow
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatalf("decode: %v %s", err, out)
	}
	if receipt.Status != "failed" || receipt.LastError == "" || receipt.DispatchAttempts != 1 || receipt.CanCancel {
		t.Fatalf("lost error receipt: %+v", receipt)
	}
	if receipt.LastError == "private executor detail must not reach the receipt" {
		t.Fatal("raw error exposed")
	}
	out, err = runRoutineTriggerCLI(t, cfg, "routine", "pending", "get", "legacy-running", "-f", "json")
	if err != nil {
		t.Fatalf("get in flight: %v %s", err, out)
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "fired" || receipt.RunID != "" || receipt.LastError != "" {
		t.Fatalf("in-flight receipt falsely failed: %+v", receipt)
	}
	out, err = runRoutineTriggerCLI(t, cfg, "routine", "pending", "list", "--status", "failed", "-f", "json")
	if err != nil {
		t.Fatalf("list: %v %s", err, out)
	}
	var rows []pendingTriggerRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].ID != "dispatch-failed" {
		t.Fatalf("failure list: %v %s", err, out)
	}
	if out, err = runRoutineTriggerCLI(t, cfg, "routine", "pending", "get", "missing", "-f", "json"); err == nil {
		t.Fatalf("missing receipt accepted: %s", out)
	}
}
