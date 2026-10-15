package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/ws"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

func TestDocumentSelectedRequestSQLBounds(t *testing.T) {
	for _, scenario := range []string{"outside target", "outside range", "human edit fresh conditions", "changed unrelated block", "revoked", "success retry checkpoint"} {
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
			semantic := &documentSemanticPatch{Target: "intro", Text: "HeNEW world"}
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
				if !strings.Contains(prompt, `"quote":"llo"`) || !strings.Contains(prompt, `"text":"Hello world"`) {
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
				c.Guard = h.documentRequestGuard(r.Context(), page, "commit", c.ID, c.IfRev, c.IfHashes, &documentSemanticPatch{Target: "intro", Text: "HeNEW world"}, c.Projection)
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
