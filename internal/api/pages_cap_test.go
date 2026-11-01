package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

type capRefusal struct {
	Error    string `json:"error"`
	Message  string `json:"message"`
	Code     string `json:"code"`
	Kind     string `json:"kind"`
	Rejected bool   `json:"rejected"`
	Detail   struct {
		Existing int `json:"pages_existing"`
		Limit    int `json:"pages_limit"`
	} `json:"detail"`
}

// fillPages inserts plain pages until the workspace holds n, a share of them
// archived: archived pages still count toward the cap.
func fillPages(t *testing.T, h *PageHandler, ws, owner string, n int) {
	t.Helper()
	var have int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id=?`, ws).Scan(&have); err != nil {
		t.Fatal(err)
	}
	for i := have; i < n; i++ {
		archived := "NULL"
		if i%2 == 0 {
			archived = "'2026-01-01T00:00:00Z'"
		}
		if _, err := h.db.Exec(fmt.Sprintf(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at,archived_at) VALUES(?,?,?,?,?,'{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',%s)`, archived), fmt.Sprintf("fill-%d", i), ws, fmt.Sprintf("fill-%d", i), "Fill", owner); err != nil {
			t.Fatal(err)
		}
	}
}

func setPageCap(t *testing.T, h *PageHandler, n int) {
	t.Helper()
	if _, err := h.db.Exec(`UPDATE instance_page_limits SET max_pages_per_workspace=?`, n); err != nil {
		t.Fatal(err)
	}
}

func wantCapRefusal(t *testing.T, w *httptest.ResponseRecorder, limit int) {
	t.Helper()
	var got capRefusal
	if w.Code != http.StatusUnprocessableEntity || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("want 422 pages_limit, got %d %s", w.Code, w.Body)
	}
	if got.Code != "pages_limit" || got.Kind != "cap" || !got.Rejected || got.Detail.Limit != limit || got.Detail.Existing != limit {
		t.Fatalf("refusal %+v", got)
	}
	if got.Error != got.Message || !strings.Contains(got.Error, "reached its page limit") || !strings.Contains(got.Error, fmt.Sprintf("limit is %d", limit)) {
		t.Fatalf("message %q", got.Error)
	}
	if strings.Contains(got.Error, "quota") || strings.Contains(got.Error, "capacity") || strings.Contains(got.Error, "history") {
		t.Fatalf("storage wording leaked into the page limit message: %q", got.Error)
	}
}

// Every create path reads the configured cap (the default 100 or the admin's
// value) from the one function and answers with the same typed refusal.
func TestPageCapSameRefusalOnEveryCreatePath(t *testing.T) {
	for _, limit := range []int{pages.DefaultMaxPagesPerWorkspace, 10} {
		for _, path := range []string{"page", "design", "artifact_project"} {
			t.Run(fmt.Sprintf("%s/limit_%d", path, limit), func(t *testing.T) {
				switch path {
				case "page":
					h, _, _, ws, owner := newPagesFixture(t)
					setPageCap(t, h, limit)
					fillPages(t, h, ws, owner, limit)
					w := httptest.NewRecorder()
					h.Create(w, pagesRequest(t, "POST", "/api/v1/pages", ws, owner, "OWNER", pagesSpecBody("one-too-many")))
					wantCapRefusal(t, w, limit)
				case "design":
					h, ws, owner, call := designFixture(t)
					setPageCap(t, h, limit)
					fillPages(t, h, ws, owner, limit)
					wantCapRefusal(t, call(owner, "create", `{"slug":"board","name":"Board"}`), limit)
					var n int
					_ = h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE slug='board'`).Scan(&n)
					if n != 0 {
						t.Fatal("a refused Design left a Page behind")
					}
				case "artifact_project":
					h, ws, owner, call := artifactProjectFixture(t)
					setPageCap(t, h, limit)
					fillPages(t, h, ws, owner, limit)
					body := strings.TrimPrefix(artifactProjectBody(0, "One"), "{")
					wantCapRefusal(t, call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+body), limit)
				}
			})
		}
	}
}

// Below the cap creation still works, and lowering the setting below the
// current count refuses new pages without removing any.
func TestPageCapBelowCapCreatesAndLoweringNeverDeletes(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	fillPages(t, h, ws, owner, 12)
	setPageCap(t, h, 13)
	w := httptest.NewRecorder()
	h.Create(w, pagesRequest(t, "POST", "/api/v1/pages", ws, owner, "OWNER", pagesSpecBody("fits")))
	if w.Code != http.StatusCreated {
		t.Fatalf("under the cap: %d %s", w.Code, w.Body)
	}
	setPageCap(t, h, 10)
	w = httptest.NewRecorder()
	h.Create(w, pagesRequest(t, "POST", "/api/v1/pages", ws, owner, "OWNER", pagesSpecBody("over")))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over the lowered cap: %d %s", w.Code, w.Body)
	}
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id=?`, ws).Scan(&n)
	if n < 13 {
		t.Fatalf("lowering the cap deleted pages: %d", n)
	}
}

func TestPageCapCheckCapReadsSettingAndDefault(t *testing.T) {
	h, _, _, ws, owner := newPagesFixture(t)
	if got, err := pages.Limit(t.Context(), h.db); err != nil || got != 100 {
		t.Fatalf("default %d %v", got, err)
	}
	if err := pages.CheckCap(t.Context(), h.db, ws); err != nil {
		t.Fatal(err)
	}
	fillPages(t, h, ws, owner, 10)
	setPageCap(t, h, 10)
	err := pages.CheckCap(t.Context(), h.db, ws)
	if le, ok := pagesLimitFrom(err); !ok || le.Limit != 10 || le.Existing != 10 {
		t.Fatalf("at cap: %v", err)
	}
	// A different workspace is counted separately.
	if err := pages.CheckCap(t.Context(), h.db, "another-workspace"); err != nil {
		t.Fatal(err)
	}
}
