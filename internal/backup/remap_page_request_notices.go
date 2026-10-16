package backup

import (
	"encoding/json"
	"strings"
)

func remapPageRequestNotices(dump *DBDump, ids map[string]map[string]string) {
	for _, row := range dump.Tables["inbox_items"] {
		if row["kind"] != "message" {
			continue
		}
		source, _ := row["source_id"].(string)
		for _, kind := range []string{"artifact", "board"} {
			prefix := "page-request:" + kind + ":"
			if !strings.HasPrefix(source, prefix) {
				continue
			}
			table := "page_artifact_requests"
			if kind == "board" {
				table = "page_board_requests"
			}
			old := strings.TrimPrefix(source, prefix)
			mapped, ok := ids[table][old]
			if !ok {
				continue
			}
			row["source_id"] = prefix + mapped
			raw, _ := row["payload_json"].(string)
			var payload map[string]any
			if json.Unmarshal([]byte(raw), &payload) != nil || payload["subkind"] != "page_request_result" {
				continue
			}
			if payload["request_id"] == old {
				payload["request_id"] = mapped
			}
			if page, ok := payload["page_id"].(string); ok {
				if newPage, exists := ids["pages"][page]; exists {
					payload["page_id"] = newPage
				}
			}
			if encoded, err := json.Marshal(payload); err == nil {
				row["payload_json"] = string(encoded)
			}
		}
	}
}
