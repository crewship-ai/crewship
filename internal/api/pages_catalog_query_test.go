package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func catalogQuery(t *testing.T, h *PageHandler, ws, user, role, query string) ([]pageListWire, int, int) {
	t.Helper()
	w := httptest.NewRecorder()
	h.List(w, pagesRequest(t, "GET", "/api/v1/pages?"+query, ws, user, role, ""))
	if w.Code != 200 {
		return nil, 0, w.Code
	}
	var rows []pageListWire
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	total, err := strconv.Atoi(w.Header().Get("X-Total-Count"))
	if err != nil {
		t.Fatal(err)
	}
	return rows, total, w.Code
}

func TestPageCatalogQueryFiltersACLBeforeCountAndPage(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	pagesSeedUser(t, h, ws, "catalog-reader", "catalog-reader@example.test", "MEMBER")
	pagesJoinCrew(t, h, "catalog-reader", "crew-lookout")
	for _, slug := range []string{"a-budget", "b-other", "c-budget", "hidden-budget"} {
		row := pagesCreate(t, h, ws, owner, slug)
		execOrFatal(t, h.db, `UPDATE pages SET name=?,updated_at='2026-10-29T10:00:00Z',private_audience=? WHERE id=?`, slug, slug == "hidden-budget", row["id"])
	}
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('catalog-foreign','Foreign','catalog-foreign')`)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at,private_audience) VALUES('catalog-foreign-page','catalog-foreign','foreign-budget','Foreign budget',?,'{}','2026-10-29T10:00:00Z','2026-10-29T10:00:00Z',1)`, owner)
	rows, total, status := catalogQuery(t, h, ws, "catalog-reader", "MEMBER", "q=BUDGET&access=open&kind=page&version=unversioned&sort=name&limit=1&offset=1")
	if status != 200 || total != 2 || len(rows) != 1 || rows[0].Slug != "c-budget" {
		t.Fatalf("filtered authorized page status%d total%d rows%+v", status, total, rows)
	}
}

func TestPageCatalogQueryBadValues(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	for _, query := range []string{"empty=yes", "kind=unknown", "access=owner", "version=latest", "sort=oldest", "owner=You", "owner=user/", "owner=crew/a/b", "folder=", "kind=word,,sheet"} {
		if _, _, status := catalogQuery(t, h, ws, owner, "OWNER", query); status != 400 {
			t.Errorf("%s status%d want400", query, status)
		}
	}
}

func TestPageCatalogQueryProjectionAndStablePaging(t *testing.T) {
	// loadPageIndex returns rows ordered updated_at DESC, slug ASC; with equal
	// timestamps that is slug order, and every sort keeps it for ties.
	index := &pageIndex{rows: []pageIndexRow{
		{rec: pageRecord{Slug: "design", Name: "Same", OwnerUserID: "u", ArtifactContentType: "design"}},
		{rec: pageRecord{Slug: "legacy-sheet", Name: "Same", OwnerCrewID: "c", HasWorkbook: true}, ownerCrewSlug: "ops"},
		{rec: pageRecord{Slug: "legacy-word", Name: "Same", OwnerUserID: "u", HasDocument: true}},
		{rec: pageRecord{Slug: "native-word", Name: "Same", OwnerUserID: "u", ArtifactContentType: "wordcraft", ArtifactSharedVersion: 2}},
		{rec: pageRecord{Slug: "project", Name: "Same", OwnerUserID: "u", ArtifactContentType: "artifact_project"}},
	}}
	// Panel Pages are kind page; one with no panels and no storage is empty.
	pageRows := []pageIndexRow{
		{rec: pageRecord{Slug: "legacy-empty", Name: "Zz", OwnerUserID: "u"}},
		{rec: pageRecord{Slug: "panels", Name: "Zz", OwnerUserID: "u"}, panels: []*panelRecord{{}}},
	}
	index.rows = append(index.rows, pageRows...)
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"kind=word&kind=sheet&version=unversioned", []string{"legacy-sheet", "legacy-word"}},
		{"kind=word,sheet&version=shared&owner=me", []string{"native-word"}},
		{"version=unshared&kind=project", []string{"project"}},
		{"owner=crew/ops&owner=user/other", []string{"legacy-sheet"}},
		{"sort=kind", []string{"project", "legacy-empty", "panels", "legacy-word", "native-word", "legacy-sheet", "design"}},
		{"sort=name&limit=2&offset=2", []string{"legacy-word", "native-word"}},
		{"kind=design&version=unshared", []string{"design"}},
		{"empty=only", []string{"legacy-empty"}},
		{"empty=exclude&kind=page", []string{"panels"}},
		{"empty=include&kind=page", []string{"legacy-empty", "panels"}},
	} {
		req := httptest.NewRequest("GET", "/?"+tc.query, nil)
		rows, _, err := pageCatalogRows(req, index, "u")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, row := range rows {
			got = append(got, row.rec.Slug)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s got%v want%v", tc.query, got, tc.want)
		}
	}
}

