package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// capturingWaitpoints hands the test the exact context the executor blocks
// on at the approval gate, then parks WaitFor until release() fires. The
// captured context lets the test assert detachment deterministically (no
// polling/timeout races): cancel the request and prove the run's context
// is NOT cancelled with it.
type capturingWaitpoints struct {
	captured chan context.Context
	release  chan bool
}

func newCapturingWaitpoints() *capturingWaitpoints {
	return &capturingWaitpoints{
		captured: make(chan context.Context, 1),
		release:  make(chan bool, 1),
	}
}

func (c *capturingWaitpoints) CreateApproval(_ context.Context, _ pipeline.WaitpointApprovalRequest) (string, error) {
	return "tok-cap", nil
}

func (c *capturingWaitpoints) WaitFor(ctx context.Context, _ string) (bool, error) {
	select {
	case c.captured <- ctx:
	default:
	}
	select {
	case v := <-c.release:
		return v, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
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
// failed. This is RED on the pre-fix code: there the handler runs exec.Run
// on r.Context(), so cancelling the request cancels the parked WaitFor and
// the run fails at the gate. The WithoutCancel detach makes it GREEN: the
// run context is independent of the request, so it survives the cancel and
// completes on approval.
func TestPipelineRun_WaitGate_DurableAcrossRequestCancel(t *testing.T) {
	db := openSmokeDB(t)
	defer db.Close()
	seedWaitPipeline(t, db, "waitdemo")

	h := NewPipelineHandler(db, slog.Default(), &stubRunner{output: "x"}, nil)
	h.SetRunStore(pipeline.NewRunStore(db))
	wp := newCapturingWaitpoints()
	h.SetWaitpointStore(wp)
	// Always unblock a parked WaitFor so the run goroutine can't leak past
	// the test (a leaked goroutine would touch the DB after db.Close()).
	t.Cleanup(func() {
		select {
		case wp.release <- true:
		default:
		}
	})

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
		defer close(handlerReturned)
		h.Run(rr, req)
	}()

	// Block until the run has actually reached the gate. The 60s ceiling is
	// generous on purpose: under a loaded CI runner the run goroutine can be
	// CPU-starved for many seconds before it threads down to WaitFor. If the
	// handler returns *before* the gate, that's an early executor error —
	// surface its response instead of timing out on a mystery.
	var runCtx context.Context
	select {
	case runCtx = <-wp.captured:
	case <-handlerReturned:
		t.Fatalf("handler returned before reaching the approval gate (early error): code=%d body=%s", rr.Code, rr.Body.String())
	case <-time.After(60 * time.Second):
		t.Fatal("run never reached the approval gate within 60s")
	}

	// The proxy drops the original request.
	cancel()

	// The run's context must NOT be cancelled with the request — that's the
	// whole fix. On the pre-fix r.Context() path this Done channel fires.
	select {
	case <-runCtx.Done():
		t.Fatal("run context was cancelled with the request — not detached")
	case <-time.After(100 * time.Millisecond):
	}

	// Approve the gate → the run resumes and the handler returns.
	wp.release <- true
	select {
	case <-handlerReturned:
	case <-time.After(30 * time.Second):
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
