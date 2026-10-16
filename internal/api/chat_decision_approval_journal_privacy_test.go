package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestVerifiedRunApprovalJournalKeepsCurrentAudienceAndRetiredClassification(t *testing.T) {
	d := newApprovalDecisionRig(t)
	writer := d.r.journal.(*journal.Writer)
	id, err := harbormaster.Enqueue(t.Context(), d.f.h.db, writer, harbormaster.Request{WorkspaceID: d.f.workspace, CrewID: d.f.crew, AgentID: d.f.agent, RequestedBy: d.f.agent, Kind: harbormaster.KindCustom, Reason: "Private approval question", Payload: map[string]any{"tool": "agent_run", "args": map[string]any{}, "gate_mode": "sync", "correlation": map[string]any{"run_id": d.f.run, "chat_id": d.f.chat, "trigger_message_id": "trigger-" + d.f.run, "trigger_type": "human_chat"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var entryID string
	if err = d.f.h.db.QueryRow(`SELECT id FROM journal_entries WHERE entry_type='approval.requested' AND json_extract(refs,'$.approval_id')=?`, id).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES('approval-hidden-reader','approval-hidden-reader@example.test')`)
	execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('approval-hidden-member',?,'approval-hidden-reader','MEMBER','trusted')`, d.f.workspace)
	h := NewJournalHandler(d.f.h.db, newTestLogger(), nil)
	request := func(user, role string, handler http.HandlerFunc, path string) *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("GET", path, nil), user, d.f.workspace, role)
		req.SetPathValue("id", entryID)
		rr := httptest.NewRecorder()
		handler(rr, req)
		return rr
	}
	for _, tc := range []struct {
		name, path string
		handler    http.HandlerFunc
		direct     bool
	}{{"get", "/journal/" + entryID, h.Get, true}, {"list", "/journal?entry_type=approval.requested", h.List, false}, {"count", "/journal/count?entry_type=approval.requested", h.Count, false}} {
		hidden := request("approval-hidden-reader", "MEMBER", tc.handler, tc.path)
		if (tc.direct && hidden.Code != 404) || (!tc.direct && hidden.Code != 200) || strings.Contains(hidden.Body.String(), id) || strings.Contains(hidden.Body.String(), "Private approval question") {
			t.Fatalf("hidden approval%s%d %s", tc.name, hidden.Code, hidden.Body.String())
		}
		if tc.name == "count" {
			var count map[string]int
			if json.Unmarshal(hidden.Body.Bytes(), &count) != nil || count["total"] != 0 {
				t.Fatalf("hidden approval count%s", hidden.Body.String())
			}
		}
		allowed := request(d.f.user, "OWNER", tc.handler, tc.path)
		if allowed.Code != 200 {
			t.Fatalf("allowed approval%s%d %s", tc.name, allowed.Code, allowed.Body.String())
		}
		if !tc.direct && tc.name == "count" {
			var count map[string]int
			if json.Unmarshal(allowed.Body.Bytes(), &count) != nil || count["total"] != 1 {
				t.Fatalf("allowed count%s", allowed.Body.String())
			}
		}
	}
	// The actual canonical hard-delete keeps only a host content-free marker.
	if n, err := harbormaster.DeleteSources(t.Context(), d.f.h.db, writer, d.f.workspace, "aq.id=?", id); err != nil || n != 1 {
		t.Fatalf("retire source%d %v", n, err)
	}
	for _, reader := range []struct{ user, role string }{{d.f.user, "OWNER"}, {"approval-hidden-reader", "MEMBER"}} {
		rr := request(reader.user, reader.role, h.Get, "/journal/"+entryID)
		if rr.Code != 404 {
			t.Fatalf("retired approval resurrected%d %s", rr.Code, rr.Body.String())
		}
		rr = request(reader.user, reader.role, h.List, "/journal")
		if rr.Code != 200 || strings.Contains(rr.Body.String(), id) || strings.Contains(rr.Body.String(), "Private approval question") || strings.Contains(rr.Body.String(), "source_retired") {
			t.Fatalf("retired marker/history leaked%d %s", rr.Code, rr.Body.String())
		}
	}
	// The marker exists in the immutable journal even though every read omits it.
	var markers int
	if err = d.f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='approval.source_retired' AND json_extract(refs,'$.approval_id')=?`, id).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("retirement marker%d %v", markers, err)
	}
}

func TestVerifiedApprovalTraceDoesNotAddHiddenChainAgentEdge(t *testing.T) {
	d := newApprovalDecisionRig(t)
	writer := d.r.journal.(*journal.Writer)
	seedVersionedPipeline(t, d.f.h.db, d.f.workspace, "approval-parent-recipe", "approval-parent-recipe")
	execOrFatal(t, d.f.h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES('approval-parent-run',?,'approval-parent-recipe','approval-parent-recipe','running','2026-10-16T08:00:00Z')`, d.f.workspace)
	execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES('chain-hidden-approval','chain-hidden-approval@example.test')`)
	execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('chain-hidden-approval-member',?,'chain-hidden-approval','MEMBER','trusted')`, d.f.workspace)
	h := NewChainHandler(d.f.h.db, newTestLogger())
	read := func(user, role string) string {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/chains/approval-parent-run", nil), user, d.f.workspace, role)
		req.SetPathValue("anchor", "approval-parent-run")
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != 200 {
			t.Fatalf("chain%d %s", rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}
	baseline := read("chain-hidden-approval", "MEMBER")
	_, err := harbormaster.Enqueue(journal.WithRunID(t.Context(), "approval-parent-run"), d.f.h.db, writer, harbormaster.Request{WorkspaceID: d.f.workspace, CrewID: d.f.crew, AgentID: d.f.agent, RequestedBy: d.f.agent, Kind: harbormaster.KindCustom, Reason: "Private trace approval", Payload: map[string]any{"tool": "agent_run", "args": map[string]any{}, "gate_mode": "sync", "correlation": map[string]any{"run_id": d.f.run, "chat_id": d.f.chat, "trigger_message_id": "trigger-" + d.f.run, "trigger_type": "human_chat"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if hidden := read("chain-hidden-approval", "MEMBER"); hidden != baseline {
		t.Fatalf("private approval changed visible chain\nbefore%s\nafter%s", baseline, hidden)
	}
	if allowed := read(d.f.user, "OWNER"); !strings.Contains(allowed, "agent:"+d.f.agent) {
		t.Fatalf("current source reader lost agent edge%s", allowed)
	}
	_, err = harbormaster.Enqueue(journal.WithRunID(t.Context(), "approval-parent-run"), d.f.h.db, writer, harbormaster.Request{WorkspaceID: d.f.workspace, CrewID: d.f.crew, AgentID: d.f.agent, RequestedBy: d.f.agent, Kind: harbormaster.KindCustom, Reason: "Legacy trace approval", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if legacy := read("chain-hidden-approval", "MEMBER"); !strings.Contains(legacy, "agent:"+d.f.agent) {
		t.Fatalf("legacy chain edge hidden%s", legacy)
	}
}

func TestVerifiedApprovalSSEIgnoresHiddenResumeWatermark(t *testing.T) {
	d := newApprovalDecisionRig(t)
	writer := d.r.journal.(*journal.Writer)
	legacy, err := harbormaster.Enqueue(t.Context(), d.f.h.db, writer, harbormaster.Request{WorkspaceID: d.f.workspace, RequestedBy: d.f.agent, Kind: harbormaster.KindCustom, Reason: "Earlier legacy approval", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	private, err := harbormaster.Enqueue(t.Context(), d.f.h.db, writer, harbormaster.Request{WorkspaceID: d.f.workspace, CrewID: d.f.crew, AgentID: d.f.agent, RequestedBy: d.f.agent, Kind: harbormaster.KindCustom, Reason: "Hidden later approval", Payload: map[string]any{"tool": "agent_run", "args": map[string]any{}, "gate_mode": "sync", "correlation": map[string]any{"run_id": d.f.run, "chat_id": d.f.chat, "trigger_message_id": "trigger-" + d.f.run, "trigger_type": "human_chat"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var privateEntry string
	if err = d.f.h.db.QueryRow(`SELECT id FROM journal_entries WHERE entry_type='approval.requested' AND json_extract(refs,'$.approval_id')=?`, private).Scan(&privateEntry); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, d.f.h.db, `INSERT INTO users(id,email) VALUES('sse-hidden-approval','sse-hidden-approval@example.test')`)
	execOrFatal(t, d.f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('sse-hidden-approval-member',?,'sse-hidden-approval','MEMBER')`, d.f.workspace)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/journal/stream?entry_type=approval.requested", nil), "sse-hidden-approval", d.f.workspace, "MEMBER").WithContext(context.WithValue(ctx, ctxWorkspaceID, d.f.workspace))
	// Preserve the authenticated principal when attaching the cancellation.
	req = withWorkspaceUser(req, "sse-hidden-approval", d.f.workspace, "MEMBER")
	req.Header.Set("Last-Event-ID", privateEntry)
	rr := httptest.NewRecorder()
	NewJournalHandler(d.f.h.db, newTestLogger(), nil).Stream(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), legacy) || strings.Contains(rr.Body.String(), private) || strings.Contains(rr.Body.String(), "Hidden later approval") {
		t.Fatalf("hidden SSE resume affected visible history%d %s", rr.Code, rr.Body.String())
	}
}
