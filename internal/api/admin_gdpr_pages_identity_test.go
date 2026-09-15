package api

// Art. 17 erasure vs the four Pages tables that name a user (issue #1976,
// defect 2).
//
// The contract under test: a workspace-scoped erasure promises the subject is
// UNNAMED IN THAT WORKSPACE. Before this, DeleteUserData transferred the
// subject's pages and stopped, leaving them named on page_versions
// (author_user_id), page_grants (granted_by_user_id / a user subject),
// page_public_tokens and page_webhooks (created_by_user_id). Every one of
// those columns carries an FK action that would have cleaned it up — three
// CASCADE, one SET NULL — and none of them ever fires, because a
// workspace-scoped erasure never deletes the users row.
//
// So these tests assert two halves of one sentence:
//   - in the erased workspace, none of the four tables names the subject;
//   - in a SECOND workspace, all four still do.
//
// Since #2308 the same rig also seeds every table eraseSubjectIdentity
// (admin_gdpr_erase_identity.go) reaches — see identityShapes — and the
// sweep at the bottom (subjectSightings) is what proves the whole list, not
// just the Pages four.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"context"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// pagesIdentityRig holds an AdminGDPRHandler over a real sqlite seeded with
// TWO workspaces, each carrying the same four shapes attributed to the same
// subject. Erasure runs against wsA only.
type pagesIdentityRig struct {
	h       *AdminGDPRHandler
	db      *sql.DB
	spy     *pagesJournalSpy
	adminID string
	userID  string
	other   string // a second user, whose rows must survive untouched
}

const (
	pidWSA = "pid-ws-a"
	pidWSB = "pid-ws-b"
)

