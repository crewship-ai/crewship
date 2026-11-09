package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
)

// proposeIterationFixture admits a schema-4 (propose) mandate on the human's
// own personal private Document, optionally scoped to blocks.
func proposeIterationFixture(t *testing.T, blocks string) (*artifactPrincipalFixture, string, documents.IterationIntent) {
	t.Helper()
	f, _, token, intent := iterationFixtureFor(t, func(f *artifactPrincipalFixture, intent *documents.IterationIntent) {
		mustExec(t, f.rig.db, `UPDATE documents SET schema_version=4 WHERE page_id=?`, intent.PageID)
		intent.Schema = documents.ProposalSchema
		intent.Blocks = blocks
	})
	return f, token, intent
}

// The schema-4 mandate's grant gates (PRD §0.2/§0.3): the requester needs Edit,
// a shared Document needs a live agent write grant, and nothing reaches another
// person's private Document.
func TestDocumentIterationProposeMandateGates(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *artifactPrincipalFixture, page string)
		want  error
	}{
		{"own personal private document", func(*testing.T, *artifactPrincipalFixture, string) {}, nil},
		{"shared without agent grant", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, page)
		}, errDocumentAccess},
		{"shared with agent read grant", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, page)
			mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'read',?)`, page, f.a.AgentID, f.rig.adminID)
		}, errDocumentAccess},
		{"shared with agent write grant", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, page)
			mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'write',?)`, page, f.a.AgentID, f.rig.adminID)
		}, nil},
		{"crew write grant never reaches the agent", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, page)
			mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'crew',?,'write',?)`, page, f.a.CrewID, f.rig.adminID)
		}, errDocumentAccess},
		{"requester holds only a comment grant", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0,owner_user_id=? WHERE id=?`, f.rig.adminID, page)
			mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'comment',?)`, page, f.a.HumanUserID, f.rig.adminID)
			mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'write',?)`, page, f.a.AgentID, f.rig.adminID)
		}, errDocumentAccess},
		{"requester with write grant on another owner's document", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0,owner_user_id=? WHERE id=?`, f.rig.adminID, page)
			mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, page, f.a.HumanUserID, f.rig.adminID)
			mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'write',?)`, page, f.a.AgentID, f.rig.adminID)
		}, nil},
		{"another person's private document", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id=?`, f.rig.adminID, page)
			mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'write',?)`, page, f.a.AgentID, f.rig.adminID)
		}, errDocumentAccess},
		{"schema 3 document refuses a schema 4 mandate", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE documents SET schema_version=3 WHERE page_id=?`, page)
		}, documents.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, intent := proposeIterationFixture(t, "")
			tt.setup(t, f, intent.PageID)
			scope, _, err := f.pages.documentIterationPage(t.Context(), f.a.WorkspaceID, f.a.HumanUserID, f.a.AgentID, intent)
			if tt.want == nil {
				if err != nil || !scope.Propose || scope.PageID != intent.PageID {
					t.Fatalf("propose mandate refused: %+v %v", scope, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err=%v want %v", err, tt.want)
			}
		})
	}
}

// A schema-3 mandate on a shared Document is still refused as before.
func TestDocumentIterationSchemaThreeSharedUnchangedWithAgentWriteGrant(t *testing.T) {
	f, _, _, intent := iterationFixture(t)
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, intent.PageID)
	mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'write',?)`, intent.PageID, f.a.AgentID, f.rig.adminID)
	if _, _, err := f.pages.documentIterationPage(t.Context(), f.a.WorkspaceID, f.a.HumanUserID, f.a.AgentID, intent); !errors.Is(err, errDocumentIterationProposalUnavailable) {
		t.Fatalf("schema 3 shared mandate changed: %v", err)
	}
}

func TestDocumentIterationProposeScopeNeverCommits(t *testing.T) {
	f, _, intent := proposeIterationFixture(t, "")
	key := documents.Key{WorkspaceID: f.a.WorkspaceID, PageID: intent.PageID}
	actor := "agent:" + f.a.AgentID + ":" + f.a.RunID
	for _, op := range []string{"commit", "checkpoint"} {
		tx, err := f.rig.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		state := documents.State{Schema: 4, Generation: intent.Generation, HeadRev: intent.Rev}
		if err = f.pages.documentIterationGuard(&f.a, key, op, intent.Rev)(t.Context(), tx, actor, state); err == nil {
			t.Fatalf("propose mandate admitted %s", op)
		}
		_ = tx.Rollback()
	}
	tx, err := f.rig.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state := documents.State{Schema: 4, Generation: intent.Generation, HeadRev: intent.Rev}
	if err = f.pages.documentProposalGuard(&f.a, key, "anything", intent.Rev)(t.Context(), tx, actor, state); err != nil {
		t.Fatalf("whole-document mandate refused a proposal: %v", err)
	}
	if err = f.pages.documentProposalGuard(&f.a, key, "anything", intent.Rev)(t.Context(), tx, "agent:other:run", state); !errors.Is(err, documents.ErrConflict) {
		t.Fatalf("another actor proposed: %v", err)
	}
	if err = f.pages.documentProposalGuard(&f.a, key, "anything", intent.Rev)(t.Context(), tx, actor, documents.State{Schema: 3, Generation: intent.Generation, HeadRev: intent.Rev}); !errors.Is(err, documents.ErrConflict) {
		t.Fatalf("schema 3 state proposed: %v", err)
	}
}

