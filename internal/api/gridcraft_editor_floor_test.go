package api

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// A named editor's edit based below the publication floor must not rebase over
// the owner's private revisions: their conflicts would disclose who changed
// which cell before publication.
func TestWorkbookEditorEditBelowPublicationFloorRequiresSnapshot(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, f, "ws-old", true)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "bob", "bob@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "workbookann000000000000000")
	bob := mintTokenFor(t, f.db, "bob", "workbookbob000000000000000")
	q := "?workspace_id=ws-old"
	liveWorkbook(t, f, ann, "ann", "floor-shared")
	base := "/api/v1/pages/floor-shared/workbook"

	owner := openEditorSession(t, f, ann, base)
	wantCode(t, f.do(ann, "POST", base+"/edits"+q, cellEdit(owner.SessionID, 1, 1, "B2", "draft-b2")), 200, "owner rev2")
	wantCode(t, f.do(ann, "POST", base+"/edits"+q, cellEdit(owner.SessionID, 2, 2, "C3", "draft-c3")), 200, "owner rev3")
	wantCode(t, f.do(ann, "POST", base+"/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "share")

	editor := openEditorSession(t, f, bob, base)
	if editor.Rev != 3 {
		t.Fatalf("editor rev %d", editor.Rev)
	}
	body := `{"session_id":"` + editor.SessionID + `","mutation_id":"` + editor.SessionID + `:1","base_rev":1,"generation":1,"ops":[{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"x"},{"kind":"cell.set","sheet":"Sheet1","cell":"C3","input":"y"}]}`
	w := f.do(bob, "POST", base+"/edits"+q, body)
	var out struct {
		Error            string            `json:"error"`
		SnapshotRequired bool              `json:"snapshot_required"`
		Conflicts        []json.RawMessage `json:"conflicts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %d %s: %v", w.Code, w.Body.String(), err)
	}
	if w.Code != 409 || out.Error != "precondition_failed" || !out.SnapshotRequired || len(out.Conflicts) != 0 {
		t.Fatalf("edit below floor: %d %s", w.Code, w.Body.String())
	}

	// From the published head the same edit still applies.
	wantCode(t, f.do(bob, "POST", base+"/edits"+q, cellEdit(editor.SessionID, 2, 3, "B2", "x")), 200, "edit at floor")
}
