package chataudience

import (
	"context"
	"testing"
)

func TestCaptureTrustedReceiptFencesEveryIdentityField(t *testing.T) {
	db := audienceDB(t)
	receipt, err := CaptureTrusted(t.Context(), db, "private", "author", nil)
	if err != nil || receipt == nil {
		t.Fatalf("capture: %+v %v", receipt, err)
	}
	if receipt.UserID != "author" || receipt.ChatID != "private" || receipt.WorkspaceID != "ws" || receipt.AgentID != "agent" || receipt.MemberID != "author-member" || receipt.MemberRevision != 1 || receipt.ChatRevision != 1 || receipt.ChatGeneration == "" {
		t.Fatalf("incomplete authoritative snapshot: %+v", receipt)
	}
	if actual, err := CaptureTrusted(t.Context(), db, "private", "author", receipt); err != nil || actual == nil || *actual != *receipt {
		t.Fatalf("unchanged authority denied: %+v %v", actual, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Receipt)
	}{
		{"user", func(r *Receipt) { r.UserID = "other" }},
		{"chat", func(r *Receipt) { r.ChatID = "group" }},
		{"workspace", func(r *Receipt) { r.WorkspaceID = "foreign" }},
		{"agent", func(r *Receipt) { r.AgentID = "other-agent" }},
		{"membership identity", func(r *Receipt) { r.MemberID = "new-member" }},
		{"membership revision", func(r *Receipt) { r.MemberRevision++ }},
		{"chat generation", func(r *Receipt) { r.ChatGeneration = "recreated" }},
		{"chat revision", func(r *Receipt) { r.ChatRevision++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stale := *receipt
			tc.mutate(&stale)
			if actual, err := CaptureTrusted(t.Context(), db, "private", "author", &stale); err != nil || actual != nil {
				t.Fatalf("mismatched receipt refreshed authority: %+v %v", actual, err)
			}
		})
	}
	if got := ReceiptFromContext(context.Background()); got != nil {
		t.Fatal("context without host receipt acquired authority")
	}
	ctx := WithReceipt(t.Context(), receipt)
	if got := ReceiptFromContext(ctx); got != receipt {
		t.Fatalf("host receipt lost: %+v", got)
	}
	if got := ReceiptFromContext(WithReceipt(ctx, nil)); got != nil {
		t.Fatal("cleared host receipt retained authority")
	}
}

func TestCaptureTrustedRevokedThenRestoredAuthorityCannotRefreshRetry(t *testing.T) {
	for _, tc := range []struct{ name, chat, user, mutation string }{
		{"membership removed and rejoined", "private", "author", `DELETE FROM workspace_members WHERE id='author-member'; INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('replacement-member','ws','author','MEMBER')`},
		{"role changed and restored", "private", "author", `UPDATE workspace_members SET role='VIEWER' WHERE id='author-member'; UPDATE workspace_members SET role='MEMBER' WHERE id='author-member'`},
		{"audience changed and restored", "private", "author", `UPDATE chats SET visibility='group' WHERE id='private'; UPDATE chats SET visibility='private' WHERE id='private'`},
		{"agent deleted and restored", "private", "author", `UPDATE agents SET deleted_at='2026-10-02T00:00:00Z' WHERE id='agent'; UPDATE agents SET deleted_at=NULL WHERE id='agent'`},
		{"workspace deleted and restored", "private", "author", `UPDATE workspaces SET deleted_at='2026-10-02T00:00:00Z' WHERE id='ws'; UPDATE workspaces SET deleted_at=NULL WHERE id='ws'`},
		{"chat recreated under same ID", "private", "author", `DELETE FROM chats WHERE id='private'; INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('private','ws','agent','author','private')`},
		{"participant removed and restored", "group", "other", `DELETE FROM chat_participants WHERE chat_id='group' AND user_id='other'; INSERT INTO chat_participants(chat_id,user_id,role) VALUES('group','other','member')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := audienceDB(t)
			old, err := CaptureTrusted(t.Context(), db, tc.chat, tc.user, nil)
			if err != nil || old == nil {
				t.Fatalf("initial admission: %+v %v", old, err)
			}
			audienceExec(t, db, tc.mutation)
			if actual, err := CaptureTrusted(t.Context(), db, tc.chat, tc.user, old); err != nil || actual != nil {
				t.Fatalf("queued admission survived revoke/restore: %+v %v", actual, err)
			}
			fresh, err := CaptureTrusted(t.Context(), db, tc.chat, tc.user, nil)
			if err != nil || fresh == nil || *fresh == *old {
				t.Fatalf("new send did not get a new epoch: %+v %v", fresh, err)
			}
		})
	}
}

func TestCaptureTrustedDeniesUnavailableAuthority(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"removed membership", `DELETE FROM workspace_members WHERE id='author-member'`},
		{"removed chat", `DELETE FROM chats WHERE id='private'`},
		{"deleted agent", `UPDATE agents SET deleted_at='2026-10-02T00:00:00Z' WHERE id='agent'`},
		{"agent moved to foreign workspace", `UPDATE agents SET workspace_id='foreign' WHERE id='agent'`},
		{"deleted workspace", `UPDATE workspaces SET deleted_at='2026-10-02T00:00:00Z' WHERE id='ws'`},
		{"restricted membership elsewhere", `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('author-foreign','foreign','author','MEMBER','restricted')`},
		{"missing chat epoch", `UPDATE chats SET authority_generation='' WHERE id='private'`},
		{"invalid chat revision", `UPDATE chats SET authority_revision=0 WHERE id='private'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := audienceDB(t)
			audienceExec(t, db, tc.mutation)
			if receipt, err := CaptureTrusted(t.Context(), db, "private", "author", nil); err != nil || receipt != nil {
				t.Fatalf("unavailable authority admitted: %+v %v", receipt, err)
			}
		})
	}
}
