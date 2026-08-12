package database

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

// Pages — the six tables behind PRD §10 (docs/prd/pages.md).
//
// A page holds no query, no datasource and no credentials; it renders the last
// payload a producer pushed. That makes the SCHEMA the whole security model:
//
//   - page_panels.owner_crew_id is the ACL (§7.1 rule 2). It is not decoration
//     and it is not nullable — a panel that loses its owning crew would become
//     a panel everybody can read.
//   - page_grants.granted_by_user_id is NOT NULL because only a human issues a
//     grant (§7.1b rule 1). A nullable column here is the escalation path where
//     an injected agent grows its own blast radius one grant at a time.
//   - page_public_tokens.expires_at is NOT NULL because every public link
//     expires (§7.3.2 rule 4), and created_by_user_id is NOT NULL because only
//     a human publishes (rule 3).
//   - page_panel_data.produced_at is written by the server. Freshness is
//     computed from it and never from a producer-supplied timestamp (§4 rule 2);
//     there is deliberately no column a producer could claim it in.
//
// Each of those is a one-word edit away from being wrong, and none of them
// fails loudly at runtime when it is — the failures are "a panel rendered for
// somebody who should not see it" and "a public link that never expires". So
// they are pinned here, against the migrated schema, rather than trusted to the
// handlers that will be written on top.

// migratePagesCounter generates unique in-memory database names so parallel
// tests do not share state — the bare `file::memory:?cache=shared` DSN points
// every connection at the SAME global database. Same idiom as
// migrateV89Counter in migrate_v89_test.go.
var migratePagesCounter atomic.Int64

// pagesMigratedDB opens a fresh in-memory database with foreign keys ON and
// runs the full migration chain. Foreign keys matter here: half the assertions
// below are about what a DELETE does, and with the pragma off they would all
// pass vacuously.
func pagesMigratedDB(t *testing.T) *sql.DB {
	t.Helper()

	name := fmt.Sprintf("crewship-migrate-pages-%d", migratePagesCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Guard the guard: if foreign_keys did not actually take, every cascade
	// assertion in this file becomes a no-op that still reports PASS.
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if fk != 1 {
		t.Fatal("foreign_keys is OFF — the cascade and RESTRICT assertions below would pass vacuously")
	}
	return db
}

// seedPagesFixture builds the FK targets a page needs: two workspaces (so
// cross-tenant slug reuse can be checked), two users, two crews, an agent.
func seedPagesFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws_pg', 'WS', 'ws-pg')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws_pg2', 'WS2', 'ws-pg2')`,
		`INSERT INTO users (id, email, full_name) VALUES ('user_pg', 'pg@example.com', 'Owner')`,
		`INSERT INTO users (id, email, full_name) VALUES ('user_pg2', 'pg2@example.com', 'Granter')`,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_pg', 'ws_pg', 'Lookout', 'lookout')`,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_pg2', 'ws_pg', 'DevOps', 'devops')`,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		   VALUES ('agent_pg', 'crew_pg', 'ws_pg', 'Producer', 'producer', 'WORKER')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("fixture %q: %v", s, err)
		}
	}
}

// seedOnePage inserts a user-owned page with one status panel and returns
// nothing — the ids are fixed so the assertions can name them.
func seedOnePage(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO pages (id, workspace_id, slug, name, owner_user_id, spec_json)
		   VALUES ('page_pg', 'ws_pg', 'fleet-201', 'Flotila .201', 'user_pg', '{}')`,
		`INSERT INTO page_panels (id, page_id, panel_id, schema, title, owner_crew_id,
		                          producer_kind, producer_ref, sla_seconds, span)
		   VALUES ('panel_pg', 'page_pg', 'sluzby', 'status.v1', 'Jede to?', 'crew_pg',
		           'script', 'watch-services.sh', 30, 8)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed page %q: %v", s, err)
		}
	}
}

// columnFacts is one row of PRAGMA table_info, reduced to the three properties
// worth pinning: the name, whether it is NOT NULL, and its position in the
// primary key (0 = not part of it).
type columnFacts struct {
	name    string
	notNull bool
	pkPos   int
}

func tableColumns(t *testing.T, db *sql.DB, table string) []columnFacts {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()

	var out []columnFacts
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		out = append(out, columnFacts{name: name, notNull: notNull == 1, pkPos: pk})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return out
}

