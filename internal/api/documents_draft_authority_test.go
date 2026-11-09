package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/documents"
)

// newDocumentDraftFixture is a Doc created before #5137 WP2, still at schema
// 3, so the legacy direct-drafting path stays covered unchanged.
func newDocumentDraftFixture(t *testing.T, drafting bool) (*artifactPrincipalFixture, *documentAuthority, string, documentCreationResult) {
	t.Helper()
	return newDocumentDraftFixtureAt(t, drafting, 3)
}

func newDocumentDraftFixtureAt(t *testing.T, drafting bool, schema int) (*artifactPrincipalFixture, *documentAuthority, string, documentCreationResult) {
	t.Helper()
	f, intent := documentCreationAdmissionFixture(t, drafting)
	if err := bindCreation(f, intent); err != nil {
		t.Fatal(err)
	}
	f.pages.artifactMasterToken = "synthetic-draft"
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	f.pages.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		return []byte(`{"schema":4,"snapshot":"c3ludGhldGlj","blocks":[{"id":"paragraph","hash":"synthetic"}]}`), 200, nil
	}
	w := httptest.NewRecorder()
	f.pages.CreateDocumentAgent(w, documentDraftRequest(f, token, http.MethodPost, documentCreationBody))
	if w.Code != 201 {
		t.Fatalf("valid creation control: status=%d %s", w.Code, w.Body.String())
	}
	var created documentCreationResult
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Schema != documents.LatestSchema {
		t.Fatalf("created %+v: %v", created, err)
	}
	if schema != documents.LatestSchema {
		mustExec(t, f.rig.db, `UPDATE documents SET schema_version=? WHERE page_id=?`, schema, created.PageID)
		mustExec(t, f.rig.db, `UPDATE document_checkpoints SET schema_version=? WHERE page_id=?`, schema, created.PageID)
		created.Schema = schema
	}
	return f, &documentAuthority{pages: f.pages, store: documents.New(f.rig.db, documents.Options{})}, token, created
}

func documentDraftRequest(f *artifactPrincipalFixture, token, method, body string) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/internal/documents/draft", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
	return r.WithContext(ctx)
}

func TestDocumentDraftCreationPinsOnlyExplicitPurpose(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-one-shot", true: "explicit-drafting"}[enabled], func(t *testing.T) {
			f, _, _, created := newDocumentDraftFixture(t, enabled)
			var generation, rev sql.NullInt64
			var page string
			if err := f.rig.db.QueryRow(`SELECT draft_generation,draft_rev,result_page_id FROM document_creation_consents WHERE run_id=?`, f.a.RunID).Scan(&generation, &rev, &page); err != nil {
				t.Fatal(err)
			}
			if page != created.PageID {
				t.Fatal("consent lost original created Page")
			}
			if enabled {
				if !generation.Valid || !rev.Valid || generation.Int64 != 0 || rev.Int64 != 0 {
					t.Fatalf("explicit creation did not pin initialized generation/revision: generation=%v revision=%v", generation, rev)
				}
			} else if generation.Valid || rev.Valid {
				t.Fatal("one-shot consent gained drafting scope")
			}
		})
	}
}

func TestDocumentDraftPrivateAuthorityAdmitsOnlyOriginalPurpose(t *testing.T) {
	for _, schema := range []int{3, documents.LatestSchema} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("schema-%d-%s", schema, map[bool]string{false: "legacy-one-shot", true: "explicit-drafting"}[enabled]), func(t *testing.T) {
				testDocumentDraftOriginalPurpose(t, schema, enabled)
			})
		}
	}
}