func pagesIdentitySetup(t *testing.T) *pagesIdentityRig {
	t.Helper()
	dbh := testutil.MigratedDB(t)
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))

	pidExec(t, dbh.DB, `INSERT INTO workspaces (id, name, slug) VALUES (?,?,?),(?,?,?)`,
		pidWSA, "A", "pid-a", pidWSB, "B", "pid-b")
	pidExec(t, dbh.DB, `INSERT INTO users (id, email) VALUES
		('pid-admin','pid-admin@x'),('pid-subject','pid-subject@x'),('pid-other','pid-other@x')`)
	pidExec(t, dbh.DB, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES
		('pid-m1',?, 'pid-admin','OWNER'),
		('pid-m2',?, 'pid-subject','MEMBER'),
		('pid-m3',?, 'pid-admin','OWNER'),
		('pid-m4',?, 'pid-subject','MEMBER')`, pidWSA, pidWSA, pidWSB, pidWSB)

	h := NewAdminGDPRHandler(dbh.DB, silent, t.TempDir())
	spy := &pagesJournalSpy{}
	h.SetJournal(spy)
	r := &pagesIdentityRig{h: h, db: dbh.DB, spy: spy, adminID: "pid-admin", userID: "pid-subject", other: "pid-other"}
	r.seedWorkspaceShapes(t, pidWSA, "a")
	r.seedWorkspaceShapes(t, pidWSB, "b")
	return r
}

// seedWorkspaceShapes plants, in one workspace, one crew-owned page carrying
// every row shape this fix has to reach: a version the subject authored, a
// version somebody else authored, a grant the subject issued, a grant naming
// the subject as its user subject, a public token and a webhook the subject
// created. The page is owned by a CREW so the owner-transfer precondition
// (§7.1 rule 1b) is a no-op and these four tables are what the test observes.
func (r *pagesIdentityRig) seedWorkspaceShapes(t *testing.T, wsID, tag string) {
	t.Helper()
	crew := "pid-crew-" + tag
	page := "pid-page-" + tag
	panel := "pid-panel-" + tag

	pidExec(t, r.db, `INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
		VALUES (?,?,?,?, 'free', '[]')`, crew, wsID, "Crew "+tag, "crew-"+tag)
	pidExec(t, r.db, `INSERT INTO pages (id, workspace_id, slug, name, owner_crew_id, spec_json)
		VALUES (?,?,?,?,?, '{}')`, page, wsID, "page-"+tag, "Page "+tag, crew)
	pidExec(t, r.db, `INSERT INTO page_panels
		(id, page_id, panel_id, schema, owner_crew_id, producer_kind, producer_ref, sla_seconds)
		VALUES (?,?,?, 'status.v1', ?, 'script', 'script/watch.sh', 60)`, panel, page, "sensor", crew)

	pidExec(t, r.db, `INSERT INTO page_versions (page_id, seq, spec_json, author_user_id)
		VALUES (?, 1, '{}', ?), (?, 2, '{}', ?)`, page, r.userID, page, r.other)
	pidExec(t, r.db, `INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id)
		VALUES (?, 'crew', ?, 'read', ?), (?, 'user', ?, 'write', ?)`,
		page, crew, r.userID, // a grant the subject ISSUED
		page, r.userID, r.adminID) // a grant naming the subject as its SUBJECT
	pidExec(t, r.db, `INSERT INTO page_public_tokens (id, page_id, token_hash, expires_at, created_by_user_id)
		VALUES (?,?,?, '2999-01-01T00:00:00Z', ?)`, "pid-tok-"+tag, page, "hash-"+tag, r.userID)
	pidExec(t, r.db, `INSERT INTO page_webhooks (id, panel_id, token_hash, name, created_by_user_id)
		VALUES (?,?,?, 'cron', ?)`, "pid-wh-"+tag, panel, "whhash-"+tag, r.userID)

	r.seedIdentityShapes(t, wsID, tag, crew)
}

// identityShapes is every table eraseSubjectIdentity (#2308) reaches, with
// the scope key it reports under and the number of rows seedIdentityShapes
// plants for the subject in ONE workspace. It is the expected receipt of an
// erasure, and the list the sweep of the untouched workspace must still find
// by name — a table missing here is a table the rig no longer proves.
var identityShapes = map[string]struct {
	table string
	rows  int
}{
	"saved_views_removed":                    {"saved_views", 1},
	"user_notification_prefs_removed":        {"user_notification_prefs", 1},
	"notification_deliveries_removed":        {"notification_deliveries", 1},
	"notification_channels_removed":          {"notification_channels", 1}, // the scope='user' one
	"onboarding_proposals_removed":           {"onboarding_proposals", 1},
	"workspace_invitations_revoked":          {"workspace_invitations", 1},
	"trust_grants_revoked":                   {"waitpoint_trust_grants", 1}, // granted by the subject
	"trust_grants_anonymised":                {"waitpoint_trust_grants", 1}, // revoked by the subject
	"credentials_reattributed":               {"credentials", 1},
	"credential_bindings_anonymised":         {"credential_bindings", 1},
	"credential_rotations_anonymised":        {"credential_rotations", 1},
	"notification_channels_anonymised":       {"notification_channels", 1}, // the workspace one they created
	"notification_channel_agents_anonymised": {"notification_channel_agents", 1},
	"hooks_config_anonymised":                {"hooks_config", 1},
	"automations_anonymised":                 {"automations", 1},
	"recurring_issues_anonymised":            {"recurring_issues", 1},
	"composio_settings_anonymised":           {"composio_settings", 1},
	"agents_anonymised":                      {"agents", 1},
	"crews_anonymised":                       {"crews", 1},
	"pipelines_anonymised":                   {"pipelines", 1},
	"pipeline_versions_anonymised":           {"pipeline_versions", 1},
	"pipeline_runs_anonymised":               {"pipeline_runs", 1},
	"pending_runs_anonymised":                {"pending_runs", 1},
	"pipeline_waitpoints_anonymised":         {"pipeline_waitpoints", 1},
	"agent_runs_archive_anonymised":          {"agent_runs_archive", 1},
	"missions_anonymised":                    {"missions", 1},
	"mission_proposals_anonymised":           {"mission_proposals", 1},
	"mission_tasks_anonymised":               {"mission_tasks", 1},
	"mission_comments_anonymised":            {"mission_comments", 1},
	"mission_code_links_anonymised":          {"mission_code_links", 1},
	"checkpoints_anonymised":                 {"checkpoints", 1},
	"backup_catalog_anonymised":              {"backup_catalog", 1},
	"backup_restore_origins_anonymised":      {"backup_restore_origins", 1},
	"eval_runs_anonymised":                   {"eval_runs", 1},
	"workspace_files_anonymised":             {"workspace_files", 1},
	"attachments_anonymised":                 {"attachments", 1},
	"escalations_anonymised":                 {"escalations", 1},
	"gate_reward_history_anonymised":         {"gate_reward_history", 1},
	"keeper_governance_settings_anonymised":  {"keeper_governance_settings", 1},
	"memory_versions_anonymised":             {"memory_versions", 1},
	"inbox_items_anonymised":                 {"inbox_items", 1},
}

// seedIdentityShapes plants, in one workspace, one row per table of
// identityShapes naming the subject — and, where the verb is "anonymised",
// the same shape naming r.other, so the tests can prove the statement did
// not clear a stranger's attribution along the way. Rows are kept minimal:
// only the NOT NULL columns and the one naming the subject.
func (r *pagesIdentityRig) seedIdentityShapes(t *testing.T, wsID, tag, crew string) {
	t.Helper()
	u, o := r.userID, r.other
	id := func(kind string) string { return "pid-" + kind + "-" + tag }

	// ── the subject's own records ──
	pidExec(t, r.db, `INSERT INTO saved_views (id, workspace_id, user_id, name) VALUES (?,?,?,'mine'),(?,?,?,'theirs')`,
		id("sv"), wsID, u, id("sv-o"), wsID, o)
	pidExec(t, r.db, `INSERT INTO notification_channels (id, workspace_id, type, scope, owner_user_id, created_by)
		VALUES (?,?,'email','user',?,?), (?,?,'webhook','workspace',NULL,?), (?,?,'webhook','workspace',NULL,?)`,
		id("nc-own"), wsID, u, u, // the subject's personal channel — removed
		id("nc-ws"), wsID, u, // a workspace channel they created — anonymised
		id("nc-o"), wsID, o) // somebody else's — untouched
	pidExec(t, r.db, `INSERT INTO user_notification_prefs (id, workspace_id, user_id, category, channel_id, state)
		VALUES (?,?,?,'*',?,'immediate'), (?,?,?,'*',?,'immediate')`,
		id("np"), wsID, u, id("nc-ws"), id("np-o"), wsID, o, id("nc-ws"))
	pidExec(t, r.db, `INSERT INTO notification_deliveries (id, workspace_id, channel_id, user_id, category, dedup_key)
		VALUES (?,?,?,?,'issues.created',?), (?,?,?,?,'issues.created',?)`,
		id("nd"), wsID, id("nc-ws"), u, "dk-"+tag, id("nd-o"), wsID, id("nc-ws"), o, "dk-o-"+tag)
	pidExec(t, r.db, `INSERT INTO onboarding_proposals (id, workspace_id, created_by, payload_json) VALUES (?,?,?,'{}')`,
		id("op"), wsID, u)

	// ── capabilities ──
	pidExec(t, r.db, `INSERT INTO workspace_invitations (id, workspace_id, email, invited_by, token, expires_at)
		VALUES (?,?,'new@x',?,?,'2999-01-01T00:00:00Z')`, id("inv"), wsID, u, "tok-"+tag)
	pidExec(t, r.db, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, author_user_id)
		VALUES (?,?,?,?,'{}','h1',?)`, id("pl"), wsID, "routine-"+tag, "Routine "+tag, u)
	pidExec(t, r.db, `INSERT INTO waitpoint_trust_grants (id, workspace_id, pipeline_id, step_id, definition_hash, granted_by_user_id, revoked_at, revoked_by_user_id)
		VALUES (?,?,?,'gate','h1',?,NULL,NULL), (?,?,?,'gate','h1',?,'2026-01-01T00:00:00Z',?)`,
		id("tg"), wsID, id("pl"), u, // issued by the subject, live — revoked
		id("tg-o"), wsID, id("pl"), o, u) // issued by another, withdrawn by the subject — anonymised
	pidExec(t, r.db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by, created_by_actor_id, approved_by_user_id)
		VALUES (?,?,?,'enc',?,?,?), (?,?,?,'enc',?,?,NULL)`,
		id("cr"), wsID, "cred-"+tag, u, u, u,
		id("cr-o"), wsID, "cred-o-"+tag, o, o)
	pidExec(t, r.db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, scope, slot, created_by)
		VALUES (?,?,?,'WORKSPACE','TOKEN',?)`, id("cb"), wsID, id("cr"), u)
	pidExec(t, r.db, `INSERT INTO credential_rotations (id, credential_id, old_value, expires_at, rotated_by)
		VALUES (?,?,'old','2999-01-01T00:00:00Z',?)`, id("rot"), id("cr"), u)
	pidExec(t, r.db, `INSERT INTO agents (id, workspace_id, crew_id, name, slug, created_by_user_id, self_learning_set_by_user_id)
		VALUES (?,?,?,?,?,?,?), (?,?,?,?,?,?,NULL)`,
		id("ag"), wsID, crew, "Agent "+tag, "agent-"+tag, u, u,
		id("ag-o"), wsID, crew, "Other "+tag, "other-"+tag, o)
	pidExec(t, r.db, `INSERT INTO notification_channel_agents (id, workspace_id, channel_id, agent_id, granted_by)
		VALUES (?,?,?,?,?)`, id("nca"), wsID, id("nc-ws"), id("ag"), u)
	pidExec(t, r.db, `INSERT INTO hooks_config (id, workspace_id, event, handler_kind, created_by) VALUES (?,?,'run.failed','shell',?)`,
		id("hk"), wsID, u)
	pidExec(t, r.db, `INSERT INTO automations (id, workspace_id, name, event_type, action_kind, created_by, created_at, updated_at)
		VALUES (?,?,'auto','issue.created','issue',?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id("au"), wsID, u)
	pidExec(t, r.db, `INSERT INTO recurring_issues (id, workspace_id, crew_id, title, cron_expression, created_by)
		VALUES (?,?,?,'weekly','0 9 * * 1',?)`, id("ri"), wsID, crew, u)
	pidExec(t, r.db, `INSERT INTO composio_settings (workspace_id, encrypted_api_key, created_by, default_user_id)
		VALUES (?,'enc',?,?)`, wsID, u, u)

	// ── history ──
	pidExec(t, r.db, `UPDATE crews SET autonomy_set_by_user_id = ? WHERE id = ?`, u, crew)
	pidExec(t, r.db, `INSERT INTO pipeline_versions (id, pipeline_id, version, definition_json, definition_hash, author_type, author_id)
		VALUES (?,?,1,'{}','h1','user',?), (?,?,2,'{"v":2}','h2','user',?)`,
		id("pv"), id("pl"), u, id("pv-o"), id("pl"), o)
	pidExec(t, r.db, `INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, invoking_user_id)
		VALUES (?,?,?,?,'completed','2026-01-01T00:00:00Z',?)`, id("pr"), wsID, id("pl"), "routine-"+tag, u)
	pidExec(t, r.db, `INSERT INTO pending_runs (id, workspace_id, pipeline_id, pipeline_slug, fire_at, invoking_user_id)
		VALUES (?,?,?,?,'2999-01-01T00:00:00Z',?)`, id("pn"), wsID, id("pl"), "routine-"+tag, u)
	pidExec(t, r.db, `INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, timeout_at, status, decided_by_user_id)
		VALUES (?,?,?,'gate','approval','2999-01-01T00:00:00Z','approved',?)`, id("wp"), wsID, id("pr"), u)
	pidExec(t, r.db, `INSERT INTO agent_runs_archive (id, workspace_id, agent_id, triggered_by) VALUES (?,?,?,?)`,
		id("ara"), wsID, id("ag"), u)
	pidExec(t, r.db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, created_by_user_id)
		VALUES (?,?,?,?,?,'issue',?)`, id("mi"), wsID, crew, id("ag"), "trace-"+tag, u)
	pidExec(t, r.db, `INSERT INTO mission_proposals (id, workspace_id, title, reviewed_by) VALUES (?,?,'proposal',?)`,
		id("mp"), wsID, u)
	pidExec(t, r.db, `INSERT INTO mission_tasks (id, mission_id, title, approved_by) VALUES (?,?,'task',?)`,
		id("mt"), id("mi"), u)
	pidExec(t, r.db, `INSERT INTO mission_comments (id, mission_id, author_type, author_id, body)
		VALUES (?,?,'user',?,'hi'), (?,?,'user',?,'hello')`, id("mc"), id("mi"), u, id("mc-o"), id("mi"), o)
	pidExec(t, r.db, `INSERT INTO mission_code_links (id, workspace_id, mission_id, provider, host, owner, repo, number, url, created_by_user_id)
		VALUES (?,?,?,'GITHUB','github.com','acme','app',7,'https://github.com/acme/app/pull/7',?)`, id("mcl"), wsID, id("mi"), u)
	pidExec(t, r.db, `INSERT INTO checkpoints (id, workspace_id, journal_cursor, created_by) VALUES (?,?,'0',?)`, id("cp"), wsID, u)
	pidExec(t, r.db, `INSERT INTO backup_catalog (id, file_path, scope, workspace_id, created_at, created_by, size, sha256, encrypted, format_version)
		VALUES (?,?,'workspace',?,'2026-01-01T00:00:00Z',?,1,'x',0,1)`, id("bc"), "/b/"+tag+".tar", wsID, u)
	pidExec(t, r.db, `INSERT INTO backup_restore_origins (workspace_id, bundle_sha256, restored_at, restored_by)
		VALUES (?,'x','2026-01-01T00:00:00Z',?)`, wsID, u)
	pidExec(t, r.db, `INSERT INTO eval_runs (id, workspace_id, kind, created_by) VALUES (?,?,'replay',?)`, id("ev"), wsID, u)
	pidExec(t, r.db, `INSERT INTO workspace_files (id, workspace_id, rel_path, created_by) VALUES (?,?,?,?)`,
		id("wf"), wsID, "notes-"+tag+".md", u)
	pidExec(t, r.db, `INSERT INTO attachments (id, workspace_id, owner_type, mission_id, filename, content_type, size_bytes, sha256, storage_key, uploaded_by_user_id)
		VALUES (?,?,'issue',?,'a.png','image/png',1,?,?,?)`,
		id("at"), wsID, id("mi"), strings.Repeat("a", 64), "k-"+tag, u)
	pidExec(t, r.db, `INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, created_at, status, resolved_by)
		VALUES (?,?,?,?,?,'help','2026-01-01T00:00:00Z','RESOLVED',?)`, id("es"), wsID, crew, "chat-"+tag, id("ag"), u)
	pidExec(t, r.db, `INSERT INTO gate_reward_history (id, workspace_id, tool_name, args_hash, outcome, decided_by)
		VALUES (?,?,'bash','h','approved',?)`, id("gr"), wsID, u)
	pidExec(t, r.db, `INSERT INTO keeper_governance_settings (workspace_id, security_contact_user_id, updated_by) VALUES (?,?,?)`,
		wsID, u, u)
	// data_subject_id deliberately NULL on both: these rows survive the
	// cascade's own delete and are what the anonymise step has to reach.
	pidExec(t, r.db, `INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, payload_ref, written_by)
		VALUES (?,?,'crew/notes.md','crew','s',1,'ref',?)`, id("mv"), wsID, u)
	pidExec(t, r.db, `INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, target_user_id, read_by_user_id, resolved_by_user_id)
		VALUES (?,?,'message',?,'ping',?,?,?)`, id("ii"), wsID, "src-"+tag, u, u, u)
}

func pidExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("seed (%s): %v", strings.SplitN(strings.TrimSpace(q), "\n", 2)[0], err)
	}
}

// erase runs the Art. 17 cascade against one workspace as the rig's admin and
// returns the decoded response body.
func (r *pagesIdentityRig) erase(t *testing.T, wsID string) map[string]any {
	t.Helper()
	return r.eraseAs(t, wsID, r.adminID, http.StatusAccepted)
}

// eraseAs is erase with the acting admin and the expected status spelled out.
func (r *pagesIdentityRig) eraseAs(t *testing.T, wsID, actorID string, wantStatus int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/", strings.NewReader(`{"reason":"SAR #1976"}`))
	req.SetPathValue("userId", r.userID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: actorID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, req.WithContext(ctx))
	if rec.Code != wantStatus {
		t.Fatalf("erasure: status %d, want %d; body=%s", rec.Code, wantStatus, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode erasure response: %v (%s)", err, rec.Body.String())
	}
	return out
}

// countIn answers "how many rows of this shape does workspace wsID still
// hold". Every query joins through pages.workspace_id (or page_panels →
// pages) because none of these four tables carries a workspace column of its
// own — which is exactly why the fix has to scope every statement the same
// way.
func (r *pagesIdentityRig) countIn(t *testing.T, wsID, what string) int {
	t.Helper()
	// The count of subject mentions this workspace still holds, per shape.
	// "page_versions_total" is the odd one out — it counts rows regardless of
	// author, to prove anonymising did not turn into deleting — so it takes
	// the workspace alone and is handled separately below rather than being
	// padded with a dummy predicate to fit one binding convention.
	if what == "page_versions_total" {
		var n int
		if err := r.db.QueryRow(`SELECT COUNT(*) FROM page_versions v JOIN pages p ON p.id = v.page_id
			WHERE p.workspace_id = ?`, wsID).Scan(&n); err != nil {
			t.Fatalf("count page_versions rows in %s: %v", wsID, err)
		}
		return n
	}
	queries := map[string]string{
		"page_versions": `SELECT COUNT(*) FROM page_versions v JOIN pages p ON p.id = v.page_id
			WHERE p.workspace_id = ? AND v.author_user_id = ?`,
		"page_grants": `SELECT COUNT(*) FROM page_grants g JOIN pages p ON p.id = g.page_id
			WHERE p.workspace_id = ? AND (g.granted_by_user_id = ? OR (g.subject_type = 'user' AND g.subject_id = ?))`,
		"page_public_tokens": `SELECT COUNT(*) FROM page_public_tokens tk JOIN pages p ON p.id = tk.page_id
			WHERE p.workspace_id = ? AND tk.created_by_user_id = ?`,
		"page_webhooks": `SELECT COUNT(*) FROM page_webhooks wh
			JOIN page_panels pl ON pl.id = wh.panel_id
			JOIN pages p ON p.id = pl.page_id
			WHERE p.workspace_id = ? AND wh.created_by_user_id = ?`,
	}
	q, ok := queries[what]
	if !ok {
		t.Fatalf("countIn: unknown shape %q", what)
	}
	args := []any{wsID, r.userID}
	if what == "page_grants" {
		args = append(args, r.userID)
	}
	var n int
	if err := r.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %s in %s: %v", what, wsID, err)
	}
	return n
}

