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

func (b *blockingWaitpoints) parked() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.created > 0
}

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
// failed. This is RED on main: there the handler runs exec.Run on
// r.Context(), so cancelling the request cancels the parked WaitFor and the
// run fails at the gate (handler returns 500). The WithoutCancel detach
// makes it GREEN: the run ignores the request cancel and completes on
// approval (200).
func TestPipelineRun_WaitGate_DurableAcrossRequestCancel(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedWaitPipeline(t, db, "waitdemo")

	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "x"}, nil)
	h.SetRunStore(pipeline.NewRunStore(db))
	wp := newBlockingWaitpoints()
	h.SetWaitpointStore(wp)

	// Cancellable request context = the proxy connection that will time out.
	ctx, cancel := context.WithCancel(context.Background())
	base := httptest.NewRequest("POST", "/x", nil).WithContext(ctx)
	req := withWorkspaceCtx(base, "ws_smoke")
	req.SetPathValue("slug", "waitdemo")
	rr := httptest.NewRecorder()

	// The handler is synchronous and blocks at the gate, so drive it on a
	// goroutine while the test orchestrates the cancel + approval.
	handlerReturned := make(chan struct{})
	go func() {
		h.Run(rr, req)
		close(handlerReturned)
	}()

	// Wait until the run has parked on the approval waitpoint.
	parkDeadline := time.Now().Add(3 * time.Second)
	for !wp.parked() {
		if time.Now().After(parkDeadline) {
			t.Fatal("run never reached the approval gate")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The proxy drops the original request — the detached run must survive.
	cancel()
	// Give a buggy (request-context-coupled) implementation a beat to fail.
	time.Sleep(50 * time.Millisecond)
	// Approve the gate → the run resumes and the handler returns.
	wp.approve()

	select {
	case <-handlerReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after approval — run did not resume")
	}

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (run resumed); body=%s", rr.Code, rr.Body.String())
	}
	var res pipeline.RunResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode RunResult: %v; body=%s", err, rr.Body.String())
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("run status = %q, want COMPLETED (resumed across request cancel)", res.Status)
	}
}