// TestMigratePages_TablesAndColumns pins the exact column list of all six
// tables, in order, with the NOT NULL and PRIMARY KEY facts that carry the
// security rules of §7 and §7.3. An extra column is a review question; a
// missing or newly-nullable one is a bug that only shows up as a leak.
func TestMigratePages_TablesAndColumns(t *testing.T) {
	t.Parallel()
	db := pagesMigratedDB(t)

	cases := []struct {
		table string
		want  []columnFacts
	}{
		{
			// §10: pages(id, workspace_id, slug, name, description,
			// owner_user_id, created_by_agent_id NULL, spec_json, created_at,
			// updated_at). owner_crew_id is added from §7.1 rule 1 / §15
			// decision 3 — a page has exactly one owner and it is a user XOR
			// a crew.
			table: "pages",
			want: []columnFacts{
				{name: "id", notNull: false, pkPos: 1},
				{name: "workspace_id", notNull: true},
				{name: "slug", notNull: true},
				{name: "name", notNull: true},
				{name: "description"},
				{name: "owner_user_id"},
				{name: "owner_crew_id"},
				{name: "created_by_agent_id"},
				{name: "spec_json", notNull: true},
				{name: "created_at", notNull: true},
				{name: "updated_at", notNull: true},
			},
		},
		{
			table: "page_panels",
			want: []columnFacts{
				{name: "id", notNull: false, pkPos: 1},
				{name: "page_id", notNull: true},
				{name: "panel_id", notNull: true},
				{name: "schema", notNull: true},
				{name: "title"},
				// The ACL anchor (§7.1 rule 2), and therefore NOT NULL.
				{name: "owner_crew_id", notNull: true},
				{name: "producer_kind", notNull: true},
				{name: "producer_ref", notNull: true},
				// §4 rule 1: a panel without an SLA does not validate. There
				// is no default that means "never mind".
				{name: "sla_seconds", notNull: true},
				{name: "span", notNull: true},
				{name: "config_json", notNull: true},
				{name: "created_at", notNull: true},
				{name: "updated_at", notNull: true},
			},
		},
		{
			table: "page_panel_data",
			want: []columnFacts{
				{name: "panel_id", notNull: true, pkPos: 1},
				{name: "seq", notNull: true, pkPos: 2},
				{name: "payload_json", notNull: true},
				// Server clock. §4 rule 2 + rule 5: freshness and provenance
				// are attached, never producer-claimed.
				{name: "produced_at", notNull: true},
				{name: "producer_run_id"},
				{name: "state", notNull: true},
			},
		},
		{
			table: "page_versions",
			want: []columnFacts{
				{name: "page_id", notNull: true, pkPos: 1},
				{name: "seq", notNull: true, pkPos: 2},
				{name: "spec_json", notNull: true},
				{name: "author_user_id"},
				{name: "author_agent_id"},
				{name: "created_at", notNull: true},
			},
		},
		{
			table: "page_grants",
			want: []columnFacts{
				{name: "page_id", notNull: true, pkPos: 1},
				{name: "subject_type", notNull: true, pkPos: 2},
				{name: "subject_id", notNull: true, pkPos: 3},
				{name: "level", notNull: true, pkPos: 4},
				{name: "panel_ids"},
				// §7.1b rule 1: only a human issues a grant.
				{name: "granted_by_user_id", notNull: true},
				{name: "granted_at", notNull: true},
			},
		},
		{
			table: "page_public_tokens",
			want: []columnFacts{
				{name: "id", notNull: false, pkPos: 1},
				{name: "page_id", notNull: true},
				{name: "token_hash", notNull: true},
				{name: "password_hash"},
				// §7.3.2 rule 4: every public link expires.
				{name: "expires_at", notNull: true},
				// §7.3.2 rule 5: provenance is stripped by default.
				{name: "show_provenance", notNull: true},
				// §7.3.2 rule 3: only a human publishes.
				{name: "created_by_user_id", notNull: true},
				{name: "revoked_at"},
				{name: "last_seen_at"},
				{name: "created_at", notNull: true},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			got := tableColumns(t, db, tc.table)
			if len(got) == 0 {
				t.Fatalf("table %s does not exist after migration", tc.table)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("%s: got %d columns %v, want %d %v",
					tc.table, len(got), got, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.name != w.name {
					t.Errorf("%s column %d: got %q, want %q", tc.table, i, g.name, w.name)
					continue
				}
				if g.notNull != w.notNull {
					t.Errorf("%s.%s: NOT NULL = %v, want %v", tc.table, w.name, g.notNull, w.notNull)
				}
				if g.pkPos != w.pkPos {
					t.Errorf("%s.%s: primary-key position = %d, want %d", tc.table, w.name, g.pkPos, w.pkPos)
				}
			}
		})
	}
}

// indexFacts describes one index as PRAGMA index_list + index_info report it.
type indexFacts struct {
	name    string
	unique  bool
	columns []string
}

func tableIndexes(t *testing.T, db *sql.DB, table string) []indexFacts {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(`PRAGMA index_list(%q)`, table))
	if err != nil {
		t.Fatalf("index_list(%s): %v", table, err)
	}
	var out []indexFacts
	for rows.Next() {
		var (
			seq, unique, partial int
			name, origin         string
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			t.Fatalf("scan index_list(%s): %v", table, err)
		}
		out = append(out, indexFacts{name: name, unique: unique == 1})
	}
	rows.Close()

	for i := range out {
		cols, err := db.Query(fmt.Sprintf(`PRAGMA index_info(%q)`, out[i].name))
		if err != nil {
			t.Fatalf("index_info(%s): %v", out[i].name, err)
		}
		for cols.Next() {
			var seqno, cid int
			var name sql.NullString
			if err := cols.Scan(&seqno, &cid, &name); err != nil {
				cols.Close()
				t.Fatalf("scan index_info(%s): %v", out[i].name, err)
			}
			out[i].columns = append(out[i].columns, name.String)
		}
		cols.Close()
	}
	return out
}

