package backup

import "testing"

func TestBoardHistoryRestoreInitializesOnlyLegacyTombstoneDeadline(t *testing.T) {
	old := map[string]any{"element_json": nil}
	got := safeBoardHistoryRestoreRow("page_board_elements", old)
	if got["deleted_at"] == nil || old["deleted_at"] != nil {
		t.Fatal("legacy deadline not initialized safely")
	}
	known := map[string]any{"element_json": nil, "deleted_at": "original-deadline"}
	if safeBoardHistoryRestoreRow("page_board_elements", known)["deleted_at"] != "original-deadline" {
		t.Fatal("restore refreshed deadline")
	}
	live := map[string]any{"element_json": "{}"}
	if safeBoardHistoryRestoreRow("page_board_elements", live)["deleted_at"] != nil {
		t.Fatal("live element marked deleted")
	}
}
