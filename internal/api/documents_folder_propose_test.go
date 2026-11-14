package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/documents"
)

// #5317 step 5.2: a folder's "propose" level (Can suggest) gives the agents of
// its owning crew a real proposal on a Docs Document, through the existing
// iteration path: the human's explicit iteration intent, the guard re-checked
// on every use, and acceptance only by a human Editor. A folder never adds a
// write, a commit or a checkpoint.

const folderProposeFolder = "folder-docs-propose"

// folderProposeDoc admits an iteration intent on a shared Document another
// person owns, filed in a folder of the agent's crew at level, with the flag
// on. The run's human edits it as an ordinary member (a user write grant).
// change runs before the human send.
func folderProposeDoc(t *testing.T, level string, change func(f *artifactPrincipalFixture, page string)) (*artifactPrincipalFixture, *documentAuthority, string, documents.IterationIntent, error) {
	t.Helper()
	return folderProposeDocBefore(t, level, nil, change)
}

// folderProposeDocBefore also runs before ahead of the human's chat receipt.
func folderProposeDocBefore(t *testing.T, level string, before func(f *artifactPrincipalFixture), change func(f *artifactPrincipalFixture, page string)) (*artifactPrincipalFixture, *documentAuthority, string, documents.IterationIntent, error) {
	t.Helper()
	f, _, token, intent, err := iterationFixtureTryBefore(t, before, func(f *artifactPrincipalFixture, intent *documents.IterationIntent) {
		mustExec(t, f.rig.db, `UPDATE documents SET schema_version=? WHERE page_id=?`, documents.FormattedSchema, intent.PageID)
		intent.Schema = documents.FormattedSchema
		agentAccessModesOn(t, f)
		mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0,owner_user_id=? WHERE id=?`, f.rig.adminID, intent.PageID)
		mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, intent.PageID, f.a.HumanUserID, f.rig.adminID)
		mustExec(t, f.rig.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at,agent_access) VALUES(?,?,?,?,?,'2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',?)`,
			folderProposeFolder, f.a.WorkspaceID, folderProposeFolder, "Docs", f.a.CrewID, level)
		mustExec(t, f.rig.db, `UPDATE pages SET folder_id=? WHERE id=?`, folderProposeFolder, intent.PageID)
		if change != nil {
			change(f, intent.PageID)
		}
	})
	h := &documentAuthority{pages: f.pages, store: documents.New(f.rig.db, documents.Options{})}
	return f, h, token, intent, err
}

func mustFolderProposeDoc(t *testing.T, change func(f *artifactPrincipalFixture, page string)) (*artifactPrincipalFixture, *documentAuthority, string, documents.IterationIntent) {
	t.Helper()
	f, h, token, intent, err := folderProposeDoc(t, folderAgentAccessPropose, change)
	if err != nil {
		t.Fatalf("folder propose: the human's iteration intent was refused: %v", err)
	}
	f.rig.db.SetMaxOpenConns(1)
	initializeDocumentDraftProjection(t, f, intent.PageID)
	return f, h, token, intent
}

func docHeadAndProposals(t *testing.T, f *artifactPrincipalFixture, page string) (int64, int) {
	t.Helper()
	var head int64
	var proposals int
	if err := f.rig.db.QueryRow(`SELECT head_rev,(SELECT count(*) FROM document_proposals WHERE page_id=documents.page_id) FROM documents WHERE page_id=?`, page).Scan(&head, &proposals); err != nil {
		t.Fatal(err)
	}
	return head, proposals
}

func folderProposeBody(t *testing.T, id string, rev int64) map[string]any {
	t.Helper()
	propose := documentDraftPayloadAt(t, documents.FormattedSchema, id, "initial", "agent edit", rev, documents.Lease{})
	for _, k := range []string{"kind", "epoch", "leaseToken", "leaseOwner", "snapshot", "update"} {
		delete(propose, k)
	}
	propose["target"] = "paragraph"
	return propose
}

func folderPropose(t *testing.T, f *artifactPrincipalFixture, h *documentAuthority, token, page, id string) *httptest.ResponseRecorder {
	t.Helper()
	head, _ := docHeadAndProposals(t, f, page)
	return callDocumentDraftAuthority(t, f, h, token, page, "propose", folderProposeBody(t, id, head))
}

