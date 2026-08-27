package api

// crews_issue_prefix_format_test.go — #2035.
//
// crews.issue_prefix becomes the prefix of missions.identifier
// (issue_create_core.go: crewIssuePrefix), and that identifier is a SINGLE URL
// path segment on ~20 routes in router_orchestration.go — GET/PATCH/DELETE on
// the issue, its comments, attachments and relations all address it as
// {identifier}. A prefix containing "/" therefore mints an issue at an address
// no route can match: it exists, it lists, and it can never be opened again.
// Whitespace and "%" have their own versions of the same failure.
//
// PATCH /api/v1/crews/{crewId} wrote the field verbatim (the only branch was
// "" -> NULL), and since #2033 the CLI has a --issue-prefix flag pointing at it,
// so the unaddressable identifier is now two commands away. The guard lives on
// the write path rather than in the CLI, which covers the UI and the CLI at once.
//
// Two things this deliberately does NOT do, both settled in #2033:
//   - No uniqueness check. Two crews may share a prefix and simply share one
//     sequence; refusing "Engine" next to "Engineering" was rejected there.
//   - No migration and no read-side rejection. An odd prefix already stored
//     keeps minting exactly as it does today — only new writes are refused.
//     TestIssuePrefixFormat_ExistingOddPrefixStillMints pins that half.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// prefixJSON renders one prefix as a JSON string literal, so a case can carry a
// quote, a backslash or a non-ASCII rune without the test body hand-escaping it.
func prefixJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal prefix %q: %v", s, err)
	}
	return string(b)
}

func TestIssuePrefixFormat_RejectedOnUpdate(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		why    string
	}{
		{"slash", "A/B", "splits the identifier path segment — the issue becomes unroutable"},
		{"space", "A B", "a raw space in a path segment"},
		{"percent", "A%B", "percent starts an escape in a path segment"},
		{"hash", "A#B", "everything after # never reaches the server"},
		{"question mark", "A?B", "everything after ? is parsed as the query string"},
		{"dot dot", "..", "traverses the route tree"},
		{"empty-ish whitespace", " ", "not empty, so it does not mean clear"},
		{"too long", strings.Repeat("X", 17), "17 chars, over the 16 limit"},
		{"non-ascii", "ÉNG", "outside the ASCII rule"},
		// Anchors. Go's regexp is RE2 and compiles `$` as \z (OneLine), not as
		// Perl's "end of text or before a final newline" — so "ENG\n" is
		// refused. That is the behaviour the guard depends on, and it is one
		// flag away from the Perl reading, so it is pinned rather than assumed:
		// a prefix ending in a newline would reopen the bug with an identifier
		// that looks addressable and is not.
		{"trailing newline", "ENG\n", `Go's $ is \z — a trailing newline must not slip past the anchor`},
		{"leading newline", "\nENG", "the ^ anchor, from the other side"},
		{"embedded newline", "A\nB", "splits the identifier across two lines"},
		{"tab", "A\tB", "whitespace that is not a space"},
		{"nul byte", "A\x00B", "truncates the segment at the first NUL for anything reading it as a C string"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := covCruNewCrew(t)
			seedCrewRow(t, db, "cru-prefix", wsID, "Crew", "crew")

			body := `{"issue_prefix":` + prefixJSON(t, tc.prefix) + `}`
			rr := covCruDoUpdate(h, "cru-prefix", userID, wsID, "OWNER", body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("issue_prefix %q (%s) = %d, want 400, body: %s",
					tc.prefix, tc.why, rr.Code, rr.Body.String())
			}
			// The 400 has to name the field and the rule, or the operator is
			// left guessing which of the fields in their PATCH was refused.
			msg := rr.Body.String()
			if !strings.Contains(msg, "issue_prefix") {
				t.Errorf("400 body does not name the field: %s", msg)
			}
			if !strings.Contains(msg, "A-Za-z0-9_-") || !strings.Contains(msg, "16") {
				t.Errorf("400 body does not state the rule: %s", msg)
			}

			// Nothing was written: the rejection happens before the UPDATE.
			var stored any
			if err := db.QueryRow(`SELECT issue_prefix FROM crews WHERE id = ?`,
				"cru-prefix").Scan(&stored); err != nil {
				t.Fatalf("read crew: %v", err)
			}
			if stored != nil {
				t.Errorf("issue_prefix = %v after a rejected write, want unchanged NULL", stored)
			}
		})
	}
}

func TestIssuePrefixFormat_AcceptedOnUpdate(t *testing.T) {
	cases := []struct{ name, prefix string }{
		{"plain", "ENG"},
		{"lowercase", "eng"},
		{"digits", "ENG2"},
		{"underscore", "ENG_X"},
		{"hyphen", "ENG-X"},
		{"single char", "E"},
		{"sixteen chars", strings.Repeat("X", 16)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := covCruNewCrew(t)
			seedCrewRow(t, db, "cru-prefix-ok", wsID, "Crew", "crew")

			body := `{"issue_prefix":` + prefixJSON(t, tc.prefix) + `}`
			rr := covCruDoUpdate(h, "cru-prefix-ok", userID, wsID, "OWNER", body)
			if rr.Code != http.StatusOK {
				t.Fatalf("issue_prefix %q = %d, want 200, body: %s",
					tc.prefix, rr.Code, rr.Body.String())
			}

			var stored string
			if err := db.QueryRow(`SELECT issue_prefix FROM crews WHERE id = ?`,
				"cru-prefix-ok").Scan(&stored); err != nil {
				t.Fatalf("read crew: %v", err)
			}
			if stored != tc.prefix {
				t.Errorf("issue_prefix = %q, want %q", stored, tc.prefix)
			}
		})
	}
}

// The empty string keeps meaning "clear it" — that is how a crew falls back to
// the first three letters of its slug. The validator must not treat "" as a
// length violation.
func TestIssuePrefixFormat_EmptyStillClears(t *testing.T) {
	h, db, userID, wsID := covCruNewCrew(t)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix)
		VALUES ('cru-prefix-clear', ?, 'Crew', 'crew', 'OLD')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	rr := covCruDoUpdate(h, "cru-prefix-clear", userID, wsID, "OWNER", `{"issue_prefix":""}`)
	if rr.Code != http.StatusOK {
		t.Fatalf(`issue_prefix:"" = %d, want 200, body: %s`, rr.Code, rr.Body.String())
	}

	var stored any
	if err := db.QueryRow(`SELECT issue_prefix FROM crews WHERE id = ?`,
		"cru-prefix-clear").Scan(&stored); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if stored != nil {
		t.Errorf(`issue_prefix = %v after "", want NULL`, stored)
	}
}

// A row that already holds an odd prefix is not migrated and not refused on
// read: it keeps minting the identifier it mints today. Anything stricter would
// break crews that are currently working, however awkwardly.
func TestIssuePrefixFormat_ExistingOddPrefixStillMints(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Written straight to the column, the way a pre-#2035 PATCH or the web UI
	// could — the handler would refuse this now.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix)
		VALUES ('cru-legacy', ?, 'Legacy', 'legacy', 'A/B')`, wsID); err != nil {
		t.Fatalf("seed legacy crew: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	identifier, number, err := nextIssueIdentifierTx(context.Background(), tx, wsID, "cru-legacy")
	if err != nil {
		t.Fatalf("nextIssueIdentifierTx on a legacy odd prefix: %v", err)
	}
	if identifier != "A/B-1" || number != 1 {
		t.Errorf("identifier/number = %q/%d, want A/B-1/1 — existing rows must be left alone",
			identifier, number)
	}
}