func TestPageCatalogQuerySelectedKindCannotRevealPrivateLatest(t *testing.T) {
	h, _, owner, reader, call := commentGrantFixture(t, "comment")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	if w := call(owner, "POST", "three-screens/artifact/versions", dashboardBody(2, "sales")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var ws string
	if err := h.db.QueryRow(`SELECT workspace_id FROM pages WHERE slug='three-screens'`).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		user, kind string
		count      int
	}{{reader, "dashboard", 0}, {reader, "project", 1}, {owner, "dashboard", 1}} {
		rows, total, status := catalogQuery(t, h, ws, tc.user, "MEMBER", "kind="+tc.kind)
		if status != 200 || total != tc.count || len(rows) != tc.count {
			t.Fatalf("%s kind%s status%d total%d rows%+v", tc.user, tc.kind, status, total, rows)
		}
	}
}

func TestPageCatalogQueryCountDoesNotGrow(t *testing.T) {
	count := func(n int) int {
		h, counter, ws, owner := newCountingPagesFixture(t)
		for i := 0; i < n; i++ {
			pagesCreate(t, h, ws, owner, fmt.Sprintf("budget-%03d", i))
		}
		counter.reset()
		rows, total, status := catalogQuery(t, h, ws, owner, "OWNER", "q=budget&kind=page&access=open&version=unversioned&owner=me&folder=~unfiled&sort=name&limit=2")
		if status != 200 || total != n || len(rows) != 2 {
			t.Fatalf("status%d count%d rows%d", status, total, len(rows))
		}
		return counter.count()
	}
	small, large := count(3), count(40)
	if small == 0 || small != large {
		t.Fatalf("query growth small%d large%d", small, large)
	}
	t.Logf("filtered catalog statements:3pages=%d 40pages=%d", small, large)
}

func TestPageCatalogQueryStableWindowsAndLegacyPagination(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	for _, slug := range []string{"c", "a", "b"} {
		row := pagesCreate(t, h, ws, owner, slug)
		execOrFatal(t, h.db, `UPDATE pages SET name='Same',updated_at='2026-10-29T10:00:00Z' WHERE id=?`, row["id"])
	}
	seen := map[string]bool{}
	for offset, want := range []string{"a", "b", "c"} {
		rows, total, status := catalogQuery(t, h, ws, owner, "OWNER", fmt.Sprintf("sort=name&kind=page&kind=page&limit=1&offset=%d", offset))
		if status != 200 || total != 3 || len(rows) != 1 || rows[0].Slug != want || seen[rows[0].Slug] {
			t.Fatalf("window%d status%d total%d rows%+v", offset, status, total, rows)
		}
		seen[rows[0].Slug] = true
	}
	for _, query := range []string{"", "limit=invalid&offset=-5", "limit=99999"} {
		rows, total, status := catalogQuery(t, h, ws, owner, "OWNER", query)
		if status != 200 || total != 3 || len(rows) != 3 {
			t.Fatalf("legacy %s status%d total%d rows%d", query, status, total, len(rows))
		}
	}
}

