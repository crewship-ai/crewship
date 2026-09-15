package api

// Pages — the page's own icon and colour (#2563).
//
// A page wears a crew icon and a palette colour the way a folder does, stored
// on the row and echoed by the index and the document. The PATCH follows the
// pointer rule every other field follows: omitted keeps, "" clears, a name the
// registry does not carry is refused by name and stores nothing.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pagesPatch(t *testing.T, h *PageHandler, wsID, userID, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesRequest(t, "PATCH", "/api/v1/pages/"+slug, wsID, userID, "OWNER", body)
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

func pagesAvatarOf(t *testing.T, doc map[string]any) (icon, color string) {
	t.Helper()
	i, ok := doc["icon"]
	if !ok {
		t.Fatalf("document carries no icon field at all: %s", mustPagesJSON(t, doc))
	}
	c, ok := doc["color"]
	if !ok {
		t.Fatalf("document carries no color field at all: %s", mustPagesJSON(t, doc))
	}
	icon, _ = i.(string)
	color, _ = c.(string)
	return icon, color
}

func TestPagesAvatar_CreateStoresItAndEveryReadEchoesIt(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	body := strings.Replace(pagesSpecBody("fleet-201"), `"name": "Flotila .201",`,
		`"name": "Flotila .201", "icon": "rocket", "color": "amber",`, 1)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	if icon, color := pagesAvatarOf(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201")); icon != "rocket" || color != "amber" {
		t.Errorf("document avatar = %q/%q, want rocket/amber", icon, color)
	}
	row := pagesListRows(t, h, wsID, userID, "OWNER")["fleet-201"]
	if icon, color := pagesAvatarOf(t, row); icon != "rocket" || color != "amber" {
		t.Errorf("index row avatar = %q/%q, want rocket/amber — the rail draws from the index", icon, color)
	}
}

func TestPagesAvatar_NoneIsAnEmptyStringNeverAnAbsentField(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if icon, color := pagesAvatarOf(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201")); icon != "" || color != "" {
		t.Errorf("a page created without an avatar reports %q/%q, want empty strings", icon, color)
	}
	if icon, color := pagesAvatarOf(t, pagesListRows(t, h, wsID, userID, "OWNER")["fleet-201"]); icon != "" || color != "" {
		t.Errorf("index row reports %q/%q for a page without an avatar, want empty strings", icon, color)
	}
}

func TestPagesAvatar_PatchFollowsThePointerRule(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	tests := []struct {
		name            string
		body            string
		wantIcon, wantC string
	}{
		{"set both", `{"icon": "rocket", "color": "amber"}`, "rocket", "amber"},
		{"omitted keeps: a rename leaves the avatar alone", `{"name": "Flotila .202"}`, "rocket", "amber"},
		{"one at a time: colour only", `{"color": "cyan"}`, "rocket", "cyan"},
		{"empty clears: icon", `{"icon": ""}`, "", "cyan"},
		{"empty clears: colour", `{"color": ""}`, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rr := pagesPatch(t, h, wsID, userID, "fleet-201", tc.body); rr.Code != http.StatusOK {
				t.Fatalf("patch %s: status = %d, body: %s", tc.body, rr.Code, rr.Body.String())
			}
			icon, color := pagesAvatarOf(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"))
			if icon != tc.wantIcon || color != tc.wantC {
				t.Errorf("after %s: avatar = %q/%q, want %q/%q", tc.body, icon, color, tc.wantIcon, tc.wantC)
			}
		})
	}
}

func TestPagesAvatar_RefusesNamesOutsideTheRegistryByName(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if rr := pagesPatch(t, h, wsID, userID, "fleet-201", `{"icon": "rocket", "color": "amber"}`); rr.Code != http.StatusOK {
		t.Fatalf("seed avatar: %d %s", rr.Code, rr.Body.String())
	}

	tests := []struct {
		name string
		body string
		want string
	}{
		{"unknown icon", `{"icon": "unicorn"}`, "icon unicorn is not a crew icon"},
		{"hex is not a palette key", `{"color": "#ff0000"}`, "color #ff0000 is not in the crew palette"},
		{"case matters", `{"icon": "Rocket"}`, "icon Rocket is not a crew icon"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := pagesPatch(t, h, wsID, userID, "fleet-201", tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("patch %s: status = %d, want 400, body: %s", tc.body, rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.want) {
				t.Errorf("refusal %q does not name the value: want %q", rr.Body.String(), tc.want)
			}
			// A refused write stores nothing, and the avatar it was refused for
			// is still the one from before.
			if icon, color := pagesAvatarOf(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201")); icon != "rocket" || color != "amber" {
				t.Errorf("a refused patch changed the avatar to %q/%q", icon, color)
			}
		})
	}

	// On create, the refusal happens before the row exists.
	body := strings.Replace(pagesSpecBody("fleet-202"), `"name": "Flotila .201",`, `"name": "Flotila .202", "icon": "unicorn",`, 1)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("create with a bad icon: status = %d, want 400, body: %s", rr.Code, rr.Body.String())
	}
	if _, ok := pagesListRows(t, h, wsID, userID, "OWNER")["fleet-202"]; ok {
		t.Errorf("a page refused for its icon was created anyway")
	}
}

func TestPagesAvatar_IsNotPartOfTheSpecOrItsVersions(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if rr := pagesPatch(t, h, wsID, userID, "fleet-201", `{"icon": "rocket", "color": "amber"}`); rr.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}
	var spec string
	if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE slug = 'fleet-201'`).Scan(&spec); err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if strings.Contains(spec, "rocket") || strings.Contains(spec, "amber") {
		t.Errorf("the avatar leaked into spec_json — it is a column, a rollback must not restore it: %s", spec)
	}

	// An avatar-only patch is not a save of the spec: no version row, and
	// updated_at — the spec's mtime, which the index orders on — stays put.
	var versions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_versions`).Scan(&versions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 1 {
		t.Errorf("page_versions holds %d rows after create + an avatar patch, want 1 — a colour click must not push an identical spec through the 50-version window", versions)
	}
	var updatedAt string
	if err := h.db.QueryRow(`SELECT updated_at FROM pages WHERE slug = 'fleet-201'`).Scan(&updatedAt); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	var createdAt string
	if err := h.db.QueryRow(`SELECT created_at FROM pages WHERE slug = 'fleet-201'`).Scan(&createdAt); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if updatedAt != createdAt {
		t.Errorf("updated_at moved to %s on an avatar-only patch (created %s); §10 makes it the spec's mtime", updatedAt, createdAt)
	}
	// And a rename still is a save: one more version, updated_at moves.
	clock.advance(time.Minute)
	if rr := pagesPatch(t, h, wsID, userID, "fleet-201", `{"name": "Flotila .202", "color": "cyan"}`); rr.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rr.Code, rr.Body.String())
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_versions`).Scan(&versions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 2 {
		t.Errorf("page_versions holds %d rows after a rename, want 2", versions)
	}
	if icon, color := pagesAvatarOf(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201")); icon != "rocket" || color != "cyan" {
		t.Errorf("avatar after a rename with a colour = %q/%q, want rocket/cyan", icon, color)
	}
}
