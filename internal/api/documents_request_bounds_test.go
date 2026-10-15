package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/ws"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

func TestDocumentSelectedRequestSQLBounds(t *testing.T) {
	for _, scenario := range []string{"outside target", "outside range", "human edit fresh conditions", "changed unrelated block", "edit interval outside selection", "revoked", "success retry checkpoint"} {
		t.Run(scenario, func(t *testing.T) {
			h, workspace, user, page := documentRequestFixture(t)
			id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
			execOrFatal(t, h.db, `UPDATE document_requests SET status='running' WHERE id=?`, id)
			trusted, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", user, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "artifact-run", trusted); err != nil {
				t.Fatal(err)
			}
			a := &artifactRunAuthority{WorkspaceID: workspace, AgentID: "artifact-agent", CrewID: "artifact-crew", RunID: "artifact-run", HumanUserID: user, ChatID: "artifact-chat", MessageID: "artifact-message"}
			r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", "")
			r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, a))
			r.SetPathValue("operation", "commit")
			authority := &documentAuthority{pages: h}
			key := documents.Key{WorkspaceID: workspace, PageID: page}
			store := documents.New(h.db, documents.Options{})
			auth := authority.authorize(r, key, true, false)
			lease, err := store.Acquire(r.Context(), key, "test-owner", auth)
			if err != nil {
				t.Fatal(err)
			}
			semantic := &documentSemanticPatch{Target: "intro", Text: "HeNEW world", Edit: &documentSemanticEdit{From: 2, To: 5, Insert: "NEW"}}
			projection := json.RawMessage(`[{"id":"intro","hash":"new"},{"id":"other","hash":"h2"}]`)
			c := documents.Commit{Lease: lease, ID: "own-patch", Fingerprint: "stable", IfRev: 0, Schema: 2, Update: []byte{1}, CandidateSnapshot: []byte{1}, Projection: projection, IfHashes: map[string]string{"intro": "h1"}, Kind: "agent"}
			switch scenario {
			case "outside target":
				semantic.Target = "other"
				c.IfHashes = map[string]string{"other": "h2"}
			case "outside range":
				semantic.Text = "CHANGED ALL"
			case "human edit fresh conditions":
				execOrFatal(t, h.db, `UPDATE documents SET head_rev=1,projection=CAST('[{"id":"intro","hash":"human"},{"id":"other","hash":"h2"}]' AS BLOB) WHERE page_id=?`, page)
				c.IfRev = 1
				c.IfHashes["intro"] = "human"
			case "changed unrelated block":
				c.Projection = json.RawMessage(`[{"id":"intro","hash":"new"},{"id":"other","hash":"evil"}]`)
			case "edit interval outside selection":
				semantic.Edit = &documentSemanticEdit{From: 0, To: 5, Insert: "HeNEW"}
			case "revoked":
				execOrFatal(t, h.db, `DELETE FROM page_grants WHERE subject_type='agent'`)
			}
			c.Guard = h.documentRequestGuard(r.Context(), page, "commit", c.ID, c.IfRev, c.IfHashes, semantic, c.Projection)
			result, err := store.Commit(r.Context(), key, c, auth)
			if scenario != "success retry checkpoint" {
				if err == nil {
					t.Fatal("unauthorized new mutation succeeded")
				}
				var count int
				if err = h.db.QueryRow(`SELECT count(*) FROM document_receipts WHERE page_id=?`, page).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rejected commit retained receipt count=%d error=%v", count, err)
				}
				var head int64
				var checkpoints, successes int
				expectedHead := int64(0)
				if scenario == "human edit fresh conditions" {
					expectedHead = 1
				}
				if err = h.db.QueryRow(`SELECT head_rev FROM documents WHERE page_id=?`, page).Scan(&head); err != nil || head != expectedHead {
					t.Fatalf("rejected write changed head=%d expected=%d error=%v", head, expectedHead, err)
				}
				_ = h.db.QueryRow(`SELECT count(*) FROM document_checkpoints WHERE page_id=?`, page).Scan(&checkpoints)
				_ = h.db.QueryRow(`SELECT count(*) FROM document_requests WHERE page_id=? AND status='succeeded'`, page).Scan(&successes)
				if checkpoints != 0 || successes != 0 {
					t.Fatalf("rejection certified success checkpoints=%d cards=%d", checkpoints, successes)
				}
				return
			}
			if err != nil || result.Rev != 1 {
				t.Fatalf("commit=%+v %v", result, err)
			}
			retry, err := store.Commit(r.Context(), key, c, auth)
			if err != nil || retry != result {
				t.Fatalf("exact retry=%+v %v", retry, err)
			}
			fresh := c
			fresh.ID = "second-patch"
			fresh.IfRev = 1
			fresh.IfHashes = map[string]string{"intro": "new"}
			fresh.Guard = h.documentRequestGuard(r.Context(), page, "commit", fresh.ID, 1, fresh.IfHashes, semantic, fresh.Projection)
			if _, err = store.Commit(r.Context(), key, fresh, auth); !errors.Is(err, documents.ErrConflict) {
				t.Fatalf("fresh conditions bypass=%v", err)
			}
			// A fresh checkpoint cannot bless an intervening human revision, even with current preconditions.
			execOrFatal(t, h.db, `UPDATE documents SET head_rev=2 WHERE page_id=?`, page)
			badGuard := h.documentRequestGuard(r.Context(), page, "checkpoint", id+"-checkpoint", 2, nil, nil, nil)
			if _, err = store.CheckpointRequestGuarded(r.Context(), key, lease, id+"-checkpoint", "bad", []byte{1}, 2, nil, auth, badGuard); !errors.Is(err, documents.ErrConflict) {
				t.Fatalf("checkpoint blessed concurrent revision: %v", err)
			}
			execOrFatal(t, h.db, `UPDATE documents SET head_rev=1 WHERE page_id=?`, page)
			cpID := id + "-checkpoint"
			guard := h.documentRequestGuard(r.Context(), page, "checkpoint", cpID, 1, nil, nil, nil)
			cp, err := store.CheckpointRequestGuarded(r.Context(), key, lease, cpID, "cp", []byte{1}, 1, nil, auth, guard)
			if err != nil || cp.Rev != 1 {
				t.Fatalf("checkpoint=%+v %v", cp, err)
			}
			execOrFatal(t, h.db, `UPDATE documents SET head_rev=2 WHERE page_id=?`, page)
			if _, err = store.CheckpointRequestGuarded(r.Context(), key, lease, cpID, "cp", []byte{1}, 1, nil, auth, guard); err != nil {
				t.Fatalf("checkpoint exact retry after intervening write=%v", err)
			}
			if _, err = store.Commit(r.Context(), key, c, auth); err != nil {
				t.Fatalf("own exact retry after intervening write=%v", err)
			}
		})
	}
}

