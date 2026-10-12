package main

// The built CLI starts a routine whose concurrency slot is held by another
// run. The default queues the start (#3025) and the receipt is readable by its
// pending ID; --reject-if-busy keeps the 429 refusal.
import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAcceptance_RoutineRunQueuesWhenSlotIsFull(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	const ws = "cqueueifbusy000000001"
	const token = "crewship_cli_queueifbusy00000000000000000"
	const def = `{"name":"serial-job","concurrency_key":"serial","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`
	now := time.Now().UTC().Format(time.RFC3339)
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,full_name) VALUES('queue-owner','queue@example.test','Owner')`, nil},
		{`INSERT INTO workspaces(id,name,slug) VALUES(?,'Queue','queue-if-busy')`, []any{ws}},
		{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('queue-member',?,'queue-owner','OWNER')`, []any{ws}},
		{`INSERT INTO cli_tokens(id,user_id,name,token_hash) VALUES('queue-token','queue-owner','test',?)`, []any{sha256HexToken(token)}},
		{`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,head_version,created_at,updated_at,last_test_run_at) VALUES('queue-recipe',?,'serial-job','Serial job',?,'hq',1,?,?,?)`, []any{ws, def, now, now, now}},
		{`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,created_at) VALUES('plnv_queue_v1','queue-recipe',1,?,'hq','user','queue-owner',?)`, []any{def, now}},
	} {
		if _, err := db.ExecContext(t.Context(), seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := pipeline.NewRunRegistry()
	router.PipelinesHandler.SetRunRegistry(reg)
	router.PipelinesHandler.SetRunStore(pipeline.NewRunStore(db))
	router.PipelinesHandler.SetRunner(unusedAgentRunner{})
	// Another run holds the routine's only slot.
	_, release, err := reg.Acquire(context.Background(), pipeline.AcquireOpts{
		RunID: "holder", WorkspaceID: ws, PipelineID: "queue-recipe", PipelineSlug: "serial-job", ConcurrencyKey: "serial",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runRoutineTriggerCLI(t, cfg, "routine", "run", "serial-job", "-f", "json")
	if err != nil {
		t.Fatalf("queued start failed: %v\n%s", err, out)
	}
	var receipt struct {
		Status    string `json:"status"`
		PendingID string `json:"pending_id"`
		Queued    bool   `json:"queued"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if receipt.Status != "SCHEDULED" || !receipt.Queued || receipt.Reason != "concurrency_limit" || receipt.PendingID == "" {
		t.Fatalf("start was not queued for capacity: %s", out)
	}

	out, err = runRoutineTriggerCLI(t, cfg, "routine", "pending", "get", receipt.PendingID, "-f", "json")
	if err != nil {
		t.Fatalf("pending get: %v\n%s", err, out)
	}
	var pending pendingTriggerRow
	if err := json.Unmarshal([]byte(out), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" || !pending.CanCancel {
		t.Fatalf("queued start is not a cancellable pending receipt: %s", out)
	}

	out, err = runRoutineTriggerCLI(t, cfg, "routine", "run", "serial-job")
	if err != nil || !strings.Contains(out, "Queued:") || !strings.Contains(out, "routine pending get") {
		t.Fatalf("human output does not say the start was queued: %v\n%s", err, out)
	}

	out, err = runRoutineTriggerCLI(t, cfg, "routine", "run", "serial-job", "--reject-if-busy")
	if err == nil || !strings.Contains(out, "429") {
		t.Fatalf("--reject-if-busy did not refuse with 429: %v\n%s", err, out)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pending_runs WHERE workspace_id=?`, ws).Scan(&count); err != nil || count != 2 {
		t.Fatalf("pending rows = %d (%v), want 2 queued starts and none for the refused one", count, err)
	}
}
