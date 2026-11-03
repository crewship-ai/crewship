package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode"
)

// Filter only rows already admitted by loadPageIndex. OWNER custody is a
// separate audited read path and never enters another human's personal view.
func pageCatalogRows(r *http.Request, index *pageIndex, human string) ([]pageIndexRow, int, error) {
	rows, err := pageCatalogCandidates(r, index, human)
	if err != nil {
		return nil, 0, err
	}
	rows, err = pageCatalogQueryRows(r, index, rows, human)
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

// These are the admitted rows whose metadata the query predicates consume.
// Custody reads must audit this set even when a query or page window is empty.
func pageCatalogCandidates(r *http.Request, index *pageIndex, human string) ([]pageIndexRow, error) {
	audience := r.URL.Query().Get("audience")
	if audience != "" && audience != "all" && audience != "personal" {
		return nil, errors.New("audience must be all or personal")
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
	return pageCatalogVisibilityRows(r, rows)
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

// Facets consume only the already-authorized index and its selected metadata.
// No grant checks, content reads or per-row database queries belong here.
func pageCatalogQueryRows(r *http.Request, index *pageIndex, rows []pageIndexRow, human string) ([]pageIndexRow, error) {
	query := r.URL.Query()
	facets := make(map[string]map[string]bool)
	allowed := map[string]string{
		"kind":    "project,dashboard,page,word,sheet,slides,design",
		"access":  "private,open",
		"version": "shared,unshared,unversioned",
	}
	for _, name := range []string{"kind", "access", "version", "owner"} {
		values := map[string]bool{}
		for _, raw := range query[name] {
			for _, value := range strings.Split(raw, ",") {
				value = strings.TrimSpace(value)
				if name == "owner" {
					if value == "me" {
						value = "user/" + human
					}
					parts := strings.Split(value, "/")
					if len(parts) != 2 || (parts[0] != "user" && parts[0] != "crew") || !pageCatalogToken(parts[1]) {
						return nil, errors.New("owner must be me, user/ID or crew/slug")
					}
				} else if value == "" || !strings.Contains(","+allowed[name]+",", ","+value+",") {
					return nil, errors.New(name + " must be one of " + allowed[name])
				}
				values[value] = true
			}
		}
		facets[name] = values
	}
	folder := query.Get("folder")
	if query.Has("folder") && folder != "~unfiled" && !pageCatalogToken(folder) {
		return nil, errors.New("folder must be a slug or ~unfiled")
	}
	// Empty is the library's "nothing to open yet": no typed kind, no Word,
	// Sheet or Slides storage and no panels. Omitted means include.
	empty := query.Get("empty")
	if empty != "" && empty != "exclude" && empty != "only" && empty != "include" {
		return nil, errors.New("empty must be exclude, only or include")
	}
	sortBy := query.Get("sort")
	if sortBy != "" && sortBy != "updated" && sortBy != "name" && sortBy != "kind" {
		return nil, errors.New("sort must be updated, name or kind")
	}
	text := strings.ToLower(strings.TrimSpace(query.Get("q")))
	type selectedRow struct {
		row        pageIndexRow
		kind, name string
	}
	selected := make([]selectedRow, 0, len(rows))
	matches := func(name, value string) bool { return len(facets[name]) == 0 || facets[name][value] }
	for _, row := range rows {
		rec := &row.rec
		if text != "" && !strings.Contains(strings.ToLower(rec.Name), text) && !strings.Contains(strings.ToLower(rec.Slug), text) {
			continue
		}
		typedKind := artifactSelectedMetadataKind(rec.ArtifactContentType, rec.ArtifactDashboardDeclaration, true, rec.ArtifactSelectedManifest, rec.ArtifactSlidesDeclaration, rec.ArtifactSlidesEnabled)
		kind := typedKind
		if kind == "" {
			switch {
			case rec.HasWorkbook:
				kind = "sheet"
			case rec.HasSlides:
				kind = "slides"
			case rec.HasDocument:
				kind = "word"
			default:
				kind = "page"
			}
		}
		access := "open"
		if rec.PrivateAudience {
			access = "private"
		}
		version := "unversioned"
		if typedKind != "" {
			version = "unshared"
			if rec.ArtifactSharedVersion > 0 {
				version = "shared"
			}
		}
		owner := "user/" + rec.OwnerUserID
		if rec.OwnerCrewID != "" {
			owner = "crew/" + row.ownerCrewSlug
		}
		if !matches("kind", kind) || !matches("access", access) || !matches("version", version) || !matches("owner", owner) {
			continue
		}
		if isEmpty := typedKind == "" && !rec.HasWorkbook && !rec.HasSlides && !rec.HasDocument && len(row.panels) == 0; empty == "exclude" && isEmpty || empty == "only" && !isEmpty {
			continue
		}
		if query.Has("folder") {
			actual := "~unfiled"
			if value := index.folders[rec.FolderID]; value != nil {
				actual = value.Slug
			}
			if actual != folder {
				continue
			}
		}
		selected = append(selected, selectedRow{row: row, kind: kind, name: strings.ToLower(rec.Name)})
	}
	// Matches LIBRARY_KINDS order in components/features/artifacts-library/library-items.ts.
	rank := map[string]int{"project": 0, "dashboard": 1, "page": 2, "word": 3, "sheet": 4, "slides": 5, "design": 6}
	// loadPageIndex already orders by updated_at DESC, slug ASC in SQL, so a
	// stable sort keeps that order for "updated" and as the tie-break for name
	// and kind. Re-parsing updated_at here would misorder datetime('now') rows.
	sort.SliceStable(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		switch {
		case sortBy == "name" && a.name != b.name:
			return a.name < b.name
		case sortBy == "kind":
			return rank[a.kind] < rank[b.kind]
		}
		return false
	})
	result := make([]pageIndexRow, 0, len(selected))
	for _, entry := range selected {
		result = append(result, entry.row)
	}
	return result, nil
}

// IDs and slugs are canonical wire values, never labels or path expressions.
func pageCatalogToken(value string) bool {
	return value != "" && !strings.ContainsAny(value, "/,") && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