func runAccessRows(t *testing.T, f *artifactPrincipalFixture) int {
	t.Helper()
	var n int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_run_access WHERE run_id=? AND source='standing'`, f.a.RunID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The folder's propose level gives a real proposal: stored, the head
// unchanged, audited as reach folder / operation propose, charged once to the
// run's read limit.
func TestFolderPropose_DocsProposalSucceeds(t *testing.T) {
	f, h, token, intent := mustFolderProposeDoc(t, nil)
	before, _ := docHeadAndProposals(t, f, intent.PageID)
	if w := folderPropose(t, f, h, token, intent.PageID, "folder-proposal"); w.Code != 200 {
		t.Fatalf("folder propose: %d %s", w.Code, w.Body)
	}
	after, proposals := docHeadAndProposals(t, f, intent.PageID)
	if after != before || proposals != 1 {
		t.Fatalf("head %d -> %d, proposals %d: want an unchanged head and one proposal", before, after, proposals)
	}
	var actor string
	if err := f.rig.db.QueryRow(`SELECT actor FROM document_proposals WHERE page_id=?`, intent.PageID).Scan(&actor); err != nil || actor != "agent:"+f.a.AgentID+":"+f.a.RunID {
		t.Fatalf("proposal author %q %v", actor, err)
	}
	wantKindAudit(t, f, map[string]string{intent.PageID + "/propose": runReachFolder})
	if n := runAccessRows(t, f); n != 1 {
		t.Fatalf("run read limit rows %d, want 1", n)
	}
	// A second proposal on the same Document is the same distinct read.
	if w := folderPropose(t, f, h, token, intent.PageID, "folder-proposal-2"); w.Code != 200 {
		t.Fatalf("second proposal: %d %s", w.Code, w.Body)
	}
	if n := runAccessRows(t, f); n != 1 {
		t.Fatalf("run read limit rows after a second proposal %d, want 1", n)
	}
	wantKindAudit(t, f, map[string]string{intent.PageID + "/propose": runReachFolder})
}

// Reads under the folder mandate are audited as folder reads.
func TestFolderPropose_ReadIsAuditedAsFolder(t *testing.T) {
	f, h, token, intent := mustFolderProposeDoc(t, nil)
	wantCode(t, callDocumentDraftAuthority(t, f, h, token, intent.PageID, "state", map[string]any{}), 200, "folder mandate read")
	wantKindAudit(t, f, map[string]string{intent.PageID + "/read": runReachFolder})
}

// Every refusal at the human send: nothing is admitted, so no proposal can follow.
func TestFolderPropose_RefusedAtTheHumanSend(t *testing.T) {
	cases := []struct {
		name   string
		level  string
		change func(t *testing.T, f *artifactPrincipalFixture, page string)
	}{
		{"folder read", folderAgentAccessRead, nil},
		{"folder none", folderAgentAccessNone, nil},
		{"folder of another crew", folderAgentAccessPropose, func(t *testing.T, f *artifactPrincipalFixture, page string) {
			other := seedCrewRow(t, f.rig.db, "crew-folder-propose-other", f.a.WorkspaceID, "Other", "folder-propose-other")
			mustExec(t, f.rig.db, `UPDATE page_folders SET owner_crew_id=? WHERE id=?`, other, folderProposeFolder)
		}},
		{"private page", folderAgentAccessPropose, func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
		}},
		{"owning crew deleted", folderAgentAccessPropose, func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE crews SET deleted_at='2026-11-14T00:00:00Z' WHERE id=?`, f.a.CrewID)
		}},
		{"human only reads the Document", folderAgentAccessPropose, func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_type='user' AND subject_id=?`, page, f.a.HumanUserID)
		}},
		{"flag off", folderAgentAccessPropose, func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='kinds-flag-on'`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, _, intent, err := folderProposeDoc(t, tc.level, func(f *artifactPrincipalFixture, page string) {
				if tc.change != nil {
					tc.change(t, f, page)
				}
			})
			if err == nil {
				t.Fatal("the human send admitted a folder proposal")
			}
			var intents, proposals int
			if err := f.rig.db.QueryRow(`SELECT (SELECT count(*) FROM document_iteration_intents WHERE page_id=?),(SELECT count(*) FROM document_proposals WHERE page_id=?)`, intent.PageID, intent.PageID).Scan(&intents, &proposals); err != nil {
				t.Fatal(err)
			}
			if intents != 0 || proposals != 0 {
				t.Fatalf("refused send left intents=%d proposals=%d", intents, proposals)
			}
		})
	}
}