// TestMigratePages_IndexesAndUniqueConstraints pins the UNIQUE constraints the
// PRD names — UNIQUE(workspace_id, slug), UNIQUE(page_id, panel_id) — and the
// read-path indexes the permission filter and the retention sweep depend on.
//
// The UNIQUE assertions are matched on columns, not name: a table-level UNIQUE
// produces an auto-index whose name is an implementation detail.
func TestMigratePages_IndexesAndUniqueConstraints(t *testing.T) {
	t.Parallel()
	db := pagesMigratedDB(t)

	uniques := []struct {
		table string
		cols  []string
		why   string
	}{
		{"pages", []string{"workspace_id", "slug"}, "pages are slug-addressable per workspace (§10, obstacle 10)"},
		{"page_panels", []string{"page_id", "panel_id"}, "a panel id is the address a producer pushes to (§10)"},
		{"page_public_tokens", []string{"token_hash"}, "the token hash is the lookup key for /p/{token} (§7.3.1)"},
	}
	for _, u := range uniques {
		t.Run("unique/"+u.table+"/"+strings.Join(u.cols, "+"), func(t *testing.T) {
			for _, idx := range tableIndexes(t, db, u.table) {
				if idx.unique && equalStrings(idx.columns, u.cols) {
					return
				}
			}
			t.Errorf("%s: no UNIQUE index over %v — %s", u.table, u.cols, u.why)
		})
	}

	named := []struct {
		table string
		index string
		cols  []string
		why   string
	}{
		{"pages", "idx_pages_owner_user", []string{"owner_user_id"},
			"the owner-departure transfer (§7.1 rule 1b) and the RESTRICT check both look pages up by owner"},
		{"pages", "idx_pages_owner_crew", []string{"owner_crew_id"},
			"crew-owned pages are listed by crew (§7.1 rule 1)"},
		{"pages", "idx_pages_created_by_agent", []string{"created_by_agent_id"},
			"every FK child column in these tables leads an index, so no parent delete scans"},
		{"page_panels", "idx_page_panels_owner_crew", []string{"owner_crew_id"},
			"the per-viewer panel filter runs on every page read (§7.1 rule 2)"},
		{"page_panel_data", "idx_page_panel_data_produced_at", []string{"produced_at"},
			"the 7-day age cut sweeps by timestamp across every panel (§10b.3)"},
		{"page_panel_data", "idx_page_panel_data_run", []string{"producer_run_id"},
			"run retention deletes runs in bulk; without this each one scans the payload ring"},
		{"page_versions", "idx_page_versions_author_user", []string{"author_user_id"},
			"same blanket rule: an unindexed child column turns a parent delete into a full scan"},
		{"page_versions", "idx_page_versions_author_agent", []string{"author_agent_id"},
			"agents ARE hard deleted (the compensating deletes in agents_hire.go)"},
		{"page_grants", "idx_page_grants_subject", []string{"subject_type", "subject_id"},
			"'which pages may I see' is a lookup by subject, not by page (§7.1 rule 3)"},
		{"page_grants", "idx_page_grants_granted_by", []string{"granted_by_user_id"},
			"a grant dies with the human who issued it (§7.1b), which is a delete keyed on this column"},
		{"page_public_tokens", "idx_page_public_tokens_page", []string{"page_id"},
			"one page may have several tokens; revoking one lists them by page (§7.3.2 rule 4)"},
		{"page_public_tokens", "idx_page_public_tokens_created_by", []string{"created_by_user_id"},
			"tokens die with the human who published them (§7.3.2 rule 3)"},
	}
	for _, n := range named {
		t.Run("index/"+n.index, func(t *testing.T) {
			for _, idx := range tableIndexes(t, db, n.table) {
				if idx.name != n.index {
					continue
				}
				if !equalStrings(idx.columns, n.cols) {
					t.Errorf("%s covers %v, want %v", n.index, idx.columns, n.cols)
				}
				return
			}
			t.Errorf("%s: index %s missing — %s", n.table, n.index, n.why)
		})
	}
}

