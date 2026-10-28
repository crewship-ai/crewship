package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Production trusted-message recording, run admission and signed tool transport;
// no live provider/model execution is claimed by this controlled server fixture.
func TestArtifactProjectAgentTestOriginUsesRecordedHumanCreation(t *testing.T) {
	for _, tc := range []struct {
		name                                                string
		marked, payload, changedAdmission, revoked, rawOnly bool
		want                                                int
	}{
		{name: "human marker survives omitted model argument", marked: true, want: 201},
		{name: "model may repeat recorded marker", marked: true, payload: true, want: 201},
		{name: "model cannot invent marker", payload: true, want: 403},
		{name: "ordinary typed creation stays unmarked", want: 201},
		{name: "admission cannot change captured marker", marked: true, changedAdmission: true, want: 403},
		{name: "revoked creation cannot mark or create", marked: true, revoked: true, want: 403},
		{name: "raw metadata is not captured consent", marked: true, payload: true, rawOnly: true, want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
			f := artifactProjectOriginalRunFixture(t)
			intent := artifacts.ProjectIntent{SubmissionID: "explicit-qa-create", CreatedByTest: tc.marked}
			if !tc.rawOnly {
				if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
					t.Fatal("record trusted message", err)
				}
			}
			writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-test-origin-admission-chain"), FlushSize: 1})
			t.Cleanup(func() { _ = writer.Close() })
			host := NewInternalHandler(f.rig.db, "synthetic-test-origin-host", newTestLogger())
			host.SetJournal(writer)
			admission := intent
			if tc.changedAdmission {
				admission.CreatedByTest = !intent.CreatedByTest
			}
			body := map[string]any{"id": f.a.RunID, "workspace_id": f.a.WorkspaceID, "agent_id": f.a.AgentID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": f.a.MessageID, "artifact_project_intent": admission}
			if tc.rawOnly {
				delete(body, "artifact_project_intent")
				body["metadata"] = map[string]any{"artifact_project_intent": intent}
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			admitted := httptest.NewRecorder()
			host.CreateRun(admitted, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)))
			if tc.changedAdmission {
				if admitted.Code != 403 {
					t.Fatalf("untrusted admission%d %s", admitted.Code, admitted.Body)
				}
				return
			}
			if admitted.Code != 201 {
				t.Fatalf("trusted admission%d %s", admitted.Code, admitted.Body)
			}
			if tc.rawOnly {
				for _, table := range []string{"artifact_project_message_intents", "artifact_project_run_consents"} {
					var retained int
					if err := f.rig.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&retained); err != nil || retained != 0 {
						t.Fatalf("raw metadata minted %s: %d %v", table, retained, err)
					}
				}
			}
			if tc.revoked {
				mustExec(t, f.rig.db, `UPDATE artifact_project_message_intents SET revoked_at='2026-10-28T19:56:59Z' WHERE message_id=?`, f.a.MessageID)
			}
			f.pages.artifactMasterToken = "synthetic-qa-artifact-master"
			token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
			manifest := `{"name":"QA artifact",` + strings.TrimPrefix(artifactProjectBody(0, "QA"), "{")
			if tc.payload {
				manifest = strings.Replace(manifest, "{", `{"created_by_test":true,`, 1)
			}
			packet := fmt.Sprintf(`{"operation":"artifact_create","slug":"agent-qa-item","payload":%s}`, manifest)
			req := httptest.NewRequest("POST", "/api/v1/internal/pages/artifact-tool", strings.NewReader(packet))
			req.Header.Set("Authorization", "Bearer "+token)
			ctx := context.WithValue(req.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
			ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
			rr := httptest.NewRecorder()
			f.pages.ArtifactProjectTool(rr, req.WithContext(ctx))
			if rr.Code != tc.want {
				t.Fatalf("tool%d want%d %s", rr.Code, tc.want, rr.Body)
			}
			var count int
			if err := f.rig.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE slug='agent-qa-item'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if tc.want != 201 {
				if count != 0 {
					t.Fatal("denied tool left Page")
				}
				return
			}
			var marker bool
			var actor string
			if err := f.rig.db.QueryRow(`SELECT p.created_by_test,v.actor FROM pages p JOIN artifact_versions v ON v.page_id=p.id AND v.workspace_id=p.workspace_id AND v.version=1 WHERE p.slug='agent-qa-item'`).Scan(&marker, &actor); err != nil {
				t.Fatal(err)
			}
			if marker != tc.marked || !strings.HasPrefix(actor, "agent:"+f.a.AgentID+":"+f.a.RunID) {
				t.Fatalf("marker%t actor%q", marker, actor)
			}
			// No later edit can rewrite the captured provenance.
			if _, err := f.rig.db.Exec(`UPDATE artifact_project_message_intents SET created_by_test=? WHERE message_id=?`, !tc.marked, f.a.MessageID); err == nil {
				t.Fatal("captured marker became mutable")
			}
		})
	}
}

func TestArtifactProjectTestOriginDuplicateRecordCannotChangePurpose(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	intent := artifacts.ProjectIntent{SubmissionID: "same-qa-submission", CreatedByTest: true}
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatal(err)
	}
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatal("exact retry", err)
	}
	intent.CreatedByTest = false
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err == nil {
		t.Fatal("same message changed human purpose")
	}
	var marker bool
	if err := f.rig.db.QueryRow(`SELECT created_by_test FROM artifact_project_message_intents WHERE message_id=?`, f.a.MessageID).Scan(&marker); err != nil || !marker {
		t.Fatalf("captured marker%t %v", marker, err)
	}
}