// The guard is re-checked on every use: a change after the human's intent
// stops the next proposal.
func TestFolderPropose_RefusedAfterTheIntent(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, f *artifactPrincipalFixture, page string)
	}{
		{"folder downgraded to read", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE page_folders SET agent_access='read',acl_version=acl_version+1 WHERE id=?`, folderProposeFolder)
		}},
		{"folder downgraded to none", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE page_folders SET agent_access='none',acl_version=acl_version+1 WHERE id=?`, folderProposeFolder)
		}},
		{"agent moved to another crew", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			other := seedCrewRow(t, f.rig.db, "crew-folder-propose-moved", f.a.WorkspaceID, "Moved", "folder-propose-moved")
			mustExec(t, f.rig.db, `UPDATE agents SET crew_id=? WHERE id=?`, other, f.a.AgentID)
		}},
		{"folder handed to another crew", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			other := seedCrewRow(t, f.rig.db, "crew-folder-propose-handed", f.a.WorkspaceID, "Handed", "folder-propose-handed")
			mustExec(t, f.rig.db, `UPDATE page_folders SET owner_crew_id=? WHERE id=?`, other, folderProposeFolder)
		}},
		{"owning crew deleted", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE crews SET deleted_at='2026-11-14T00:00:00Z' WHERE id=?`, f.a.CrewID)
		}},
		{"page moved out of the folder", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET folder_id=NULL WHERE id=?`, page)
		}},
		{"page made private", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
		}},
		{"human lost Edit", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_type='user' AND subject_id=?`, page, f.a.HumanUserID)
		}},
		{"flag turned off", func(t *testing.T, f *artifactPrincipalFixture, page string) {
			mustExec(t, f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='kinds-flag-on'`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, h, token, intent := mustFolderProposeDoc(t, nil)
			tc.change(t, f, intent.PageID)
			if w := folderPropose(t, f, h, token, intent.PageID, "late-proposal"); w.Code == 200 {
				t.Fatalf("a proposal after %s: %s", tc.name, w.Body)
			}
			if _, proposals := docHeadAndProposals(t, f, intent.PageID); proposals != 0 {
				t.Fatalf("refused proposal stored %d rows", proposals)
			}
		})
	}
}

