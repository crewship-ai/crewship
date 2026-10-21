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
