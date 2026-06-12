package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// activity_handler_cov_test.go covers buildWhere's filter combinators
// and the fetch* query-error fallbacks (each source degrades to nil,
// the merged feed stays 200). Helpers prefixed covACT.

func TestCovACT_BuildWhere_Combinations(t *testing.T) {
	base := "a.workspace_id = ?"
	args := []any{"ws"}

	// Agent filter expands into an OR list over the given columns.
	f := activityFilter{WorkspaceID: "ws", AgentID: "ag-1"}
	where, outArgs := f.buildWhere(base, args, []string{"a.assigned_by_id", "a.assigned_to_id"}, "c.id")
	wantWhere := "a.workspace_id = ? AND (a.assigned_by_id = ? OR a.assigned_to_id = ?)"
	if where != wantWhere {
		t.Errorf("where = %q, want %q", where, wantWhere)
	}
	if !reflect.DeepEqual(outArgs, []any{"ws", "ag-1", "ag-1"}) {
		t.Errorf("args = %v", outArgs)
	}

	// Crew filter appends a single equality.
	f = activityFilter{WorkspaceID: "ws", CrewID: "crew-1"}
	where, outArgs = f.buildWhere(base, []any{"ws"}, []string{"x"}, "c.id")
	if where != "a.workspace_id = ? AND c.id = ?" {
		t.Errorf("where = %q", where)
	}
	if !reflect.DeepEqual(outArgs, []any{"ws", "crew-1"}) {
		t.Errorf("args = %v", outArgs)
	}

	// Both filters together.
	f = activityFilter{WorkspaceID: "ws", AgentID: "ag-1", CrewID: "crew-1"}
	where, outArgs = f.buildWhere(base, []any{"ws"}, []string{"col1"}, "c.id")
	if where != "a.workspace_id = ? AND (col1 = ?) AND c.id = ?" {
		t.Errorf("where = %q", where)
	}
	if !reflect.DeepEqual(outArgs, []any{"ws", "ag-1", "crew-1"}) {
		t.Errorf("args = %v", outArgs)
	}

	// No filters: unchanged.
	f = activityFilter{WorkspaceID: "ws"}
	where, outArgs = f.buildWhere(base, []any{"ws"}, []string{"col1"}, "c.id")
	if where != base || len(outArgs) != 1 {
		t.Errorf("where = %q args = %v, want base unchanged", where, outArgs)
	}
}

func TestCovACT_ListAllActivity_DBErrorDegradesToEmptyFeed(t *testing.T) {
	db := setupTestDB(t)
	h := NewQueryHandler(db, nil, nil, "", newTestLogger())
	db.Close()
	// All three fetchers hit the closed DB, log, and return nil — the
	// handler still answers 200 with an empty array.
	req := httptest.NewRequest("GET", "/api/v1/activity?agent_id=a1&crew_id=c1", nil)
	req = req.WithContext(withWorkspace(req.Context(), "ws", "OWNER"))
	rr := httptest.NewRecorder()
	h.ListAllActivity(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	if len(items) != 0 {
		t.Errorf("items = %d, want 0", len(items))
	}
}