// ── The contract: unnamed in the erased workspace, untouched elsewhere ──

func TestGDPRErasure_PageTablesNoLongerNameSubject(t *testing.T) {
	r := pagesIdentitySetup(t)

	// Sanity: every shape is present in both workspaces before erasure, so a
	// green assertion below cannot be an empty-table tautology.
	for _, ws := range []string{pidWSA, pidWSB} {
		for _, shape := range []string{"page_versions", "page_grants", "page_public_tokens", "page_webhooks"} {
			want := 1
			if shape == "page_grants" {
				want = 2 // one issued by the subject, one naming them as subject
			}
			if got := r.countIn(t, ws, shape); got != want {
				t.Fatalf("pre-erasure %s in %s = %d, want %d", shape, ws, got, want)
			}
		}
	}

	r.erase(t, pidWSA)

	for _, shape := range []string{"page_versions", "page_grants", "page_public_tokens", "page_webhooks"} {
		if got := r.countIn(t, pidWSA, shape); got != 0 {
			t.Errorf("after erasure, %s still names the subject in the erased workspace: %d row(s)", shape, got)
		}
	}

	// History stays: the version row survives, only its authorship is gone,
	// and the version somebody else authored is untouched.
	if got := r.countIn(t, pidWSA, "page_versions_total"); got != 2 {
		t.Errorf("page_versions rows in the erased workspace = %d, want 2 — anonymise, never delete", got)
	}
	var otherAuthored int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM page_versions v JOIN pages p ON p.id = v.page_id
		WHERE p.workspace_id = ? AND v.author_user_id = ?`, pidWSA, r.other).Scan(&otherAuthored); err != nil {
		t.Fatalf("count other author: %v", err)
	}
	if otherAuthored != 1 {
		t.Errorf("another user's authorship was cleared too: %d, want 1", otherAuthored)
	}

	// The page itself is never deleted by this cascade (§7.1 rule 1b), and
	// neither is its panel — the webhook goes, the panel stays.
	var pages, panels int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, pidWSA).Scan(&pages); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM page_panels pl JOIN pages p ON p.id = pl.page_id
		WHERE p.workspace_id = ?`, pidWSA).Scan(&panels); err != nil {
		t.Fatalf("count panels: %v", err)
	}
	if pages != 1 || panels != 1 {
		t.Errorf("erasure removed page/panel rows: pages=%d panels=%d, want 1/1", pages, panels)
	}
}

