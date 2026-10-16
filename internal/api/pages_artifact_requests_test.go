package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

func requestFixture(t *testing.T) (*PageHandler, string, string, string) {
	h, workspace, user, page, _ := artifactRunFixture(t)
	artifactExpect(t, artifactSave(t, h, workspace, user, "<h1>base</h1>", "initial", 0), 201)
	h.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error { return nil }
	return h, workspace, user, page
}
func createArtifactRequestForTest(t *testing.T, h *PageHandler, workspace, user, body string, want int) string {
	t.Helper()
	r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", body)
	r.SetPathValue("slug", "report")
	w := httptest.NewRecorder()
	h.CreateArtifactRequest(w, r)
	artifactExpect(t, w, want)
	var v struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	return v.ID
}

const requestBody = `{"chat_id":"artifact-chat","instruction":"Improve the report","base_rev":1,"submission_id":"first-request"}`

func TestArtifactRequestIdempotencySnapshotAndCancel(t *testing.T) {
	h, workspace, user, page := requestFixture(t)
	id := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	if again := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202); again != id {
		t.Fatal("retry created new request")
	}
	createArtifactRequestForTest(t, h, workspace, user, strings.Replace(requestBody, "Improve the report", "Different instruction", 1), 409)
	r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	r.SetPathValue("requestId", id)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.CancelArtifactRequest(w, r)
		artifactExpect(t, w, 200)
	}
	var state, cancelActor string
	if h.db.QueryRow(`SELECT status,cancelled_by_user_id FROM page_artifact_requests WHERE id=? AND page_id=?`, id, page).Scan(&state, &cancelActor) != nil || state != "cancelled" || cancelActor != user {
		t.Fatal("cancel not durable")
	}
}
func TestArtifactRequestDispatchPublishesVerifiedResult(t *testing.T) {
	h, workspace, user, page := requestFixture(t)
	id := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	h.artifactDispatch = func(ctx context.Context, actualUser, chat, prompt string, opts ws.ChatMessageOption) error {
		if actualUser != user || chat != "artifact-chat" || opts.ArtifactRequestID != id || !strings.Contains(prompt, `request_id:"`+id+`"`) {
			t.Fatal("dispatch lost trusted context")
		}
		execOrFatal(t, h.db, `INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) SELECT 'request-run',workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at FROM artifact_run_principals WHERE run_id='artifact-run'`)
		r := httptest.NewRequest("POST", "/", nil)
		if err := bindArtifactRequestRun(r, h.db, id, "request-run", opts.HumanAuthority); err != nil {
			t.Fatal(err)
		}
		execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('request-start',?,'artifact-crew','artifact-agent','run.started','system','test','request-run')`, workspace)
		token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "request-run")
		body := fmt.Sprintf(`{"html":"<h1>result</h1>","base_rev":1,"mutation_id":"request-publish","request_id":%q}`, id)
		artifactExpect(t, artifactAgentSave(t, h, workspace, token, body), 201)
		artifactExpect(t, artifactAgentSave(t, h, workspace, token, body), 200)
		artifactExpect(t, artifactAgentSave(t, h, workspace, token, `{"html":"escape","base_rev":2,"mutation_id":"escape"}`), 403)
		return nil
	}
	h.dispatchNextArtifactRequest(context.Background())
	var status string
	var result int64
	if err := h.db.QueryRow(`SELECT status,result_revision FROM page_artifact_requests WHERE id=? AND page_id=?`, id, page).Scan(&status, &result); err != nil || status != "succeeded" || result != 2 {
		t.Fatalf("result %s %d %v", status, result, err)
	}
}
func TestArtifactRequestRevocationBeforeAdmission(t *testing.T) {
	h, workspace, user, _ := requestFixture(t)
	id := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	execOrFatal(t, h.db, `UPDATE page_artifact_requests SET status='running' WHERE id=?`, id)
	receipt, _ := chataudience.CaptureTrusted(context.Background(), h.db, "artifact-chat", user, nil)
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE subject_type='agent'`)
	if bindArtifactRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "artifact-run", receipt) == nil {
		t.Fatal("revoked agent grant admitted dispatch")
	}
}
func TestArtifactRequestRejectsSharedChatsAndReaders(t *testing.T) {
	h, workspace, user, _ := requestFixture(t)
	execOrFatal(t, h.db, `UPDATE chats SET visibility='group' WHERE id='artifact-chat'`)
	createArtifactRequestForTest(t, h, workspace, user, requestBody, 403)
	execOrFatal(t, h.db, `UPDATE chats SET visibility='private' WHERE id='artifact-chat'`)
	createArtifactRequestForTest(t, h, workspace, user, strings.Replace(requestBody, `"base_rev":1`, `"base_rev":2`, 1), 409)
}
func TestArtifactRequestBusyRetryAndUnknownOutcome(t *testing.T) {
	h, workspace, user, _ := requestFixture(t)
	id := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	h.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error { return ws.ErrAgentBusy }
	h.dispatchNextArtifactRequest(context.Background())
	var state string
	_ = h.db.QueryRow(`SELECT status FROM page_artifact_requests WHERE id=?`, id).Scan(&state)
	if state != "queued" {
		t.Fatal(state)
	}
	execOrFatal(t, h.db, `UPDATE page_artifact_requests SET next_attempt_at='' WHERE id=?`, id)
	h.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error {
		execOrFatal(t, h.db, `UPDATE page_artifact_requests SET run_id='possibly-started' WHERE id=?`, id)
		return ws.ErrAgentBusy
	}
	h.dispatchNextArtifactRequest(context.Background())
	_ = h.db.QueryRow(`SELECT status FROM page_artifact_requests WHERE id=?`, id).Scan(&state)
	if state != "running" {
		t.Fatalf("bound unknown admission must remain unconfirmed, got %s", state)
	}
}

