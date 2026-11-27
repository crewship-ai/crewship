package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func (rig *workbookRig) operations(from string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pages/native-page/craft/document/operations?from="+from, nil).WithContext(rig.ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", rig.token)
	w := httptest.NewRecorder()
	rig.d.listOperations(w, r)
	return w
}

// An agent change is saved with the operations it applied; the editor reads them from its base to
// the head, with the head's hash, and they replay only when every version in between has them. A
// save over a base that moved answers 409 moved with the head and where its operations are.
func TestWorkbookOperationsListWhatAgentVersionsApplied(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	mustExec(t, rig.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent-morgan',?,'Morgan','morgan')`, rig.ws)
	// v2: the person's own save (no operations).
	v2 := craftTestWorkbook(t, "xlsm", "person")
	if w := rig.save(v2, "1", workbookHash(v2)); w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	// v3: an agent change with its operations, recorded in the version's own transaction.
	v3 := craftTestWorkbook(t, "xlsm", "agent")
	ops := json.RawMessage(`[{"command":"edit.clearContents","params":{"range":"'L'!B8:B577"}}]`)
	s := artifacts.NewVersionStore(rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: rig.ws, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 2, EntryPoint: "workbook.xlsm",
		Files: map[string]artifacts.VersionFile{"workbook.xlsm": {Content: v3, MediaType: mooring.WorkbookMediaTypeXLSM}},
		Record: func(ctx context.Context, tx *sql.Tx, r artifacts.VersionReceipt) error {
			return recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: rig.ws, PageID: "native-page", Version: r.Version, BaseVersion: 2, ActorKind: "agent", ActorID: "agent-morgan",
				OperationID: "op-1", EnginePin: rig.pin, Calculate: "calculated", Replayable: true, Ops: ops})
		}}, func(context.Context, *sql.Tx) (string, error) { return "agent:agent-morgan", nil }); err != nil {
		t.Fatal(err)
	}
	type version struct {
		Version, BaseVersion, ActorKind, ActorName, OperationID, EnginePin string
		Replayable                                                         bool
		Ops                                                                json.RawMessage
	}
	type answer struct {
		From, To, Head, HeadContentHash string
		Replayable                      bool
		Versions                        []version
	}
	read := func(from string) (int, answer, string) {
		w := rig.operations(from)
		var a struct {
			From            string `json:"from"`
			To              string `json:"to"`
			Head            string `json:"head"`
			HeadContentHash string `json:"head_content_hash"`
			Replayable      bool   `json:"replayable"`
			Versions        []struct {
				Version     string          `json:"version"`
				BaseVersion string          `json:"base_version"`
				ActorKind   string          `json:"actor_kind"`
				ActorName   string          `json:"actor_name"`
				OperationID string          `json:"operation_id"`
				EnginePin   string          `json:"engine_pin"`
				Replayable  bool            `json:"replayable"`
				Ops         json.RawMessage `json:"ops"`
			} `json:"versions"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &a)
		out := answer{a.From, a.To, a.Head, a.HeadContentHash, a.Replayable, nil}
		for _, v := range a.Versions {
			out.Versions = append(out.Versions, version{v.Version, v.BaseVersion, v.ActorKind, v.ActorName, v.OperationID, v.EnginePin, v.Replayable, v.Ops})
		}
		return w.Code, out, w.Body.String()
	}
	code, a, raw := read("2")
	if code != 200 || a.Head != "3" || a.HeadContentHash != workbookHash(v3) || !a.Replayable || len(a.Versions) != 1 {
		t.Fatalf("from 2: %d %s", code, raw)
	}
	if v := a.Versions[0]; v.Version != "3" || v.BaseVersion != "2" || v.ActorKind != "agent" || v.ActorName != "Morgan" || v.OperationID != "op-1" || v.EnginePin != rig.pin || !v.Replayable || string(v.Ops) != string(ops) {
		t.Fatalf("agent version %+v", v)
	}
	code, a, raw = read("1")
	if code != 200 || a.Replayable || len(a.Versions) != 2 || a.Versions[0].Version != "2" || a.Versions[0].Replayable || a.Versions[0].Ops != nil || a.Versions[0].ActorKind != "human" {
		t.Fatalf("from 1: %d %s", code, raw)
	}
	for _, bad := range []string{"0", "x", "4"} {
		if code, _, raw = read(bad); code != 400 {
			t.Errorf("from %s: %d %s", bad, code, raw)
		}
	}
	// A save over base 2 (the head is 3) is moved.
	mine := craftTestWorkbook(t, "xlsm", "mine")
	w := rig.save(mine, "2", workbookHash(mine))
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"moved"`) || !strings.Contains(w.Body.String(), `"head":"3"`) || !strings.Contains(w.Body.String(), `/craft/document/operations?from=2`) {
		t.Fatalf("moved %d %s", w.Code, w.Body)
	}
	// Keep mine: the same save over the head, marked keep-local, is saved and audited as such.
	rig.rebase = "keep-local"
	if w = rig.save(mine, "3", workbookHash(mine)); w.Code != 200 {
		t.Fatalf("keep-local %d %s", w.Code, w.Body)
	}
	var marked int
	if err := rig.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.sheet_saved' AND metadata LIKE '%"source":"keep_local"%'`).Scan(&marked); err != nil || marked != 1 {
		t.Fatalf("keep-local audit %d %v", marked, err)
	}
}