func TestDocumentRequestSnapshotImmutableAndAdmissionRace(t *testing.T) {
	h, workspace, user, page := documentRequestFixture(t)
	original := h.documentCall
	h.documentCall = func(ctx context.Context, ws, bearer, p, op string, data any) ([]byte, int, error) {
		raw, status, err := original(ctx, ws, bearer, p, op, data)
		execOrFatal(t, h.db, `UPDATE documents SET head_rev=1 WHERE page_id=?`, page)
		return raw, status, err
	}
	createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 409)
	var count int
	_ = h.db.QueryRow(`SELECT count(*) FROM document_requests`).Scan(&count)
	if count != 0 {
		t.Fatal("race admitted request")
	}
	execOrFatal(t, h.db, `UPDATE documents SET head_rev=0 WHERE page_id=?`, page)
	h.documentCall = original
	id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
	for _, statement := range []string{`UPDATE document_requests SET targets_json='[]' WHERE id=?`, `UPDATE document_requests SET base_rev=1 WHERE id=?`} {
		if _, err := h.db.Exec(statement, id); err == nil {
			t.Fatal("snapshot changed")
		}
	}
	execOrFatal(t, h.db, `UPDATE documents SET head_rev=1 WHERE page_id=?`, page)
	if retry := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202); retry != id {
		t.Fatal("admission retry changed ID")
	}
}

func TestDocumentTargetUTF16Boundaries(t *testing.T) {
	base := documentVerifiedTarget{DocumentSelectedTarget: DocumentSelectedTarget{ID: "p", Hash: "h", From: 1, To: 3, Quote: "😀"}, Text: "A😀B"}
	if !validDocumentTarget(base) {
		t.Fatal("valid surrogate pair rejected")
	}
	base.From = 2
	if validDocumentTarget(base) {
		t.Fatal("split surrogate accepted")
	}
}

