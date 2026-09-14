package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectAuthoringRequiresEveryPanel(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "authoring")
	pagesSeedUser(t, h, ws, "grantee", "grantee@example.com", "MEMBER")
	// A live grant issued by an administrator reaches the page but must not
	// confer another crew's panel visibility.
	if _, err := h.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) SELECT id,'user','grantee','write',? FROM pages WHERE slug='health'`, admin); err != nil {
		t.Fatal(err)
	}
	for _, role := range []struct {
		name, user, role string
		want             int
	}{
		{"partial page owner", publisher, "MEMBER", 403},
		{"administrator", admin, "OWNER", 200},
		{"write grantee without crew membership", "grantee", "MEMBER", 403},
	} {
		for _, route := range []struct {
			name    string
			handler http.HandlerFunc
		}{
			{"draft", h.GetProject}, {"revision", h.GetProjectRevision}, {"export", h.Export},
		} {
			t.Run(role.name+"/"+route.name, func(t *testing.T) {
				r := pagesRequest(t, "GET", "/", ws, role.user, role.role, "")
				r.SetPathValue("slug", "health")
				r.SetPathValue("revision", "1")
				w := httptest.NewRecorder()
				route.handler(w, r)
				if w.Code != role.want {
					t.Fatalf("got %d: %s", w.Code, w.Body.String())
				}
				if role.want == 403 {
					withheldAssertBodyNeutral(t, route.name, w.Body.String())
				}
			})
		}
	}
	// Evidence reads keep the partial publisher's review usable without handing
	// out the document. The exact source digest remains the publication operand.
	for _, handler := range []http.HandlerFunc{h.GetProjectSource, h.GetProjectRevisionSource} {
		r := pagesRequest(t, "GET", "/", ws, publisher, "MEMBER", "")
		r.SetPathValue("slug", "health")
		r.SetPathValue("revision", "1")
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 200 {
			t.Fatalf("source: %d %s", w.Code, w.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["definition"]; exists {
			t.Fatal("source response included a document")
		}
		if len(body["project"]) == 0 || len(body["digest"]) == 0 {
			t.Fatal("source evidence missing")
		}
		withheldAssertBodyNeutral(t, "source evidence", w.Body.String())
	}
	// Removing the hidden panel from a replacement is not an escape hatch.
	before := reviewLiveDigest(t, h, "health")
	source := projectTestSource()
	definition := `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"name":"Flotila .201","slug":"health"},"spec":{"panels":[{"id":"zatizeni","schema":"metric.v1","title":"Zatizeni","owner":"crew/engine","producer":"script/load.sh","sla":"60s"}]}}`
	body, _ := json.Marshal(map[string]any{"expected_revision": 1, "project": source, "definition": json.RawMessage(definition)})
	r := pagesRequest(t, "PUT", "/", ws, publisher, "MEMBER", string(body))
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.PutProject(w, r)
	if w.Code != 403 {
		t.Fatalf("partial replacement: %d %s", w.Code, w.Body.String())
	}
	var revision int
	if err := h.db.QueryRow(`SELECT revision FROM page_project_drafts`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || reviewLiveDigest(t, h, "health") != before {
		t.Fatal("refused save changed the draft or live page")
	}
}

func TestProjectRevisionAuthorizationUsesArchivedOwner(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "old")
	// The live page and newest draft may now be fully visible. History must
	// still apply the archived document's owners, not today's panel rows.
	if _, err := h.db.Exec(`UPDATE page_project_drafts SET spec_json=replace(spec_json,'crew/lookout','crew/engine')`); err != nil {
		t.Fatal(err)
	}
	r := pagesRequest(t, "GET", "/", ws, publisher, "MEMBER", "")
	r.SetPathValue("slug", "health")
	r.SetPathValue("revision", "1")
	w := httptest.NewRecorder()
	h.GetProjectRevision(w, r)
	if w.Code != 403 {
		t.Fatalf("archived hidden document: %d %s", w.Code, w.Body.String())
	}
	withheldAssertBodyNeutral(t, "archive", w.Body.String())
}

func TestProjectDefinitionAgentVisibility(t *testing.T) {
	h, ws, admin, _ := withheldFixture(t)
	withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "agent")
	if _, err := h.db.Exec(`UPDATE pages SET owner_user_id=NULL,owner_crew_id='crew-engine' WHERE slug='health'`); err != nil {
		t.Fatal(err)
	}
	r := pagesRequest(t, "GET", "/", ws, admin, "OWNER", "")
	r = r.WithContext(context.WithValue(r.Context(), projectAgentKey{}, &projectAgent{WorkspaceID: ws, OwnerCrewID: "crew-engine", CrewID: "crew-engine", AgentID: "test-agent"}))
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.GetProject(w, r)
	if w.Code != 403 {
		t.Fatalf("agent crossed crew visibility: %d %s", w.Code, w.Body.String())
	}
	withheldAssertBodyNeutral(t, "agent draft", w.Body.String())
}

func TestPartialAuthorHistoryDoesNotOfferSourceRestore(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)
	withheldPublishableDraft(t, h, ws, admin, withheldDefinition("Jede to?", "crew/engine"), 0, "history")
	r := pagesRequest(t, "GET", "/", ws, publisher, "MEMBER", "")
	r.SetPathValue("slug", "health")
	w := httptest.NewRecorder()
	h.ProjectHistory(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body struct {
		Revisions []pageProjectRevision `json:"revisions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Revisions) != 1 || body.Revisions[0].Restorable {
		t.Fatalf("partial author offered a restore: %s", w.Body.String())
	}
}
