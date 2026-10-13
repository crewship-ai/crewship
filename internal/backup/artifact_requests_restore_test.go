package backup

import "testing"

func TestArtifactRequestRestoreSuspendsAuthorityPreservesHistory(t *testing.T) {
	for _, state := range []string{"queued", "running", "succeeded", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			original := map[string]any{"status": state, "receipt_json": "secret authority", "run_id": "live-run", "instruction": "make report", "result_revision": 4, "error": "historical"}
			got := safeArtifactRequestRestoreRow("page_artifact_requests", original)
			if got["receipt_json"] != "{}" || got["run_id"] != nil || got["instruction"] != "make report" || got["result_revision"] != 4 {
				t.Fatalf("unsafe or lost history: %v", got)
			}
			want := state
			if state == "queued" || state == "running" {
				want = "failed"
			}
			if got["status"] != want {
				t.Fatalf("status: %v", got)
			}
			if original["receipt_json"] != "secret authority" || original["status"] != state {
				t.Fatal("mutated source backup")
			}
		})
	}
}

func TestBoardRequestRestoreSuspendsExecutionPreservesHistory(t *testing.T) {
	for _, state := range []string{"queued", "running", "ready_to_insert", "inserted", "succeeded", "failed", "cancelled"} {
		original := map[string]any{"status": state, "receipt_json": "live authority", "run_id": "historical-run", "snapshot_json": "immutable scene", "result_revision": 4}
		got := safeArtifactRequestRestoreRow("page_board_requests", original)
		if got["receipt_json"] != "" || got["run_id"] != "historical-run" || got["snapshot_json"] != "immutable scene" || got["result_revision"] != 4 {
			t.Fatalf("lost history or retained authority: %+v", got)
		}
		expected := state
		if state == "queued" || state == "running" {
			expected = "failed"
		}
		if got["status"] != expected || original["receipt_json"] != "live authority" {
			t.Fatalf("state/input changed: %+v", got)
		}
	}
}
