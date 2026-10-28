package api

import (
	"errors"
	"net/http"
)

// Filter only rows already admitted by loadPageIndex. OWNER custody is a
// separate audited read path and never enters another human's personal view.
func pageCatalogRows(r *http.Request, index *pageIndex, human string) ([]pageIndexRow, int, error) {
	audience := r.URL.Query().Get("audience")
	if audience != "" && audience != "all" && audience != "personal" {
		return nil, 0, errors.New("audience must be all or personal")
	}
	rows := index.rows
	if audience == "personal" {
		rows = make([]pageIndexRow, 0, len(index.rows))
		for _, row := range index.rows {
			if row.rec.PrivateAudience && row.rec.OwnerUserID == human {
				rows = append(rows, row)
			}
		}
	}
	rows, err := pageCatalogVisibilityRows(r, rows)
	if err != nil {
		return nil, 0, err
	}

	total := len(rows)
	// Preserve the established unpaged array when neither parameter is present.
	// Counts and windows are computed after authorization and audience filtering.
	query := r.URL.Query()
	if query.Has("limit") || query.Has("offset") {
		limit, offset := parsePagination(r, 100, 500)
		start := min(offset, total)
		end := start + min(limit, total-start)
		rows = rows[start:end]
	}
	return rows, total, nil
}

// Catalog classification narrows already-authorized rows, never Page access.
// Keep this separate from pagination so folder counts use the same predicate.
func pageCatalogVisibilityRows(r *http.Request, rows []pageIndexRow) ([]pageIndexRow, error) {
	testItems, archived := r.URL.Query().Get("test_items"), r.URL.Query().Get("archived")
	for name, value := range map[string]string{"test_items": testItems, "archived": archived} {
		if value != "" && value != "exclude" && value != "only" && value != "include" {
			return nil, errors.New(name + " must be exclude, only or include")
		}
	}
	matches := func(value string, marked bool) bool {
		return value == "include" || value == "only" && marked || (value == "" || value == "exclude") && !marked
	}
	result := make([]pageIndexRow, 0, len(rows))
	for _, row := range rows {
		if matches(testItems, row.rec.CreatedByTest) && matches(archived, row.rec.ArchivedAt != "") {
			result = append(result, row)
		}
	}
	return result, nil
}