// A folder never adds a write: the run cannot commit or pin a checkpoint.
func TestFolderPropose_NeverCommitsOrCheckpoints(t *testing.T) {
	f, h, token, intent := mustFolderProposeDoc(t, nil)
	before, _ := docHeadAndProposals(t, f, intent.PageID)
	lease := documentDraftLease(t, f, h, token, intent.PageID)
	commit := documentDraftPayloadAt(t, documents.FormattedSchema, "folder-commit", "initial", "agent edit", before, lease)
	if w := callDocumentDraftAuthority(t, f, h, token, intent.PageID, "commit", commit); w.Code == 200 {
		t.Fatalf("a folder run committed: %s", w.Body)
	}
	checkpoint := map[string]any{"id": "folder-checkpoint", "fingerprint": "pin", "generation": 0, "ifRev": before, "epoch": lease.Epoch, "leaseToken": lease.Writer, "leaseOwner": lease.Owner, "snapshot": []byte("synthetic candidate")}
	if w := callDocumentDraftAuthority(t, f, h, token, intent.PageID, "checkpoint", checkpoint); w.Code == 200 {
		t.Fatalf("a folder run pinned a checkpoint: %s", w.Body)
	}
	after, _ := docHeadAndProposals(t, f, intent.PageID)
	var checkpoints int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM document_checkpoints WHERE page_id=? AND id='folder-checkpoint'`, intent.PageID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if after != before || checkpoints != 0 {
		t.Fatalf("head %d -> %d, checkpoints %d", before, after, checkpoints)
	}
	for _, r := range kindAuditRows(t, f) {
		if r.Operation == "write" {
			t.Fatalf("a refused write was audited: %+v", r)
		}
	}
}

// The per-run read limit counts the folder Document like any folder read.
func TestFolderPropose_ChargedToTheRunReadLimit(t *testing.T) {
	f, h, token, intent := mustFolderProposeDoc(t, nil)
	mustExec(t, f.rig.db, `UPDATE workspace_artifact_creation_policy SET run_read_limit=1 WHERE workspace_id=?`, f.a.WorkspaceID)
	// The run has already read another folder Document.
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "folder-propose-first", "folder-propose-first", "First", f.rig.adminID, "")
	mustExec(t, f.rig.db, `INSERT INTO artifact_run_access(run_id,workspace_id,page_id,source,created_at) VALUES(?,?,'folder-propose-first','standing','2026-11-14T00:00:00Z')`, f.a.RunID, f.a.WorkspaceID)
	w := folderPropose(t, f, h, token, intent.PageID, "over-limit")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "artifact_run_limit") {
		t.Fatalf("second folder Document past run_read_limit=1: %d %s", w.Code, w.Body)
	}
	if _, proposals := docHeadAndProposals(t, f, intent.PageID); proposals != 0 || runAccessRows(t, f) != 1 {
		t.Fatalf("refused proposal: proposals %d, access rows %d", proposals, runAccessRows(t, f))
	}
	if rows := kindAuditRows(t, f); len(rows) != 0 {
		t.Fatalf("refused proposal was audited: %+v", rows)
	}
	mustExec(t, f.rig.db, `UPDATE workspace_artifact_creation_policy SET run_read_limit=2 WHERE workspace_id=?`, f.a.WorkspaceID)
	if w := folderPropose(t, f, h, token, intent.PageID, "within-limit"); w.Code != 200 {
		t.Fatalf("within the limit: %d %s", w.Code, w.Body)
	}
	if n := runAccessRows(t, f); n != 2 {
		t.Fatalf("access rows %d, want 2", n)
	}
}

// Fail closed: an audit that cannot be written, or is silently dropped,
// refuses the proposal.
func TestFolderPropose_AuditFailsClosed(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER run_audit_abort BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.agent_run_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`,
		`CREATE TRIGGER run_audit_ignore BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.agent_run_accessed' BEGIN SELECT RAISE(IGNORE); END`,
	} {
		t.Run(strings.Fields(trigger)[2], func(t *testing.T) {
			f, h, token, intent := mustFolderProposeDoc(t, nil)
			mustExec(t, f.rig.db, trigger)
			if w := folderPropose(t, f, h, token, intent.PageID, "unaudited"); w.Code == 200 {
				t.Fatal("a proposal succeeded without its audit")
			}
			if _, proposals := docHeadAndProposals(t, f, intent.PageID); proposals != 0 || runAccessRows(t, f) != 0 {
				t.Fatalf("refused proposal left proposals=%d access rows=%d", proposals, runAccessRows(t, f))
			}
		})
	}
}