func testDocumentDraftOriginalPurpose(t *testing.T, schema int, enabled bool) {
	t.Helper()
	f, authority, token, created := newDocumentDraftFixtureAt(t, enabled, schema)
	// Isolate authority from the independently tested creation binding.
	if enabled {
		mustExec(t, f.rig.db, `UPDATE document_creation_consents SET draft_generation=0,draft_rev=0 WHERE run_id=?`, f.a.RunID)
	}
	key := documents.Key{WorkspaceID: f.a.WorkspaceID, PageID: created.PageID}
	session, err := sessions.NewDBStore(f.rig.db).Create(t.Context(), f.a.HumanUserID, "draft-control", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	human := httptest.NewRequest("POST", "/", nil)
	hctx := context.WithValue(human.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID, SessionID: session.ID})
	human = human.WithContext(hctx)
	if _, err = authority.store.Read(human.Context(), key, authority.authorize(human, key, false, false)); err != nil {
		t.Fatalf("current human control: %v", err)
	}
	request := documentDraftRequest(f, token, "POST", "")
	w := httptest.NewRecorder()
	checked, ok := f.pages.artifactAgentRequest(w, request)
	if !ok {
		t.Fatalf("original run control: %d", w.Code)
	}
	checked.SetPathValue("operation", "state")
	_, err = authority.store.Read(checked.Context(), key, authority.authorize(checked, key, false, false))
	if enabled && err != nil {
		t.Fatalf("explicit original run cannot read its own created draft: %v", err)
	}
	if !enabled && err == nil {
		t.Fatal("one-shot creation acquired continuation authority")
	}
}

// Direct drafting under creation consent works on schema 4 and on legacy
// schema 3 Docs; the scope carries the stored schema. Older schemas refuse.
func TestDocumentDraftScopeCarriesStoredSchema(t *testing.T) {
	for _, tc := range []struct {
		schema int
		ok     bool
	}{{4, true}, {3, true}, {2, false}} {
		t.Run(fmt.Sprintf("schema-%d", tc.schema), func(t *testing.T) {
			f, _, token, created := newDocumentDraftFixtureAt(t, true, documents.LatestSchema)
			mustExec(t, f.rig.db, `UPDATE documents SET schema_version=? WHERE page_id=?`, tc.schema, created.PageID)
			w := httptest.NewRecorder()
			checked, ok := f.pages.artifactAgentRequest(w, documentDraftRequest(f, token, "POST", ""))
			if !ok {
				t.Fatalf("original run control: %d", w.Code)
			}
			a, err := documentDraftRun(checked.Context())
			if err != nil {
				t.Fatal(err)
			}
			scope, err := f.pages.documentDraftScope(checked.Context(), a, created.PageID)
			if tc.ok && (err != nil || scope.Schema != tc.schema || scope.Propose) {
				t.Fatalf("scope=%+v err=%v", scope, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("schema %d gained drafting scope", tc.schema)
			}
		})
	}
}

// At the schema-4 default, creation-consent drafting still belongs only to
// the original run, chat and message: any other run of the same agent is
// refused, so a follow-up never gains direct write through creation.
func TestDocumentDraftSchemaFourScopeRefusesOtherRuns(t *testing.T) {
	f, _, token, created := newDocumentDraftFixtureAt(t, true, documents.LatestSchema)
	w := httptest.NewRecorder()
	checked, ok := f.pages.artifactAgentRequest(w, documentDraftRequest(f, token, "POST", ""))
	if !ok {
		t.Fatalf("original run control: %d", w.Code)
	}
	original, err := documentDraftRun(checked.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pages.documentDraftScope(checked.Context(), original, created.PageID); err != nil {
		t.Fatalf("original run control: %v", err)
	}
	for name, change := range map[string]func(*artifactRunAuthority){
		"other run":     func(a *artifactRunAuthority) { a.RunID = "followup-run" },
		"other message": func(a *artifactRunAuthority) { a.MessageID = "followup-message" },
		"other chat":    func(a *artifactRunAuthority) { a.ChatID = "followup-chat" },
		"other agent":   func(a *artifactRunAuthority) { a.AgentID = "other-agent" },
	} {
		other := *original
		change(&other)
		if _, err := f.pages.documentDraftScope(checked.Context(), &other, created.PageID); err == nil {
			t.Errorf("%s gained schema-4 drafting scope", name)
		}
	}
}
