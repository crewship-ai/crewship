package backup

// Board child FKs address page_boards.page_id and revisions.page_id rather
// than an id column. Generic row-ID remapping cannot resolve those aliases;
// their canonical identity is the owning Page, while element IDs/revisions
// remain unchanged inside that Page namespace.
func remapBoardPageReferences(dump *DBDump, ids map[string]map[string]string) {
	for _, ref := range []struct{ table, column string }{
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
	}
}
