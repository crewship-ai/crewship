package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func craftNativeProposalCall(d *craftNativeDocumentsHTTP, ctx context.Context, token, method, path, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/v1/pages/@native-agent-page/craft/proposals"+path, strings.NewReader(body)).WithContext(ctx)
	r.SetPathValue("slug", "@native-agent-page")
	r.SetPathValue("proposalID", id)
	r.Header.Set("X-Craft-Session", token)
	w := httptest.NewRecorder()
	switch {
	case path == "":
		d.listProposals(w, r)
	case strings.HasSuffix(path, "/accept"):
		d.acceptProposal(w, r)
	case strings.HasSuffix(path, "/reject"):
		d.rejectProposal(w, r)
	default:
		d.proposalDocument(w, r)
	}
	return w
}

func craftNativeProposalVersionsCall(d *craftNativeDocumentsHTTP, ctx context.Context, token, version string) *httptest.ResponseRecorder {
	path := "/api/v1/pages/@native-agent-page/craft/document/versions"
	if version != "" {
		path += "/" + version
	}
	r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	r.SetPathValue("slug", "@native-agent-page")
	r.Header.Set("X-Craft-Session", token)
	w := httptest.NewRecorder()
	if version == "" {
		d.listVersions(w, r)
	} else {
		r.SetPathValue("version", version)
		d.readVersion(w, r)
	}
	return w
}

