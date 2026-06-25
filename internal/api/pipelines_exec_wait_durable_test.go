package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// blockingWaitpoints parks WaitFor on a channel until approve() fires, so
// a test can hold a run at its approval gate, cancel the originating HTTP
// request, and then prove the run still resumes.
type blockingWaitpoints struct {
	mu       sync.Mutex
	created  int
	approved chan bool
}

func newBlockingWaitpoints() *blockingWaitpoints {
	return &blockingWaitpoints{approved: make(chan bool, 1)}
}

func (b *blockingWaitpoints) CreateApproval(_ context.Context, _ pipeline.WaitpointApprovalRequest) (string, error) {
	b.mu.Lock()
	b.created++
	b.mu.Unlock()
	return "tok-block", nil
}

func (b *blockingWaitpoints) WaitFor(ctx context.Context, _ string) (bool, error) {
	select {
	case v := <-b.approved:
		return v, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (b *blockingWaitpoints) approve() { b.approved <- true }

func seedWaitPipeline(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	def := `{"name":"` + slug + `","steps":[{"id":"gate","type":"wait","wait":{"kind":"approval","approval_prompt":"ok?"},"timeout_seconds":3600}]}`
	_, err := db.ExecContext(context.Background(), `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, ephemeral, workspace_visible, author_crew_id, author_agent_id, authored_via, last_test_run_at, last_test_run_passed, created_at, updated_at)
VALUES (?, 'ws_smoke', ?, ?, ?, ?, 0, 1, 'crew_a', 'agent_lead', 'agent_tool_call', ?, 1, ?, ?)`,
		"pln_wait_"+slug, slug, slug, def, "hash_"+slug, now, now, now)
	if err != nil {
		t.Fatalf("seed wait pipeline: %v", err)
	}
}

// A run parked on an approval gate must survive the originating request
// being cancelled (the reverse proxy timing out the held-open request) and
// resume to completion once the waitpoint is approved — not get marked
// failed. Regression test for the manual-run-fails-at-wait bug.
func TestPipelineRun_WaitGate_DurableAcrossRequestCancel(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedWaitPipeline(t, db, "waitdemo")

	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "x"}, nil)
	h.SetRunStore(pipeline.NewRunStore(db))
	wp := newBlockingWaitpoints()
	h.SetWaitpointStore(wp)
	// Tiny budget so the handler hands back 202 without a real 50s wait.
	h.SetRunResponseBudget(50 * time.Millisecond)

	// Cancellable request context = the proxy connection that will time out.
	ctx, cancel := context.WithCancel(context.Background())
	base := httptest.NewRequest("POST", "/x", nil).WithContext(ctx)
	req := withWorkspaceCtx(base, "ws_smoke")
	req.SetPathValue("slug", "waitdemo")
	rr := httptest.NewRecorder()

	h.Run(rr, req)

	// Parked at the gate past the budget → 202 + run id (not 500/failed).
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode 202: %v", err)
	}
	if resp.RunID == "" {
		t.Fatalf("202 response carried no run_id: %s", rr.Body.String())
	}

	// The proxy drops the original request — the detached run must survive.
	cancel()
	// Approve the gate → the background run resumes.
	wp.approve()

	rs := pipeline.NewRunStore(db)
	deadline := time.Now().Add(3 * time.Second)
	var final pipeline.RunStatus
	for time.Now().Before(deadline) {
		rec, err := rs.Get(context.Background(), resp.RunID)
		if err == nil && rec != nil &&
			(rec.Status == pipeline.RunStatusCompleted || rec.Status == pipeline.RunStatusFailed) {
			final = rec.Status
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final != pipeline.RunStatusCompleted {
		t.Fatalf("run did not resume to completed after approval; final status = %q", final)
	}
}