// TestMigratePages_ConstraintsRefuseBadRows drives every CHECK and UNIQUE in
// the migration from the outside. Each rejected row is a rule from the PRD that
// would otherwise have to be remembered by every handler that ever writes here.
func TestMigratePages_ConstraintsRefuseBadRows(t *testing.T) {
	t.Parallel()
	db := pagesMigratedDB(t)
	seedPagesFixture(t, db)
	seedOnePage(t, db)

	cases := []struct {
		name    string
		stmt    string
		wantErr bool
		why     string
	}{
		{
			name: "page/owner is user xor crew — both set",
			stmt: `INSERT INTO pages (id, workspace_id, slug, name, owner_user_id, owner_crew_id, spec_json)
			       VALUES ('p_both', 'ws_pg', 'both', 'Both', 'user_pg', 'crew_pg', '{}')`,
			wantErr: true,
			why:     "§7.1 rule 1: a page has exactly one owner",
		},
		{
			name: "page/owner is user xor crew — neither set",
			stmt: `INSERT INTO pages (id, workspace_id, slug, name, spec_json)
			       VALUES ('p_none', 'ws_pg', 'none', 'None', '{}')`,
			wantErr: true,
			why:     "§7.1 rule 1b: a page must never be orphaned",
		},
		{
			name: "page/crew-owned is legal",
			stmt: `INSERT INTO pages (id, workspace_id, slug, name, owner_crew_id, spec_json)
			       VALUES ('p_crew', 'ws_pg', 'crew-board', 'Crew board', 'crew_pg', '{}')`,
			why: "§15 decision 3: a crew-owned page needs no personal owner",
		},
		{
			name: "page/slug is unique per workspace",
			stmt: `INSERT INTO pages (id, workspace_id, slug, name, owner_user_id, spec_json)
			       VALUES ('p_dup', 'ws_pg', 'fleet-201', 'Dup', 'user_pg', '{}')`,
			wantErr: true,
			why:     "UNIQUE(workspace_id, slug)",
		},
		{
			name: "page/the same slug in another workspace is a different page",
			stmt: `INSERT INTO pages (id, workspace_id, slug, name, owner_user_id, spec_json)
			       VALUES ('p_ws2', 'ws_pg2', 'fleet-201', 'Other tenant', 'user_pg', '{}')`,
			why: "the uniqueness is per workspace, not global",
		},
		{
			name: "panel/schema comes from the closed set",
			stmt: `INSERT INTO page_panels (id, page_id, panel_id, schema, owner_crew_id, producer_kind, producer_ref, sla_seconds, span)
			       VALUES ('pn_bad', 'page_pg', 'bad', 'gauge.v1', 'crew_pg', 'routine', 'r', 60, 6)`,
			wantErr: true,
			why:     "§3: a new panel kind is a server release, never a user-supplied string",
		},
		{
			name: "panel/embed.v1 is reserved from the first migration",
			stmt: `INSERT INTO page_panels (id, page_id, panel_id, schema, owner_crew_id, producer_kind, producer_ref, sla_seconds, span)
			       VALUES ('pn_embed', 'page_pg', 'embed', 'embed.v1', 'crew_pg', 'routine', 'r', 60, 6)`,
			why: "§3.1: the type name is reserved now so admitting it later is not a breaking change",
		},
		{
			name: "panel/sla must be positive",
			stmt: `INSERT INTO page_panels (id, page_id, panel_id, schema, owner_crew_id, producer_kind, producer_ref, sla_seconds, span)
			       VALUES ('pn_sla', 'page_pg', 'nosla', 'metric.v1', 'crew_pg', 'routine', 'r', 0, 6)`,
			wantErr: true,
			why:     "§4 rule 1: there is no SLA that means 'never mind'",
		},
		{
			name: "panel/span is a grid span, 1..12",
			stmt: `INSERT INTO page_panels (id, page_id, panel_id, schema, owner_crew_id, producer_kind, producer_ref, sla_seconds, span)
			       VALUES ('pn_span', 'page_pg', 'wide', 'metric.v1', 'crew_pg', 'routine', 'r', 60, 13)`,
			wantErr: true,
			why:     "§9: span maps to col-span-n on a 12-column grid",
		},
		{
			name: "panel/producer_kind comes from the closed set",
			stmt: `INSERT INTO page_panels (id, page_id, panel_id, schema, owner_crew_id, producer_kind, producer_ref, sla_seconds, span)
			       VALUES ('pn_kind', 'page_pg', 'kind', 'metric.v1', 'crew_pg', 'sql', 'select 1', 60, 6)`,
			wantErr: true,
			why:     "§1: a page holds no query and no datasource — 'sql' must not be expressible",
		},
		{
			name: "panel/panel_id is unique within the page",
			stmt: `INSERT INTO page_panels (id, page_id, panel_id, schema, owner_crew_id, producer_kind, producer_ref, sla_seconds, span)
			       VALUES ('pn_dup', 'page_pg', 'sluzby', 'metric.v1', 'crew_pg', 'routine', 'r', 60, 6)`,
			wantErr: true,
			why:     "UNIQUE(page_id, panel_id): the panel id is the push address",
		},
		{
			name: "panel/owner crew is required",
			stmt: `INSERT INTO page_panels (id, page_id, panel_id, schema, producer_kind, producer_ref, sla_seconds, span)
			       VALUES ('pn_noacl', 'page_pg', 'noacl', 'metric.v1', 'routine', 'r', 60, 6)`,
			wantErr: true,
			why:     "§7.1 rule 2: owner_crew_id is the ACL, so a panel without one is a panel with no ACL",
		},
		{
			name: "data/state is the push outcome, not a freshness state",
			stmt: `INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, state)
			       VALUES ('panel_pg', 1, '{}', '2026-08-12T10:00:00Z', 'stale')`,
			wantErr: true,
			why:     "§4 rule 2: fresh/stale are computed server-side from produced_at and are never stored",
		},
		{
			name: "data/an ok push is stored",
			stmt: `INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, state)
			       VALUES ('panel_pg', 1, '{}', '2026-08-12T10:00:00Z', 'ok')`,
			why: "the happy path, so the refusals above are about state and not about the fixture",
		},
		{
			name: "data/an explicit failure push is stored",
			stmt: `INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, state)
			       VALUES ('panel_pg', 2, '{}', '2026-08-12T10:01:00Z', 'failed')`,
			why: "§4 rule 2: 'failed' is a producer verdict, unlike fresh/stale",
		},
		{
			name: "data/seq is unique per panel",
			stmt: `INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, state)
			       VALUES ('panel_pg', 2, '{}', '2026-08-12T10:02:00Z', 'ok')`,
			wantErr: true,
			why:     "PRIMARY KEY(panel_id, seq) is the ring's ordering",
		},
		{
			name: "grant/subject_type is user|crew|agent",
			stmt: `INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id)
			       VALUES ('page_pg', 'workspace', 'ws_pg', 'read', 'user_pg')`,
			wantErr: true,
			why:     "§7.1b: three subject types, closed",
		},
		{
			name: "grant/level is read|produce|write",
			stmt: `INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id)
			       VALUES ('page_pg', 'user', 'user_pg2', 'admin', 'user_pg')`,
			wantErr: true,
			why:     "§7.1b: three verbs, closed",
		},
		{
			name: "grant/panel_ids only narrows a produce grant",
			stmt: `INSERT INTO page_grants (page_id, subject_type, subject_id, level, panel_ids, granted_by_user_id)
			       VALUES ('page_pg', 'user', 'user_pg2', 'read', '["sluzby"]', 'user_pg')`,
			wantErr: true,
			why:     "§10: panel_ids is only meaningful for produce — a silently ignored scope reads as a scope",
		},
		{
			name: "grant/a scoped produce grant is legal",
			stmt: `INSERT INTO page_grants (page_id, subject_type, subject_id, level, panel_ids, granted_by_user_id)
			       VALUES ('page_pg', 'agent', 'agent_pg', 'produce', '["sluzby"]', 'user_pg')`,
			why: "§7.1b: an agent granted produce on one panel cannot overwrite another agent's panel",
		},
		{
			name: "grant/only a human issues one",
			stmt: `INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id)
			       VALUES ('page_pg', 'crew', 'crew_pg2', 'read', NULL)`,
			wantErr: true,
			why:     "§7.1b rule 1: granted_by_user_id NOT NULL is that rule in the schema",
		},
		{
			name: "token/expiry is required",
			stmt: `INSERT INTO page_public_tokens (id, page_id, token_hash, created_by_user_id)
			       VALUES ('tok_noexp', 'page_pg', 'hash-noexp', 'user_pg')`,
			wantErr: true,
			why:     "§7.3.2 rule 4: every public link expires",
		},
		{
			name: "token/publisher is required",
			stmt: `INSERT INTO page_public_tokens (id, page_id, token_hash, expires_at)
			       VALUES ('tok_nouser', 'page_pg', 'hash-nouser', '2026-09-11T00:00:00Z')`,
			wantErr: true,
			why:     "§7.3.2 rule 3: only a human publishes",
		},
		{
			name: "token/a well-formed token is stored",
			stmt: `INSERT INTO page_public_tokens (id, page_id, token_hash, expires_at, created_by_user_id)
			       VALUES ('tok_ok', 'page_pg', 'hash-ok', '2026-09-11T00:00:00Z', 'user_pg')`,
			why: "the happy path",
		},
		{
			name: "token/hash is unique across the instance",
			stmt: `INSERT INTO page_public_tokens (id, page_id, token_hash, expires_at, created_by_user_id)
			       VALUES ('tok_dup', 'page_pg', 'hash-ok', '2026-09-11T00:00:00Z', 'user_pg')`,
			wantErr: true,
			why:     "the hash is the credential; two pages sharing one is a cross-page read",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(tc.stmt)
			switch {
			case tc.wantErr && err == nil:
				t.Errorf("row was accepted but must be refused — %s", tc.why)
			case !tc.wantErr && err != nil:
				t.Errorf("row was refused but must be accepted (%s): %v", tc.why, err)
			}
		})
	}
}

