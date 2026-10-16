package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func requestResultCall(t *testing.T, h *PageHandler, workspace, user, kind, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesGrantRequest(t, "GET", "/", workspace, user, "MEMBER", "")
	req.SetPathValue("kind", kind)
	req.SetPathValue("requestId", id)
	rr := httptest.NewRecorder()
	h.ResolvePageRequestResult(rr, req)
	return rr
}

func TestPageRequestNoticeAtomicPrivateGenericAndExactlyOnce(t *testing.T) {
	h, workspace, user, id, token := lifecycleFixture(t, "artifact")
	lifecycleBindDuringDispatch(t, h, "artifact", id, nil)
	execOrFatal(t, h.db, `CREATE TRIGGER reject_fixture_notice BEFORE INSERT ON inbox_items WHEN NEW.source_id LIKE 'page-request:%' BEGIN SELECT RAISE(ABORT,'fixture notice failure'); END`)
	body := fmt.Sprintf(`{"html":"<h1>Private result 93751</h1>","base_rev":1,"mutation_id":"notice-result","request_id":%q}`, id)
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, body), 500)
	lifecycleJobState(t, h, "artifact", id, "running")
	var head int
	if err := h.db.QueryRow(`SELECT head_rev FROM page_artifacts WHERE page_id=(SELECT page_id FROM page_artifact_requests WHERE id=?)`, id).Scan(&head); err != nil || head != 1 {
		t.Fatalf("result committed without inbox: %d %v", head, err)
	}
	execOrFatal(t, h.db, `DROP TRIGGER reject_fixture_notice`)
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, body), 201)
	artifactExpect(t, artifactAgentSave(t, h, workspace, token, body), 200)
	if err := h.reconcilePageRequestNotices(t.Context()); err != nil {
		t.Fatal(err)
	}
	var target, title, text, payload string
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id=?`, "page-request:artifact:"+id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("notices=%d %v", count, err)
	}
	if err := h.db.QueryRow(`SELECT target_user_id,title,body_md,payload_json FROM inbox_items WHERE source_id=?`, "page-request:artifact:"+id).Scan(&target, &title, &text, &payload); err != nil {
		t.Fatal(err)
	}
	if target != user || strings.Contains(title+text+payload, "93751") || strings.Contains(title+text+payload, "Improve the report") {
		t.Fatalf("private content copied to notice: %s %s %s", title, text, payload)
	}
	artifactExpect(t, requestResultCall(t, h, workspace, user, "artifact", id), 200)
	pagesSeedUser(t, h, workspace, "notice-other", "notice-other@example.test", "MEMBER")
	artifactExpect(t, requestResultCall(t, h, workspace, "notice-other", "artifact", id), 404)
	execOrFatal(t, h.db, `UPDATE pages SET owner_user_id='notice-other' WHERE id=(SELECT page_id FROM page_artifact_requests WHERE id=?)`, id)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	artifactExpect(t, requestResultCall(t, h, workspace, user, "artifact", id), 404)
	execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, user)
	artifactExpect(t, requestResultCall(t, h, workspace, user, "artifact", id), 404)
}

func TestPageRequestRetryUnknownRuntimeFreshSnapshotAndBackupFork(t *testing.T) {
	h, workspace, user, parent, _ := lifecycleFixture(t, "artifact")
	lifecycleBindDuringDispatch(t, h, "artifact", parent, nil)
	execOrFatal(t, h.db, `UPDATE page_artifact_requests SET status='failed' WHERE id=?`, parent)
	if err := h.reconcilePageRequestNotices(t.Context()); err != nil {
		t.Fatal(err)
	}
	artifactExpect(t, artifactSave(t, h, workspace, user, "<h1>Fresh revision</h1>", "fresh-after-failure", 1), 201)
	payload := map[string]any{"chat_id": "artifact-chat", "instruction": "Review fresh state", "base_rev": 2, "submission_id": "explicit-new-retry", "retry_of_request_id": parent}
	encoded, _ := json.Marshal(payload)
	child := createArtifactRequestForTest(t, h, workspace, user, string(encoded), 202)
	if child == parent {
		t.Fatal("retry resumed old request")
	}
	if again := createArtifactRequestForTest(t, h, workspace, user, string(encoded), 202); again != child {
		t.Fatal("retry duplicate created another request")
	}
	var base int
	var snapshot string
	if err := h.db.QueryRow(`SELECT base_rev,snapshot_json FROM page_artifact_requests WHERE id=?`, child).Scan(&base, &snapshot); err != nil || base != 2 || !strings.Contains(snapshot, `"revision":2`) {
		t.Fatalf("retry did not capture fresh source: %d %s %v", base, snapshot, err)
	}
	if h.pageRequestRetryWarning(t.Context(), h.db, "artifact", parent) == "" {
		t.Fatal("unknown runtime retry omitted duplicate warning")
	}
	payload["retry_of_request_id"] = child
	encoded, _ = json.Marshal(payload)
	createArtifactRequestForTest(t, h, workspace, user, string(encoded), 409)
	const pass = "request-notice-retry-roundtrip-password"
	made, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []string{"", "retry-notice-fork"} {
		t.Run(fork, func(t *testing.T) {
			db := setupTestDB(t)
			root := storageDir(t)
			restored, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: made.Path, Passphrase: pass, AsWorkspace: fork, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			var c, p, source, raw string
			var pending int
			if err := db.QueryRow(`SELECT c.id,p.id,c.notice_pending FROM page_artifact_requests c JOIN page_artifact_requests p ON p.id=c.retry_of_request_id WHERE c.submission_id='explicit-new-retry'`).Scan(&c, &p, &pending); err != nil || pending != 0 {
				t.Fatalf("restored retry linkage/pending=%d %v", pending, err)
			}
			if fork != "" && (c == child || p == parent) {
				t.Fatal("fork kept original retry IDs")
			}
			if err := db.QueryRow(`SELECT source_id,payload_json FROM inbox_items WHERE workspace_id=? AND source_id LIKE 'page-request:artifact:%'`, restored.RestoredWorkspaceID).Scan(&source, &raw); err != nil {
				t.Fatal(err)
			}
			var notice map[string]any
			if err := json.Unmarshal([]byte(raw), &notice); err != nil {
				t.Fatal(err)
			}
			if source != "page-request:artifact:"+p || notice["request_id"] != p {
				t.Fatalf("fork notice points to old source: %s %s", source, raw)
			}
			restoredHandler := NewPageHandler(db, nil, newTestLogger())
			if err := restoredHandler.reconcilePageRequestNotices(context.Background()); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id LIKE 'page-request:%'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("restore replayed notices: %d %v", n, err)
			}
		})
	}
}

func TestPageRequestNoticeRecoverySuppressesRevokedRecipient(t *testing.T) {
	h, workspace, user, id, _ := lifecycleFixture(t, "artifact")
	execOrFatal(t, h.db, `UPDATE page_artifact_requests SET status='failed' WHERE id=?`, id)
	execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, user)
	if err := h.reconcilePageRequestNotices(t.Context()); err != nil {
		t.Fatal(err)
	}
	var n, pending int
	if err := h.db.QueryRow(`SELECT notice_pending FROM page_artifact_requests WHERE id=?`, id).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("revoked pending=%d %v", pending, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id=?`, "page-request:artifact:"+id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("revoked recipient notified: %d %v", n, err)
	}
}

