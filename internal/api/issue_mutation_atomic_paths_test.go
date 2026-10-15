package api

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func rejectIssueOutbox(t *testing.T, db *sql.DB) {
	t.Helper()
	execOrFatal(t, db, `CREATE TRIGGER reject_issue_outbox BEFORE INSERT ON issue_mutation_outbox BEGIN SELECT RAISE(ABORT,'synthetic outbox outage'); END`)
}
func assertIssueRows(t *testing.T, db *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil || n != want {
		t.Fatalf("query %s count=%d want=%d err=%v", query, n, want, err)
	}
}

func TestAgentIssueMutationAuditAndMentionsAreAtomic(t *testing.T) {
	for _, verb := range []string{"create", "patch", "comment"} {
		t.Run(verb, func(t *testing.T) {
			f := newChatIssueFixture(t)
			mission := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-1", "TODO")
			rejectIssueOutbox(t, f.h.db)
			body := map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "author_run_id": f.run, "operation_key": "atomic-" + verb}
			handler, method := f.h.CreateComment, "POST"
			switch verb {
			case "create":
				body = f.createBody("atomic-create", "Must roll back")
				handler = f.h.Create
			case "patch":
				body["status"] = "IN_PROGRESS"
				body["comment"] = mentionToken("lead", f.agent)
				handler, method = f.h.UpdateStatus, "PATCH"
			case "comment":
				body["body"] = mentionToken("lead", f.agent) + " review"
			}
			rr := chatIssueCall(t, handler, method, "ENG-1", body)
			if rr.Code != 500 {
				t.Fatalf("outbox outage was accepted: %d %s", rr.Code, rr.Body.String())
			}
			assertIssueRows(t, f.h.db, `SELECT COUNT(*) FROM missions`, 1)
			assertIssueRows(t, f.h.db, `SELECT COUNT(*) FROM mission_comments`, 0)
			assertIssueRows(t, f.h.db, `SELECT COUNT(*) FROM agent_operations`, 0)
			assertIssueRows(t, f.h.db, `SELECT COUNT(*) FROM mission_activity WHERE mission_id=?`, 0, mission)
			var status string
			if err := f.h.db.QueryRow(`SELECT status FROM missions WHERE id=?`, mission).Scan(&status); err != nil || status != "TODO" {
				t.Fatalf("partial mutation: %q %v", status, err)
			}
		})
	}
}

