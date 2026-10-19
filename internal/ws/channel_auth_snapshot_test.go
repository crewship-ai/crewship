package ws

import (
	"context"
	"errors"
	"testing"
)

func TestDeliveryAuthorizationSingleSnapshot(t *testing.T) {
	db, counter := deliveryAuthorizationFixture(t, 1, 1)
	for _, q := range []string{
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('snapshot-crew','bench-workspace-0','Snapshot','snapshot')`,
		`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status) VALUES('snapshot-mission','bench-workspace-0','snapshot-crew','bench-agent-0','snapshot-trace','Snapshot','TODO')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('snapshot-page','bench-workspace-0','snapshot','Snapshot','bench-user-0','{}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a := NewDBChannelAuthorizer(db)
	for _, channel := range []string{"workspace:bench-workspace-0", "keeper:bench-workspace-0", "journal:bench-workspace-0", "crew:snapshot-crew", "files:snapshot-crew", "agent:bench-agent-0", "mission:snapshot-mission", "page:snapshot-page", "session:bench-chat-0-0", "chat-hints:bench-chat-0-0"} {
		t.Run(channel, func(t *testing.T) {
			counter.Reset()
			allowed, err := a.CanDeliver(t.Context(), "bench-user-0", channel)
			if err != nil || !allowed {
				t.Fatalf("native delivery denied: %v", err)
			}
			if got := counter.Count(); got != 1 {
				t.Errorf("per-frame authorization used %d SQL statements, want one current snapshot", got)
			}
		})
	}
}

func TestDeliverySnapshotFreshState(t *testing.T) {
	db, _ := deliveryAuthorizationFixture(t, 1, 1)
	a := NewDBChannelAuthorizer(db)
	channels := []string{"workspace:bench-workspace-0", "agent:bench-agent-0", "session:bench-chat-0-0"}
	check := func(want bool) {
		t.Helper()
		for _, channel := range channels {
			allowed, err := a.CanDeliver(t.Context(), "bench-user-0", channel)
			if err != nil || allowed != want {
				t.Fatalf("%s: allow=%v error=%v want=%v", channel, allowed, err, want)
			}
		}
	}
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	check(true)
	exec(`UPDATE workspace_members SET access_mode='restricted' WHERE user_id='bench-user-0'`)
	check(false)
	exec(`UPDATE workspace_members SET access_mode='trusted' WHERE user_id='bench-user-0'`)
	check(true)
	exec(`DELETE FROM workspace_members WHERE user_id='bench-user-0'`)
	check(false)
	// Operational errors remain different from a definitive denied decision.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if allowed, err := a.CanDeliver(ctx, "bench-user-0", channels[0]); allowed || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled snapshot: allowed=%v error=%v", allowed, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if allowed, err := a.CanDeliver(t.Context(), "bench-user-0", channels[1]); allowed || err == nil {
		t.Fatalf("closed DB became definitive deny: allowed=%v error=%v", allowed, err)
	}
}

// The private-artifact audience is part of the same live snapshot as account
// and membership eligibility. A previously allowed frame grants no next frame.
func TestDeliverySnapshotPrivatePageAudience(t *testing.T) {
	for _, tc := range []struct {
		name, user, mutation string
		shared, allowed      bool
	}{
		{name: "shared-member", user: "bench-user-0", shared: true, allowed: true},
		{name: "shared-nonowner-member", user: "bench-user-3", shared: true, allowed: true},
		{name: "private-page-owner", user: "bench-user-0", allowed: true},
		{name: "private-workspace-owner", user: "bench-user-1", allowed: true},
		{name: "private-nonowner-admin", user: "bench-user-2"},
		{name: "private-other-member", user: "bench-user-3"},
		{name: "page-owner-transfer", user: "bench-user-0", allowed: true, mutation: `UPDATE pages SET owner_user_id='bench-user-2' WHERE id='private-snapshot'`},
		{name: "workspace-owner-demotion", user: "bench-user-1", allowed: true, mutation: `UPDATE workspace_members SET role='ADMIN' WHERE user_id='bench-user-1' AND workspace_id='bench-workspace-0'`},
		{name: "page-owner-membership-removal", user: "bench-user-0", allowed: true, mutation: `DELETE FROM workspace_members WHERE user_id='bench-user-0' AND workspace_id='bench-workspace-0'`},
		{name: "page-owner-restricted", user: "bench-user-0", allowed: true, mutation: `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='bench-user-0' AND workspace_id='bench-workspace-0'`},
		{name: "page-owner-globally-restricted", user: "bench-user-0", allowed: true, mutation: `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='bench-user-0' AND workspace_id='bench-workspace-1'`},
		{name: "page-owner-suspended", user: "bench-user-0", allowed: true, mutation: `UPDATE users SET suspended_at='2026-10-18T00:00:00Z' WHERE id='bench-user-0'`},
		{name: "workspace-owner-suspended", user: "bench-user-1", allowed: true, mutation: `UPDATE users SET suspended_at='2026-10-18T00:00:00Z' WHERE id='bench-user-1'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, counter := deliveryAuthorizationFixture(t, 4, 2)
			exec := func(query string) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			exec(`UPDATE workspace_members SET role='OWNER' WHERE user_id='bench-user-1' AND workspace_id='bench-workspace-0'`)
			exec(`UPDATE workspace_members SET role='ADMIN' WHERE user_id='bench-user-2' AND workspace_id='bench-workspace-0'`)
			exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('private-snapshot','bench-workspace-0','private-snapshot','Snapshot','bench-user-0','{}',1)`)
			if tc.shared {
				exec(`UPDATE pages SET private_audience=0 WHERE id='private-snapshot'`)
			}
			a := NewDBChannelAuthorizer(db)
			check := func(want bool) {
				t.Helper()
				counter.Reset()
				allowed, err := a.CanDeliver(t.Context(), tc.user, "page:private-snapshot")
				if err != nil {
					t.Fatalf("native page snapshot returned an operational error: %v", err)
				}
				if allowed != want {
					t.Errorf("private page frame allowed=%v want=%v", allowed, want)
				}
				if got := counter.Count(); got != 1 {
					t.Errorf("page frame used %d SQL statements, want one current snapshot", got)
				}
			}
			check(tc.allowed)
			if tc.mutation != "" {
				exec(tc.mutation)
				check(false)
			}
		})
	}
}
