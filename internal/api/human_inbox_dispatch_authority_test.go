package api

import (
	"context"
	"errors"
	"github.com/crewship-ai/crewship/internal/groupchat"
	"testing"
)

// The receipt tool is not the execution gate: a model can ignore that tool.
// Frozen canonical source authority must hold at prepare, final dispatch and
// reply publication/replay even when no model ever calls inbox_read.
func TestHumanInboxChannelSourceAuthorityAtEveryExecutionBoundary(t *testing.T) {
	for _, phase := range []string{"prepare", "dispatch", "reply", "reply_replay"} {
		for _, change := range []string{"account_epoch", "membership_regrant", "role_changed", "changed_content"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				h, s, c, u, agent, _ := conversationJobFixture(t)
				ctx := context.Background()
				job := enqueueConversationJob(t, h, s, c, u, agent, "source-bound-once")
				assignment := ""
				if phase != "prepare" {
					if _, err := h.prepareConversationJob(ctx, job); err != nil {
						t.Fatal(err)
					}
					if err := h.db.QueryRow(`SELECT assignment_id FROM workspace_conversation_agent_jobs WHERE id=?`, job).Scan(&assignment); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "reply_replay" {
					execOrFatal(t, h.db, `UPDATE assignments SET status='COMPLETED' WHERE id=?`, assignment)
					if _, err := s.SendAgentReply(ctx, job, "Originally authorized reply"); err != nil {
						t.Fatal(err)
					}
				}
				switch change {
				case "account_epoch":
					execOrFatal(t, h.db, `UPDATE users SET authority_revision=authority_revision+1 WHERE id=?`, u)
				case "membership_regrant":
					execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, c.WorkspaceID, u)
					execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('new-source-membership',?,?,'OWNER','trusted')`, c.WorkspaceID, u)
				case "role_changed":
					execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, c.WorkspaceID, u)
				case "changed_content":
					execOrFatal(t, h.db, `UPDATE workspace_conversation_messages SET content='Not the accepted canonical source' WHERE id=(SELECT message_id FROM workspace_conversation_agent_jobs WHERE id=?)`, job)
				}
				switch phase {
				case "prepare":
					if _, err := h.prepareConversationJob(ctx, job); err != nil {
						t.Fatal(err)
					}
					var n int
					if err := h.db.QueryRow(`SELECT COUNT(*) FROM assignments`).Scan(&n); err != nil || n != 0 {
						t.Fatalf("revoked request prepared assignments=%d %v", n, err)
					}
				case "dispatch":
					allowed, err := h.authorizeConversationAssignment(ctx, assignment)
					if err != nil || allowed {
						t.Fatalf("revoked request admitted: %v %v", allowed, err)
					}
				case "reply", "reply_replay":
					if _, err := s.SendAgentReply(ctx, job, "Must never be returned/published"); !errors.Is(err, groupchat.ErrForbidden) {
						t.Fatalf("source reply admitted: %v", err)
					}
				}
				var n int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_messages WHERE author_agent_id=?`, agent).Scan(&n); err != nil {
					t.Fatal(err)
				}
				want := 0
				if phase == "reply_replay" {
					want = 1
				}
				if n != want {
					t.Fatalf("unauthorized reply count=%d want=%d", n, want)
				}
			})
		}
	}
}
