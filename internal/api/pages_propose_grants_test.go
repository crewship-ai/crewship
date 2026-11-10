package api

import (
	"context"
	"net/http"
	"testing"
)

// R71 §0.14/7: Propose is its own agent level. It is issued to agents only,
// never carries a panel scope, and never stands in for Comment.
func TestPageProposeGrantNamesAgentsOnly(t *testing.T) {
	h, _, wsID, owner, _ := pagesGrantFixture(t, "")
	pagesSeedAgent(t, h, wsID, "agt-proposer", "proposer", "crew-engine")
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"subject_type":"agent","subject":"proposer","level":"propose"}`, http.StatusOK},
		{`{"subject_type":"agent","subject":"proposer","level":"PROPOSE"}`, http.StatusOK},
		{`{"subject_type":"user","subject":"` + owner + `","level":"propose"}`, http.StatusBadRequest},
		{`{"subject_type":"crew","subject":"engine","level":"propose"}`, http.StatusBadRequest},
		{`{"subject_type":"workspace","level":"propose"}`, http.StatusBadRequest},
		{`{"subject_type":"agent","subject":"proposer","level":"propose","panels":["sluzby"]}`, http.StatusBadRequest},
		{`{"subject_type":"agent","subject":"proposer","level":"comment"}`, http.StatusBadRequest},
	} {
		got := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, owner, "OWNER", "fleet-201", tc.body)
		if got.Code != tc.status {
			t.Fatalf("grant %s: %d %s", tc.body, got.Code, got.Body)
		}
	}
	listed := pagesGrantList(t, h, wsID, owner, "OWNER", "fleet-201")
	if len(listed) != 1 || listed[0]["level"] != "propose" || listed[0]["subject_type"] != "agent" {
		t.Fatalf("listed grants: %v", listed)
	}
}

// The ladder for an agent: read < propose < write. Propose reaches the Page and
// admits proposals; it never edits, produces or opens a writable session. A
// write grant keeps its exact meaning and still admits proposals.
func TestAgentProposeGrantLadder(t *testing.T) {
	h, _, wsID, owner, _ := pagesGrantFixture(t, "")
	for _, a := range []struct{ id, slug, level string }{
		{"agt-reader", "reader", "read"},
		{"agt-proposer", "proposer", "propose"},
		{"agt-writer", "writer", "write"},
		{"agt-none", "none", ""},
	} {
		pagesSeedAgent(t, h, wsID, a.id, a.slug, "crew-engine")
		if a.level != "" {
			pagesGrant(t, h, wsID, owner, "fleet-201", `{"subject_type":"agent","subject":"`+a.slug+`","level":"`+a.level+`"}`)
		}
	}
	ctx := context.Background()
	rec, err := h.loadPage(ctx, wsID, "fleet-201")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		agent                                    string
		propose, edit, produce, collabR, collabW bool
	}{
		{"agt-none", false, false, false, false, false},
		{"agt-reader", false, false, false, true, false},
		{"agt-proposer", true, false, false, true, false},
		{"agt-writer", true, true, false, true, true},
	} {
		if got := h.agentMayProposeOn(ctx, wsID, rec, tc.agent); got != tc.propose {
			t.Errorf("%s propose=%v want %v", tc.agent, got, tc.propose)
		}
		if got := h.agentMayEditSpec(ctx, wsID, rec, tc.agent); got != tc.edit {
			t.Errorf("%s edit=%v want %v", tc.agent, got, tc.edit)
		}
		if got := h.agentMayProduce(ctx, wsID, rec, tc.agent, "sluzby"); got != tc.produce {
			t.Errorf("%s produce=%v want %v", tc.agent, got, tc.produce)
		}
		if got := collaborationAgentGrantAllows(ctx, h, wsID, rec, tc.agent, false); got != tc.collabR {
			t.Errorf("%s collaboration read=%v want %v", tc.agent, got, tc.collabR)
		}
		if got := collaborationAgentGrantAllows(ctx, h, wsID, rec, tc.agent, true); got != tc.collabW {
			t.Errorf("%s collaboration write=%v want %v", tc.agent, got, tc.collabW)
		}
	}
}

// A comment-level grant can never reach an agent, so it can never become a
// proposal mandate: the handler and the schema both refuse it.
func TestCommentGrantNeverMakesAnAgentAProposer(t *testing.T) {
	h, _, wsID, owner, pageID := pagesGrantFixture(t, "")
	pagesSeedAgent(t, h, wsID, "agt-commenter", "commenter", "crew-engine")
	if got := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, owner, "OWNER", "fleet-201", `{"subject_type":"agent","subject":"commenter","level":"comment"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("agent comment grant: %d %s", got.Code, got.Body)
	}
	if _, err := h.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent','agt-commenter','comment',?)`, pageID, owner); err == nil {
		t.Fatal("schema accepted an agent comment grant")
	}
	rec, err := h.loadPage(context.Background(), wsID, "fleet-201")
	if err != nil {
		t.Fatal(err)
	}
	if h.agentMayProposeOn(context.Background(), wsID, rec, "agt-commenter") {
		t.Fatal("an agent without a propose or write grant may propose")
	}
}

// The access view names Propose for an agent holding propose or write, and only
// Read for an agent holding read.
func TestPageAccessShowsAgentPropose(t *testing.T) {
	for _, tc := range []struct {
		path string
		want pageAccessPermissions
	}{
		{pageReachGrantPage + pageGrantRead, pageAccessPermissions{Read: true}},
		{pageReachGrantPage + pageGrantPropose, pageAccessPermissions{Read: true, Propose: true}},
		{pageReachGrantPage + pageGrantWrite, pageAccessPermissions{Read: true, Edit: true, Propose: true}},
	} {
		subjects := []pageAccessSubjectWire{{SubjectType: pageSubjectAgent, SubjectID: "agt", Paths: []string{tc.path}}}
		decoratePageAccess(subjects, &pageRecord{}, &pageAccessWorkspace{}, nil)
		if subjects[0].EffectivePermissions != tc.want {
			t.Fatalf("%s: %+v want %+v", tc.path, subjects[0].EffectivePermissions, tc.want)
		}
	}
}
