package api

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
)

func TestResolvePageChatContextRechecksBothReadersAndLiveGrant(t *testing.T) {
	h, wsID, ownerID := pagesAccessFixture(t)
	ctx := context.Background()
	resolve := func(user, agent, slug string) error {
		_, err := h.ResolvePageChatContext(ctx, wsID, user, agent, slug)
		return err
	}
	page, err := h.ResolvePageChatContext(ctx, wsID, "alice", "agent-watcher", "fleet-201")
	if err != nil || page.Name != "Reach fleet-201" || page.Slug != "fleet-201" || page.PageID == "" || page.WorkspaceID != wsID || page.SnapshotAt == "" {
		t.Fatalf("authorized context = %+v, %v", page, err)
	}
	for _, tc := range []struct{ name, user, agent, slug string }{
		{"human cannot read", "dave", "agent-watcher", "ops-board"},
		{"agent has no grant", "alice", "agent-watcher", "ops-board"},
		{"agent missing", "alice", "nonexistent", "fleet-201"},
		{"human missing", "nonexistent", "agent-watcher", "fleet-201"},
		{"page missing", "alice", "agent-watcher", "nonexistent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := resolve(tc.user, tc.agent, tc.slug); !errors.Is(err, chatbridge.ErrPageChatAccess) {
				t.Fatalf("error = %v, want indistinguishable refusal", err)
			}
		})
	}
	if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE workspace_id=? AND user_id='alice'`, wsID); err != nil {
		t.Fatal(err)
	}
	if err := resolve("alice", "agent-watcher", "fleet-201"); !errors.Is(err, chatbridge.ErrPageChatAccess) {
		t.Fatalf("revoked human membership: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE agents SET expired_at='2026-09-23T00:00:00Z' WHERE id='agent-watcher'`); err != nil {
		t.Fatal(err)
	}
	if err := resolve("bob", "agent-watcher", "fleet-201"); !errors.Is(err, chatbridge.ErrPageChatAccess) {
		t.Fatalf("retired agent: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE agents SET expired_at=NULL WHERE id='agent-watcher'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, wsID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`DELETE FROM crew_members WHERE user_id=?`, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := resolve("bob", "agent-watcher", "fleet-201"); !errors.Is(err, chatbridge.ErrPageChatAccess) {
		t.Fatalf("grant with revoked issuer: %v", err)
	}
}