// This is a deterministic runtime callback, not model-backed acceptance. It
// proves result cards depend on actual admitted-run Store receipts/checkpoints.
func TestDocumentSelectedRequestWorkerCertifiesOnlyBoundDurableResult(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "durable success", true: "concurrent edit fails"}[changed], func(t *testing.T) {
			h, workspace, user, page := documentRequestFixture(t)
			id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
			h.artifactDispatch = func(_ context.Context, _, _, prompt string, opts ws.ChatMessageOption) error {
				if !strings.Contains(prompt, `"page_id":"`+page+`"`) || !strings.Contains(prompt, `"base_rev":0`) || !strings.Contains(prompt, `"targets":[`) || !strings.Contains(prompt, `"quote":"llo"`) || !strings.Contains(prompt, `"text":"Hello world"`) {
					t.Fatal("trusted prompt lost immutable selection")
				}
				if err := bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "artifact-run", opts.HumanAuthority); err != nil {
					return err
				}
				a := &artifactRunAuthority{WorkspaceID: workspace, AgentID: "artifact-agent", CrewID: "artifact-crew", RunID: "artifact-run", HumanUserID: user, ChatID: "artifact-chat", MessageID: "artifact-message"}
				r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", "")
				r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, a))
				r.SetPathValue("operation", "commit")
				authority := &documentAuthority{pages: h}
				key := documents.Key{WorkspaceID: workspace, PageID: page}
				store := documents.New(h.db, documents.Options{})
				auth := authority.authorize(r, key, true, false)
				lease, err := store.Acquire(r.Context(), key, "worker-owner", auth)
				if err != nil {
					return err
				}
				if changed {
					execOrFatal(t, h.db, `UPDATE documents SET head_rev=1 WHERE page_id=?`, page)
				}
				c := documents.Commit{Lease: lease, ID: "worker-patch", Fingerprint: "worker", IfRev: 0, Schema: 2, Update: []byte{1}, CandidateSnapshot: []byte{1}, Projection: json.RawMessage(`[{"id":"intro","hash":"new"},{"id":"other","hash":"h2"}]`), IfHashes: map[string]string{"intro": "h1"}, Kind: "agent"}
				c.Guard = h.documentRequestGuard(r.Context(), page, "commit", c.ID, c.IfRev, c.IfHashes, &documentSemanticPatch{Target: "intro", Text: "HeNEW world", Edit: &documentSemanticEdit{From: 2, To: 5, Insert: "NEW"}}, c.Projection)
				if _, err = store.Commit(r.Context(), key, c, auth); err != nil {
					return nil
				} // A completion claim after refusal must not create a success card.
				_, err = store.CheckpointRequestGuarded(r.Context(), key, lease, id+"-checkpoint", "worker-cp", []byte{1}, 1, nil, auth, h.documentRequestGuard(r.Context(), page, "checkpoint", id+"-checkpoint", 1, nil, nil, nil))
				return err
			}
			h.dispatchNextDocumentRequest(t.Context())
			var status, checkpoint string
			var revision int64
			if err := h.db.QueryRow(`SELECT status,COALESCE(checkpoint_id,''),COALESCE(result_revision,0) FROM document_requests WHERE id=?`, id).Scan(&status, &checkpoint, &revision); err != nil {
				t.Fatal(err)
			}
			if changed {
				if status != "failed" || checkpoint != "" || revision != 0 {
					t.Fatalf("false success %s %s %d", status, checkpoint, revision)
				}
			} else if status != "succeeded" || checkpoint != id+"-checkpoint" || revision != 1 {
				t.Fatalf("durable result missing %s %s %d", status, checkpoint, revision)
			}
		})
	}
}

