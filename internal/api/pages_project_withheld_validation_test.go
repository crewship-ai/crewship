package api

// Pages — candidate validation must not name what the caller may not read
// (counter-review of 6fdc2cef, §R4).
//
// PublishProject and CheckProject validate the whole candidate before anything
// else: every owner crew, producer, `call` routine and gate target must exist.
// Those errors named the panel, the action and the reference in a sentence —
// and they were written before the withheld check ever ran, so a Page owner
// who may not read one panel was told, in the publish response, that panel's
// id, its action id and the routine it calls, the moment that routine was
// deleted. The publication did not happen; the withholding did not either.
//
// Validation stays. The resolvers now return typed errors carrying the panel
// and its owner, and the two candidate paths decide with canSeePanel whether
// the sentence may be shown. The first test is the counter-review's probe,
// unchanged in intent; the rest walk every resolver branch, for every
// standing, on both endpoints.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestCounter6fdc2cefMissingWithheldRoutine is the reviewer's probe.
func TestCounter6fdc2cefMissingWithheldRoutine(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	build, revision := withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Bezi to?", "crew/engine"), 0, "probe")
	if _, err := h.db.Exec(`UPDATE pipelines SET deleted_at='2026-09-11T00:00:00Z' WHERE workspace_id=? AND slug='ops-secret'`, ws); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	w := publishCall(t, h, ws, publisher, "MEMBER", "health", pageProjectPublishRequest{
		BuildID: build, ExpectedRevision: revision, ExpectedPublication: &zero, ReviewedCode: true,
		ExpectedRoutineDigests: map[string]string{},
	})
	t.Logf("status=%d body=%s", w.Code, w.Body.String())
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403 for changed withheld panel, got %d", w.Code)
	}
	withheldAssertBodyNeutral(t, "publish with deleted withheld routine", w.Body.String())
}

// withheldReference is one resolver branch: how the definition declares the
// reference on a panel, and how the test makes it stop resolving.
type withheldReference struct {
	name string
	// declare returns the extra JSON fields for the target panel.
	declare func(target string) string
	// sever deletes the referenced row after the build.
	sever func(t *testing.T, h *PageHandler, ws, target string)
	// names is what the FULL sentence carries for the target panel, which a
	// caller entitled to it must still see.
	names func(target string) []string
}

func withheldReferences() []withheldReference {
	crewOf := map[string]string{"sluzby": "lookout", "zatizeni": "engine"}
	softDelete := func(t *testing.T, h *PageHandler, table, where string, args ...any) {
		t.Helper()
		if _, err := h.db.Exec(`UPDATE `+table+` SET deleted_at='2026-09-11T00:00:00Z' WHERE `+where, args...); err != nil {
			t.Fatal(err)
		}
	}
	return []withheldReference{
		{
			name: "call routine",
			declare: func(target string) string {
				return `"actions": [{"id": "act-` + target + `", "kind": "call", "label": "Run", "routine": "ops-` + target + `"}]`
			},
			sever: func(t *testing.T, h *PageHandler, ws, target string) {
				softDelete(t, h, "pipelines", "workspace_id=? AND slug=?", ws, "ops-"+target)
			},
			names: func(target string) []string { return []string{target, "act-" + target, "ops-" + target} },
		},
		{
			name:    "producer routine",
			declare: func(target string) string { return `"producer": "routine/feed-` + target + `"` },
			sever: func(t *testing.T, h *PageHandler, ws, target string) {
				softDelete(t, h, "pipelines", "workspace_id=? AND slug=?", ws, "feed-"+target)
			},
			names: func(target string) []string { return []string{target, "feed-" + target} },
		},
		{
			name:    "owner crew",
			declare: func(string) string { return "" },
			sever: func(t *testing.T, h *PageHandler, ws, target string) {
				softDelete(t, h, "crews", "workspace_id=? AND slug=?", ws, crewOf[target])
			},
			names: func(target string) []string { return []string{target, "crew/" + crewOf[target]} },
		},
		{
			name:    "on_failure crew",
			declare: func(string) string { return `"on_failure": {"issue": "crew/galley"}` },
			sever: func(t *testing.T, h *PageHandler, ws, _ string) {
				softDelete(t, h, "crews", "workspace_id=? AND slug='galley'", ws)
			},
			names: func(target string) []string { return []string{target, "crew/galley"} },
		},
		{
			name:    "wake gate crew",
			declare: func(string) string { return `"wake": [{"when": "any(state == \"critical\")", "agent": "crew/galley"}]` },
			sever: func(t *testing.T, h *PageHandler, ws, _ string) {
				softDelete(t, h, "crews", "workspace_id=? AND slug='galley'", ws)
			},
			names: func(target string) []string { return []string{target, "crew/galley"} },
		},
	}
}