func TestDocumentDraftProposeModeFiltersViewAndRefusesOutsideMandate(t *testing.T) {
	f, token, intent := proposeIterationFixture(t, "intro")
	hash := strings.Repeat("a", 64)
	var forwarded []string
	f.pages.documentCall = func(_ context.Context, _, _, page, op string, body any) ([]byte, int, error) {
		if page != intent.PageID {
			t.Fatal("wrong Page")
		}
		forwarded = append(forwarded, op)
		if op == "read" {
			return []byte(fmt.Sprintf(`{"schema":4,"rev":0,"generation":0,"epoch":"opaque","outline":[{"id":"intro","type":"paragraph","parent":null,"hash":%q,"text":"Intro"},{"id":"body","type":"paragraph","parent":null,"hash":%q,"text":"Private body"}]}`, hash, hash)), 200, nil
		}
		return []byte(`{"id":"prop-1","rev":0,"epoch":"opaque","kind":"proposal","actor":"agent:` + f.a.AgentID + `:` + f.a.RunID + `"}`), 200, nil
	}
	w := httptest.NewRecorder()
	f.pages.GetDocumentDraftAgent(w, documentDraftRequest(f, token, "GET", ""))
	wantCode(t, w, 200, "propose view")
	var view DocumentDraftView
	if json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Mode != "propose" || view.Schema != 4 || len(view.Targets) != 1 || view.Targets[0].ID != "intro" || strings.Contains(w.Body.String(), "Private body") {
		t.Fatalf("propose view: %s", w.Body.String())
	}
	request := func(op, target string, schema int) string {
		return fmt.Sprintf(`{"operation":%q,"generation":0,"request":{"id":"prop-1","schema":%d,"page_id":%q,"epoch":"opaque","ifRev":0,"target":%q,"ifHash":%q,"text":"New intro"}}`, op, schema, intent.PageID, target, hash)
	}
	tests := []struct {
		name string
		body string
		want int
	}{
		{"patch on a propose mandate", request("patch", "intro", 3), 403},
		{"target outside the mandate", request("propose", "body", 4), 403},
		{"propose with schema 3", request("propose", "intro", 3), 400},
		{"propose inside the mandate", request("propose", "intro", 4), 200},
	}
	for _, tt := range tests {
		forwarded = nil
		w = httptest.NewRecorder()
		f.pages.MutateDocumentDraftAgent(w, documentDraftRequest(f, token, "POST", tt.body))
		wantCode(t, w, tt.want, tt.name)
		if (tt.want == 200) != (len(forwarded) == 1 && forwarded[0] == "propose") {
			t.Fatalf("%s forwarded %v", tt.name, forwarded)
		}
	}
	// A proposal confirmation that is not the agent's own same-revision receipt is not trusted.
	f.pages.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		return []byte(`{"id":"prop-1","rev":1,"epoch":"opaque","kind":"agent","actor":"agent:` + f.a.AgentID + `:` + f.a.RunID + `"}`), 200, nil
	}
	w = httptest.NewRecorder()
	f.pages.MutateDocumentDraftAgent(w, documentDraftRequest(f, token, "POST", request("propose", "intro", 4)))
	wantCode(t, w, 502, "commit receipt for a proposal")
}

func TestDocumentDraftDirectMandateNeverProposes(t *testing.T) {
	f, _, token, intent := iterationFixture(t)
	f.pages.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		t.Fatal("refused proposal reached the engine")
		return nil, 0, nil
	}
	body := fmt.Sprintf(`{"operation":"propose","generation":0,"request":{"id":"prop-1","schema":4,"page_id":%q,"epoch":"opaque","ifRev":0,"target":"intro","ifHash":%q,"text":"x"}}`, intent.PageID, strings.Repeat("a", 64))
	w := httptest.NewRecorder()
	f.pages.MutateDocumentDraftAgent(w, documentDraftRequest(f, token, "POST", body))
	wantCode(t, w, 403, "direct mandate proposal")
}