// TestMigratePages_DefaultsAreSafe pins the two defaults whose wrong value is a
// disclosure rather than a cosmetic difference.
func TestMigratePages_DefaultsAreSafe(t *testing.T) {
	t.Parallel()
	db := pagesMigratedDB(t)
	seedPagesFixture(t, db)
	seedOnePage(t, db)

	if _, err := db.Exec(`INSERT INTO page_public_tokens (id, page_id, token_hash, expires_at, created_by_user_id)
	                      VALUES ('tok_def', 'page_pg', 'hash-def', '2026-09-11T00:00:00Z', 'user_pg')`); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	var showProvenance int
	if err := db.QueryRow(`SELECT show_provenance FROM page_public_tokens WHERE id = 'tok_def'`).
		Scan(&showProvenance); err != nil {
		t.Fatalf("read show_provenance: %v", err)
	}
	if showProvenance != 0 {
		t.Error("show_provenance defaults to on — §7.3.2 rule 5 says provenance is stripped by default; " +
			"run ids, agent slugs and crew names map our org chart for a reader outside it")
	}

	var config string
	if err := db.QueryRow(`SELECT config_json FROM page_panels WHERE id = 'panel_pg'`).Scan(&config); err != nil {
		t.Fatalf("read config_json: %v", err)
	}
	if config != "{}" {
		t.Errorf("page_panels.config_json default = %q, want %q so a reader never has to handle NULL", config, "{}")
	}
}

