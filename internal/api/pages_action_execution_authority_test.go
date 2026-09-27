package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestPageActionExecutionAuthority(t *testing.T) {
	for _, scenario := range []string{"owner", "manager-in-crew", "manager-removed-from-crew", "demoted", "action-removed", "routine-rebound", "params-changed", "routine-deleted", "crew-deleted", "page-deleted", "other-workspace", "nested-origin", "forged-metadata"} {
		t.Run(scenario, func(t *testing.T) {
			h, _, ws, user := newPageActionFixture(t)
			rr := pagesDispatch(t, h, ws, user, "OWNER", pageActionID, `{"inputs":{"reason":"authority test"}}`, "")
			if rr.Code != http.StatusAccepted {
				t.Fatalf("admission: %d %s", rr.Code, rr.Body.String())
			}
			due, err := pipeline.NewPendingRunStore(h.db).DueRuns(t.Context(), time.Now().Add(time.Hour), 10)
			if err != nil || len(due) != 1 {
				t.Fatalf("due=%+v err=%v", due, err)
			}
			pr := due[0]
			in := pipeline.RunInput{WorkspaceID: ws, PipelineID: pr.PipelineID, InvokingUserID: pr.InvokingUserID, InvocationAuthority: pr.InvocationAuthority, Mode: pipeline.ModeRun}
			check := pipeline.NewInvocationAuthorityChecker(h.db)
			if err := check(t.Context(), in); err != nil {
				t.Fatalf("initial valid authority: %v", err)
			}
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := h.db.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			allowed := false
			switch scenario {
			case "owner":
				allowed = true
			case "manager-in-crew", "manager-removed-from-crew":
				exec(`UPDATE workspace_members SET role='MANAGER' WHERE workspace_id=? AND user_id=?`, ws, user)
				exec(`INSERT INTO crew_members(id,crew_id,user_id) VALUES('authority-member','crew-lookout',?)`, user)
				if err := check(t.Context(), in); err != nil {
					t.Fatalf("manager in crew: %v", err)
				}
				if scenario == "manager-in-crew" {
					allowed = true
				} else {
					exec(`DELETE FROM crew_members WHERE id='authority-member'`)
				}
			case "demoted":
				exec(`UPDATE workspace_members SET role='MEMBER', capabilities='["routine.run"]' WHERE workspace_id=? AND user_id=?`, ws, user)
			case "action-removed", "routine-rebound", "params-changed":
				var raw string
				if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE slug=? AND workspace_id=?`, pageActionSlug, ws).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var doc pages.Document
				if err := json.Unmarshal([]byte(raw), &doc); err != nil {
					t.Fatal(err)
				}
				panel, _ := doc.FindPanel(pageActionPanel)
				if scenario == "action-removed" {
					panel.Actions = nil
				} else {
					action, _ := panel.FindAction(pageActionID)
					if scenario == "params-changed" {
						action.Params = map[string]any{"cluster": "other"}
					} else {
						action.Routine = "different-routine"
					}
				}
				rawBytes, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				exec(`UPDATE pages SET spec_json=? WHERE slug=? AND workspace_id=?`, string(rawBytes), pageActionSlug, ws)
			case "routine-deleted":
				exec(`UPDATE pipelines SET deleted_at='2026-09-27T00:00:00Z' WHERE id=?`, pr.PipelineID)
			case "crew-deleted":
				exec(`UPDATE crews SET deleted_at='2026-09-27T00:00:00Z' WHERE id='crew-lookout'`)
			case "page-deleted":
				exec(`DELETE FROM pages WHERE workspace_id=? AND slug=?`, ws, pageActionSlug)
			case "other-workspace":
				in.WorkspaceID = "other"
			case "nested-origin":
				in.PipelineID = "nested-target"
				allowed = true
			case "forged-metadata":
				in.MetadataJSON = `{"page_id":"different","source":"routine.run"}`
				allowed = true
			}
			err = check(t.Context(), in)
			if allowed {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, pipeline.ErrInvocationAuthorityRevoked) {
				t.Fatalf("revocation=%v", err)
			}
		})
	}
}