func TestPageCatalogQueryLegacyKindsAndPlainText(t *testing.T) {
	index := &pageIndex{rows: []pageIndexRow{
		{rec: pageRecord{Slug: "legacy-application", Name: "kind:word report", HasProject: true, HasApplication: true}},
		{rec: pageRecord{Slug: "legacy-priority", HasWorkbook: true, HasSlides: true, HasDocument: true}},
		{rec: pageRecord{Slug: "native-priority", ArtifactContentType: "wordcraft", HasWorkbook: true}},
	}}
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"q=kind:word", "legacy-application"},
		{"kind=page&version=unversioned", "legacy-application"},
		{"kind=sheet&version=unversioned", "legacy-priority"},
		{"kind=word&version=unshared", "native-priority"},
	} {
		rows, total, err := pageCatalogRows(httptest.NewRequest("GET", "/?"+tc.query, nil), index, "u")
		if err != nil || total != 1 || len(rows) != 1 || rows[0].rec.Slug != tc.want {
			t.Fatalf("%s total%d rows%+v err%v", tc.query, total, rows, err)
		}
	}
}

func TestPageCatalogQueryPrivateCountAuditFailure(t *testing.T) {
	for _, query := range []string{
		"q=Created&limit=1&offset=999",
		"q=does-not-match&limit=1&offset=999",
		"kind=page&limit=1&offset=999",
		"owner=user/absent&limit=1&offset=999",
	} {
		t.Run(query, func(t *testing.T) {
			h, ws, _, page := privateMetadataFixture(t)
			execOrFatal(t, h.db, `CREATE TRIGGER reject_catalog_count_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			w := httptest.NewRecorder()
			h.List(w, pagesRequest(t, "GET", "/api/v1/pages?"+query, ws, "ptadmin", "OWNER", ""))
			if w.Code != 500 || w.Header().Get("X-Total-Count") != "" {
				t.Errorf("failed custody audit released count: status%d total%q body%s", w.Code, w.Header().Get("X-Total-Count"), w.Body)
			}
			for _, canary := range []string{"Created report", "created-doc", page} {
				if strings.Contains(w.Body.String(), canary) {
					t.Errorf("failed custody audit released metadata %q", canary)
				}
			}
		})
	}
}

func TestPageCatalogQueryDefaultHiddenRowsDoNotRequireCustodyAudit(t *testing.T) {
	for _, hidden := range []string{"archived", "test", "personal"} {
		t.Run(hidden, func(t *testing.T) {
			h, ws, _, page := privateMetadataFixture(t)
			query := "q=Created&limit=1&offset=999"
			switch hidden {
			case "archived":
				execOrFatal(t, h.db, `UPDATE pages SET archived_at='2026-10-29T10:00:00Z' WHERE id=?`, page)
			case "test":
				execOrFatal(t, h.db, `UPDATE pages SET created_by_test=1 WHERE id=?`, page)
			case "personal":
				query += "&audience=personal"
			}
			execOrFatal(t, h.db, `CREATE TRIGGER reject_hidden_catalog_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			w := httptest.NewRecorder()
			h.List(w, pagesRequest(t, "GET", "/api/v1/pages?"+query, ws, "ptadmin", "OWNER", ""))
			if w.Code != 200 || w.Header().Get("X-Total-Count") != "0" {
				t.Fatalf("hidden rows demanded custody audit: status%d total%q body%s", w.Code, w.Header().Get("X-Total-Count"), w.Body)
			}
		})
	}
}

func TestPageCatalogQueryPrivateAuditSingleConnection(t *testing.T) {
	h, ws, custodian, page := privateMetadataFixture(t)
	h.db.SetMaxOpenConns(1)
	req := pagesRequest(t, "GET", "/api/v1/pages?q=Created&limit=1&offset=999", ws, "ptadmin", "OWNER", "")
	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.List(w, req)
	if w.Code != 200 || w.Header().Get("X-Total-Count") != "1" {
		t.Fatalf("single-connection custody catalog status%d count%q body%s", w.Code, w.Header().Get("X-Total-Count"), w.Body)
	}
	if got := privateMetadataAuditCount(t, h, ws, page, "ptadmin", custodian); got != 1 {
		t.Fatalf("single-connection durable audit count%d", got)
	}
}

func TestPageCatalogQueryPrivateBatchLateAuditDeletion(t *testing.T) {
	for _, query := range []string{"q=Created&limit=1&offset=999", "q=does-not-match&limit=1&offset=999"} {
		t.Run(query, func(t *testing.T) {
			h, ws, _, first := privateMetadataFixture(t)
			execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) SELECT 'catalog-second-private',workspace_id,'catalog-second-private','Second private catalog',owner_user_id,spec_json,1 FROM pages WHERE id=?`, first)
			execOrFatal(t, h.db, `CREATE TRIGGER erase_previous_catalog_audit AFTER INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' AND (SELECT COUNT(*) FROM audit_logs WHERE action=NEW.action)>1 BEGIN DELETE FROM audit_logs WHERE action=NEW.action AND id<>NEW.id; END`)
			w := httptest.NewRecorder()
			h.List(w, pagesRequest(t, "GET", "/api/v1/pages?"+query, ws, "ptadmin", "OWNER", ""))
			if w.Code != 500 || w.Header().Get("X-Total-Count") != "" {
				t.Errorf("late-deleted audit released catalog: status%d total%q body%s", w.Code, w.Header().Get("X-Total-Count"), w.Body)
			}
			for _, canary := range []string{"created-doc", "Created report", first, "catalog-second-private", "Second private catalog"} {
				if strings.Contains(w.Body.String(), canary) {
					t.Errorf("failed batch released metadata %q", canary)
				}
			}
			var audits int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.private_owner_accessed'`).Scan(&audits); err != nil || audits != 0 {
				t.Fatalf("batch committed partial audits count%d err%v", audits, err)
			}
		})
	}
}