// TestMigratePages_DeletesFollowTheOwnershipRules covers the three delete
// behaviours the PRD argues for explicitly, each of which is a different FK
// action and none of which is the SQLite default.
func TestMigratePages_DeletesFollowTheOwnershipRules(t *testing.T) {
	t.Parallel()
	db := pagesMigratedDB(t)
	seedPagesFixture(t, db)
	seedOnePage(t, db)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, state)
	          VALUES ('panel_pg', 1, '{"value":0}', '2026-08-12T10:00:00Z', 'ok')`)
	mustExec(`INSERT INTO page_versions (page_id, seq, spec_json, author_user_id)
	          VALUES ('page_pg', 1, '{}', 'user_pg')`)
	mustExec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id)
	          VALUES ('page_pg', 'user', 'user_pg2', 'read', 'user_pg2')`)
	mustExec(`INSERT INTO page_public_tokens (id, page_id, token_hash, expires_at, created_by_user_id)
	          VALUES ('tok_cascade', 'page_pg', 'hash-cascade', '2026-09-11T00:00:00Z', 'user_pg2')`)

	count := func(q string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", q, err)
		}
		return n
	}

	t.Run("a page owner cannot be deleted out from under the page", func(t *testing.T) {
		if _, err := db.Exec(`DELETE FROM users WHERE id = 'user_pg'`); err == nil {
			t.Error("deleting the owning user was allowed; §7.1 rule 1b says the page transfers to a crew " +
				"first — it must never be orphaned and must never silently vanish")
		}
	})

	t.Run("a grant dies with the human who issued it", func(t *testing.T) {
		if _, err := db.Exec(`DELETE FROM users WHERE id = 'user_pg2'`); err != nil {
			t.Fatalf("deleting a non-owner granter should be allowed: %v", err)
		}
		if n := count(`SELECT COUNT(*) FROM page_grants WHERE page_id = 'page_pg'`); n != 0 {
			t.Errorf("%d grants survived their granter; §7.1b says an agent's authority is a subset of the "+
				"authorising human's, so a grant with no living issuer is authority from nobody", n)
		}
		if n := count(`SELECT COUNT(*) FROM page_public_tokens WHERE id = 'tok_cascade'`); n != 0 {
			t.Errorf("%d public tokens survived their publisher; a public link nobody owns cannot be revoked "+
				"by anybody", n)
		}
	})

	t.Run("deleting the page takes panels, payloads and versions with it", func(t *testing.T) {
		if _, err := db.Exec(`DELETE FROM pages WHERE id = 'page_pg'`); err != nil {
			t.Fatalf("delete page: %v", err)
		}
		for _, q := range []string{
			`SELECT COUNT(*) FROM page_panels WHERE page_id = 'page_pg'`,
			`SELECT COUNT(*) FROM page_panel_data WHERE panel_id = 'panel_pg'`,
			`SELECT COUNT(*) FROM page_versions WHERE page_id = 'page_pg'`,
		} {
			if n := count(q); n != 0 {
				t.Errorf("%s left %d rows behind after the page was deleted", q, n)
			}
		}
	})
}