// Unlike the three tests around it, this one passes on unfixed code too — an
// erasure that deletes nothing anywhere trivially deletes nothing in workspace
// B. That is what a blast-radius guard looks like: it is not the regression
// test for #1976 (its neighbours are), it is the test that stops the fix for
// #1976 from becoming a bigger bug than the defect.
func TestGDPRErasure_PageTablesInOtherWorkspacesSurvive(t *testing.T) {
	r := pagesIdentitySetup(t)
	r.erase(t, pidWSA)

	for _, shape := range []string{"page_versions", "page_grants", "page_public_tokens", "page_webhooks"} {
		want := 1
		if shape == "page_grants" {
			want = 2
		}
		if got := r.countIn(t, pidWSB, shape); got != want {
			t.Errorf("erasing workspace A changed %s in workspace B: %d row(s), want %d — every statement must be workspace-scoped",
				shape, got, want)
		}
	}
}

// ── The audit row has to say what it did, per table ─────────────────────

func TestGDPRErasure_PageTableCountsInAuditScope(t *testing.T) {
	r := pagesIdentitySetup(t)
	body := r.erase(t, pidWSA)

	scope, _ := body["scope"].(map[string]any)
	if scope == nil {
		t.Fatalf("response carries no scope: %v", body)
	}
	want := map[string]float64{
		"page_versions_anonymised":   1,
		"page_grants_removed":        2,
		"page_public_tokens_revoked": 1,
		"page_webhooks_revoked":      1,
	}
	for key, n := range want {
		got, ok := scope[key].(float64)
		if !ok {
			t.Errorf("scope has no %q key — the operator cannot see what the erasure did: %v", key, scope)
			continue
		}
		if got != n {
			t.Errorf("scope[%q] = %v, want %v", key, got, n)
		}
	}

	// The same counts have to reach the durable audit row, not just the
	// response — the gdpr_actions row is the operator's artefact.
	var scopeJSON string
	if err := r.db.QueryRow(`SELECT COALESCE(scope_json,'') FROM gdpr_actions
		WHERE workspace_id = ? AND data_subject_id = ? AND action = 'delete'`,
		pidWSA, r.userID).Scan(&scopeJSON); err != nil {
		t.Fatalf("load gdpr_actions row: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal([]byte(scopeJSON), &persisted); err != nil {
		t.Fatalf("decode scope_json %q: %v", scopeJSON, err)
	}
	for key, n := range want {
		if got, _ := persisted[key].(float64); got != n {
			t.Errorf("gdpr_actions.scope_json[%q] = %v, want %v", key, got, n)
		}
	}

	// Anonymising is not deleting: rows_deleted counts rows that went away,
	// and a version whose author was cleared did not. 4 from the Pages
	// tables, plus the seven rows seedIdentityShapes plants that the #2308
	// step DELETES (their own five records, an invitation, a trust grant);
	// the thirty-odd anonymised and the re-attributed credential are not in
	// it.
	if got, _ := body["rows_deleted"].(float64); got != 11 {
		t.Errorf("rows_deleted = %v, want 11 (2 grants + 1 token + 1 webhook + 7 #2308 deletes; anonymised rows are not deletions)", got)
	}
}

// ── Who else still names the subject after an erasure ───────────────────

// subjectSightings walks the LIVE schema — not a hand-written list that
// silently rots when a migration adds a column — and reports every table that
// still names userID inside wsID. It is the mechanical answer to "is the
// subject unnamed in this workspace", and it is what stops the next table
// with a created_by_user_id from repeating #1976 unnoticed.
//
// Scoping mirrors the cascade's own rule: a workspace_id column when the
// table has one, else the pages/page_panels chain, else the table is reported
// with an "(unscoped)" marker so a reviewer can see the count is global.
func subjectSightings(t *testing.T, db *sql.DB, wsID, userID string) map[string]int {
	t.Helper()

	tables := []string{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	_ = rows.Close()

	out := map[string]int{}
	for _, table := range tables {
		cols, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("table_info %s: %v", table, err)
		}
		var (
			named    []string
			hasWS    bool
			hasPage  bool
			hasPanel bool
			// One-hop parents that carry a workspace_id of their own; the
			// #2308 tables without a workspace column reach it through
			// exactly one of these.
			parent string
		)
		for cols.Next() {
			var c string
			if err := cols.Scan(&c); err != nil {
				t.Fatalf("scan column of %s: %v", table, err)
			}
			switch c {
			case "workspace_id":
				hasWS = true
			case "page_id":
				hasPage = true
			case "panel_id":
				hasPanel = true
			case "pipeline_id", "mission_id", "credential_id":
				if parent == "" {
					parent = c
				}
			}
			if columnCanNameAUser(c) {
				named = append(named, c)
			}
		}
		if err := cols.Err(); err != nil {
			t.Fatalf("iterate columns of %s: %v", table, err)
		}
		_ = cols.Close()
		if len(named) == 0 {
			continue
		}

		var preds []string
		args := []any{}
		for _, c := range named {
			preds = append(preds, fmt.Sprintf("tbl.%q = ?", c))
			args = append(args, userID)
		}
		where := "(" + strings.Join(preds, " OR ") + ")"
		label := table
		// The page_id / panel_id arms assume those columns mean the Pages
		// chain, which is true of every table in the schema today. A future
		// unrelated `page_id` would be scoped against the wrong parent and
		// silently under-count — i.e. this sweep fails OPEN, which is the
		// wrong direction. If that day comes, key the arms on the table name.
		switch {
		case hasWS:
			where += " AND tbl.workspace_id = ?"
			args = append(args, wsID)
		case hasPage:
			where += " AND tbl.page_id IN (SELECT id FROM pages WHERE workspace_id = ?)"
			args = append(args, wsID)
		case hasPanel:
			where += ` AND tbl.panel_id IN (SELECT pl.id FROM page_panels pl
				JOIN pages p ON p.id = pl.page_id WHERE p.workspace_id = ?)`
			args = append(args, wsID)
		case parent != "":
			// Same caveat as page_id above: assumes pipeline_id / mission_id
			// / credential_id mean those three parents, true of every table
			// today.
			parentTable := map[string]string{
				"pipeline_id": "pipelines", "mission_id": "missions", "credential_id": "credentials",
			}[parent]
			where += fmt.Sprintf(" AND tbl.%q IN (SELECT id FROM %s WHERE workspace_id = ?)", parent, parentTable)
			args = append(args, wsID)
		default:
			label = table + " (unscoped)"
		}

		var n int
		q := fmt.Sprintf("SELECT COUNT(*) FROM %q AS tbl WHERE %s", table, where)
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatalf("scan %s for the subject: %v (%s)", table, err, q)
		}
		if n > 0 {
			out[label] = n
		}
	}
	return out
}

