package retention

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// row is one fixture row and whether a 30-day sweep must delete it.
type row struct {
	id      string
	insert  []string
	deleted bool
}

func ids(t *testing.T, db *sql.DB, q string) []string {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func expectSurvivors(t *testing.T, db *sql.DB, q string, rows []row) {
	t.Helper()
	var want []string
	for _, r := range rows {
		if !r.deleted {
			want = append(want, r.id)
		}
	}
	sort.Strings(want)
	if got := ids(t, db, q); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("survivors = %v, want %v", got, want)
	}
}

func deletedCount(rows []row) int {
	n := 0
	for _, r := range rows {
		if r.deleted {
			n++
		}
	}
	return n
}

// sweepCase runs one window's fixture three ways: with no window set (nothing
// goes), with a NULL window (nothing goes), then with 30 days — where the
// preview count must equal what the sweep deletes, and exactly the rows marked
// deleted must go.
func sweepCase(t *testing.T, key Key, fixture []row, survivorsQ string) {
	t.Helper()
	db := newDB(t)
	exec(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag1','ws1','Agent','agent'), ('ag2','ws2','Other','other')`)
	exec(t, db, `INSERT INTO users (id, email) VALUES ('u1','u1@example.invalid')`)
	for _, r := range fixture {
		for _, q := range r.insert {
			exec(t, db, q)
		}
	}
	ctx := context.Background()
	all := len(ids(t, db, survivorsQ))

	if err := SweepAll(ctx, db, slog.Default(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := len(ids(t, db, survivorsQ)); n != all {
		t.Fatalf("no window set: %d of %d rows left, want all", n, all)
	}
	exec(t, db, `INSERT INTO retention_settings (workspace_id, key, days) VALUES ('ws1', ?, NULL)`, string(key))
	if err := SweepAll(ctx, db, slog.Default(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := len(ids(t, db, survivorsQ)); n != all {
		t.Fatalf("NULL window: %d of %d rows left, want all", n, all)
	}
	if n, err := Count(ctx, db, "ws1", key, nil, time.Now()); err != nil || n != 0 {
		t.Fatalf("count under forever = %d, %v", n, err)
	}

	preview, err := Count(ctx, db, "ws1", key, days(30), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if int(preview) != deletedCount(fixture) {
		t.Fatalf("preview count = %d, want %d", preview, deletedCount(fixture))
	}
	exec(t, db, `UPDATE retention_settings SET days = 30 WHERE workspace_id = 'ws1' AND key = ?`, string(key))
	if err := SweepAll(ctx, db, slog.Default(), time.Now()); err != nil {
		t.Fatal(err)
	}
	expectSurvivors(t, db, survivorsQ, fixture)
	if n, _ := Count(ctx, db, "ws1", key, days(30), time.Now()); n != 0 {
		t.Fatalf("after the sweep the count is %d, want 0", n)
	}
}

func TestSweepInbox(t *testing.T) {
	item := func(id, ws, state string, blocking int, at string) string {
		readAt, resolvedAt := "NULL", "NULL"
		if state != "unread" {
			readAt = "'" + at + "'"
		}
		if state == "resolved" {
			resolvedAt = "'" + at + "'"
		}
		return fmt.Sprintf(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, blocking, created_at, updated_at, read_at, resolved_at)
			VALUES ('%s','%s','escalation','src-%s','t','%s',%d,'%s','%s',%s,%s)`, id, ws, id, state, blocking, at, at, readAt, resolvedAt)
	}
	fixture := []row{
		{id: "resolved-old", insert: []string{item("resolved-old", "ws1", "resolved", 1, ago(40))}, deleted: true},
		{id: "read-nonblocking-old", insert: []string{item("read-nonblocking-old", "ws1", "read", 0, ago(40))}, deleted: true},
		{id: "resolved-recent", insert: []string{item("resolved-recent", "ws1", "resolved", 1, ago(5))}},
		{id: "unread-old", insert: []string{item("unread-old", "ws1", "unread", 1, ago(400))}},
		{id: "unread-old-nonblocking", insert: []string{item("unread-old-nonblocking", "ws1", "unread", 0, ago(400))}},
		{id: "read-blocking-old", insert: []string{item("read-blocking-old", "ws1", "read", 1, ago(400))}},
		{id: "other-workspace", insert: []string{item("other-workspace", "ws2", "resolved", 0, ago(400))}},
	}
	sweepCase(t, Inbox, fixture, `SELECT id FROM inbox_items`)
}

func TestSweepChats(t *testing.T) {
	chat := func(id, ws, agent, at string) string {
		return `INSERT INTO chats (id, agent_id, workspace_id, created_at, updated_at, last_activity_at) VALUES ('` +
			id + `','` + agent + `','` + ws + `','` + at + `','` + at + `','` + at + `')`
	}
	old, recent := ago(40), ago(5)
	fixture := []row{
		{id: "idle", deleted: true, insert: []string{
			chat("idle", "ws1", "ag1", old),
			`INSERT INTO conversation_messages (id, session_id, agent_id, role, content) VALUES ('m1','idle','ag1','user','hello')`,
			`INSERT INTO chat_read_cursors (user_id, chat_id) VALUES ('u1','idle')`,
			`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, blocking, resolved_at) VALUES ('ib-idle','ws1','message','chat_reply_idle_u1','t','resolved',0,'` + old + `')`,
		}},
		{id: "idle-finished-run", deleted: true, insert: []string{
			`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('p-done','ws1','done','Done','{}','h')`,
			`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at) VALUES ('r-done','ws1','p-done','done','completed','` + old + `')`,
			chat("idle-finished-run", "ws1", "ag1", old),
			`UPDATE chats SET pipeline_run_id = 'r-done' WHERE id = 'idle-finished-run'`,
		}},
		{id: "recent", insert: []string{chat("recent", "ws1", "ag1", recent)}},
		{id: "delegated", insert: []string{
			chat("delegated", "ws1", "ag1", old),
			`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status) VALUES ('as1','ws1','delegated','ag1','ag1','x','COMPLETED')`,
		}},
		{id: "escalated", insert: []string{
			chat("escalated", "ws1", "ag1", old),
			`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at) VALUES ('e1','ws1','c1','escalated','ag1','why','PENDING','` + old + `')`,
		}},
		{id: "unread-reply", insert: []string{
			chat("unread-reply", "ws1", "ag1", old),
			`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state) VALUES ('ib-unread','ws1','message','chat_reply_unread-reply_u1','t','unread')`,
		}},
		{id: "pending-approval", insert: []string{
			chat("pending-approval", "ws1", "ag1", old),
			`INSERT INTO approvals_queue (id, workspace_id, requested_by, kind, reason, payload, status) VALUES ('ap1','ws1','ag1','tool_call','r','{"chat_id":"pending-approval"}','pending')`,
		}},
		{id: "running-run", insert: []string{
			`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('p-run','ws1','run','Run','{}','h')`,
			`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at) VALUES ('r-run','ws1','p-run','run','running','` + old + `')`,
			chat("running-run", "ws1", "ag1", old),
			`UPDATE chats SET pipeline_run_id = 'r-run' WHERE id = 'running-run'`,
		}},
		{id: "other-workspace", insert: []string{chat("other-workspace", "ws2", "ag2", old)}},
	}
	sweepCase(t, Chats, fixture, `SELECT id FROM chats`)
}

func TestSweepChatsTakesItsMessagesAlong(t *testing.T) {
	db := newDB(t)
	exec(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag1','ws1','Agent','agent')`)
	exec(t, db, `INSERT INTO chats (id, agent_id, workspace_id, created_at, updated_at, last_activity_at) VALUES ('c1','ag1','ws1',?,?,?)`, ago(40), ago(40), ago(40))
	exec(t, db, `INSERT INTO conversation_messages (id, session_id, agent_id, role, content) VALUES ('m1','c1','ag1','user','hello')`)
	exec(t, db, `INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, blocking, resolved_at) VALUES ('ib','ws1','message','chat_reply_c1_u1','t','resolved',0,?)`, ago(40))
	if _, _, err := SweepChats(context.Background(), db, "ws1", 30, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM conversation_messages WHERE session_id = 'c1'`) + count(t, db, `SELECT COUNT(*) FROM inbox_items WHERE id = 'ib'`); n != 0 {
		t.Fatalf("%d rows of a swept chat survived", n)
	}
}

func TestSweepKeeperDecisions(t *testing.T) {
	old, recent := ago(40), ago(5)
	req := func(id, agent, decision, decidedAt string) string {
		dec, at := "NULL", "NULL"
		if decision != "" {
			dec = "'" + decision + "'"
		}
		if decidedAt != "" {
			at = "'" + decidedAt + "'"
		}
		return `INSERT INTO keeper_requests (id, requesting_agent_id, intent, decision, decided_at, created_at) VALUES ('` +
			id + `','` + agent + `','x',` + dec + `,` + at + `,'` + old + `')`
	}
	event := func(id, req, ws string) string {
		return `INSERT INTO keeper_request_events (id, request_id, workspace_id, seq, state, recorded_at) VALUES ('` +
			id + `','` + req + `','` + ws + `',1,'ALLOW','` + old + `')`
	}
	lease := func(req, expires string) []string {
		return []string{
			`INSERT OR IGNORE INTO credentials (id, workspace_id, name, encrypted_value, created_by) VALUES ('cred-` + req + `','ws1','c-` + req + `','x','u1')`,
			`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, expires_at, lease_request_id) VALUES ('ac-` + req + `','ag1','cred-` + req + `','E_` + strings.ReplaceAll(req, "-", "_") + `','` + expires + `','` + req + `')`,
		}
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	fixture := []row{
		{id: "allow-old", deleted: true, insert: []string{req("allow-old", "ag1", "ALLOW", old), event("ev1", "allow-old", "ws1")}},
		{id: "deny-old", deleted: true, insert: []string{req("deny-old", "ag1", "DENY", old)}},
		{id: "expired-lease", deleted: true, insert: append([]string{req("expired-lease", "ag1", "ALLOW", old)}, lease("expired-lease", past)...)},
		{id: "allow-recent", insert: []string{req("allow-recent", "ag1", "ALLOW", recent)}},
		{id: "pending", insert: []string{req("pending", "ag1", "", "")}},
		{id: "pending-literal", insert: []string{req("pending-literal", "ag1", "PENDING", "")}},
		{id: "escalated", insert: []string{req("escalated", "ag1", "ESCALATE", old)}},
		{id: "live-lease", insert: append([]string{req("live-lease", "ag1", "ALLOW", old)}, lease("live-lease", future)...)},
		{id: "other-workspace", insert: []string{req("other-workspace", "ag2", "DENY", old)}},
	}
	sweepCase(t, KeeperDecisions, fixture, `SELECT id FROM keeper_requests`)
}

func TestSweepKeeperDecisionsTakesTheLedgerAlongAndReachesOrphans(t *testing.T) {
	db := newDB(t)
	old := ago(40)
	// An orphan: its agent is gone, only the ledger names the workspace.
	exec(t, db, `INSERT INTO keeper_requests (id, requesting_agent_id, intent, decision, decided_at) VALUES ('orphan', NULL, 'x', 'DENY', ?)`, old)
	exec(t, db, `INSERT INTO keeper_request_events (id, request_id, workspace_id, seq, state, recorded_at) VALUES ('ev','orphan','ws1',1,'DENY',?)`, old)
	n, _, err := SweepKeeperDecisions(context.Background(), db, "ws1", 30, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("swept %d, %v; want the orphan", n, err)
	}
	if left := count(t, db, `SELECT COUNT(*) FROM keeper_request_events`); left != 0 {
		t.Fatalf("%d ledger rows survived their request", left)
	}
}

func TestCountEveryExistingWindow(t *testing.T) {
	db := newDB(t)
	old, recent := ago(40), ago(5)
	exec(t, db, `INSERT INTO audit_logs (id, workspace_id, action, entity_type, created_at) VALUES ('a1','ws1','x','y',?), ('a2','ws1','x','y',?), ('a3','ws2','x','y',?)`, old, recent, old)
	exec(t, db, `INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, payload_ref, written_at) VALUES ('mv1','ws1','p','agent','s',1,'r',?), ('mv2','ws1','p','agent','s',1,'r',?)`, old, recent)
	exec(t, db, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES ('p1','ws1','p','P','{}','h')`)
	for i := 0; i < 12; i++ {
		exec(t, db, `INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at) VALUES (?, 'ws1', 'p1', 'p', 'completed', ?)`,
			fmt.Sprintf("run%02d", i), ago(40+i))
	}
	exec(t, db, `INSERT INTO approvals_queue (id, workspace_id, requested_by, kind, reason, status, decided_at) VALUES
		('ap1','ws1','x','tool_call','r','approved',?), ('ap2','ws1','x','autonomy_gate','r','approved',?), ('ap3','ws1','x','tool_call','r','pending',NULL)`,
		time.Now().Add(-40*24*time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"), time.Now().Add(-40*24*time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"))
	ctx := context.Background()
	for _, c := range []struct {
		key  Key
		want int64
	}{{Audit, 1}, {MemoryVersions, 1}, {CredentialAudit, 0}, {RoutineRuns, 2}, {Approvals, 1}, {PagePanelData, 0}} {
		got, err := Count(ctx, db, "ws1", c.key, days(30), time.Now())
		if err != nil {
			t.Fatalf("%s: %v", c.key, err)
		}
		if got != c.want {
			t.Errorf("%s count = %d, want %d", c.key, got, c.want)
		}
	}
	// The counts are what the existing sweeps then delete.
	if n, err := pipeline.SweepRunRetention(ctx, db, nil, "ws1", 30, pipeline.DefaultKeepLastNRunsPerPipeline); err != nil || n != 2 {
		t.Fatalf("run sweep deleted %d, %v; the preview said 2", n, err)
	}
	if n, _, err := harbormaster.SweepApprovalsRetention(ctx, db, "ws1", 30); err != nil || n != 1 {
		t.Fatalf("approvals sweep deleted %d, %v; the preview said 1", n, err)
	}
}
