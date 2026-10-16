package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func artifactResultsCall(t *testing.T, h *PageHandler, ws, user, role, chat string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, "GET", "/api/v1/chats/"+chat+"/artifact-results", ws, user, role, "")
	r.SetPathValue("chatId", chat)
	rr := httptest.NewRecorder()
	h.ListChatArtifactResults(rr, r)
	return rr
}
func artifactResults(t *testing.T, rr *httptest.ResponseRecorder) []artifactChatResult {
	t.Helper()
	artifactExpect(t, rr, 200)
	var result struct {
		Results []artifactChatResult `json:"results"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Results == nil {
		t.Fatal("results must be an array")
	}
	return result.Results
}
func TestChatArtifactResultsVerifiedNewestExactRevision(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	execOrFatal(t, h.db, `UPDATE journal_entries SET payload='{"chat_id":"artifact-chat","metadata":{"human_user_id":"forged-human"}}' WHERE id='artifact-start'`)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<h1>Agent first</h1>","base_rev":0,"mutation_id":"first"}`), 201)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<h1>Agent second</h1>","base_rev":1,"mutation_id":"second"}`), 201)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Later human edit</h1>", "human-third", 2), 201)
	var chat string
	if err := h.db.QueryRow(`SELECT source_chat_id FROM page_artifact_revisions WHERE page_id=? AND revision=2`, page).Scan(&chat); err != nil || chat != "artifact-chat" {
		t.Fatalf("verified chat not persisted: %q %v", chat, err)
	}

	rr := artifactResultsCall(t, h, ws, user, "OWNER", "artifact-chat")
	results := artifactResults(t, rr)
	if len(results) != 1 || results[0].Revision != 2 || results[0].PageSlug != "report" || results[0].Title != "Report" || results[0].AgentID != "artifact-agent" || results[0].RunID != "artifact-run" || results[0].OnBehalfOfUserID != user {
		t.Fatalf("unverified card %+v", results)
	}
	if strings.Contains(rr.Body.String(), "<h1>") || strings.Contains(rr.Body.String(), "forged-human") {
		t.Fatal("card leaked source or trusted payload human")
	}
	// Runtime credentials may expire/clean up without erasing historical cards.
	execOrFatal(t, h.db, `DELETE FROM artifact_run_principals WHERE run_id='artifact-run'`)
	if len(artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "artifact-chat"))) != 1 {
		t.Fatal("card depends on live authority")
	}
	execOrFatal(t, h.db, `DELETE FROM page_artifact_revisions WHERE page_id=? AND revision=2`, page)
	results = artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "artifact-chat"))
	if len(results) != 1 || results[0].Revision != 1 {
		t.Fatalf("card points to pruned revision %+v", results)
	}
}
func TestChatArtifactResultsRequireBothCurrentAudiences(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	execOrFatal(t, h.db, `UPDATE journal_entries SET payload='{"chat_id":"artifact-chat"}' WHERE id='artifact-start'`)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<h1>Secret</h1>","base_rev":0,"mutation_id":"first"}`), 201)
	pagesSeedUser(t, h, ws, "card-reader", "card-reader@example.com", "MEMBER")
	execOrFatal(t, h.db, `UPDATE chats SET visibility='group' WHERE id='artifact-chat'`)
	execOrFatal(t, h.db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('artifact-chat','card-reader','member')`)
	rr := artifactResultsCall(t, h, ws, "card-reader", "OWNER", "artifact-chat")
	if len(artifactResults(t, rr)) != 0 || strings.Contains(rr.Body.String(), "Report") || strings.Contains(rr.Body.String(), "report") {
		t.Fatal("chat visibility or stale role leaked private Page")
	}
	pagesGrant(t, h, ws, user, "report", `{"subject_type":"user","subject":"card-reader@example.com","level":"comment"}`)
	if len(artifactResults(t, artifactResultsCall(t, h, ws, "card-reader", "MEMBER", "artifact-chat"))) != 1 {
		t.Fatal("comment/read Page access not honored")
	}
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE subject_id='card-reader'`)
	if len(artifactResults(t, artifactResultsCall(t, h, ws, "card-reader", "MEMBER", "artifact-chat"))) != 0 {
		t.Fatal("Page revocation not enforced")
	}
	execOrFatal(t, h.db, `DELETE FROM chat_participants WHERE user_id='card-reader'`)
	artifactExpect(t, artifactResultsCall(t, h, ws, "card-reader", "OWNER", "artifact-chat"), 404)
	artifactExpect(t, artifactResultsCall(t, h, "foreign-workspace", user, "OWNER", "artifact-chat"), 404)
}
func TestChatArtifactResultsNoTextOrMetadataAuthorityAndRequestFallback(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	execOrFatal(t, h.db, `UPDATE journal_entries SET payload='{"metadata":{"chat_id":"artifact-chat"}}' WHERE id='artifact-start'`)
	execOrFatal(t, h.db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,ts) VALUES('claimed-card','artifact-chat','artifact-agent','assistant','Artifact report published revision 999','2026-10-13T00:00:00Z')`)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<h1>Actual</h1>","base_rev":0,"mutation_id":"first"}`), 201)
	// Simulate a retained publication from before source_chat_id existed.
	execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at,author_agent_id,author_run_id) SELECT id||'-legacy',page_id,2,sha256,author_user_id,'legacy',request_digest,created_at,author_agent_id,author_run_id FROM page_artifact_revisions WHERE page_id=? AND revision=1`, page)
	execOrFatal(t, h.db, `DELETE FROM page_artifact_revisions WHERE page_id=? AND revision=1`, page)
	execOrFatal(t, h.db, `UPDATE page_artifacts SET head_rev=2 WHERE page_id=?`, page)

	if len(artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "artifact-chat"))) != 0 {
		t.Fatal("message or metadata created a card")
	}
	execOrFatal(t, h.db, `INSERT INTO page_artifact_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,snapshot_json,receipt_json,status,run_id,result_revision,created_at,updated_at) VALUES('card-request',?,?,'artifact-chat','artifact-agent',0,'card','fingerprint','test','{}','{}','succeeded','artifact-run',2,'2026-10-13T00:00:00Z','2026-10-13T00:00:00Z')`, page, user)
	results := artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "artifact-chat"))
	if len(results) != 1 || results[0].Revision != 2 {
		t.Fatal("verified request result missing")
	}
	execOrFatal(t, h.db, `UPDATE page_artifact_requests SET result_revision=999 WHERE id='card-request'`)
	if len(artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "artifact-chat"))) != 0 {
		t.Fatal("unretained request result became a card")
	}
	execOrFatal(t, h.db, `UPDATE journal_entries SET payload='not-json' WHERE id='artifact-start'`)
	artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "artifact-chat"))
	r := pagesGrantRequest(t, "GET", "/", ws, user, "OWNER", "")
	r.SetPathValue("chatId", "artifact-chat")
	r = r.WithContext(context.WithValue(r.Context(), ctxInternalTokenWS, ws))
	rr := httptest.NewRecorder()
	h.ListChatArtifactResults(rr, r)
	artifactExpect(t, rr, 401)
}

func TestChatArtifactResultsIgnoreForgedJournalLinkAndImmutableChat(t *testing.T) {
	h, ws, user, page, token := artifactRunFixture(t)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<p>verified</p>","base_rev":0,"mutation_id":"verified"}`), 201)
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('forged-chat',?,'artifact-agent',?,'private')`, ws, user)
	execOrFatal(t, h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('forged-start',?,'artifact-agent','run.started','system','forged replay','artifact-run','{"chat_id":"forged-chat"}')`, ws)
	if len(artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "forged-chat"))) != 0 {
		t.Fatal("journal replay moved a publication into a different chat")
	}
	if len(artifactResults(t, artifactResultsCall(t, h, ws, user, "OWNER", "artifact-chat"))) != 1 {
		t.Fatal("verified linkage lost")
	}
	if _, err := h.db.Exec(`UPDATE page_artifact_revisions SET source_chat_id='forged-chat' WHERE page_id=?`, page); err == nil {
		t.Fatal("historical source chat reassigned")
	}
	if _, err := h.db.Exec(`UPDATE page_artifact_revisions SET source_chat_id=NULL WHERE page_id=?`, page); err == nil {
		t.Fatal("live historical source chat cleared")
	}
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"<p>forged</p>","base_rev":1,"mutation_id":"forged-body","source_chat_id":"forged-chat"}`), 400)
	execOrFatal(t, h.db, `DELETE FROM chats WHERE id='artifact-chat'`)
	var source sql.NullString
	if err := h.db.QueryRow(`SELECT source_chat_id FROM page_artifact_revisions WHERE page_id=?`, page).Scan(&source); err != nil || source.Valid {
		t.Fatalf("chat delete did not clear historical FK: %+v %v", source, err)
	}
}