// columnCanNameAUser is the pattern half of the sweep: which column names
// plausibly hold a user id. Deliberately generous — a false positive costs a
// line in an allow-list with a reason next to it, a false negative is another
// #1976. The suffix rules carry most of the weight (`_by` alone subsumes
// created_by, decided_by, invited_by and every sibling that has not been
// invented yet); the explicit names are the ones no suffix reaches.
//
// KNOWN LIMIT, so nobody reads a green sweep as more than it is: this matches
// on the user's ID. A table that names the subject by slug or email —
// peer_cards.user_slug and user_models.user_slug are live examples, both
// erased today by other means — is invisible to it.
func columnCanNameAUser(col string) bool {
	switch col {
	// "userId" is not a typo: the NextAuth-era tables (accounts, sessions)
	// spell it camelCase, and a sweep that only knows snake_case would call
	// them clean without ever having looked.
	case "userId":
		return true
	case "subject_id", "actor_id", "author_id", "user_id", "data_subject_id",
		"owner_id", "member_id", "sender_id", "recipient_id",
		"assigned_to", "author":
		return true
	}
	return strings.HasSuffix(col, "_by") ||
		strings.HasSuffix(col, "_user_id") ||
		strings.HasSuffix(col, "_by_id") ||
		strings.HasSuffix(col, "_by_user_id")
}

// TestGDPRErasure_NoPageTableStillNamesTheSubject is the sweep applied to the
// rows this rig actually seeds. It fails if ANY table in the erased workspace
// still names the subject, except the ones listed below with the reason they
// are allowed to.
func TestGDPRErasure_NoPageTableStillNamesTheSubject(t *testing.T) {
	r := pagesIdentitySetup(t)
	r.erase(t, pidWSA)

	// Deliberate survivors, each for a reason the file header of
	// admin_gdpr.go argues at length:
	//   gdpr_actions / journal_entries / journal_entries_archived / audit_logs
	//                  — append-only accountability records. "A SAR does not
	//                    erase the SAR itself." Listed even though this rig's
	//                    journal is a spy that writes no rows: the day someone
	//                    wires a real emitter in, the test should keep testing
	//                    what it means to test rather than fail on a survivor
	//                    the docs already promise.
	//   workspace_members — membership is removed by RemoveMember, not by an
	//                    Art. 17 erasure; the two are separate operations and
	//                    an erasure that silently evicted the subject would be
	//                    doing something the operator did not ask for.
	allowed := map[string]bool{
		"gdpr_actions":             true,
		"journal_entries":          true,
		"journal_entries_archived": true,
		"audit_logs":               true,
		"workspace_members":        true,
	}

	sightings := subjectSightings(t, r.db, pidWSA, r.userID)
	var offenders []string
	for table, n := range sightings {
		if allowed[strings.TrimSuffix(table, " (unscoped)")] {
			continue
		}
		offenders = append(offenders, fmt.Sprintf("%s (%d row(s))", table, n))
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("after erasure the subject is still named in workspace %s by: %s",
			pidWSA, strings.Join(offenders, ", "))
	}

	// And the same sweep in the untouched workspace must still find every one
	// of the four by name — otherwise the sweep above is passing because the
	// seed never landed, or because a statement reached across the workspace
	// boundary. A count threshold would tolerate exactly one table going
	// missing, which is the failure most worth catching.
	elsewhere := subjectSightings(t, r.db, pidWSB, r.userID)
	mustFind := []string{"page_versions", "page_grants", "page_public_tokens", "page_webhooks"}
	for _, shape := range identityShapes {
		mustFind = append(mustFind, shape.table)
	}
	sort.Strings(mustFind)
	for _, table := range mustFind {
		if elsewhere[table] == 0 {
			t.Errorf("sweep of the untouched workspace no longer finds %s — the seed did not land, or workspace A's erasure reached into B: %v",
				table, elsewhere)
		}
	}
}

// ── #2308: the rest of the schema, per table and per verb ────────────────

// The receipt has to say, per table, what the erasure did — and the same
// counts have to reach gdpr_actions.scope_json, which is the artefact the
// operator keeps. identityShapes is the expected receipt: one row per table
// naming the subject, so every key is expected at exactly that count.
func TestGDPRErasure_IdentityCountsInAuditScope(t *testing.T) {
	r := pagesIdentitySetup(t)
	body := r.erase(t, pidWSA)

	scope, _ := body["scope"].(map[string]any)
	if scope == nil {
		t.Fatalf("response carries no scope: %v", body)
	}
	var scopeJSON string
	if err := r.db.QueryRow(`SELECT COALESCE(scope_json,'') FROM gdpr_actions
		WHERE workspace_id = ? AND data_subject_id = ? AND action = 'delete'`,
		pidWSA, r.userID).Scan(&scopeJSON); err != nil {
		t.Fatalf("load gdpr_actions row: %v", err)
	}
	var persisted map[string]any
	if err := json.Unmarshal([]byte(scopeJSON), &persisted); err != nil {
		t.Fatalf("decode scope_json %q: %v", scopeJSON, err)
	}
	for _, key := range identityScopeKeys() {
		want, ok := identityShapes[key]
		if !ok {
			t.Errorf("identityStep %q has no row in the rig — the sweep cannot prove it", key)
			continue
		}
		got, ok := scope[key].(float64)
		if !ok {
			t.Errorf("scope has no %q key: %v", key, scope)
			continue
		}
		if int(got) != want.rows {
			t.Errorf("scope[%q] = %v, want %d", key, got, want.rows)
		}
		if p, _ := persisted[key].(float64); int(p) != want.rows {
			t.Errorf("gdpr_actions.scope_json[%q] = %v, want %d", key, p, want.rows)
		}
	}
	for key := range identityShapes {
		if _, ok := scope[key]; !ok {
			t.Errorf("rig seeds %q but no identityStep reports it", key)
		}
	}
}