// humanDocumentAuthorize is the Docs authority callback for a signed-in person.
// The store must be called with the returned context: the callback reads the
// person from it.
func humanDocumentAuthorize(t *testing.T, f *artifactPrincipalFixture, h *documentAuthority, key documents.Key, user string) (context.Context, documents.Authorize) {
	t.Helper()
	session, err := sessions.NewDBStore(f.rig.db).Create(t.Context(), user, "folder-propose-"+user, "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxUser, &AuthUser{ID: user, SessionID: session.ID}))
	return r.Context(), h.authorize(r, key, true, false)
}

// acceptCandidate is the head projection with the proposal's target replaced
// by the proposed block.
func acceptCandidate(t *testing.T, f *artifactPrincipalFixture, page, proposal string) (json.RawMessage, string) {
	t.Helper()
	var target, proposed string
	var raw []byte
	if err := f.rig.db.QueryRow(`SELECT p.target,p.proposed_hash,d.projection FROM document_proposals p JOIN documents d ON d.page_id=p.page_id WHERE p.page_id=? AND p.id=?`, page, proposal).Scan(&target, &proposed, &raw); err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	base := ""
	for _, b := range blocks {
		if b["id"] == target {
			base, _ = b["hash"].(string)
			b["hash"] = proposed
		}
	}
	out, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return out, base
}

// Acceptance stays human: the agent cannot accept, a Viewer cannot, an Editor's
// commit accepts it, and a stale base is a conflict.
func TestFolderPropose_AcceptOnlyByAHumanEditor(t *testing.T) {
	f, h, token, intent := mustFolderProposeDoc(t, nil)
	key := documents.Key{WorkspaceID: f.a.WorkspaceID, PageID: intent.PageID}
	if w := folderPropose(t, f, h, token, intent.PageID, "to-accept"); w.Code != 200 {
		t.Fatalf("proposal: %d %s", w.Code, w.Body)
	}
	head, _ := docHeadAndProposals(t, f, intent.PageID)
	candidate, base := acceptCandidate(t, f, intent.PageID, "to-accept")

	agentAccept := documentDraftPayloadAt(t, documents.FormattedSchema, "agent-accept", "initial", "agent edit", head, documents.Lease{})
	agentAccept["proposal"] = "to-accept"
	if w := callDocumentDraftAuthority(t, f, h, token, intent.PageID, "commit", agentAccept); w.Code != http.StatusForbidden {
		t.Fatalf("the agent accepted its own proposal: %d %s", w.Code, w.Body)
	}

	mustExec(t, f.rig.db, `INSERT INTO users(id,email) VALUES('folder-propose-viewer','folder-propose-viewer@x')`)
	mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('folder-propose-viewer-m',?,'folder-propose-viewer','MEMBER')`, f.a.WorkspaceID)
	mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','folder-propose-viewer','read',?)`, intent.PageID, f.rig.adminID)

	editorCtx, editor := humanDocumentAuthorize(t, f, h, key, f.a.HumanUserID)
	lease, err := h.store.Acquire(editorCtx, key, "editor", editor)
	if err != nil {
		t.Fatalf("editor lease: %v", err)
	}
	generation := intent.Generation
	accept := func(id string, rev int64) documents.Commit {
		return documents.Commit{Lease: lease, ID: id, Fingerprint: "f-" + id, IfRev: rev, IfGeneration: &generation, Schema: documents.FormattedSchema, Update: []byte("u"), CandidateSnapshot: []byte("c"),
			Projection: candidate, IfHashes: map[string]string{"paragraph": base}, Kind: "human", Guard: documents.AcceptProposalGuard(key, "to-accept", id, candidate)}
	}
	viewerCtx, viewer := humanDocumentAuthorize(t, f, h, key, "folder-propose-viewer")
	if _, err := h.store.Commit(viewerCtx, key, accept("viewer-accept", head), viewer); !errors.Is(err, errDocumentAccess) {
		t.Fatalf("a Viewer accepted: %v", err)
	}
	if _, err := h.store.Commit(editorCtx, key, accept("stale-accept", head+1), editor); !errors.Is(err, documents.ErrConflict) {
		t.Fatalf("a stale base accepted: %v", err)
	}
	var status string
	if err := f.rig.db.QueryRow(`SELECT status FROM document_proposals WHERE id='to-accept'`).Scan(&status); err != nil || status != "open" {
		t.Fatalf("a refused accept decided the proposal: %q %v", status, err)
	}
	receipt, err := h.store.Commit(editorCtx, key, accept("editor-accept", head), editor)
	if err != nil || receipt.Kind != "human" || receipt.Rev != head+1 || receipt.Actor != "user:"+f.a.HumanUserID {
		t.Fatalf("editor accept: %+v %v", receipt, err)
	}
	if err := f.rig.db.QueryRow(`SELECT status FROM document_proposals WHERE id='to-accept'`).Scan(&status); err != nil || status != "accepted" {
		t.Fatalf("accepted proposal status %q %v", status, err)
	}
}

