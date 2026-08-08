package api

import (
	"net/http/httptest"
	"testing"
)

// The journal is the only place a run's whole story exists: assignments and
// pipeline_runs are separate execution substrates with no table joining
// them (see internal/api/issue_handler_runs.go:10-13). journal.Query grew a
// RunID filter that ORs the three doors a run id arrives by; without the
// param the API could not reach it, so /activity's execution graph came up
// empty for every routine run.
//
// parseJournalQuery is shared by List AND Stream, so this one param also
// scopes the live SSE tail to a single run.
func TestParseJournalQuery_RunID(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/journal?run_id=run_abc", nil)
	q, err := parseJournalQuery(req, "ws_1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.RunID != "run_abc" {
		t.Fatalf("RunID = %q, want run_abc", q.RunID)
	}
}

func TestParseJournalQuery_RunIDAbsentIsEmpty(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/journal", nil)
	q, err := parseJournalQuery(req, "ws_1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.RunID != "" {
		t.Fatalf("RunID = %q, want empty", q.RunID)
	}
}

// run_id must not quietly replace trace_id — they are different questions
// and a caller may legitimately send both.
func TestParseJournalQuery_RunIDAndTraceIDCoexist(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/journal?run_id=run_abc&trace_id=tr_1", nil)
	q, err := parseJournalQuery(req, "ws_1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q.RunID != "run_abc" || q.TraceID != "tr_1" {
		t.Fatalf("RunID=%q TraceID=%q — both must survive", q.RunID, q.TraceID)
	}
}