// Anonymised means the row is still there; revoked means it is not; and
// neither touches a stranger's rows in the same table. One assertion per
// shape that could plausibly be got wrong — the sweep proves the subject is
// gone, this proves the erasure did not take the workspace with them.
func TestGDPRErasure_IdentityVerbsAreTheRightOnes(t *testing.T) {
	r := pagesIdentitySetup(t)
	r.erase(t, pidWSA)

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := r.db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatalf("count (%s): %v", q, err)
		}
		return n
	}

	// History survives without its author; the other author is untouched.
	if n := count(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id = 'pid-pl-a'`); n != 2 {
		t.Errorf("pipeline_versions in A = %d, want 2 — anonymise, never delete", n)
	}
	if n := count(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id = 'pid-pl-a' AND author_type = 'user' AND author_id = ''`); n != 1 {
		t.Errorf("anonymised pipeline_versions = %d, want 1 (author_type stays 'user', author_id becomes '')", n)
	}
	if n := count(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id = 'pid-pl-a' AND author_id = ?`, r.other); n != 1 {
		t.Errorf("another user's pipeline version authorship was cleared: %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM mission_comments WHERE mission_id = 'pid-mi-a'`); n != 2 {
		t.Errorf("mission_comments in A = %d, want 2", n)
	}
	if n := count(`SELECT COUNT(*) FROM mission_comments WHERE mission_id = 'pid-mi-a' AND author_id = ?`, r.other); n != 1 {
		t.Errorf("another user's comment authorship was cleared: %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND created_by_user_id = ?`, pidWSA, r.other); n != 1 {
		t.Errorf("another user's agent attribution was cleared: %d, want 1", n)
	}
	if n := count(`SELECT COUNT(*) FROM saved_views WHERE workspace_id = ? AND user_id = ?`, pidWSA, r.other); n != 1 {
		t.Errorf("another user's saved view was deleted: %d, want 1", n)
	}

	// The subject's personal channel is gone; the workspace channel they
	// created stays, unnamed; the stranger's channel is untouched.
	if n := count(`SELECT COUNT(*) FROM notification_channels WHERE id = 'pid-nc-own-a'`); n != 0 {
		t.Errorf("the subject's personal channel survived the erasure")
	}
	if n := count(`SELECT COUNT(*) FROM notification_channels WHERE id = 'pid-nc-ws-a' AND created_by IS NULL`); n != 1 {
		t.Errorf("the workspace channel the subject created was deleted or still names them")
	}
	if n := count(`SELECT COUNT(*) FROM notification_channels WHERE id = 'pid-nc-o-a' AND created_by = ?`, r.other); n != 1 {
		t.Errorf("a stranger's channel lost its creator")
	}

	// The grant the subject issued is gone; the one they merely withdrew
	// stays, with the withdrawer cleared and the issuer intact.
	if n := count(`SELECT COUNT(*) FROM waitpoint_trust_grants WHERE id = 'pid-tg-a'`); n != 0 {
		t.Errorf("a trust grant the subject issued survived the erasure")
	}
	if n := count(`SELECT COUNT(*) FROM waitpoint_trust_grants WHERE id = 'pid-tg-o-a' AND granted_by_user_id = ? AND revoked_by_user_id IS NULL`, r.other); n != 1 {
		t.Errorf("the grant the subject withdrew was deleted, or still names them as the withdrawer")
	}

	// Credentials: still there, still bound, with the erasing admin as
	// custodian and the hand-over on the credential's own timeline.
	var createdBy string
	var actorID, approvedBy sql.NullString
	if err := r.db.QueryRow(`SELECT created_by, created_by_actor_id, approved_by_user_id FROM credentials WHERE id = 'pid-cr-a'`).
		Scan(&createdBy, &actorID, &approvedBy); err != nil {
		t.Fatalf("the subject's credential is gone — it must be re-attributed, never deleted: %v", err)
	}
	if createdBy != r.adminID || actorID.Valid || approvedBy.Valid {
		t.Errorf("credential attribution after erasure = created_by %q actor %v approved_by %v; want custodian %q, NULL, NULL",
			createdBy, actorID, approvedBy, r.adminID)
	}
	if n := count(`SELECT COUNT(*) FROM credential_bindings WHERE credential_id = 'pid-cr-a' AND created_by IS NULL`); n != 1 {
		t.Errorf("the credential's binding was deleted or still names the subject")
	}
	if n := count(`SELECT COUNT(*) FROM credentials WHERE id = 'pid-cr-o-a' AND created_by = ?`, r.other); n != 1 {
		t.Errorf("a stranger's credential changed hands")
	}
	var meta string
	if err := r.db.QueryRow(`SELECT COALESCE(metadata_json,'') FROM credential_audit
		WHERE credential_id = 'pid-cr-a' AND event_type = ?`, string(AuditEventReattributed)).Scan(&meta); err != nil {
		t.Fatalf("no REATTRIBUTED event on the credential's timeline: %v", err)
	}
	if !strings.Contains(meta, r.adminID) || strings.Contains(meta, r.userID) {
		t.Errorf("REATTRIBUTED metadata should name the custodian and never the subject: %s", meta)
	}

	// The revoked grant is journalled the way the ordinary revoke path does
	// it, with the routine and gate in Refs and never the subject's id.
	var trustEntries []journal.Entry
	for _, e := range r.spy.entries {
		if e.Type == journal.EntryTrustRevoked {
			trustEntries = append(trustEntries, e)
		}
	}
	if len(trustEntries) != 1 {
		t.Fatalf("approval.trust_revoked entries = %d, want 1 (the withdrawn grant changed no gate and gets none)", len(trustEntries))
	}
	e := trustEntries[0]
	if e.ActorID != r.adminID || e.Refs["trust_grant_id"] != "pid-tg-a" || e.Refs["step_id"] != "gate" || e.Payload["definition_hash"] != "h1" {
		t.Errorf("trust revocation entry is not shaped like the ordinary one: actor=%q refs=%v payload=%v", e.ActorID, e.Refs, e.Payload)
	}
}