// TestMigratePages_ZeroAndNoDataSurviveTheRoundTrip is §9b.4 at the storage
// layer: `0` is a measured zero and `null` is no basis to compute, and the two
// must not converge into one value on the way to disk and back. The em-dash
// rule is the whole point of the freshness contract; a TEXT column that
// normalises JSON would quietly destroy it.
func TestMigratePages_ZeroAndNoDataSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()
	db := pagesMigratedDB(t)
	seedPagesFixture(t, db)
	seedOnePage(t, db)

	cases := []struct {
		name    string
		seq     int
		payload string
	}{
		{"a measured zero", 1, `{"value":0}`},
		{"no basis to compute", 2, `{"value":null}`},
	}
	for _, tc := range cases {
		if _, err := db.Exec(`INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, state)
		                      VALUES ('panel_pg', ?, ?, '2026-08-12T10:00:00Z', 'ok')`, tc.seq, tc.payload); err != nil {
			t.Fatalf("insert %s: %v", tc.name, err)
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			if err := db.QueryRow(`SELECT payload_json FROM page_panel_data WHERE panel_id = 'panel_pg' AND seq = ?`,
				tc.seq).Scan(&got); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if got != tc.payload {
				t.Errorf("payload round-tripped as %q, want %q — §9b.4 keeps 0 and 'no data' distinct", got, tc.payload)
			}
		})
	}
}