// #4480: while a human holds the lease the agent prepares a separate proposal
// over the version it read; only the human's own fenced accept changes the
// document. A bare 409 is not a proposal.
func TestCraftNativeAgentProposalWhileHumanHoldsLeaseAcceptedByHuman(t *testing.T) {
	f, m, human, call, calls := nativeAgentToolFixture(t)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`)).WithContext(human)
	r.SetPathValue("slug", "@native-agent-page")
	w := httptest.NewRecorder()
	m.human.sessions(w, r)
	var session struct {
		Token string `json:"session_token"`
		ID    string `json:"session_id"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &session) != nil {
		t.Fatal("human admission", w.Code, w.Body)
	}
	h := craftNativeLeaseCall(m.human, human, session.Token, "@native-agent-page", "acquire", `{}`)
	var humanLease struct {
		Generation string `json:"generation"`
	}
	if h.Code != 200 || json.Unmarshal(h.Body.Bytes(), &humanLease) != nil {
		t.Fatal("human acquire", h.Code, h.Body)
	}
	if refused := call(`{"operation":"acquire"}`); refused.Code != 409 {
		t.Fatal("human lease not respected", refused.Code, refused.Body)
	}
	packet := `{"operation":"propose","base_version":"1","request_id":"p1","summary":"Insert greeting","commands":[{"command":"text.insert","params":{"text":"x"}}]}`
	proposed := call(packet)
	var created struct {
		ID    string `json:"proposal_id"`
		Base  string `json:"base_version"`
		State string `json:"state"`
	}
	if proposed.Code != 201 || json.Unmarshal(proposed.Body.Bytes(), &created) != nil || created.ID == "" || created.Base != "1" || created.State != "open" {
		t.Fatal("agent proposal", proposed.Code, proposed.Body)
	}
	if again := call(packet); again.Code != 201 || !strings.Contains(again.Body.String(), created.ID) || *calls != 1 {
		t.Fatal("exact retry reran engine or recorded another proposal", again.Code, again.Body, *calls)
	}
	var head int64
	if err := f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-agent-page'`).Scan(&head); err != nil || head != 1 {
		t.Fatal("proposal changed the document", head, err)
	}

	d := m.documents
	list := craftNativeProposalCall(d, human, session.Token, "GET", "", "", "")
	var listed struct {
		Proposals []craftNativeProposalResponse `json:"proposals"`
	}
	if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Proposals) != 1 || listed.Proposals[0].ID != created.ID || listed.Proposals[0].Author.Kind != "agent" || listed.Proposals[0].Summary != "Insert greeting" || listed.Proposals[0].BaseVersion != 1 {
		t.Fatal("human review list", list.Code, list.Body)
	}
	if doc := craftNativeProposalCall(d, human, session.Token, "GET", "/"+created.ID, created.ID, ""); doc.Code != 200 || !strings.Contains(doc.Body.String(), `"bytes"`) {
		t.Fatal("candidate for comparison", doc.Code, doc.Body)
	}
	if foreign := craftNativeProposalCall(d, human, "not-an-admission", "GET", "", "", ""); foreign.Code == 200 {
		t.Fatal("proposals listed without admission")
	}
	if wrong := craftNativeProposalCall(d, human, session.Token, "POST", "/"+created.ID+"/accept", created.ID, `{"sessionId":"other","generation":"`+humanLease.Generation+`"}`); wrong.Code != 400 {
		t.Fatal("accept bound to another session", wrong.Code, wrong.Body)
	}
	accepted := craftNativeProposalCall(d, human, session.Token, "POST", "/"+created.ID+"/accept", created.ID, fmt.Sprintf(`{"sessionId":%q,"generation":%q}`, session.ID, humanLease.Generation))
	if accepted.Code != 200 || !strings.Contains(accepted.Body.String(), `"state":"accepted"`) || !strings.Contains(accepted.Body.String(), `"version":"2"`) {
		t.Fatal("human accept", accepted.Code, accepted.Body)
	}
	var actor string
	if err := f.rig.db.QueryRow(`SELECT actor FROM artifact_versions WHERE page_id='native-agent-page' AND version=2`).Scan(&actor); err != nil || actor != "user:"+f.a.HumanUserID {
		t.Fatal("accepted version must be the human's own write", actor, err)
	}
	// #5599: the agent stays the proposal's author; the human is recorded as
	// the one who decided it, and the version history says where version 2
	// came from.
	var acceptBody craftNativeProposalAcceptResponse
	if err := json.Unmarshal(accepted.Body.Bytes(), &acceptBody); err != nil {
		t.Fatal(err)
	}
	if by := acceptBody.Proposal.DecidedBy; by == nil || by.ActorID != "user:"+f.a.HumanUserID || by.Kind != "human" || by.DisplayName == "" || acceptBody.Proposal.DecidedAt == "" || acceptBody.Proposal.Author.Kind != "agent" {
		t.Fatal("accepted proposal must name its human decider", accepted.Body)
	}
	craftNativeEnableVersionHistory(t, f.rig.db, true)
	history := craftNativeProposalVersionsCall(d, human, session.Token, "")
	var versions struct {
		Versions []craftNativeVersionResponse `json:"versions"`
	}
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &versions) != nil || len(versions.Versions) != 2 {
		t.Fatal("version history", history.Code, history.Body)
	}
	v2, v1 := versions.Versions[0], versions.Versions[1]
	if v2.Version != "2" || v2.Author.Kind != "human" || v2.Author.ActorID != "user:"+f.a.HumanUserID || v2.Proposal == nil || v2.Proposal.ID != created.ID || v2.Proposal.Author.Kind != "agent" || v2.Proposal.Author.ActorID != listed.Proposals[0].Author.ActorID {
		t.Fatal("accepted version must carry its proposal's agent author", history.Body)
	}
	if v1.Proposal != nil {
		t.Fatal("a plain save claims a proposal", history.Body)
	}
	one := craftNativeProposalVersionsCall(d, human, session.Token, "2")
	var single craftNativeVersionDocumentResponse
	if one.Code != 200 || json.Unmarshal(one.Body.Bytes(), &single) != nil || single.Proposal == nil || single.Proposal.ID != created.ID || single.Proposal.Author.Kind != "agent" {
		t.Fatal("single version read must carry its proposal", one.Code, one.Body)
	}
	if foreign := craftNativeProposalVersionsCall(d, human, "not-an-admission", ""); foreign.Code == 200 || strings.Contains(foreign.Body.String(), created.ID) {
		t.Fatal("history provenance served without admission", foreign.Code, foreign.Body)
	}
	if twice := craftNativeProposalCall(d, human, session.Token, "POST", "/"+created.ID+"/accept", created.ID, fmt.Sprintf(`{"sessionId":%q,"generation":%q}`, session.ID, humanLease.Generation)); twice.Code != 409 {
		t.Fatal("proposal accepted twice", twice.Code, twice.Body)
	}

	// A proposal over a version the human has since replaced goes stale on
	// accept and leaves the document untouched; reject needs no lease.
	stale := call(`{"operation":"propose","base_version":"2","request_id":"p2","commands":[{"command":"text.insert","params":{"text":"y"}}]}`)
	var second struct {
		ID string `json:"proposal_id"`
	}
	if stale.Code != 201 || json.Unmarshal(stale.Body.Bytes(), &second) != nil {
		t.Fatal("second proposal", stale.Code, stale.Body)
	}
	if old := call(`{"operation":"propose","base_version":"1","request_id":"p3","commands":[{"command":"text.insert","params":{"text":"z"}}]}`); old.Code != 409 {
		t.Fatal("proposal over an old version admitted", old.Code, old.Body)
	}
	mustExec(t, f.rig.db, `UPDATE artifact_version_heads SET latest_version=latest_version WHERE page_id='native-agent-page'`)
	rejected := craftNativeProposalCall(d, human, session.Token, "POST", "/"+second.ID+"/reject", second.ID, `{}`)
	if rejected.Code != 200 || !strings.Contains(rejected.Body.String(), `"state":"rejected"`) {
		t.Fatal("human reject", rejected.Code, rejected.Body)
	}
	var rejectedView craftNativeProposalResponse
	if err := json.Unmarshal(rejected.Body.Bytes(), &rejectedView); err != nil || rejectedView.DecidedBy == nil || rejectedView.DecidedBy.Kind != "human" || rejectedView.DecidedAt == "" {
		t.Fatal("rejected proposal must name its human decider", rejected.Body)
	}
	if err := f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-agent-page'`).Scan(&head); err != nil || head != 2 {
		t.Fatal("reject changed the document", head, err)
	}
}

func TestCraftNativeAgentToolProposeGrammar(t *testing.T) {
	if _, err := parseCraftNativeAgentTool([]byte(`{"operation":"propose","base_version":"9","request_id":"x:1","summary":"s","commands":[{"command":"text.insert","params":{"text":"x"}}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"operation":"propose","generation":"7","base_version":"9","request_id":"x","commands":[{}]}`,
		`{"operation":"propose","base_version":"9","commands":[{}]}`,
		`{"operation":"propose","base_version":"0","request_id":"x","commands":[{}]}`,
		`{"operation":"propose","base_version":"9","request_id":"x","commands":[]}`,
		`{"operation":"propose","base_version":"9","request_id":"x","commands":[{}],"summary":"` + strings.Repeat("s", 501) + `"}`,
		`{"operation":"apply","generation":"7","base_version":"9","request_id":"x","commands":[{}],"summary":"s"}`,
	} {
		if _, err := parseCraftNativeAgentTool([]byte(raw)); err == nil {
			t.Fatalf("invalid proposal accepted: %s", raw)
		}
	}
}