// withheldReferenceDefinition is the fixture page as a draft document with
// `extra` declared on `target` and everything else identical to live, so the
// candidate changes nothing the withheld check would refuse on its own — the
// validation branch is what has to answer.
func withheldReferenceDefinition(target, extra string) string {
	panel := func(id, schema, title, owner, producer, sla string, span int) string {
		fields := `"id": "` + id + `", "schema": "` + schema + `", "title": "` + title + `", "owner": "` + owner + `", "producer": "` + producer + `", "sla": "` + sla + `", "span": ` + strconv.Itoa(span)
		if id == target && extra != "" {
			fields += ", " + extra
		}
		return "{" + fields + "}"
	}
	return `{"apiVersion": "crewship/v1", "kind": "Page", "metadata": {"name": "Flotila .201", "slug": "health"}, "spec": {"panels": [` +
		panel("sluzby", "status.v1", "Jede to?", "crew/lookout", "script/watch-services.sh", "30s", 8) + `, ` +
		panel("zatizeni", "status.v1", "Zatizeni", "crew/engine", "script/load.sh", "60s", 4) + `]}}`
}

// withheldReferenceFixture is withheldFixture plus everything the five
// branches reference: routines per panel, a producer routine per panel, and a
// third crew that is nobody's owner so it can be a gate target.
//
// The live page uses status.v1 on BOTH panels so a wake predicate over
// `state` is valid on either; withheldFixture's live page has metric.v1 on
// zatizeni, so this fixture re-creates it.
func withheldReferenceFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	h, ws, admin, publisher := withheldFixture(t)
	for _, q := range []string{
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-open', ?, 'ops-zatizeni', 'Open', '{"steps":[1]}', 'h2')`,
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-secret2', ?, 'ops-sluzby', 'Secret', '{"steps":[]}', 'h3')`,
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-feed-s', ?, 'feed-sluzby', 'Feed', '{"steps":[]}', 'h4')`,
		`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('pl-feed-z', ?, 'feed-zatizeni', 'Feed', '{"steps":[]}', 'h5')`,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-galley', ?, 'Galley', 'galley')`,
	} {
		if _, err := h.db.Exec(q, ws); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"slug": "health", "name": "Flotila .201", "panels": [
		{"id": "sluzby", "schema": "status.v1", "title": "Jede to?", "owner": "crew/lookout", "producer": "script/watch-services.sh", "sla_seconds": 30, "span": 8},
		{"id": "zatizeni", "schema": "status.v1", "title": "Zatizeni", "owner": "crew/engine", "producer": "script/load.sh", "sla_seconds": 60, "span": 4}]}`
	req := pagesRequest(t, http.MethodPatch, "/api/v1/pages/health", ws, admin, "OWNER", body)
	req.SetPathValue("slug", "health")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("reshape the live page: %d %s", rr.Code, rr.Body.String())
	}
	return h, ws, admin, publisher
}

// withheldCheckCall drives CheckProject for the same candidate.
func withheldCheckCall(t *testing.T, h *PageHandler, ws, actor, role, build string, revision int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"build_id": build, "expected_revision": revision})
	r := pagesRequest(t, "POST", "/", ws, actor, role, string(body))
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.CheckProject(w, r)
	return w
}

// TestPageCandidateValidationDoesNotNameWhatTheCallerMayNotRead walks every
// resolver branch on both panels, for the Page owner who may read one of them
// and for an administrator who may read both, on both candidate endpoints.
//
// Nothing in the candidate changes, so the withheld-change refusal is not what
// answers: the reference simply stops resolving after the build. That is the
// case ordering alone cannot cover.
func TestPageCandidateValidationDoesNotNameWhatTheCallerMayNotRead(t *testing.T) {
	for _, ref := range withheldReferences() {
		for _, target := range []string{"sluzby", "zatizeni"} {
			ref, target := ref, target
			t.Run(ref.name+" on "+target, func(t *testing.T) {
				h, ws, admin, publisher := withheldReferenceFixture(t)
				build, revision := withheldPublishableDraft(t, h, ws, admin, withheldReferenceDefinition(target, ref.declare(target)), 0, "ref")
				ref.sever(t, h, ws, target)
				zero := int64(0)
				publish := func(actor, role string) *httptest.ResponseRecorder {
					return publishCall(t, h, ws, actor, role, "health", pageProjectPublishRequest{
						BuildID: build, ExpectedRevision: revision, ExpectedPublication: &zero, ReviewedCode: true,
						ExpectedRoutineDigests: map[string]string{}})
				}
				calls := []struct {
					endpoint string
					call     func(actor, role string) *httptest.ResponseRecorder
				}{
					{"publish", publish},
					{"check", func(actor, role string) *httptest.ResponseRecorder {
						return withheldCheckCall(t, h, ws, actor, role, build, revision)
					}},
				}
				// The owner crew branch has no "visible member" case by
				// construction: once a crew is gone nobody is a member of it,
				// so its panel is withheld from every non-administrator.
				memberMayRead := target == "zatizeni" && ref.name != "owner crew"
				for _, c := range calls {
					w := c.call(publisher, "MEMBER")
					if memberMayRead {
						if w.Code != http.StatusBadRequest {
							t.Errorf("%s as the member, panel they may read: status = %d, want the full 400 — the fix must "+
								"not neutralise errors the caller is entitled to: %s", c.endpoint, w.Code, w.Body.String())
						}
						for _, name := range ref.names(target) {
							if !strings.Contains(w.Body.String(), name) {
								t.Errorf("%s as the member: the sentence for a panel they may read no longer names %q: %s",
									c.endpoint, name, w.Body.String())
							}
						}
					} else {
						if w.Code != http.StatusForbidden {
							t.Errorf("%s as the member, panel they may not read: status = %d, want 403: %s", c.endpoint, w.Code, w.Body.String())
						}
						for _, name := range append(ref.names(target), "sluzby", "zatizeni", "act-sluzby", "act-zatizeni", "Jede to?", "Zatizeni", "status.v1", "lookout", "galley") {
							if strings.Contains(w.Body.String(), name) {
								t.Errorf("%s as the member: the refusal names %q for a panel they may not read: %s",
									c.endpoint, name, w.Body.String())
							}
						}
						var body map[string]any
						if err := json.Unmarshal(w.Body.Bytes(), &body); err == nil {
							if _, ok := body["conflict"]; ok {
								t.Errorf("%s: the neutral refusal carries a conflict kind: %s", c.endpoint, w.Body.String())
							}
						}
					}
					// An administrator reads every panel and always gets the
					// full sentence: nobody may later "fix" this into an
					// obstacle for the one person who can repair the reference.
					w = c.call(admin, "OWNER")
					if w.Code != http.StatusBadRequest {
						t.Errorf("%s as an administrator: status = %d, want the full 400: %s", c.endpoint, w.Code, w.Body.String())
					}
					for _, name := range ref.names(target) {
						if !strings.Contains(w.Body.String(), name) {
							t.Errorf("%s as an administrator: the sentence no longer names %q: %s", c.endpoint, name, w.Body.String())
						}
					}
				}
				var published int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_project_publications`).Scan(&published); err != nil {
					t.Fatal(err)
				}
				if published != 0 {
					t.Errorf("%d publications were written by refused requests", published)
				}
			})
		}
	}
}