// findMigrationByName returns the migration with the given name. Used by the
// idempotency test so a rename fails loudly here instead of silently testing
// nothing.
func findMigrationByName(name string) (*migration, error) {
	for i := range migrations {
		if migrations[i].name == name {
			return &migrations[i], nil
		}
	}
	return nil, fmt.Errorf("no migration named %q", name)
}

// TestMigratePages_ApplyingTwiceIsSafe runs the pages migration a second time
// against a database that already has it, and re-runs the whole chain on top.
//
// The runner records applied versions, so this cannot happen through Migrate()
// alone — but it does happen when a restored backup is migrated, when a
// half-applied migration is retried, and whenever someone runs the SQL by hand
// during an incident. IF NOT EXISTS everywhere is what makes the second run a
// no-op instead of an outage.
func TestMigratePages_ApplyingTwiceIsSafe(t *testing.T) {
	t.Parallel()
	db := pagesMigratedDB(t)
	seedPagesFixture(t, db)
	seedOnePage(t, db)

	m, err := findMigrationByName("pages")
	if err != nil {
		t.Fatalf("find pages migration: %v", err)
	}
	if m.sql == "" {
		t.Fatal("the pages migration has no SQL body — it must be a .sql file under migrations/")
	}
	if m.version <= legacySequentialCeiling {
		t.Errorf("pages claims version %d, which is inside the closed legacy block (<= v%d)",
			m.version, legacySequentialCeiling)
	}

	if _, err := db.Exec(m.sql); err != nil {
		t.Fatalf("re-applying the pages migration failed: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db, logger); err != nil {
		t.Fatalf("re-running the full chain failed: %v", err)
	}

	// The seeded rows must still be there: a re-apply that dropped and
	// recreated a table would pass every assertion above and lose the data.
	var panels int
	if err := db.QueryRow(`SELECT COUNT(*) FROM page_panels WHERE page_id = 'page_pg'`).Scan(&panels); err != nil {
		t.Fatalf("count panels: %v", err)
	}
	if panels != 1 {
		t.Errorf("after a second apply the page has %d panels, want 1", panels)
	}
}

// TestMigratePages_VersionFollowsTheTimestampScheme pins the filename rule from
// migrations/README.md against THIS migration: a stamp, above the closed legacy
// block, that sorts after everything already committed.
//
// It deliberately does not assert "pages is the newest file in the tree" — that
// would turn every later migration into a failure of this test, and strict
// global ascent is already guarded by TestMigrationsAreStrictlyIncreasing. What
// it does check is the mistake an author actually makes: reaching for the next
// small integer, or copying a neighbour's stamp.
func TestMigratePages_VersionFollowsTheTimestampScheme(t *testing.T) {
	t.Parallel()

	m, err := findMigrationByName("pages")
	if err != nil {
		t.Fatalf("find pages migration: %v", err)
	}
	if err := validateMigrationVersion(m.version); err != nil {
		t.Errorf("pages v%d: %v", m.version, err)
	}
	if m.version <= legacySequentialCeiling {
		t.Errorf("pages claims version %d, inside the closed legacy block (<= v%d): those numbers are "+
			"applied in databases nobody controls", m.version, legacySequentialCeiling)
	}

	// It must sort after every migration that shipped before it — checked
	// against the era-2 files, since those are the ones a stale stamp could
	// land among.
	files, err := loadFileMigrations()
	if err != nil {
		t.Fatalf("loadFileMigrations: %v", err)
	}
	versions := make([]int, 0, len(files))
	for _, f := range files {
		if f.name != "pages" {
			versions = append(versions, f.version)
		}
	}
	sort.Ints(versions)
	for _, v := range versions {
		if v == m.version {
			t.Fatalf("another migration already claims v%d", v)
		}
	}
	if len(versions) > 0 && m.version < versions[0] {
		t.Errorf("pages is v%d, older than the oldest file migration (v%d) — append, never insert",
			m.version, versions[0])
	}
}
