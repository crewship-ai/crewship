package api

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// #5317 step 5.2: outside Docs, a folder's "propose" level is exactly "read".
// Every write-like operation a chat run can attempt on an artifact project
// (website, dashboard, Slides), a Design, a Sheet or a Mooring artifact filed
// in a folder of its crew is refused with the same status and body under
// propose as under read, and changes nothing.

type folderKindResult struct {
	code int
	body string
}

func (r folderKindResult) String() string { return fmt.Sprintf("%d %s", r.code, r.body) }

func folderKindOutcome(w *httptest.ResponseRecorder) folderKindResult {
	return folderKindResult{w.Code, strings.TrimSpace(w.Body.String())}
}

func wantFolderLevelsAlike(t *testing.T, what string, got map[string]folderKindResult) {
	t.Helper()
	read, propose := got[folderAgentAccessRead], got[folderAgentAccessPropose]
	if read.code >= 200 && read.code < 300 {
		t.Fatalf("%s under folder read succeeded: %v", what, read)
	}
	if read != propose {
		t.Fatalf("%s: folder propose %v, folder read %v", what, propose, read)
	}
}

// Artifact projects (a website, a dashboard and Slides): the human's own
// artifact, shared and filed in the crew's folder.
func TestFolderProposeKinds_ProjectWritesRefusedAsUnderRead(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
	cases := []struct{ name, body, operation, payload string }{
		{"publish", artifactProjectBody(0, "kinds"), "artifact_publish", artifactProjectBody(1, "Second")},
		{"restore", artifactProjectBody(0, "kinds"), "artifact_restore", `{"base_version":1,"version":1}`},
		{"reply", artifactProjectBody(0, "kinds"), "artifact_reply", `{"comment_id":"no-such-comment","version":1,"text":"Hello"}`},
		{"data batch", dashboardBody(0, "sales"), "artifact_data_batch", `{"collection":"sales","base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":1}}]}`},
		{"slides publish", slidesArtifactBody(0, "Kinds"), "artifact_publish", slidesArtifactBody(1, "Second")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]folderKindResult{}
			for _, level := range []string{folderAgentAccessRead, folderAgentAccessPropose} {
				g := newRunScopeRig(t)
				id := g.createAs(t, "folder-kinds", tc.body)
				mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, id)
				g.folder(t, "kinds", g.f.a.CrewID, level, id)
				g.start(t)
				if w := g.tool("artifact_versions", "folder-kinds", `{}`); w.Code != 200 {
					t.Fatalf("folder %s read baseline: %d %s", level, w.Code, w.Body)
				}
				got[level] = folderKindOutcome(g.tool(tc.operation, "folder-kinds", tc.payload))
				var latest int
				if err := g.f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, id).Scan(&latest); err != nil || latest != 1 {
					t.Fatalf("folder %s: a refused %s left version %d %v", level, tc.operation, latest, err)
				}
				for _, r := range runAuditRows(t, g) {
					if r.Operation != "read" {
						t.Fatalf("folder %s audited %+v", level, r)
					}
				}
			}
			wantFolderLevelsAlike(t, tc.name, got)
		})
	}
}

func TestFolderProposeKinds_DesignWritesRefusedAsUnderRead(t *testing.T) {
	for _, op := range []struct{ name, payload string }{
		{"design_apply", designRunOp},
		{"design_snapshot", `{}`},
		{"design_restore", `{"version":1}`},
	} {
		t.Run(op.name, func(t *testing.T) {
			got := map[string]folderKindResult{}
			for _, level := range []string{folderAgentAccessRead, folderAgentAccessPropose} {
				g := newDesignRunRig(t)
				id := g.design(t, g.f.a.HumanUserID, "MEMBER", "folder-kinds-design")
				mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, id)
				g.folder(t, "kinds-design", g.f.a.CrewID, level, id)
				g.start(t)
				if w := g.designTool("design_get", "folder-kinds-design", `{}`); w.Code != 200 {
					t.Fatalf("folder %s read baseline: %d %s", level, w.Code, w.Body)
				}
				before := designVersionCount(t, g.f, id)
				got[level] = folderKindOutcome(g.designTool(op.name, "folder-kinds-design", op.payload))
				if after := designVersionCount(t, g.f, id); after != before {
					t.Fatalf("folder %s: a refused %s versioned the Design %d -> %d", level, op.name, before, after)
				}
			}
			wantFolderLevelsAlike(t, op.name, got)
		})
	}
}

