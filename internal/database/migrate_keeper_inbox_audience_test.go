package database

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

// crewship#1671 aligned a credential escalation's audience with its authority:
// resolving one is roleManage — OWNER or ADMIN — so addressing the inbox item at
// MANAGER showed every manager a production-credential decision they could not
// take, and inbox visibility is hierarchical.
//
// That fix governs rows written after it. This migration is the other half: on
// an upgraded instance the escalations raised BEFORE it keep their old audience,
// and those are the interesting ones — open requests naming a production
// credential, with the justification, the risk score and the asking agent.
//
// The credential's VALUE was never in there. What was is the shape of the
// estate, which is what somebody choosing a target is after. A fix that only
// covers the next leak and not the one already in people's inboxes is half a
// fix.

var keeperAudienceCounter atomic.Int64

// migratedAtKeeperAudience lands the schema as it stood just before this
// migration, so legacy rows can be seeded and then upgraded — the only way to
// test a backfill is to have something to fill.
func migratedAtKeeperAudience(t *testing.T) (*sql.DB, context.Context, *slog.Logger) {
	t.Helper()
	name := fmt.Sprintf("crewship-keeper-audience-%d", keeperAudienceCounter.Add(1))
	db, err := sql.Open("sqlite",
		fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", name))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(ctx, db, keeperInboxAudienceVersion-1, logger); err != nil {
		t.Fatalf("migrate to the version before the backfill: %v", err)
	}
	return db, ctx, logger
}

// seedLegacyKeeperInbox writes the pre-#1671 shape: a keeper request plus its
// inbox item addressed at MANAGER.
func seedLegacyKeeperInbox(t *testing.T, db *sql.DB, ctx context.Context, id, state string) {
	t.Helper()
	mustExecCtx(t, db, ctx, `INSERT OR IGNORE INTO users (id, email, full_name) VALUES ('u1','u@ex.com','U')`)
	mustExecCtx(t, db, ctx, `INSERT OR IGNORE INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`)
	mustExecCtx(t, db, ctx, `INSERT OR IGNORE INTO crews (id, workspace_id, name, slug) VALUES ('c1','ws1','C','c')`)
	mustExecCtx(t, db, ctx, `INSERT OR IGNORE INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('a1','ws1','c1','A','a')`)
	mustExecCtx(t, db, ctx, `INSERT OR IGNORE INTO credentials
		(id, workspace_id, name, encrypted_value, type, security_level, created_by)
		VALUES ('cr1','ws1','PROD_DB_ADMIN','v1:aW52YWxpZA==','SECRET',4,'u1')`)
	mustExecCtx(t, db, ctx, `INSERT INTO keeper_requests
		(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES (?, 'access', 'a1', 'c1', 'cr1', 'migrate the orders table', 'ESCALATE')`, id)
	mustExecCtx(t, db, ctx, `INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, target_role, title, state, priority, blocking, payload_json)
		VALUES (?, 'ws1', 'escalation', ?, 'MANAGER', 'Keeper escalation', ?, 'high', 1, '{}')`,
		"ibx-"+id, id, state)
}

func mustExecCtx(t *testing.T, db *sql.DB, ctx context.Context, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		t.Fatalf("exec %.60q: %v", q, err)
	}
}

func targetRoleOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var role string
	if err := db.QueryRow(`SELECT COALESCE(target_role,'') FROM inbox_items WHERE id = ?`, id).Scan(&role); err != nil {
		t.Fatalf("read target_role for %s: %v", id, err)
	}
	return role
}

func TestKeeperInboxAudience_ReTargetsOpenCredentialEscalations(t *testing.T) {
	db, ctx, logger := migratedAtKeeperAudience(t)
	seedLegacyKeeperInbox(t, db, ctx, "kr-open", "unread")
	seedLegacyKeeperInbox(t, db, ctx, "kr-read", "read")

	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, id := range []string{"ibx-kr-open", "ibx-kr-read"} {
		if got := targetRoleOf(t, db, id); got != "ADMIN" {
			t.Errorf("%s still targets %q; every manager can still read a production "+
				"credential request they cannot decide", id, got)
		}
	}
}

// Resolved rows are history. Re-addressing a decision somebody already made
// would rewrite who it was for, and it buys nothing — nobody is being asked to
// act on it.
func TestKeeperInboxAudience_LeavesResolvedRowsAlone(t *testing.T) {
	db, ctx, logger := migratedAtKeeperAudience(t)
	seedLegacyKeeperInbox(t, db, ctx, "kr-done", "resolved")

	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if got := targetRoleOf(t, db, "ibx-kr-done"); got != "MANAGER" {
		t.Errorf("a resolved row was re-addressed to %q; that rewrites who a past "+
			"decision was for", got)
	}
}

// A skill review legitimately targets MANAGER — it names no credential — and an
// escalations-backed row is a different flow with its own audience. Neither has
// a keeper_requests id in source_id, which is what keeps them out.
func TestKeeperInboxAudience_TouchesOnlyKeeperSourcedRows(t *testing.T) {
	db, ctx, logger := migratedAtKeeperAudience(t)
	seedLegacyKeeperInbox(t, db, ctx, "kr-open", "unread")

	// Same kind, same role, source_id that is not a keeper request.
	mustExecCtx(t, db, ctx, `INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, target_role, title, state, priority, blocking, payload_json)
		VALUES ('ibx-skill','ws1','escalation','esc-not-keeper','MANAGER','Skill review','unread','medium',0,'{}')`)

	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if got := targetRoleOf(t, db, "ibx-skill"); got != "MANAGER" {
		t.Errorf("a non-keeper escalation was re-addressed to %q; a skill review names "+
			"no credential and belongs with managers", got)
	}
	if got := targetRoleOf(t, db, "ibx-kr-open"); got != "ADMIN" {
		t.Errorf("the keeper row was missed: %q", got)
	}
}

// Rows already addressed to OWNER, or carrying a named security contact, must
// not be widened. This migration narrows a role fanout; it is not a re-address.
func TestKeeperInboxAudience_DoesNotWidenANarrowerAudience(t *testing.T) {
	db, ctx, logger := migratedAtKeeperAudience(t)
	seedLegacyKeeperInbox(t, db, ctx, "kr-owner", "unread")
	mustExecCtx(t, db, ctx, `UPDATE inbox_items SET target_role = 'OWNER' WHERE id = 'ibx-kr-owner'`)

	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if got := targetRoleOf(t, db, "ibx-kr-owner"); got != "OWNER" {
		t.Errorf("an OWNER-only row was widened to %q", got)
	}
}
