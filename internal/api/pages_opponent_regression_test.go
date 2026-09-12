package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

const visiblePanelPatch = `{"panels":[{"id":"zatizeni","schema":"metric.v1","title":"Zatizeni","owner":"crew/engine","producer":"script/load.sh","sla_seconds":60,"span":4}]}`

func TestPageWholeDocumentLegacyAuthorization(t *testing.T) {
	for _, op := range []string{"patch-current", "patch-target", "rollback-current", "rollback-target", "metadata"} {
		t.Run(op, func(t *testing.T) {
			h, ws, admin, publisher := withheldFixture(t)
			call := func(user, role, method, body string, rollback bool) *httptest.ResponseRecorder {
				r := pagesRequest(t, method, "/", ws, user, role, body)
				r.SetPathValue("slug", "health")
				w := httptest.NewRecorder()
				if rollback {
					h.Rollback(w, r)
				} else {
					h.Update(w, r)
				}
				return w
			}
			if op == "patch-target" || op == "rollback-target" {
				if w := call(admin, "OWNER", "PATCH", visiblePanelPatch, false); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}
			before := reviewLiveDigest(t, h, "health")
			var versions, panels int
			if err := h.db.QueryRow(`SELECT count(*) FROM page_versions`).Scan(&versions); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT count(*) FROM page_panels`).Scan(&panels); err != nil {
				t.Fatal(err)
			}
			patch := visiblePanelPatch
			rollback := op == "rollback-current" || op == "rollback-target"
			if rollback {
				patch = `{"to":1}`
			}
			if op == "patch-target" {
				patch = withheldCreateBody
			}
			if op == "metadata" {
				patch = `{"name":"Renamed"}`
			}
			w := call(publisher, "MEMBER", "PATCH", patch, rollback)
			if op == "metadata" {
				if w.Code != 200 {
					t.Fatalf("metadata-only update: %d %s", w.Code, w.Body.String())
				}
				var response struct {
					Panels []map[string]any `json:"panels"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if len(response.Panels) != 2 || response.Panels[0]["sealed"] != true {
					t.Fatalf("metadata reply must retain only the sealed placeholder: %s", w.Body.String())
				}
				for _, key := range []string{"actions", "producer", "title", "schema", "owner"} {
					if _, exists := response.Panels[0][key]; exists {
						t.Fatalf("metadata response leaked %s", key)
					}
				}
				return
			}
			if w.Code != 403 {
				t.Fatalf("%s: got %d %s, want neutral 403", op, w.Code, w.Body.String())
			}
			withheldAssertBodyNeutral(t, op, w.Body.String())
			var afterVersions, afterPanels int
			if err := h.db.QueryRow(`SELECT count(*) FROM page_versions`).Scan(&afterVersions); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT count(*) FROM page_panels`).Scan(&afterPanels); err != nil {
				t.Fatal(err)
			}
			if reviewLiveDigest(t, h, "health") != before || afterVersions != versions || afterPanels != panels {
				t.Fatal("refused write changed live document, panels or history")
			}
		})
	}
}

func TestPageCheckOnlyReportsVisibleRoutines(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	build, rev := withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "opponent")
	for _, user := range []string{publisher, admin} {
		role := "MEMBER"
		if user == admin {
			role = "OWNER"
		}
		body, _ := json.Marshal(map[string]any{"build_id": build, "expected_revision": rev})
		r := pagesRequest(t, "POST", "/", ws, user, role, string(body))
		r.SetPathValue("slug", "health")
		w := httptest.NewRecorder()
		h.CheckProject(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var reply struct {
			Checks struct {
				Routines map[string]string `json:"routine_definitions"`
			} `json:"checks"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		_, secret := reply.Checks.Routines["ops-secret"]
		if secret != (user == admin) {
			t.Fatalf("routine visibility for %s: %s", user, w.Body.String())
		}
		if user == publisher {
			withheldAssertBodyNeutral(t, "check", w.Body.String())
		}
	}
}

func TestProjectSaveChecksHiddenDraftEvenWhenLiveAndReplacementAreVisible(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "draft-only")
	r := pagesRequest(t, "PATCH", "/", ws, admin, "OWNER", visiblePanelPatch)
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.Update(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var live, draft string
	if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE slug='health'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT spec_json FROM page_project_drafts`).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"expected_revision": 1, "project": projectTestSource(), "definition": json.RawMessage(live)})
	r = pagesRequest(t, "PUT", "/", ws, publisher, "MEMBER", string(body))
	r.SetPathValue("slug", "health")
	w = httptest.NewRecorder()
	h.PutProject(w, r)
	if w.Code != 403 {
		t.Fatalf("hidden draft replacement: %d %s", w.Code, w.Body.String())
	}
	withheldAssertBodyNeutral(t, "draft replacement", w.Body.String())
	var after string
	var revision int
	if err := h.db.QueryRow(`SELECT spec_json,revision FROM page_project_drafts`).Scan(&after, &revision); err != nil {
		t.Fatal(err)
	}
	if after != draft || revision != 1 {
		t.Fatal("refused save changed hidden draft")
	}
}

func TestPageMetadataDoesNotNameInvalidHiddenRoutine(t *testing.T) {
	h, ws, _, publisher := withheldFixture(t)
	if _, err := h.db.Exec(`DELETE FROM pipelines WHERE id='pl-secret'`); err != nil {
		t.Fatal(err)
	}
	r := pagesRequest(t, "PATCH", "/", ws, publisher, "MEMBER", `{"name":"Renamed"}`)
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.Update(w, r)
	if w.Code != 403 {
		t.Fatalf("hidden metadata validation: %d %s", w.Code, w.Body.String())
	}
	withheldAssertBodyNeutral(t, "metadata validation", w.Body.String())
}

func TestPageLegacyReplacementRejectsAnInterleavedHiddenPanel(t *testing.T) {
	for _, op := range []string{"panels", "metadata", "rollback"} {
		t.Run(op, func(t *testing.T) {
			h, ws, admin, publisher := withheldFixture(t)
			update := func(user, role, body string) *httptest.ResponseRecorder {
				r := pagesRequest(t, "PATCH", "/", ws, user, role, body)
				r.SetPathValue("slug", "health")
				w := httptest.NewRecorder()
				h.Update(w, r)
				return w
			}
			if w := update(admin, "OWNER", visiblePanelPatch); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			production := h.db
			hook := &reviewQueryHook{match: "SELECT panel_id, schema, owner_crew_id", nth: 1, run: func() {
				if w := update(admin, "OWNER", withheldCreateBody); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}}
			h.db = reviewHookedDB(t, reviewDBPath(t, production), hook)
			defer func() { h.db = production }()
			var w *httptest.ResponseRecorder
			if op == "rollback" {
				r := pagesRequest(t, "POST", "/", ws, publisher, "MEMBER", `{"to":2}`)
				r.SetPathValue("slug", "health")
				w = httptest.NewRecorder()
				h.Rollback(w, r)
			} else {
				body := visiblePanelPatch
				if op == "metadata" {
					body = `{"name":"Renamed"}`
				}
				w = update(publisher, "MEMBER", body)
			}
			if !hook.didFire() {
				t.Fatal("interleaving did not run")
			}
			if w.Code != 409 {
				t.Fatalf("stale %s: %d %s", op, w.Code, w.Body.String())
			}
			var panels int
			if err := h.db.QueryRow(`SELECT count(*) FROM page_panels WHERE panel_id='sluzby'`).Scan(&panels); err != nil {
				t.Fatal(err)
			}
			if panels != 1 {
				t.Fatal("concurrently added hidden panel was lost")
			}
		})
	}
}

func TestCurrentDocumentSnapshotHonorsCancellation(t *testing.T) {
	h, ws, _, _ := withheldFixture(t)
	rec, err := h.loadPage(t.Context(), ws, "health")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, ok := h.currentDocumentSnapshot(ctx, httptest.NewRecorder(), rec); ok {
		t.Fatal("canceled read returned a document")
	}
}

func TestPageRollbackResponseFiltersPanelsAddedAfterCommit(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	update := func(body string) {
		r := pagesRequest(t, "PATCH", "/", ws, admin, "OWNER", body)
		r.SetPathValue("slug", "health")
		w := httptest.NewRecorder()
		h.Update(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	update(visiblePanelPatch)
	production := h.db
	hook := &reviewQueryHook{match: "SELECT pp.id, pp.panel_id, pp.schema", nth: 1, run: func() {
		update(withheldCreateBody)
	}}
	h.db = reviewHookedDB(t, reviewDBPath(t, production), hook)
	defer func() { h.db = production }()
	r := pagesRequest(t, "POST", "/", ws, publisher, "MEMBER", `{"to":2}`)
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.Rollback(w, r)
	if !hook.didFire() || w.Code != 200 {
		t.Fatalf("rollback response interleaving: fired=%v status=%d %s", hook.didFire(), w.Code, w.Body.String())
	}
	var response struct {
		Page struct {
			Panels []map[string]any `json:"panels"`
		} `json:"page"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, panel := range response.Page.Panels {
		if panel["panel_id"] != "sluzby" && panel["id"] != "sluzby" {
			continue
		}
		if panel["sealed"] != true {
			t.Fatalf("concurrently added hidden panel is not sealed: %s", w.Body.String())
		}
		for _, key := range []string{"actions", "producer", "title", "schema", "owner"} {
			if _, exists := panel[key]; exists {
				t.Fatalf("rollback response leaked %s", key)
			}
		}
		return
	}
	t.Fatalf("interleaved hidden panel missing from response: %s", w.Body.String())
}