func TestDocumentSelectedRunCannotUseOtherPageGenericDelegation(t *testing.T) {
	h, workspace, user, page := documentRequestFixture(t)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) SELECT 'other-doc',workspace_id,'other-doc','Other',owner_user_id,spec_json FROM pages WHERE id=?`, page)
	execOrFatal(t, h.db, `INSERT INTO documents(workspace_id,page_id,schema_version,head_rev,snapshot,projection) VALUES(?,'other-doc',2,0,X'01',CAST('[{"id":"intro","hash":"h1"},{"id":"other","hash":"h2"}]' AS BLOB))`, workspace)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) SELECT 'other-doc',subject_type,subject_id,level,granted_by_user_id,granted_at FROM page_grants WHERE page_id=?`, page)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) SELECT 'other-doc',run_id,granted_by_user_id,expires_at,granted_at FROM page_artifact_run_delegations WHERE page_id=?`, page)
	a := &artifactRunAuthority{WorkspaceID: workspace, AgentID: "artifact-agent", CrewID: "artifact-crew", RunID: "artifact-run", HumanUserID: user, ChatID: "artifact-chat", MessageID: "artifact-message"}
	if err := h.checkArtifactRun(t.Context(), "other-doc", a); err != nil {
		t.Fatalf("other Page must have live generic grant: %v", err)
	}
	if err := h.checkDocumentRequestAuthority(t.Context(), "other-doc", a); err != nil {
		t.Fatalf("unbound generic grant unexpectedly denied: %v", err)
	}
	id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
	execOrFatal(t, h.db, `UPDATE document_requests SET status='running' WHERE id=?`, id)
	receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "artifact-run", receipt); err != nil {
		t.Fatal(err)
	}
	for _, reset := range []bool{false, true} {
		if reset {
			execOrFatal(t, h.db, `UPDATE document_requests SET run_id=NULL,status='failed',receipt_json='{}' WHERE id=?`, id)
		}
		if err = h.checkArtifactRunPrincipal(t.Context(), a); err != nil {
			t.Fatalf("principal fixture changed: %v", err)
		}
		if err = h.checkArtifactRun(t.Context(), "other-doc", a); err == nil {
			t.Fatal("shared cross-format authorization escaped Doc Page")
		}
		if err = h.checkArtifactRunDelegation(t.Context(), "other-doc", a); err == nil {
			t.Fatal("direct discussion delegation escaped Doc Page")
		}
		if err = h.checkScopedArtifactCreation(t.Context(), a); err == nil {
			t.Fatal("bound Doc run admitted standalone artifact creation")
		}

		if err = h.checkDocumentRequestAuthority(t.Context(), "other-doc", a); err == nil {
			t.Fatal("bound run escaped to another Page")
		}
		r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", "")
		r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, a))
		key := documents.Key{WorkspaceID: workspace, PageID: "other-doc"}
		store := documents.New(h.db, documents.Options{})
		authority := &documentAuthority{pages: h}
		r.SetPathValue("operation", "state")
		if _, err = store.Read(r.Context(), key, authority.authorize(r, key, false, false)); err == nil {
			t.Fatal("cross-Page read succeeded")
		}
		r.SetPathValue("operation", "commit")
		if _, err = store.Acquire(r.Context(), key, "escape-owner", authority.authorize(r, key, true, false)); err == nil {
			t.Fatal("cross-Page mutation authority succeeded")
		}
		var rev int64
		var receipts, successes int
		_ = h.db.QueryRow(`SELECT head_rev FROM documents WHERE page_id='other-doc'`).Scan(&rev)
		_ = h.db.QueryRow(`SELECT count(*) FROM document_receipts WHERE page_id='other-doc'`).Scan(&receipts)
		_ = h.db.QueryRow(`SELECT count(*) FROM document_requests WHERE status='succeeded'`).Scan(&successes)
		if rev != 0 || receipts != 0 || successes != 0 {
			t.Fatalf("scope rejection mutated history %d %d %d", rev, receipts, successes)
		}
	}
	// Physical request deletion cascades its origin delegation. It must revoke
	// the complete retained principal, including the other Page's generic grant.
	execOrFatal(t, h.db, `DELETE FROM document_requests WHERE id=?`, id)
	var retained int
	if err = h.db.QueryRow(`SELECT count(*) FROM artifact_run_principals WHERE run_id='artifact-run'`).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("principal audit row lost=%d %v", retained, err)
	}
	if err = h.checkArtifactRun(t.Context(), "other-doc", a); err == nil {
		t.Fatal("request deletion restored generic authority")
	}
	// Host recapture of the same run cannot silently renew its revoked expiry.
	if err = captureArtifactChatPrincipal(httptest.NewRequest("POST", "/", nil), h.db, "artifact-run", workspace, "artifact-agent", "artifact-crew", "artifact-chat", "artifact-message", receipt); err != nil {
		t.Fatal(err)
	}
	if err = h.checkArtifactRunPrincipal(t.Context(), a); err == nil {
		t.Fatal("recapture revived deleted request principal")
	}
	r := pagesGrantRequest(t, "POST", "/", workspace, user, "OWNER", "")
	r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, a))
	r.SetPathValue("operation", "state")
	key := documents.Key{WorkspaceID: workspace, PageID: "other-doc"}
	store := documents.New(h.db, documents.Options{})
	authority := &documentAuthority{pages: h}
	if _, err = store.Read(r.Context(), key, authority.authorize(r, key, false, false)); err == nil {
		t.Fatal("post-delete cross-Page read succeeded")
	}
	r.SetPathValue("operation", "commit")
	if _, err = store.Acquire(r.Context(), key, "deleted-escape", authority.authorize(r, key, true, false)); err == nil {
		t.Fatal("post-delete cross-Page mutation authority succeeded")
	}
}

func TestDocumentSelectedRunCannotPublishDifferentFormatPage(t *testing.T) {
	h, workspace, user, page := documentRequestFixture(t)
	artifactExpect(t, artifactCreationOptIn(t, h, workspace, user, "POST"), 201)
	unbound := &artifactRunAuthority{WorkspaceID: workspace, AgentID: "artifact-agent", CrewID: "artifact-crew", RunID: "artifact-run", HumanUserID: user, ChatID: "artifact-chat", MessageID: "artifact-message"}
	if err := h.checkScopedArtifactCreation(t.Context(), unbound); err != nil {
		t.Fatalf("generic standalone creation fixture invalid: %v", err)
	}
	for _, other := range []string{"other-html", "other-board"} {
		execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) SELECT ?,workspace_id,?, ?,owner_user_id,spec_json FROM pages WHERE id=?`, other, other, other, page)
		execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) SELECT ?,subject_type,subject_id,level,granted_by_user_id,granted_at FROM page_grants WHERE page_id=?`, other, page)
		execOrFatal(t, h.db, `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) SELECT ?,run_id,granted_by_user_id,expires_at,granted_at FROM page_artifact_run_delegations WHERE page_id=?`, other, page)
	}
	execOrFatal(t, h.db, `INSERT INTO page_boards(page_id,seq,format) VALUES('other-board',0,'crewship-board-excalidraw-0.18.1-v1')`)
	id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
	execOrFatal(t, h.db, `UPDATE document_requests SET status='running' WHERE id=?`, id)
	receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, "artifact-chat", user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = bindDocumentRequestRun(httptest.NewRequest("POST", "/", nil), h.db, id, "artifact-run", receipt); err != nil {
		t.Fatal(err)
	}
	a := &artifactRunAuthority{WorkspaceID: workspace, AgentID: "artifact-agent", CrewID: "artifact-crew", RunID: "artifact-run", HumanUserID: user, ChatID: "artifact-chat", MessageID: "artifact-message"}
	if err = h.checkArtifactRun(t.Context(), "other-board", a); err == nil {
		t.Fatal("Doc-bound run admitted Board page")
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, workspace, "artifact-crew"), workspace, "artifact-agent", "artifact-run")
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"html":"<p>escaped</p>","base_rev":0,"mutation_id":"escape"}`))
	r.SetPathValue("slug", "other-html")
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, "artifact-crew")
	w := httptest.NewRecorder()
	h.PublishArtifactAgent(w, r.WithContext(ctx))
	artifactExpect(t, w, 403)
	artifactExpect(t, artifactCreateAgent(t, h, workspace, token, creationBody), 403)
	var creations int
	if err = h.db.QueryRow(`SELECT count(*) FROM page_artifact_creations WHERE run_id='artifact-run'`).Scan(&creations); err != nil || creations != 0 {
		t.Fatalf("boundrun createdotherPage=%d error=%v", creations, err)
	}
	var revisions int
	if err = h.db.QueryRow(`SELECT count(*) FROM page_artifact_revisions WHERE page_id='other-html'`).Scan(&revisions); err != nil || revisions != 0 {
		t.Fatalf("crossformat rejection wrote revisions=%d error=%v", revisions, err)
	}
}