// The custodian rule for credentials: the admin running the erasure takes
// them over — unless they are erasing themself, when the oldest other OWNER
// does; and when there is nobody, the identity step refuses whole rather
// than leaving a credential half-attributed.
func TestGDPRErasure_CredentialCustodian(t *testing.T) {
	t.Run("self-erasure falls back to another owner", func(t *testing.T) {
		r := pagesIdentitySetup(t)
		// The subject is an OWNER erasing themself; pid-admin is the other OWNER.
		pidExec(t, r.db, `UPDATE workspace_members SET role = 'OWNER' WHERE user_id = ? AND workspace_id = ?`, r.userID, pidWSA)
		r.eraseAs(t, pidWSA, r.userID, http.StatusAccepted)
		var createdBy string
		if err := r.db.QueryRow(`SELECT created_by FROM credentials WHERE id = 'pid-cr-a'`).Scan(&createdBy); err != nil {
			t.Fatal(err)
		}
		if createdBy != r.adminID {
			t.Errorf("custodian = %q, want the other OWNER %q", createdBy, r.adminID)
		}
	})
	t.Run("no possible custodian refuses the whole identity step", func(t *testing.T) {
		r := pagesIdentitySetup(t)
		pidExec(t, r.db, `UPDATE workspace_members SET role = 'OWNER' WHERE user_id = ? AND workspace_id = ?`, r.userID, pidWSA)
		pidExec(t, r.db, `DELETE FROM workspace_members WHERE user_id = ? AND workspace_id = ?`, r.adminID, pidWSA)
		body := r.eraseAs(t, pidWSA, r.userID, http.StatusMultiStatus)
		if msg, _ := body["error"].(string); !strings.Contains(msg, "custody") {
			t.Errorf("207 error does not explain the refusal: %v", body)
		}
		// Nothing in the step was applied: the credential still names the
		// subject AND so does the saved view the same transaction would have
		// deleted first — a rollback, not a partial run.
		var createdBy string
		if err := r.db.QueryRow(`SELECT created_by FROM credentials WHERE id = 'pid-cr-a'`).Scan(&createdBy); err != nil || createdBy != r.userID {
			t.Errorf("credential after a refused step: created_by=%q err=%v, want untouched", createdBy, err)
		}
		var views int
		if err := r.db.QueryRow(`SELECT COUNT(*) FROM saved_views WHERE id = 'pid-sv-a'`).Scan(&views); err != nil || views != 1 {
			t.Errorf("saved view after a refused step: %d (err %v), want 1 — the whole step must roll back", views, err)
		}
		scope, _ := body["scope"].(map[string]any)
		for _, key := range identityScopeKeys() {
			if n, ok := scope[key].(float64); !ok || n != 0 {
				t.Errorf("scope[%q] = %v after a rollback, want 0 and present", key, scope[key])
			}
		}
	})
}

// ── Every revoked capability is journalled, and none of them names the
//    subject in a table the erasure cannot reach ──────────────────────────

// The rule these entries exist under is pages_grants.go's: "an ACL nobody can
// audit is not a security control." The ordinary revoke path writes one entry
// per row on all three tables; an erasure that removed the same rows behind an
// aggregate count would leave nobody able to say which page a crew lost access
// to, or which integration the SAR just broke.
//
// The second half of the test is the subtler one. journal_entries is on the
// deliberately-excluded list, so an entry carrying the erased user's id would
// have the erasure itself write the subject into a table nothing will ever
// clean — an erasure that creates the defect it was run to fix.
func TestGDPRErasure_RevocationsAreJournalled(t *testing.T) {
	r := pagesIdentitySetup(t)
	r.erase(t, pidWSA)

	counts := map[journal.EntryType]int{}
	for _, e := range r.spy.entries {
		counts[e.Type]++
	}
	for _, want := range []struct {
		typ journal.EntryType
		n   int
	}{
		{journal.EntryPageGrantRemoved, 2},
		{journalPageLinkRevoked, 1},
		{journalPageWebhookRevoked, 1},
	} {
		if counts[want.typ] != want.n {
			t.Errorf("journal entries of type %s = %d, want %d (all entries: %v)",
				want.typ, counts[want.typ], want.n, counts)
		}
	}

	// Nothing the erasure wrote may name the subject — not in a payload value,
	// not as the entry's actor. The actor is the admin who ran it.
	for _, e := range r.spy.entries {
		if e.ActorID == r.userID {
			t.Errorf("journal entry %s names the erased subject as its actor", e.Type)
		}
		blob, err := json.Marshal(e.Payload)
		if err != nil {
			t.Fatalf("marshal payload of %s: %v", e.Type, err)
		}
		if strings.Contains(string(blob), r.userID) {
			t.Errorf("journal entry %s writes the erased subject's id into an append-only table: %s",
				e.Type, blob)
		}
		if got, _ := e.Payload["gdpr_action_id"].(string); got == "" {
			t.Errorf("journal entry %s carries no gdpr_action_id — nothing links it back to the erasure that caused it", e.Type)
		}
	}

	// The grant that named the subject as its GRANTEE records the fact without
	// the id; the one they issued names its (unrelated) grantee normally.
	var sawErasedSubject, sawNamedGrantee bool
	for _, e := range r.spy.entries {
		if e.Type != journal.EntryPageGrantRemoved {
			continue
		}
		if erased, _ := e.Payload["subject_erased"].(bool); erased {
			sawErasedSubject = true
			if _, present := e.Payload["subject_id"]; present {
				t.Errorf("grant entry for the erased subject still carries a subject_id: %v", e.Payload)
			}
			continue
		}
		if id, _ := e.Payload["subject_id"].(string); id != "" {
			sawNamedGrantee = true
		}
	}
	if !sawErasedSubject {
		t.Error("no grant entry recorded that its grantee was the erased subject")
	}
	if !sawNamedGrantee {
		t.Error("the grant the subject issued to a crew lost its grantee — the entry cannot say who lost access")
	}

	// The forensic fields the capability migrations exist for travel with the
	// entry, because the row that held them is gone.
	hook := r.spy.firstOfType(journalPageWebhookRevoked)
	if hook == nil {
		t.Fatal("no webhook revocation entry")
	}
	if _, ok := hook.Payload["fire_count"]; !ok {
		t.Errorf("webhook entry drops fire_count — 'was it used after we pulled it' has no answer once the row is deleted: %v", hook.Payload)
	}
	if live, _ := hook.Payload["was_live"].(bool); !live {
		t.Errorf("webhook entry reports a live token as already revoked: %v", hook.Payload)
	}
}
