package backup

import (
	"encoding/json"
	"testing"
)

func TestPageRequestRetryRestoreOrderAndNoticeFork(t *testing.T) {
	parent := map[string]any{"id": "parent"}
	child := map[string]any{"id": "child", "retry_of_request_id": "parent"}
	rows, err := orderPageRequestRetryRows("page_artifact_requests", []map[string]any{child, parent})
	if err != nil || rows[0]["id"] != "parent" || rows[1]["id"] != "child" {
		t.Fatalf("ancestry order=%v %v", rows, err)
	}
	if _, err := orderPageRequestRetryRows("page_board_requests", []map[string]any{{"id": "a", "retry_of_request_id": "b"}, {"id": "b", "retry_of_request_id": "a"}}); err == nil {
		t.Fatal("cyclic lineage accepted")
	}
	for _, table := range []string{"page_artifact_requests", "page_board_requests"} {
		original := map[string]any{"notice_pending": 1, "status": "succeeded"}
		safe := safeArtifactRequestRestoreRow(table, original)
		if safe["notice_pending"] != 0 || original["notice_pending"] != 1 {
			t.Fatalf("notice replay/source mutation: %v %v", safe, original)
		}
	}
	dump := &DBDump{Tables: map[string][]map[string]any{"inbox_items": {{"kind": "message", "source_id": "page-request:artifact:parent", "payload_json": `{"subkind":"page_request_result","request_id":"parent","page_id":"page","note":"parent"}`}, {"kind": "message", "source_id": "unrelated", "payload_json": `{"request_id":"parent"}`}}}}
	remapPageRequestNotices(dump, map[string]map[string]string{"page_artifact_requests": {"parent": "fork-parent"}, "pages": {"page": "fork-page"}})
	row := dump.Tables["inbox_items"][0]
	var payload map[string]any
	if err := json.Unmarshal([]byte(row["payload_json"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if row["source_id"] != "page-request:artifact:fork-parent" || payload["request_id"] != "fork-parent" || payload["page_id"] != "fork-page" || payload["note"] != "parent" {
		t.Fatalf("incorrect structured remap: %v %v", row, payload)
	}
	if dump.Tables["inbox_items"][1]["payload_json"] != `{"request_id":"parent"}` {
		t.Fatal("unrelated payload rewritten")
	}
}
