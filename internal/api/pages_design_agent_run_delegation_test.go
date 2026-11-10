package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/journal"
)

// R71 §0.14/1 and §0.3/4 at use time for a Design agent run (#5235): the human
// behind a run lends it their rights only while they OWN the Design. Editing
// it through a workspace ADMIN role or a human Edit grant is not a right to
// delegate Edit; then the agent writes only with an Edit grant the owner
// issued to it. Reads keep the human's (audited) reach.
func TestDesignAgentRunWritesOnlyWithOwnerDelegation(t *testing.T) {
	const op = `{"operations":[{"op":"upsert","node":{"id":"f","type":"frame","name":"Frame","x":0,"y":0,"width":100,"height":100,"fill":"#fff"}}]}`
	cases := []struct {
		name      string
		role      string // the run's human, in the workspace
		transfer  bool   // the Design moves to another owner after admission
		humanEdit bool   // the new owner gives the human a write grant
		agentEdit bool   // the new owner gives the AGENT a write grant
		wantWrite int
	}{
		{"owner's own run writes", "MEMBER", false, false, false, http.StatusOK},
		{"workspace admin who no longer owns it may not write through the run", "ADMIN", true, false, false, http.StatusForbidden},
		{"human Edit grantee may not write through the run", "MEMBER", true, true, false, http.StatusForbidden},
		{"admin's run writes when the owner delegated Edit to the agent", "ADMIN", true, false, true, http.StatusOK},
		{"grantee's run writes when the owner delegated Edit to the agent", "MEMBER", true, true, true, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "true")
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "false")
			f := artifactProjectOriginalRunFixture(t)
			mustExec(t, f.rig.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, tc.role, f.a.WorkspaceID, f.a.HumanUserID)
			f.receipt = artifactRunFreshReceipt(t, f)
			const master = "synthetic-design-delegation-master"
			f.pages.artifactMasterToken = master
			humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "design-delegation-human")
			r := pagesRequest(t, "POST", "/api/v1/pages/delegated-design/design", f.a.WorkspaceID, f.a.HumanUserID, tc.role, `{"slug":"delegated-design","name":"Delegated"}`)
			r.Header.Set("Authorization", "Bearer "+humanToken)
			r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
			r.SetPathValue("slug", "delegated-design")
			created := httptest.NewRecorder()
			f.pages.CreateDesign(created, r)
			if created.Code != 201 {
				t.Fatalf("create %d %s", created.Code, created.Body)
			}
			var page string
			if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='delegated-design'`).Scan(&page); err != nil {
				t.Fatal(err)
			}
			intent := artifacts.ProjectIntent{Kind: "design", SubmissionID: "delegated-design-original", PageID: page, BaseVersion: 1}
			if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
				t.Fatal(err)
			}
			writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-design-delegation-chain"), FlushSize: 1})
			t.Cleanup(func() { _ = writer.Close() })
			host := NewInternalHandler(f.rig.db, master, newTestLogger())
			host.SetJournal(writer)
			f.pages.SetJournal(writer)
			raw, _ := json.Marshal(map[string]any{"id": f.a.RunID, "workspace_id": f.a.WorkspaceID, "agent_id": f.a.AgentID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": f.a.MessageID, "artifact_project_intent": intent})
			admitted := httptest.NewRecorder()
			host.CreateRun(admitted, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)))
			if admitted.Code != 201 {
				t.Fatalf("run admission %d %s", admitted.Code, admitted.Body)
			}
			if tc.transfer {
				newOwner := f.rig.adminID
				mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id=?`, newOwner, page)
				if tc.humanEdit {
					mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, page, f.a.HumanUserID, newOwner)
				}
				if tc.agentEdit {
					mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'write',?)`, page, f.a.AgentID, newOwner)
				}
			}
			token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
			tool := func(operation, payload string) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(map[string]any{"operation": operation, "slug": page, "payload": json.RawMessage(payload)})
				req := httptest.NewRequest("POST", "/api/v1/internal/pages/design-tool", bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+token)
				ctx := context.WithValue(req.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
				ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
				w := httptest.NewRecorder()
				f.pages.DesignTool(w, req.WithContext(ctx))
				return w
			}
			if w := tool("design_get", `{}`); w.Code != http.StatusOK {
				t.Fatalf("the run lost its human's read: %d %s", w.Code, w.Body)
			}
			before := designVersionCount(t, f, page)
			w := tool("design_apply", op)
			if w.Code != tc.wantWrite {
				t.Fatalf("design_apply = %d, want %d; %s", w.Code, tc.wantWrite, w.Body)
			}
			if tc.wantWrite != http.StatusOK {
				if after := designVersionCount(t, f, page); after != before {
					t.Fatalf("a refused agent write still versioned the Design: %d -> %d", before, after)
				}
				for _, operation := range []string{"design_snapshot", "design_restore"} {
					payload := `{}`
					if operation == "design_restore" {
						payload = `{"version":1}`
					}
					if denied := tool(operation, payload); denied.Code == http.StatusOK {
						t.Errorf("%s through an undelegated run = 200; %s", operation, denied.Body)
					}
				}
			}
		})
	}
}

func designVersionCount(t *testing.T, f *artifactPrincipalFixture, page string) int {
	t.Helper()
	var n int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id=?`, page).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
