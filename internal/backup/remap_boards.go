package backup

import "encoding/json"

// Board child FKs address page_boards.page_id and revisions.page_id rather
// than an id column. Generic row-ID remapping cannot resolve those aliases;
// their canonical identity is the owning Page, while element IDs/revisions
// remain unchanged inside that Page namespace.
func remapBoardPageReferences(dump *DBDump, ids map[string]map[string]string) {
	remapBoardRequestSnapshots(dump, ids)
	for _, ref := range []struct{ table, column string }{
		{"page_board_requests", "board_page_id"},
		{"page_board_requests", "original_board_id"},
		{"page_board_requests", "result_page_id"},
		{"page_board_request_sources", "source_page_id"},
		{"page_board_source_history", "board_page_id"},
		{"page_board_source_history", "source_page_id"},
		{"page_board_write_audit", "board_page_id"},
		{"page_board_files", "board_page_id"},
		{"page_board_elements", "board_page_id"},
		{"page_board_mutations", "board_page_id"},
		{"page_board_artifact_placements", "board_page_id"},
		{"page_board_artifact_placements", "source_page_id"},
	} {
		for _, row := range dump.Tables[ref.table] {
			old, _ := row[ref.column].(string)
			if mapped, ok := ids["pages"][old]; ok {
				row[ref.column] = mapped
			}
		}
	} // Image IDs are global opaque file identities and occur in the public
	// element JSON; rewrite only that typed field, never arbitrary scene text.
	for _, row := range dump.Tables["page_board_elements"] {
		raw, _ := row["element_json"].(string)
		var element map[string]any
		if json.Unmarshal([]byte(raw), &element) != nil || element["type"] != "image" {
			continue
		}
		old, _ := element["fileId"].(string)
		if mapped, ok := ids["page_board_files"][old]; ok {
			element["fileId"] = mapped
			if encoded, err := json.Marshal(element); err == nil {
				row["element_json"] = string(encoded)
			}
		}
	}

}

// Job snapshots are immutable history, but typed opaque file/placement identities
// must follow their forked rows. Board-scoped drawing IDs and prose stay intact.
func remapBoardRequestSnapshots(dump *DBDump, ids map[string]map[string]string) {
	for _, row := range dump.Tables["page_board_requests"] {
		raw, _ := row["snapshot_json"].(string)
		var snapshot struct {
			Elements   []map[string]any `json:"elements"`
			Placements []map[string]any `json:"placements"`
		}
		if json.Unmarshal([]byte(raw), &snapshot) == nil {
			for _, el := range snapshot.Elements {
				if el["type"] == "image" {
					old, _ := el["fileId"].(string)
					if mapped, ok := ids["page_board_files"][old]; ok {
						el["fileId"] = mapped
					}
				}
			}
			for _, p := range snapshot.Placements {
				old, _ := p["id"].(string)
				if mapped, ok := ids["page_board_artifact_placements"][old]; ok {
					p["id"] = mapped
				}
			}
			if encoded, err := json.Marshal(snapshot); err == nil {
				row["snapshot_json"] = string(encoded)
			}
		}
		raw, _ = row["selected_placement_ids_json"].(string)
		var placements []string
		if json.Unmarshal([]byte(raw), &placements) == nil {
			for i, id := range placements {
				if mapped, ok := ids["page_board_artifact_placements"][id]; ok {
					placements[i] = mapped
				}
			}
			if encoded, err := json.Marshal(placements); err == nil {
				row["selected_placement_ids_json"] = string(encoded)
			}
		}
	}
}
