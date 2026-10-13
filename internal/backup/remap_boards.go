package backup

import "encoding/json"

// Board child FKs address page_boards.page_id and revisions.page_id rather
// than an id column. Generic row-ID remapping cannot resolve those aliases;
// their canonical identity is the owning Page, while element IDs/revisions
// remain unchanged inside that Page namespace.
func remapBoardPageReferences(dump *DBDump, ids map[string]map[string]string) {
	for _, ref := range []struct{ table, column string }{
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
