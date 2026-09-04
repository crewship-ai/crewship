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
}

func pidExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("seed (%s): %v", strings.SplitN(strings.TrimSpace(q), "\n", 2)[0], err)
	}
}

// erase runs the Art. 17 cascade against one workspace and returns the decoded
// response body.
func (r *pagesIdentityRig) erase(t *testing.T, wsID string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/", strings.NewReader(`{"reason":"SAR #1976"}`))
	req.SetPathValue("userId", r.userID)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: r.adminID})
	ctx = context.WithValue(ctx, ctxRole, "ADMIN")
	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, req.WithContext(ctx))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("erasure: status %d, want 202; body=%s", rec.Code, rec.Body.String())
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
	// and a version whose author was cleared did not.
	if got, _ := body["rows_deleted"].(float64); got != 4 {
		t.Errorf("rows_deleted = %v, want 4 (2 grants + 1 token + 1 webhook; an anonymised version is not a deletion)", got)
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
	for _, table := range []string{"page_versions", "page_grants", "page_public_tokens", "page_webhooks"} {
		if elsewhere[table] == 0 {
			t.Errorf("sweep of the untouched workspace no longer finds %s — the seed did not land, or workspace A's erasure reached into B: %v",
				table, elsewhere)
		}
	}
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