// TestPageReviewRoutineUnresolvedIsBuiltFromTheAuthorizedDocument pins the
// review-side half: `routine_unresolved` is derived from the authorized
// documents since the F2 fix, so it cannot name a withheld routine — and the
// counter-review asked for that to be asserted rather than assumed.
func TestPageReviewRoutineUnresolvedIsBuiltFromTheAuthorizedDocument(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "first")
	if _, err := h.db.Exec(`UPDATE pipelines SET deleted_at='2026-09-11T00:00:00Z' WHERE workspace_id=? AND slug='ops-secret'`, ws); err != nil {
		t.Fatal(err)
	}
	w, snapshot := reviewCall(t, h, ws, publisher, "MEMBER", "health")
	if message, ok := reviewBlockers(snapshot)[reviewBlockerRoutineUnresolved]; ok {
		t.Errorf("routine_unresolved raised for the member about a routine only a withheld panel calls: %q", message)
	}
	withheldAssertBodyNeutral(t, "the member's review after the withheld routine vanished", w.Body.String())
	_, adminSnapshot := reviewCall(t, h, ws, admin, "OWNER", "health")
	if message := reviewBlockers(adminSnapshot)[reviewBlockerRoutineUnresolved]; !strings.Contains(message, "ops-secret") {
		t.Errorf("the administrator was not told which routine stopped resolving: %q", message)
	}
}