// Flag off: a folder's propose level is ignored, and a page grant's propose
// works as before with no agent-run audit.
func TestFolderPropose_FlagOffPageGrantUnchanged(t *testing.T) {
	f, h, token, intent := iterationFixtureFor(t, func(f *artifactPrincipalFixture, intent *documents.IterationIntent) {
		mustExec(t, f.rig.db, `UPDATE documents SET schema_version=? WHERE page_id=?`, documents.FormattedSchema, intent.PageID)
		intent.Schema = documents.FormattedSchema
		mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, intent.PageID)
		mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'propose',?)`, intent.PageID, f.a.AgentID, f.rig.adminID)
	})
	f.rig.db.SetMaxOpenConns(1)
	initializeDocumentDraftProjection(t, f, intent.PageID)
	if w := folderPropose(t, f, h, token, intent.PageID, "grant-proposal"); w.Code != 200 {
		t.Fatalf("flag off page-grant proposal: %d %s", w.Code, w.Body)
	}
	if rows := kindAuditRows(t, f); len(rows) != 0 {
		t.Fatalf("flag off audited: %+v", rows)
	}
	if n := runAccessRows(t, f); n != 0 {
		t.Fatalf("flag off charged the run read limit: %d", n)
	}
}

// With the flag on, a page-grant proposal is audited as reach consented,
// operation propose, and is not charged to the folder read limit.
func TestFolderPropose_PageGrantProposalAuditedAsPropose(t *testing.T) {
	f, h, token, intent := iterationFixtureFor(t, func(f *artifactPrincipalFixture, intent *documents.IterationIntent) {
		mustExec(t, f.rig.db, `UPDATE documents SET schema_version=? WHERE page_id=?`, documents.FormattedSchema, intent.PageID)
		intent.Schema = documents.FormattedSchema
		agentAccessModesOn(t, f)
		mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id=?`, intent.PageID)
		mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'propose',?)`, intent.PageID, f.a.AgentID, f.rig.adminID)
	})
	f.rig.db.SetMaxOpenConns(1)
	initializeDocumentDraftProjection(t, f, intent.PageID)
	if w := folderPropose(t, f, h, token, intent.PageID, "grant-proposal"); w.Code != 200 {
		t.Fatalf("page-grant proposal: %d %s", w.Code, w.Body)
	}
	wantKindAudit(t, f, map[string]string{intent.PageID + "/propose": runReachConsented})
	if n := runAccessRows(t, f); n != 0 {
		t.Fatalf("a page grant was charged to the run read limit: %d", n)
	}
}

// Owner amendment 2 on the existing page-grant path: with the flag on, Edit
// the human holds only through the ADMIN or OWNER role never passes to the
// agent. With the flag off the path is unchanged.
func TestFolderPropose_PageGrantRoleOnlyEditRefusedWithTheFlagOn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flag     bool
		userEdit bool
		ok       bool
	}{
		{"flag off, role-only Edit (unchanged)", false, false, true},
		{"flag on, role-only Edit", true, false, false},
		{"flag on, Edit as a member", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, err := iterationFixtureTryBefore(t, func(f *artifactPrincipalFixture) {
				mustExec(t, f.rig.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.a.WorkspaceID, f.a.HumanUserID)
			}, func(f *artifactPrincipalFixture, intent *documents.IterationIntent) {
				mustExec(t, f.rig.db, `UPDATE documents SET schema_version=? WHERE page_id=?`, documents.FormattedSchema, intent.PageID)
				intent.Schema = documents.FormattedSchema
				if tc.flag {
					agentAccessModesOn(t, f)
				}
				mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0,owner_user_id=? WHERE id=?`, f.rig.adminID, intent.PageID)
				mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'propose',?)`, intent.PageID, f.a.AgentID, f.rig.adminID)
				if tc.userEdit {
					mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, intent.PageID, f.a.HumanUserID, f.rig.adminID)
				}
			})
			if tc.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.ok && !errors.Is(err, errDocumentAccess) {
				t.Fatalf("role-only Edit passed to the agent: %v", err)
			}
		})
	}
}

// Owner amendment 2 on the folder branch: the human must edit the Document as
// an ordinary member. A workspace ADMIN or OWNER without a member's Edit
// (here: no grant, not the owner) hands nothing to the agent, while the same
// admin with a member's Edit does. The role is in place before the human's
// chat receipt, as it would be for a real admin.
func TestFolderPropose_RoleOnlyEditNeverPassesToTheAgent(t *testing.T) {
	for _, role := range []string{"ADMIN", "OWNER"} {
		for _, memberEdit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/member-edit=%v", role, memberEdit), func(t *testing.T) {
				f, _, _, intent, err := folderProposeDocBefore(t, folderAgentAccessPropose, func(f *artifactPrincipalFixture) {
					mustExec(t, f.rig.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, role, f.a.WorkspaceID, f.a.HumanUserID)
				}, func(f *artifactPrincipalFixture, page string) {
					if !memberEdit {
						mustExec(t, f.rig.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='user' AND subject_id=?`, page, f.a.HumanUserID)
					}
				})
				if memberEdit && err != nil {
					t.Fatalf("an admin with a member's Edit was refused: %v", err)
				}
				if !memberEdit {
					if !errors.Is(err, errDocumentAccess) {
						t.Fatalf("role-only Edit passed to the agent: %v", err)
					}
					var intents int
					if err := f.rig.db.QueryRow(`SELECT count(*) FROM document_iteration_intents WHERE page_id=?`, intent.PageID).Scan(&intents); err != nil || intents != 0 {
						t.Fatalf("intents %d %v", intents, err)
					}
				}
			})
		}
	}
}
