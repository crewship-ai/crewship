package backup

import (
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Pre-compaction backups had no drawing deletion timestamp. Start their undo
// window at restore, rather than retaining those tombstones forever. Existing
// timestamps remain intact; reading/restoring never refreshes a known deadline.
func safeBoardHistoryRestoreRow(table string, row map[string]any) map[string]any {
	if table != "page_board_elements" || row["element_json"] != nil || row["deleted_at"] != nil {
		return row
	}
	out := make(map[string]any, len(row)+1)
	for k, v := range row {
		out[k] = v
	}
	out["deleted_at"] = tsformat.Format(time.Now())
	return out
}