func TestDocumentRichTreeSchemaFourFormatting(t *testing.T) {
	paragraph := func(marks string) string {
		return `{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"p1"},"content":[{"type":"text","text":"x","marks":` + marks + `}]}]}`
	}
	tests := []struct {
		name   string
		raw    string
		schema int
		ok     bool
	}{
		{"bold italic strike on 4", paragraph(`[{"type":"bold"},{"type":"italic"},{"type":"strike"}]`), 4, true},
		{"bold on 3", paragraph(`[{"type":"bold"}]`), 3, false},
		{"duplicate mark", paragraph(`[{"type":"bold"},{"type":"bold"}]`), 4, false},
		{"code with bold", paragraph(`[{"type":"code"},{"type":"bold"}]`), 4, false},
		{"unknown mark", paragraph(`[{"type":"underline"}]`), 4, false},
		{"code block language", `{"type":"doc","content":[{"type":"codeBlock","attrs":{"id":"c1","language":"go"},"content":[{"type":"text","text":"x"}]}]}`, 4, true},
		{"blockquote", `{"type":"doc","content":[{"type":"blockquote","attrs":{"id":"q1"},"content":[{"type":"paragraph","attrs":{"id":"p1"},"content":[{"type":"text","text":"x"}]}]}]}`, 4, true},
		{"task list", `{"type":"doc","content":[{"type":"taskList","attrs":{"id":"t1"},"content":[{"type":"taskItem","attrs":{"id":"i1","checked":true},"content":[{"type":"paragraph","attrs":{"id":"p1"},"content":[{"type":"text","text":"x"}]}]}]}]}`, 4, true},
		{"task list on 3", `{"type":"doc","content":[{"type":"taskList","attrs":{"id":"t1"},"content":[{"type":"taskItem","attrs":{"id":"i1","checked":true},"content":[{"type":"paragraph","attrs":{"id":"p1"},"content":[{"type":"text","text":"x"}]}]}]}]}`, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := documentRichTreeFor(json.RawMessage(tt.raw), tt.schema)
			if (err == nil) != tt.ok {
				t.Fatalf("err=%v want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestDocumentProposalDecisionIsClosed(t *testing.T) {
	f, _, _ := proposeIterationFixture(t, "")
	f.pages.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		t.Fatal("malformed decision reached the engine")
		return nil, 0, nil
	}
	for _, body := range []string{`{}`, `{"request_id":"r1"}`, `{"request_id":"r1","expected_rev":0,"kind":"agent"}`, `{"request_id":"bad id","expected_rev":0}`, `{"request_id":"r1","expected_rev":-1}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/pages/x/document/proposals/p1/accept", strings.NewReader(body))
		r.SetPathValue("proposal", "p1")
		f.pages.AcceptDocumentProposal(w, r)
		wantCode(t, w, 400, body)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/pages/x/document/proposals/p1/accept", strings.NewReader(`{"request_id":"r1","expected_rev":0}`))
	r.SetPathValue("proposal", "p1")
	r.Header.Set("Idempotency-Key", "r2")
	f.pages.AcceptDocumentProposal(w, r)
	wantCode(t, w, 400, "mismatched Idempotency-Key")
}

func TestIterationIntentBlocksRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"schema 4 with blocks", `{"submission_id":"s","page_id":"p","schema":4,"generation":0,"rev":0,"blocks":["a","b"]}`, true},
		{"schema 4 whole document", `{"submission_id":"s","page_id":"p","schema":4,"generation":0,"rev":0}`, true},
		{"schema 3 with blocks", `{"submission_id":"s","page_id":"p","schema":3,"generation":0,"rev":0,"blocks":["a"]}`, false},
		{"duplicate blocks", `{"submission_id":"s","page_id":"p","schema":4,"generation":0,"rev":0,"blocks":["a","a"]}`, false},
		{"empty blocks", `{"submission_id":"s","page_id":"p","schema":4,"generation":0,"rev":0,"blocks":[]}`, false},
		{"bad block id", `{"submission_id":"s","page_id":"p","schema":4,"generation":0,"rev":0,"blocks":["a b"]}`, false},
		{"schema 5", `{"submission_id":"s","page_id":"p","schema":5,"generation":0,"rev":0}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var intent documents.IterationIntent
			err := json.Unmarshal([]byte(tt.raw), &intent)
			if err == nil {
				err = intent.Validate()
			}
			if (err == nil) != tt.ok {
				t.Fatalf("err=%v want ok=%v", err, tt.ok)
			}
			if !tt.ok {
				return
			}
			again, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			var back documents.IterationIntent
			if json.Unmarshal(again, &back) != nil || back != intent {
				t.Fatalf("round trip: %s -> %+v", again, back)
			}
		})
	}
}