func TestAgentCommentDisconnectReplayKeepsOneDurableMention(t *testing.T) {
	f := setupMentionFixture(t)
	seedChatIssueRun(t, f.internal, f.wsID, f.author, f.userID, "durable-agent-chat", "durable-agent-run")
	f.internal.SetJournal(unavailableIssueJournal{})
	body := map[string]any{"workspace_id": f.wsID, "agent_id": f.author, "author_run_id": "durable-agent-run", "operation_key": "agent-disconnect", "body": mentionToken("lead", f.target) + " review"}
	req := httptest.NewRequest("POST", "/", jsonBody(body))
	req.SetPathValue("identifier", f.ident)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	rr := httptest.NewRecorder()
	f.internal.CreateComment(cancelIssueResponseHeader{rr, cancel}, req.WithContext(ctx))
	if rr.Code != 201 {
		t.Fatalf("first comment: %d %s", rr.Code, rr.Body.String())
	}
	f.assign.WaitDispatches()
	replay := chatIssueCall(t, f.internal.CreateComment, "POST", f.ident, body)
	if replay.Code != 200 {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	f.assign.WaitDispatches()
	assertIssueRows(t, f.db, `SELECT COUNT(*) FROM mission_comments WHERE body=? AND author_id=?`, 1, body["body"], f.author)
	assertIssueRows(t, f.db, `SELECT COUNT(*) FROM mission_comment_mentions`, 1)
	assertIssueRows(t, f.db, `SELECT COUNT(*) FROM assignments`, 1)
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	recovered := f.internal.mutationDelivery()
	recovered.SetJournal(writer)
	execOrFatal(t, f.db, `UPDATE issue_mutation_outbox SET next_attempt_at='2000-01-01T00:00:00.000Z'`)
	if err := recovered.DrainIssueMutationOutbox(t.Context(), 50); err != nil {
		t.Fatal(err)
	}
	assertIssueRows(t, f.db, `SELECT COUNT(*) FROM journal_entries WHERE entry_type='agent.mentioned'`, 1)
	assertIssueRows(t, f.db, `SELECT COUNT(*) FROM journal_entries WHERE entry_type='mission.comment'`, 1)
}

func TestHumanIssueCreateAndBulkAuditRollbackTogether(t *testing.T) {
	for _, verb := range []string{"create", "bulk"} {
		t.Run(verb, func(t *testing.T) {
			h, user, ws, crew, lead, _ := newTestIssueHandler(t)
			mission := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
			rejectIssueOutbox(t, h.db)
			body := map[string]any{"title": "Must roll back"}
			if verb == "bulk" {
				body = map[string]any{"ids": []string{mission}, "updates": map[string]string{"status": "TODO"}}
			}
			req := httptest.NewRequest("POST", "/", jsonBody(body))
			req.SetPathValue("crewId", crew)
			req = withWorkspaceUser(req, user, ws, "OWNER")
			rr := httptest.NewRecorder()
			if verb == "create" {
				h.Create(rr, req)
				if rr.Code != 500 {
					t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
				}
			} else {
				h.BulkUpdate(rr, req)
				if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"updated":0`) {
					t.Fatalf("bulk: %d %s", rr.Code, rr.Body.String())
				}
			}
			assertIssueRows(t, h.db, `SELECT COUNT(*) FROM missions`, 1)
			assertIssueRows(t, h.db, `SELECT COUNT(*) FROM mission_activity`, 0)
			var status string
			if err := h.db.QueryRow(`SELECT status FROM missions WHERE id=?`, mission).Scan(&status); err != nil || status != "BACKLOG" {
				t.Fatalf("partial mutation: %s %v", status, err)
			}
		})
	}
}

func TestAttachmentMetadataAndAuditRollbackTogether(t *testing.T) {
	f := newAttachmentFixture(t)
	rejectIssueOutbox(t, f.h.db)
	rr := httptest.NewRecorder()
	f.h.Upload(rr, uploadReq(t, f, "ENG-1", "test.txt", []byte("synthetic attachment")))
	if rr.Code != 500 {
		t.Fatalf("upload: %d %s", rr.Code, rr.Body.String())
	}
	assertIssueRows(t, f.h.db, `SELECT COUNT(*) FROM attachments`, 0)
	assertIssueRows(t, f.h.db, `SELECT COUNT(*) FROM mission_activity`, 0)
}

func TestCodeLinkDeleteAndAuditRollbackTogether(t *testing.T) {
	f := newCodeLinkFixture(t)
	execOrFatal(t, f.db, `INSERT INTO mission_code_links(id,workspace_id,mission_id,provider,host,owner,repo,number,kind,url,created_at,updated_at) VALUES('atomic-link',?,?,'GITHUB','github.com','acme','repo',1,'pull_request','https://github.com/acme/repo/pull/1',datetime('now'),datetime('now'))`, f.wsID, f.missionID)
	rejectIssueOutbox(t, f.db)
	req := f.req("DELETE", "")
	req.SetPathValue("linkId", "atomic-link")
	rr := httptest.NewRecorder()
	f.h.Delete(rr, req)
	if rr.Code != 500 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	assertIssueRows(t, f.db, `SELECT COUNT(*) FROM mission_code_links WHERE id='atomic-link'`, 1)
	assertIssueRows(t, f.db, `SELECT COUNT(*) FROM mission_activity`, 0)
}