func TestArtifactRequestWorkersDrainOnShutdown(t *testing.T) {
	h, workspace, user, _ := requestFixture(t)
	createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	started := make(chan struct{})
	stopped := make(chan struct{})
	h.artifactDispatch = func(ctx context.Context, _ string, _ string, _ string, _ ws.ChatMessageOption) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { h.RunArtifactRequests(ctx); close(finished) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never dispatched")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not drain")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("runtime still running after worker drain")
	}
}

type artifactNeverLeader struct{}

func (artifactNeverLeader) IsLeader() bool { return false }
func TestArtifactRequestWorkerHonorsLeaderGate(t *testing.T) {
	h, workspace, user, _ := requestFixture(t)
	id := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	h.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error {
		t.Error("nonleader dispatched")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { h.RunArtifactRequests(ctx, artifactNeverLeader{}); close(finished) }()
	time.Sleep(1200 * time.Millisecond)
	cancel()
	<-finished
	var state string
	_ = h.db.QueryRow(`SELECT status FROM page_artifact_requests WHERE id=?`, id).Scan(&state)
	if state != "queued" {
		t.Fatal(state)
	}
}
func TestArtifactRequestRejectsActiveChatReadShare(t *testing.T) {
	h, workspace, user, _ := requestFixture(t)
	execOrFatal(t, h.db, `INSERT INTO chat_read_shares(id,workspace_id,agent_id,chat_id,issued_by_user_id,token_hash,created_at,expires_at) VALUES('request-share',?,'artifact-agent','artifact-chat',?,x'01','2026-01-01T00:00:00Z','2126-01-01T00:00:00Z')`, workspace, user)
	createArtifactRequestForTest(t, h, workspace, user, requestBody, 403)
}

func TestArtifactRequestBusyBackoffAllowsAnotherChat(t *testing.T) {
	h, workspace, user, page := requestFixture(t)
	first := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
	seedAgentRow(t, h.db, "artifact-agent-two", workspace, "artifact-crew", "Agent Two", "artifact-agent-two", "AGENT")
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('second-chat',?,'artifact-agent-two',?,'private')`, workspace, user)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'agent','artifact-agent-two','write',?,'2026-10-13T00:00:00Z')`, page, user)
	second := createArtifactRequestForTest(t, h, workspace, user, strings.Replace(strings.Replace(requestBody, "artifact-chat", "second-chat", 1), "first-request", "second-request", 1), 202)
	reachedSecond := false
	h.artifactDispatch = func(_ context.Context, _ string, chat string, _ string, opts ws.ChatMessageOption) error {
		if opts.ArtifactRequestID == first {
			return ws.ErrAgentBusy
		}
		if opts.ArtifactRequestID == second && chat == "second-chat" {
			reachedSecond = true
		}
		return nil
	}
	h.dispatchNextArtifactRequest(context.Background())
	h.dispatchNextArtifactRequest(context.Background())
	if !reachedSecond {
		t.Fatal("busy oldest request starved independent chat")
	}
}

func TestArtifactRequestRejectsInactiveHumanBeforeDispatchAndAdmission(t *testing.T) {
	for _, column := range []string{"suspended_at"} {
		t.Run(column, func(t *testing.T) {
			h, workspace, user, _ := requestFixture(t)
			id := createArtifactRequestForTest(t, h, workspace, user, requestBody, 202)
			receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", user, nil)
			if err != nil || receipt == nil {
				t.Fatalf("receipt: %+v %v", receipt, err)
			}
			execOrFatal(t, h.db, `UPDATE users SET `+column+`='2026-10-13T00:00:00Z' WHERE id=?`, user)
			execOrFatal(t, h.db, `UPDATE page_artifact_requests SET status='running' WHERE id=?`, id)
			if bindArtifactRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "artifact-run", receipt) == nil {
				t.Fatal("inactivehuman admitted")
			}
			execOrFatal(t, h.db, `UPDATE page_artifact_requests SET status='queued' WHERE id=?`, id)
			called := false
			h.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error { called = true; return nil }
			h.dispatchNextArtifactRequest(t.Context())
			if called {
				t.Fatal("inactivehuman dispatched queuedwork")
			}
			var state string
			if err = h.db.QueryRow(`SELECT status FROM page_artifact_requests WHERE id=?`, id).Scan(&state); err != nil || state != "failed" {
				t.Fatalf("queued inactivehuman request: %s %v", state, err)
			}
		})
	}
}
