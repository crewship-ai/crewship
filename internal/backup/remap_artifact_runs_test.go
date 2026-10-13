package backup

import (
	"encoding/json"
	"testing"
)

func TestRemapArtifactCreationRunsPreservesHistoricalTies(t *testing.T) {
	dump := &DBDump{Tables: map[string][]map[string]any{
		"page_artifact_creations":         {{"run_id": "original-run", "mutation_id": "same-mutation"}, {"run_id": "original-run", "mutation_id": "other-mutation"}},
		"page_artifact_revisions":         {{"author_run_id": "original-run"}, {"author_run_id": "unrelated-run"}},
		"page_artifact_mutation_receipts": {{"author_run_id": "original-run"}},
		"page_artifact_requests":          {{"run_id": "original-run", "thread_id": "thread"}},
		"eval_runs":                       {{"trace_id": "original-run"}, {"trace_id": "unrelated-run"}},
		"message_feedback":                {{"trace_id": "original-run"}, {"trace_id": "unrelated-run"}},
		"journal_entries":                 {{"trace_id": "original-run", "payload": `{"run_id":"original-run","note":"original-run"}`}},
	}}
	remapArtifactCreationRuns(dump)
	mapped := dump.Tables["page_artifact_creations"][0]["run_id"]
	if mapped == "original-run" || mapped == "" {
		t.Fatalf("not forked: %v", mapped)
	}
	for _, ref := range []struct{ table, column string }{{"page_artifact_creations", "run_id"}, {"page_artifact_revisions", "author_run_id"}, {"page_artifact_mutation_receipts", "author_run_id"}, {"page_artifact_requests", "run_id"}, {"journal_entries", "trace_id"}, {"eval_runs", "trace_id"}, {"message_feedback", "trace_id"}} {
		if got := dump.Tables[ref.table][0][ref.column]; got != mapped {
			t.Fatalf("%s link lost: %v want %v", ref.table, got, mapped)
		}
	}
	if dump.Tables["page_artifact_creations"][1]["run_id"] != mapped || dump.Tables["page_artifact_revisions"][1]["author_run_id"] != "unrelated-run" {
		t.Fatal("inconsistent or unrelated rewrite")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(dump.Tables["journal_entries"][0]["payload"].(string)), &payload); err != nil || payload["run_id"] != mapped || payload["note"] != "original-run" {
		t.Fatalf("journal payload linkage: %v %v", payload, err)
	}
	for _, table := range []string{"eval_runs", "message_feedback"} {
		if dump.Tables[table][1]["trace_id"] != "unrelated-run" {
			t.Fatalf("rewrote unrelated %s trace", table)
		}
	}
	if _, ok := dump.Tables["artifact_run_principals"]; ok {
		t.Fatal("fork created live authority")
	}
}

func TestRemapArtifactRequestThreadVirtualForeignKey(t *testing.T) {
	db := newRemapTestDB(t)
	dump := &DBDump{Tables: map[string][]map[string]any{"page_artifact_threads": {{"id": "thread"}}, "page_artifact_requests": {{"id": "request", "thread_id": "thread"}}}}
	if err := RemapIDs(t.Context(), db, dump); err != nil {
		t.Fatal(err)
	}
	thread := dump.Tables["page_artifact_threads"][0]["id"]
	if thread == "thread" || dump.Tables["page_artifact_requests"][0]["thread_id"] != thread {
		t.Fatalf("thread link not remapped: %v", dump.Tables)
	}
}
