package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/work"
)

// hostChatControlRig admits one ordinary human chat turn through the real IPC
// start, so the work item and attempt are the ones a CLI/WebSocket turn owns.
type hostChatControlRig struct {
	h      *InternalHandler
	mux    *http.ServeMux
	ws     string
	user   string
	run    string
	workID string
}

func newHostChatControlRig(t *testing.T) *hostChatControlRig {
	t.Helper()
	h, ws, agent := covIRunFixture(t)
	wireTestJournalForHandler(t, h.db, h)
	var user string
	if err := h.db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id=? LIMIT 1`, ws).Scan(&user); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('control-chat',?,?,?,'private')`, ws, agent, user)
	receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "control-chat", user, nil)
	if err != nil || receipt == nil {
		t.Fatalf("human admission: %+v %v", receipt, err)
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/internal/runs", h.requireInternal(http.HandlerFunc(h.CreateRun)))
	mux.Handle("PATCH /api/v1/internal/runs/{runId}", h.requireInternal(http.HandlerFunc(h.UpdateRun)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	resolver := chatbridge.NewIPCResolver(server.URL, "tok", newTestLogger())
	ctx := chatbridge.WithHostChatWorkAdmission(runauthority.WithTriggerMessage(chataudience.WithReceipt(t.Context(), receipt), "control-message"))
	const run = "msg_control_current"
	if err = resolver.CreateRun(ctx, run, agent, "control-chat", ws, "USER", nil); err != nil {
		t.Fatal(err)
	}
	binding, owned, err := work.NewStore(h.db).HostChatAttempt(t.Context(), run)
	if err != nil || !owned {
		t.Fatalf("host chat attempt not owned: %+v %v", binding, err)
	}
	rig := &hostChatControlRig{h: h, mux: mux, ws: ws, user: user, run: run, workID: binding.WorkID}
	for _, phase := range []string{"request", "started"} {
		if code, body := rig.phase(t, "RUNNING", phase, nil); code != 200 {
			t.Fatalf("%s phase status=%d %s", phase, code, body)
		}
	}
	return rig
}

func (r *hostChatControlRig) phase(t *testing.T, status, name string, exit *int) (int, string) {
	t.Helper()
	request := httptest.NewRequest("PATCH", "/api/v1/internal/runs/"+r.run, jsonBody(map[string]any{
		"status": status, "completed_at": time.Now().UTC().Format(time.RFC3339Nano),
		"host_chat_proof": map[string]any{"phase": name, "observed_exit": exit},
	}))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Internal-Token", "tok")
	response := httptest.NewRecorder()
	r.mux.ServeHTTP(response, request)
	return response.Code, response.Body.String()
}

func (r *hostChatControlRig) cancelRequested(t *testing.T) bool {
	t.Helper()
	code, body := r.phase(t, "RUNNING", "poll", nil)
	if code != 200 {
		t.Fatalf("host cancel poll status=%d %s", code, body)
	}
	var reply struct {
		CancelRequested bool `json:"cancel_requested"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("decode poll %s: %v", body, err)
	}
	return reply.CancelRequested
}

func (r *hostChatControlRig) state(t *testing.T) string {
	t.Helper()
	var state string
	if err := r.h.db.QueryRow(`SELECT state FROM work_items WHERE id=?`, r.workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// #4799 finding 1: a cancel of a running host chat through the work-item
// endpoint must reach the host that owns the execution. Before the fix the
// request was durable but no host-chat owner could ever observe it.
func TestHostChatWorkItemCancelReachesTheExecutionOwner(t *testing.T) {
	rig := newHostChatControlRig(t)
	if rig.cancelRequested(t) {
		t.Fatal("a running turn nobody cancelled reports a cancel request")
	}

	cancelHandler := NewWorkItemsHandler(rig.h.db, quietLogger())
	req := workReq(t, "POST", "/work-items/"+rig.workID+"/cancel", "", rig.user, rig.ws, "OWNER")
	req.SetPathValue("workItemId", rig.workID)
	rr := httptest.NewRecorder()
	cancelHandler.Cancel(rr, req)
	if rr.Code != 200 {
		t.Fatalf("cancel: %d %s", rr.Code, rr.Body.String())
	}
	var cancelled workCancelResponse
	decodeJSON(t, rr, &cancelled)
	if cancelled.Outcome != workCancelOutcomeRequested {
		t.Fatalf("cancel of a running turn must be a request, got %+v", cancelled)
	}

	if !rig.cancelRequested(t) {
		t.Fatal("the execution owner cannot observe the work-item cancel request")
	}
	if got := rig.state(t); got != "running" {
		t.Fatalf("a requested cancel claimed a stop nobody confirmed: %s", got)
	}

	stopped := 143
	if code, body := rig.phase(t, "CANCELLED", "finish", &stopped); code != 200 {
		t.Fatalf("confirmed stop status=%d %s", code, body)
	}
	if got := rig.state(t); got != "cancelled" {
		t.Fatalf("confirmed host stop settled as %s, want cancelled", got)
	}
	// A settled attempt is no longer an execution anybody may poll for.
	if code, _ := rig.phase(t, "RUNNING", "poll", nil); code != 409 {
		t.Fatalf("poll of an ended attempt status=%d, want 409", code)
	}
}

// #4799 finding 2: no dispatcher executes host_chat_turn, so a replay would
// queue a record that never runs and holds workspace ingress capacity.
func TestHostChatTurnReplayIsRefusedWithoutQueueing(t *testing.T) {
	rig := newHostChatControlRig(t)
	zero := 0
	if code, body := rig.phase(t, "COMPLETED", "finish", &zero); code != 200 {
		t.Fatalf("completion status=%d %s", code, body)
	}
	if got := rig.state(t); got != "succeeded" {
		t.Fatalf("completed turn state %s", got)
	}
	var before int
	if err := rig.h.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE workspace_id=?`, rig.ws).Scan(&before); err != nil {
		t.Fatal(err)
	}

	handler := NewWorkItemsHandler(rig.h.db, quietLogger())
	req := workReq(t, "POST", "/work-items/"+rig.workID+"/replay", `{"reason":"again"}`, rig.user, rig.ws, "OWNER")
	req.SetPathValue("workItemId", rig.workID)
	rr := httptest.NewRecorder()
	handler.Replay(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("host chat replay status=%d %s, want 409", rr.Code, rr.Body.String())
	}
	var after int
	if err := rig.h.db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE workspace_id=?`, rig.ws).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("a refused replay queued work: %d -> %d items", before, after)
	}
}
