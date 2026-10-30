package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/design"
)

func TestDesignContinueCreatesOneAtomicVersionAndRetainsSource(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	var source []byte
	if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&source); err != nil {
		t.Fatal(err)
	}
	local, err := design.Run(t.Context(), design.Request{State: source, Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"pending","type":"text","x":0,"y":0,"width":100,"height":30,"text":"LOCAL_PENDING_COPY"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"slug": "continued", "name": "Continued canvas", "source_page_id": f.page, "expected_human": f.owner, "initial_state": base64.StdEncoding.EncodeToString(local.State)})
	r := f.request(t, f.owner, "POST", "", string(body))
	w := httptest.NewRecorder()
	f.h.CreateDesign(w, r)
	if w.Code != 201 {
		t.Fatalf("could not continue full history in a new Design: %d %s", w.Code, w.Body)
	}
	var created designCreateResponse
	if json.Unmarshal(w.Body.Bytes(), &created) != nil || created.PageID == f.page || created.Version != 1 {
		t.Fatal("not a new atomic Design", w.Body)
	}
	var stored, after []byte
	if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, created.PageID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	recovered, err := design.Run(t.Context(), design.Request{State: stored})
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := json.Marshal(recovered.Nodes)
	if !bytes.Contains(nodes, []byte("LOCAL_PENDING_COPY")) {
		t.Fatal("local canvas was not copied", string(nodes))
	}
	if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, after) || f.versions(t) != 1 {
		t.Fatal("continuing changed the original canvas/history")
	}
}

func TestDesignContinueRejectsIdentityPermissionAndInvalidStateWithoutOrphans(t *testing.T) {
	for _, mode := range []string{"identity", "read-only-source", "invalid-state", "revoked-at-fence", "late-credential", "publication-failure", "invalid-yjs", "oversized-state", "document-ignored", "document-rewritten", "late-document-credential", "late-document-create-authority"} {
		t.Run(mode, func(t *testing.T) {
			f := newDesignAuthorityFixture(t)
			var source []byte
			if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&source); err != nil {
				t.Fatal(err)
			}
			actor, human := f.owner, f.owner
			if mode == "identity" {
				human = "different-human"
			}
			if mode == "read-only-source" {
				actor = "continue-reader"
				human = actor
				f.addUser(t, actor)
				if w := f.grant(t, actor, "read"); w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
			}
			state := base64.StdEncoding.EncodeToString(source)
			if mode == "invalid-state" {
				state = "not base64"
			}
			if mode == "invalid-yjs" {
				state = base64.StdEncoding.EncodeToString([]byte{255})
			}
			if mode == "oversized-state" {
				state = base64.StdEncoding.EncodeToString(make([]byte, (4<<20)+1))
			}
			if mode == "publication-failure" {
				mustExec(t, f.h.db, `CREATE TRIGGER fail_continue_publication BEFORE INSERT ON artifact_versions WHEN NEW.page_id NOT IN(SELECT id FROM pages WHERE slug='board') BEGIN SELECT RAISE(ABORT,'copy publication unavailable'); END`)
			}
			if mode == "late-credential" {
				mustExec(t, f.h.db, `CREATE TRIGGER revoke_after_continue_page AFTER INSERT ON pages WHEN NEW.slug='not-created' BEGIN UPDATE cli_tokens SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'); END`)
			}
			if mode == "document-ignored" {
				mustExec(t, f.h.db, `CREATE TRIGGER ignore_continue_document BEFORE INSERT ON artifact_design_documents WHEN EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND slug='not-created') BEGIN SELECT RAISE(IGNORE); END`)
			}
			if mode == "document-rewritten" {
				mustExec(t, f.h.db, `CREATE TRIGGER rewrite_continue_document AFTER INSERT ON artifact_design_documents WHEN EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND slug='not-created') BEGIN UPDATE artifact_design_documents SET state=x'ff' WHERE page_id=NEW.page_id; END`)
			}
			if mode == "late-document-credential" {
				mustExec(t, f.h.db, `CREATE TRIGGER revoke_during_continue_document AFTER INSERT ON artifact_design_documents WHEN EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND slug='not-created') BEGIN UPDATE cli_tokens SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'); END`)
			}
			if mode == "late-document-create-authority" {
				mustExec(t, f.h.db, `CREATE TRIGGER remove_create_during_continue_document AFTER INSERT ON artifact_design_documents WHEN EXISTS(SELECT 1 FROM pages WHERE id=NEW.page_id AND slug='not-created') BEGIN UPDATE workspace_members SET role='VIEWER', capabilities='[]' WHERE workspace_id=NEW.workspace_id; END`)
			}
			f.token(t, actor)
			if mode == "revoked-at-fence" {
				mustExec(t, f.h.db, `CREATE TRIGGER revoke_continue AFTER UPDATE ON document_writer_fence BEGIN UPDATE cli_tokens SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'); END`)
			}
			body, _ := json.Marshal(map[string]any{"slug": "not-created", "name": "Refused", "source_page_id": f.page, "expected_human": human, "initial_state": state})
			r := f.request(t, actor, "POST", "", string(body))
			w := httptest.NewRecorder()
			f.h.CreateDesign(w, r)
			if w.Code < 400 {
				t.Fatal("invalid recovery copy accepted", w.Code, w.Body)
			}
			var n int
			if err := f.h.db.QueryRow(`SELECT count(*) FROM pages WHERE workspace_id=?`, f.workspace).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 || f.versions(t) != 1 {
				t.Fatal("refused recovery left new Pages or changed source history", n)
			}
			var after []byte
			if err := f.h.db.QueryRow(`SELECT state FROM artifact_design_documents WHERE page_id=?`, f.page).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(source, after) {
				t.Fatal("refusal altered source")
			}
		})
	}
}