func TestPageRequestRetryParentScopeIsNotAuthority(t *testing.T) {
	h, workspace, user, parent, _ := lifecycleFixture(t, "artifact")
	execOrFatal(t, h.db, `UPDATE page_artifact_requests SET status='failed',run_id='unknown-old-run',updated_at=? WHERE id=?`, tsformat.Format(time.Now()), parent)
	var page string
	if err := h.db.QueryRow(`SELECT page_id FROM page_artifact_requests WHERE id=?`, parent).Scan(&page); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ page, user, parent string }{{"different-source", user, parent}, {page, "different-initiator", parent}, {page, user, "unknown-parent"}} {
		if _, err := h.validatePageRequestRetry(t.Context(), h.db, "artifact", tc.parent, tc.page, workspace, tc.user, ""); err == nil {
			t.Fatalf("forged lineage accepted: %+v", tc)
		}
	}
	warning, err := h.validatePageRequestRetry(t.Context(), h.db, "artifact", parent, page, workspace, user, "")
	if err != nil || warning == "" {
		t.Fatalf("explicit unknown retry blocked/hidden: %q %v", warning, err)
	}
}

func TestBoardRequestRetryAndReadyNotice(t *testing.T) {
	h, workspace, user, parent, _ := lifecycleFixture(t, "board")
	lifecycleBindDuringDispatch(t, h, "board", parent, nil)
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='failed' WHERE id=?`, parent)
	var payload map[string]any
	if err := json.Unmarshal([]byte(boardDiagramRequest), &payload); err != nil {
		t.Fatal(err)
	}
	payload["submission_id"] = "board-explicit-retry"
	payload["retry_of_request_id"] = parent
	raw, _ := json.Marshal(payload)
	response := boardJobCall(t, h, workspace, user, "", string(raw), h.CreateBoardRequest)
	artifactExpect(t, response, 202)
	var child map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &child); err != nil {
		t.Fatal(err)
	}
	if child["id"] == parent || child["retry_of_request_id"] != parent || child["retry_warning"] != pageRequestRetryWarning {
		t.Fatalf("board retry response=%s", response.Body.String())
	}
	artifactExpect(t, boardJobCall(t, h, workspace, user, "", string(raw), h.CreateBoardRequest), 202)
	// Ready-to-insert is a reviewable result, not an invented successful job.
	h2, ws2, user2, id, token := lifecycleFixture(t, "board")
	lifecycleBindDuringDispatch(t, h2, "board", id, nil)
	source := `{"source":"flowchart TD\nA[Private diagram 83642]","mutation_id":"ready-notice","dialect":"flowchart-v1","converter_version":"2.2.2"}`
	artifactExpect(t, boardJobAgentCall(t, h2, ws2, token, id, source, h2.PublishBoardRequestMermaid), 201)
	lifecycleJobState(t, h2, "board", id, "ready_to_insert")
	artifactExpect(t, boardJobAgentCall(t, h2, ws2, token, id, source, h2.PublishBoardRequestMermaid), 200)
	var n int
	var text string
	if err := h2.db.QueryRow(`SELECT COUNT(*),MAX(body_md||payload_json) FROM inbox_items WHERE source_id=?`, "page-request:board:"+id).Scan(&n, &text); err != nil || n != 1 || strings.Contains(text, "83642") {
		t.Fatalf("board notice=%d %s %v", n, text, err)
	}
	artifactExpect(t, requestResultCall(t, h2, ws2, user2, "board", id), 200)
}

func TestBoardRequestNoticeResolvesPrivateOutputAfterSourceDeletion(t *testing.T) {
	h, workspace, user, id, token := lifecycleFixture(t, "board")
	execOrFatal(t, h.db, `UPDATE page_board_requests SET kind='artifact' WHERE id=?`, id)
	lifecycleBindDuringDispatch(t, h, "board", id, nil)
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=(SELECT board_page_id FROM page_board_requests WHERE id=?)`, id)
	artifactExpect(t, artifactCreateAgent(t, h, workspace, token, creationBody), 201)
	result := requestResultCall(t, h, workspace, user, "board", id)
	artifactExpect(t, result, 200)
	var wire map[string]any
	if err := json.Unmarshal(result.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if wire["page_slug"] != "agent-created" || wire["revision"] != float64(1) {
		t.Fatalf("detached output unresolved: %s", result.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id=? AND target_user_id=?`, "page-request:board:"+id, user).Scan(&n); err != nil || n != 1 {
		t.Fatalf("detached output notice=%d %v", n, err)
	}
}

func TestPageRequestNoticeRecoveryCursorDoesNotStarveLaterNotices(t *testing.T) {
	h, _, _, parent, _ := lifecycleFixture(t, "artifact")
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("a%03d", i)
		if i == 100 {
			id = "z-good"
		}
		execOrFatal(t, h.db, `INSERT INTO page_artifact_requests(id,page_id,human_user_id,chat_id,agent_id,base_rev,submission_id,fingerprint,instruction,snapshot_json,receipt_json,status,created_at,updated_at,notice_pending) SELECT ?,page_id,human_user_id,chat_id,agent_id,base_rev,?,'fixture','fixture','{}','{}','failed',created_at,updated_at,1 FROM page_artifact_requests WHERE id=?`, id, id, parent)
	}
	execOrFatal(t, h.db, `CREATE TRIGGER reject_bad_notice BEFORE INSERT ON inbox_items WHEN NEW.source_id LIKE 'page-request:artifact:a%' BEGIN SELECT RAISE(ABORT,'fixture failed notice'); END`)
	cursor, err := h.reconcilePageRequestNoticesAfter(t.Context(), "")
	if err == nil || cursor != "artifact:a099" {
		t.Fatalf("failed batch lost cursor=%q err=%v", cursor, err)
	}
	_, err = h.reconcilePageRequestNoticesAfter(t.Context(), cursor)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='page-request:artifact:z-good'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("later notice starved=%d %v", n, err)
	}
}

func TestPageRequestResultUsesLiveRoleNotMiddlewareRole(t *testing.T) {
	h, workspace, user, id, _ := lifecycleFixture(t, "artifact")
	pagesSeedUser(t, h, workspace, "result-new-owner", "result-new-owner@example.test", "MEMBER")
	execOrFatal(t, h.db, `UPDATE pages SET owner_user_id='result-new-owner' WHERE id=(SELECT page_id FROM page_artifact_requests WHERE id=?)`, id)
	call := func(role string) *httptest.ResponseRecorder {
		r := pagesGrantRequest(t, "GET", "/", workspace, user, role, "")
		r.SetPathValue("kind", "artifact")
		r.SetPathValue("requestId", id)
		w := httptest.NewRecorder()
		h.ResolvePageRequestResult(w, r)
		return w
	}
	// Actual administrator visibility remains available even with stale low context.
	artifactExpect(t, call("MEMBER"), 200)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	artifactExpect(t, call("OWNER"), 404)
	// Restore Page visibility through a live owner-issued grant, rather than
	// an administrator role that is incompatible with account-wide Guest mode.
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) SELECT page_id,'user',?,'read','result-new-owner' FROM page_artifact_requests WHERE id=?`, user, id)
	artifactExpect(t, call("OWNER"), 200)
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('result-restricted-ws','Restricted','result-restricted-ws')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('result-restricted-member','result-restricted-ws',?,'MEMBER','restricted')`, user)
	artifactExpect(t, call("OWNER"), 404)
}
func TestPageRequestNoticeUsesRecipientLiveRoleNotWorkerOrCancellerRole(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(fmt.Sprint(admin), func(t *testing.T) {
			h, workspace, user, id, _ := lifecycleFixture(t, "artifact")
			pagesSeedUser(t, h, workspace, "notice-new-owner", "notice-new-owner@example.test", "MEMBER")
			execOrFatal(t, h.db, `UPDATE pages SET owner_user_id='notice-new-owner' WHERE id=(SELECT page_id FROM page_artifact_requests WHERE id=?)`, id)
			execOrFatal(t, h.db, `UPDATE page_artifact_requests SET status='failed',notice_pending=1 WHERE id=?`, id)
			role := "MEMBER"
			if admin {
				role = "OWNER"
			}
			execOrFatal(t, h.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, role, workspace, user)
			// Deliberately wrong caller role: worker has none, another cancelling editor
			// may be administrator. Neither supplies the recipient's authority.
			callerRole := "OWNER"
			if admin {
				callerRole = ""
			}
			ctx := context.WithValue(t.Context(), ctxRole, callerRole)
			if err := h.reconcilePageRequestNotices(ctx); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id=?`, "page-request:artifact:"+id).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if admin {
				want = 1
			}
			if count != want {
				t.Fatalf("notice count=%d want=%d", count, want)
			}
		})
	}
}