// A Sheet and a Mooring artifact reach a chat run only through their own
// mandate; a folder at either level adds nothing, and the refusal is the one a
// missing artifact gets.
func TestFolderProposeKinds_SheetAndMooringWritesRefusedAsUnderRead(t *testing.T) {
	t.Run("sheet", func(t *testing.T) {
		got := map[string]folderKindResult{}
		var missing folderKindResult
		for _, level := range []string{folderAgentAccessRead, folderAgentAccessPropose} {
			g := newSheetRunRig(t, true)
			wmSeedPage(t, g.f.rig.db, g.f.a.WorkspaceID, "grid-run-other", "grid-run-other", "Other", g.f.a.HumanUserID, "")
			mustExec(t, g.f.rig.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at,agent_access) VALUES('folder-kinds-sheet',?,'kinds-sheet','Sheets',?,'x','x',?)`, g.f.a.WorkspaceID, g.f.a.CrewID, level)
			mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0,folder_id='folder-kinds-sheet' WHERE id='grid-run-other'`)
			got[level] = folderKindOutcome(g.call(strings.ReplaceAll(sheetRunApply, "grid-run-page", "grid-run-other")))
			missing = folderKindOutcome(g.call(strings.ReplaceAll(sheetRunApply, "grid-run-page", "grid-run-nowhere")))
			if rev := sheetHead(t, g.f); rev != 1 {
				t.Fatalf("folder %s moved the mandated Sheet to %d", level, rev)
			}
		}
		wantFolderLevelsAlike(t, "sheet apply", got)
		if got[folderAgentAccessPropose] != missing {
			t.Fatalf("folder propose %v, missing %v", got[folderAgentAccessPropose], missing)
		}
	})
	t.Run("mooring", func(t *testing.T) {
		got := map[string]folderKindResult{}
		var missing folderKindResult
		for _, level := range []string{folderAgentAccessRead, folderAgentAccessPropose} {
			f, call := newMooringRunRig(t, true)
			wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "mooring-run-other", "mooring-run-other", "Other", f.a.HumanUserID, "")
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='mooring-run-other'`)
			mustExec(t, f.rig.db, `INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES(?,'mooring-run-other','hello',1,1,'{"text":"other"}')`, f.a.WorkspaceID)
			mustExec(t, f.rig.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at,agent_access) VALUES('folder-kinds-mooring',?,'kinds-mooring','Apps',?,'x','x',?)`, f.a.WorkspaceID, f.a.CrewID, level)
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0,folder_id='folder-kinds-mooring' WHERE id='mooring-run-other'`)
			got[level] = folderKindOutcome(call(strings.ReplaceAll(mooringRunCommand, "mooring-run-page", "mooring-run-other")))
			missing = folderKindOutcome(call(strings.ReplaceAll(mooringRunCommand, "mooring-run-page", "mooring-run-nowhere")))
			var rev int
			if err := f.rig.db.QueryRow(`SELECT rev FROM mooring_documents WHERE page_id='mooring-run-other'`).Scan(&rev); err != nil || rev != 1 {
				t.Fatalf("folder %s moved the Mooring document to %d %v", level, rev, err)
			}
		}
		wantFolderLevelsAlike(t, "mooring command", got)
		if got[folderAgentAccessPropose] != missing {
			t.Fatalf("folder propose %v, missing %v", got[folderAgentAccessPropose], missing)
		}
	})
}