func TestPageCatalogQueryPrivateAuditFreshOwnerRole(t *testing.T) {
	h, ws, _, _ := privateMetadataFixture(t)
	execOrFatal(t, h.db, `CREATE TRIGGER demote_catalog_reader AFTER INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=NEW.workspace_id AND user_id=NEW.user_id; END`)
	w := httptest.NewRecorder()
	h.List(w, pagesRequest(t, "GET", "/api/v1/pages?q=does-not-match&limit=1&offset=999", ws, "ptadmin", "OWNER", ""))
	if w.Code != 404 || w.Header().Get("X-Total-Count") != "" {
		t.Fatalf("demoted custody reader received count: status%d total%q body%s", w.Code, w.Header().Get("X-Total-Count"), w.Body)
	}
	var audits int
	var role string
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.private_owner_accessed'`).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("demotion failed batch committed audit%d err%v", audits, err)
	}
	if err := h.db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id=? AND user_id='ptadmin'`, ws).Scan(&role); err != nil || role != "OWNER" {
		t.Fatalf("failed audit transaction retained demotion role%s err%v", role, err)
	}
}

func TestPageCatalogQueryWALWriterContention(t *testing.T) {
	for _, scope := range []string{"ordinary-owner", "private-custodian", "private-owner-custody"} {
		t.Run(scope, func(t *testing.T) {
			h, ws, custodian, page := privateMetadataFixture(t)
			user, role, slug := custodian, "MEMBER", "created-doc"
			if scope == "ordinary-owner" {
				var owner string
				h, _, _, ws, owner = newPagesFixture(t)
				row := pagesCreate(t, h, ws, owner, "ordinary-catalog")
				user, role, slug = owner, "OWNER", "ordinary-catalog"
				page = row["id"].(string)
			} else if scope == "private-owner-custody" {
				user, role = "ptadmin", "OWNER"
			}
			original := h.db
			h.db = privateMetadataBoundedPool(t, original)
			defer func() { h.db = original }()
			other, err := sql.Open("sqlite", reviewDBPath(t, h.db)+"?_pragma=busy_timeout(100)&_pragma=foreign_keys(ON)&_txlock=immediate")
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			tx, err := other.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			req := pagesRequest(t, "GET", "/api/v1/pages?q="+slug, ws, user, role, "")
			ctx, cancel := context.WithTimeout(req.Context(), 300*time.Millisecond)
			defer cancel()
			w := httptest.NewRecorder()
			h.List(w, req.WithContext(ctx))
			if scope != "private-owner-custody" {
				if w.Code != 200 || w.Header().Get("X-Total-Count") != "1" || !strings.Contains(w.Body.String(), slug) {
					t.Fatalf("reader took writer lock: status%d count%q body%s", w.Code, w.Header().Get("X-Total-Count"), w.Body)
				}
			} else {
				if w.Code != 500 || w.Header().Get("X-Total-Count") != "" {
					t.Fatalf("custody contention released count: status%d count%q body%s", w.Code, w.Header().Get("X-Total-Count"), w.Body)
				}
				for _, canary := range []string{"Created report", slug, page} {
					if strings.Contains(w.Body.String(), canary) {
						t.Errorf("custody contention released %q", canary)
					}
				}
			}
		})
	}
}

// pages.updated_at defaults to datetime('now') ("YYYY-MM-DD HH:MM:SS"), which
// is not RFC 3339. The filtered catalog must keep the index's SQL order for
// those rows instead of re-deriving it, or the legacy array would reorder.
func TestPageCatalogQueryKeepsIndexOrderForSQLiteTimestamps(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	for slug, updated := range map[string]string{"a-old": "2026-10-29 09:00:00", "b-new": "2026-10-29 11:00:00", "c-mid": "2026-10-29 10:00:00"} {
		row := pagesCreate(t, h, ws, owner, slug)
		execOrFatal(t, h.db, `UPDATE pages SET name='Same',updated_at=? WHERE id=?`, updated, row["id"])
	}
	want := []string{"b-new", "c-mid", "a-old"}
	for _, query := range []string{"", "sort=updated", "sort=name", "sort=kind", "limit=3"} {
		rows, total, status := catalogQuery(t, h, ws, owner, "OWNER", query)
		var got []string
		for _, row := range rows {
			got = append(got, row.Slug)
		}
		if status != 200 || total != 3 || !reflect.DeepEqual(got, want) {
			t.Errorf("%q status%d total%d got%v want%v", query, status, total, got, want)
		}
	}
}

// Rows in another workspace and private rows the caller cannot reach are not
// admitted by loadPageIndex, so no filter combination counts or returns them.
func TestPageCatalogQueryHiddenRowsNeverCounted(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	pagesSeedUser(t, h, ws, "catalog-member", "catalog-member@example.test", "MEMBER")
	pagesJoinCrew(t, h, "catalog-member", "crew-lookout")
	for _, slug := range []string{"open-plan", "private-plan"} {
		row := pagesCreate(t, h, ws, owner, slug)
		execOrFatal(t, h.db, `UPDATE pages SET name=?,updated_at='2026-10-29T10:00:00Z',private_audience=? WHERE id=?`, slug, slug == "private-plan", row["id"])
	}
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('catalog-other','Other','catalog-other')`)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at,private_audience) VALUES('catalog-other-open','catalog-other','other-plan','other-plan',?,'{}','2026-10-30T10:00:00Z','2026-10-30T10:00:00Z',0)`, owner)
	for _, tc := range []struct {
		query string
		total int
		rows  []string
	}{
		{"q=plan", 1, []string{"open-plan"}},
		{"q=plan&access=private", 0, nil},
		{"q=plan&access=open,private&limit=1&offset=1", 1, nil},
		{"q=private&sort=name&limit=5", 0, nil},
		{"q=other", 0, nil},
		{"folder=~unfiled&kind=page&sort=kind", 1, []string{"open-plan"}},
	} {
		rows, total, status := catalogQuery(t, h, ws, "catalog-member", "MEMBER", tc.query)
		var got []string
		for _, row := range rows {
			got = append(got, row.Slug)
		}
		if status != 200 || total != tc.total || !reflect.DeepEqual(got, tc.rows) {
			t.Errorf("%s status%d total%d rows%v, want total%d rows%v", tc.query, status, total, got, tc.total, tc.rows)
		}
	}
	// The owner reaches the private row, proving it exists and is filterable.
	if _, total, status := catalogQuery(t, h, ws, owner, "OWNER", "q=plan&access=private"); status != 200 || total != 1 {
		t.Fatalf("owner private total%d status%d", total, status)
	}
}
