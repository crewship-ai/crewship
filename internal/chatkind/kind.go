// Package chatkind is the one place that decides what a row in `chats` IS.
//
// Four different writers put rows in that table and only one of them is a
// conversation:
//
//	· a person opening a thread                    (origin UI / CLI)
//	· a routine, minting one chat PER STEP         (internal/pipeline)
//	· an issue starting work                       (mode MISSION)
//	· an agent delegating to another agent         (origin AGENT)
//
// It is its own package rather than a file in internal/api because the answer
// is needed on both sides of a surface boundary and neither side may own it:
// internal/api pages the conversations column by it, and internal/chatnotify
// decides by it whether a reply earns a place in somebody's inbox. A second
// copy of the rule in the notifier is the failure this package exists to
// prevent — the column would hide a routine step while the bell announced it.
//
// Nothing here touches a database or a request. It is a vocabulary and four
// predicates over it, so both importers can use it in Go and in SQL and
// cannot disagree.
package chatkind

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Kind is one bucket of the partition. The set is exhaustive and
// non-overlapping: every row in `chats` matches exactly one.
type Kind string

const (
	// Direct — a person talking to an agent. The default, and the only kind
	// the conversations column shows until asked otherwise.
	Direct Kind = "direct"
	// Routine — machine work: a routine step, a cron dispatch, a webhook
	// trigger. Its home surface is /routines.
	Routine Kind = "routine"
	// Issue — the synthetic chat an issue/mission runs its work in.
	Issue Kind = "issue"
	// Agent — agent-to-agent delegation (assignments hang off it).
	Agent Kind = "agent"
)

// OriginRoutine is the origin value the pipeline runner stamps. CRON and
// WEBHOOK predate it and mean the same thing to a reader, so they classify
// together.
const OriginRoutine = "ROUTINE"

// OriginValues is the whitelist every chat-creating endpoint shares.
// Anything outside it is stored as NULL rather than rejected — an origin is
// provenance, not input the caller is entitled to invent, and a chat with an
// unknown provenance is still a chat.
var OriginValues = []string{"UI", "CLI", "WEBHOOK", "CRON", "AGENT", OriginRoutine}

// IsOrigin reports whether v is a storable origin.
func IsOrigin(v string) bool {
	for _, ok := range OriginValues {
		if v == ok {
			return true
		}
	}
	return false
}

// All is the vocabulary, in the order a UI should offer it: the one you want
// first, then the machine ones by how often anybody opens them.
var All = []Kind{Direct, Routine, Issue, Agent}

// Predicates maps a kind onto its WHERE fragment over an aliased `chats c`.
//
// `Direct` is written as the NEGATION of the other three rather than as
// `origin IN ('UI','CLI')`, and that is what makes the set a partition: a row
// with an origin nobody has thought of yet — a future value, a hand-written
// row, a restore from an older schema — lands in Direct and stays VISIBLE.
// The opposite default is the failure this package exists to fix: a chat that
// silently belongs to no bucket is a chat nobody can find.
// `COALESCE(c.mode,”)` and not a bare `c.mode`, and it is the difference
// between the paragraph above being true and being a wish. SQL three-valued
// logic answers NULL — not false — to `NULL <> 'MISSION'`, and NULL to
// `NULL = 'MISSION'` too, so a row with no mode would match *zero* predicates
// and become exactly the invisible row the negated Direct fragment exists to
// prevent. `chats.mode` is NOT NULL DEFAULT 'CHAT' in the shipped schema, so
// nothing produces one today — but the claim being made is about rows this
// package does NOT control: a hand-written row, a restore from an older
// schema. Depending on a constraint in another package's table to keep a
// promise in this one's doc comment is the kind of unstated coupling that
// holds until the day it does not.
var Predicates = map[Kind]string{
	Issue:   `COALESCE(c.mode,'') = 'MISSION'`,
	Routine: `COALESCE(c.mode,'') <> 'MISSION' AND c.origin IN ('ROUTINE','CRON','WEBHOOK')`,
	Agent:   `COALESCE(c.mode,'') <> 'MISSION' AND c.origin = 'AGENT'`,
	Direct: `COALESCE(c.mode,'') <> 'MISSION' AND (c.origin IS NULL OR ` +
		`c.origin NOT IN ('ROUTINE','CRON','WEBHOOK','AGENT'))`,
}

// Of classifies one row the same way the SQL does. Kept beside the predicates
// so the two cannot drift — TestPredicatesMatchClassifier runs every
// combination of mode and origin through both and diffs them.
func Of(mode string, origin string) Kind {
	if mode == "MISSION" {
		return Issue
	}
	switch origin {
	case "ROUTINE", "CRON", "WEBHOOK":
		return Routine
	case "AGENT":
		return Agent
	}
	return Direct
}

// IsMachine reports whether nobody typed to start this chat.
//
// The question every notification path actually has, named once so each of
// them does not answer it with its own list. Direct is the only kind a person
// opened; the other three are work that ran on its own, and a bell about one
// of them is a bell about something the reader never asked for.
func IsMachine(k Kind) bool { return k != Direct }

// CountsHeader carries per-kind totals for one agent, e.g.
// `direct=3,routine=182,issue=0,agent=1`.
//
// A header rather than a response field: `GET /agents/{id}/chats` answers with
// a JSON ARRAY, and wrapping it in an object to make room for totals would
// break every existing caller for a number only one of them wants. See
// docs/guides/chat-surface-limits.mdx.
const CountsHeader = "X-Chat-Kind-Counts"

// FormatCounts renders the header value in `All` order, so the string is
// stable across requests and diffable in a log.
func FormatCounts(counts map[Kind]int) string {
	parts := make([]string, 0, len(All))
	for _, k := range All {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, ",")
}

// ParseFilter turns a `kind` query parameter into a WHERE fragment.
//
// Absent, empty, or "all" means no filter — so every caller that predates the
// parameter keeps its answer byte for byte. Only a caller that ASKS to narrow
// gets a narrowed list.
//
// Comma-separated, because "show me routines and issues" is one question and
// two round-trips is a worse answer than one.
func ParseFilter(raw string) (where string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	seen := map[Kind]bool{}
	elements := 0
	for _, part := range strings.Split(raw, ",") {
		// Lowercased HERE, `all` included. It used to be matched against the
		// raw string before this loop, which made it the one word in the
		// vocabulary that cared about case: every kind tolerated `ROUTINE`
		// and only `all` did not, so `--kind ALL` answered
		// `unknown kind "ALL" (want one of direct, routine, issue, agent, all)`
		// — an error whose own text offers the word it has just refused, to
		// somebody standing at a terminal.
		k := Kind(strings.ToLower(strings.TrimSpace(part)))
		if k == "" {
			// A stray separator is forgiven: `--kind "direct,"` is what a
			// shell loop appending `",$kind"` produces, and refusing it would
			// be pedantry. `elements` counts only the parts that meant
			// something, so forgiving a separator cannot be mistaken for
			// being asked nothing.
			continue
		}
		elements++
		if k == "all" {
			// Naming `all` alongside a kind is a contradiction, not a
			// refinement — and answering it with the union would silently give
			// back the mixed column the parameter exists to avoid.
			if elements > 1 || strings.Count(raw, ",") > 0 {
				return "", fmt.Errorf("kind %q cannot be combined with other kinds", part)
			}
			return "", nil
		}
		if _, ok := Predicates[k]; !ok {
			return "", fmt.Errorf("unknown kind %q (want one of %s)", part, List())
		}
		seen[k] = true
	}
	if elements == 0 {
		// Separators and nothing else. This used to fall into the branch
		// below and return NO NARROWING — the caller asked to narrow and got
		// the unfiltered list back, with no error to say the request had been
		// dropped. That is the mixed column `?kind=` exists to prevent,
		// reachable from any client that joins an empty selection with commas.
		return "", fmt.Errorf("no kind given in %q (want one of %s)", raw, List())
	}
	if len(seen) == len(Predicates) {
		// Every kind is the same query as no filter, and asking SQLite to
		// evaluate a four-branch tautology per row is not free.
		return "", nil
	}
	// Sorted so the generated SQL is stable across requests — an unstable
	// statement text defeats the prepared-statement cache for no reason.
	kinds := make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, "("+Predicates[Kind(k)]+")")
	}
	return " AND (" + strings.Join(parts, " OR ") + ")", nil
}

// List is the vocabulary as an error message names it.
func List() string {
	out := make([]string, 0, len(All)+1)
	for _, k := range All {
		out = append(out, string(k))
	}
	out = append(out, "all")
	return strings.Join(out, ", ")
}

// CountByAgent totals one agent's chats per kind.
//
// Groups on (mode, origin) in SQL and folds the groups through `Of` in Go,
// rather than running one COUNT per predicate. That is not an optimisation —
// it is what makes a total incapable of disagreeing with the list beside it. A
// second SQL copy of the partition is a second thing to keep in step, and the
// number on a bucket would be the last place anybody noticed it had drifted.
func CountByAgent(ctx context.Context, db *sql.DB, agentID, workspaceID string) (map[Kind]int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.mode, c.origin, COUNT(*)
		FROM chats c
		WHERE c.agent_id = ? AND c.workspace_id = ?
		GROUP BY c.mode, c.origin
	`, agentID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Every kind is present with a zero rather than absent, so a client can
	// tell "this agent has no routines" from "this server did not say".
	out := map[Kind]int{}
	for _, k := range All {
		out[k] = 0
	}
	for rows.Next() {
		var mode string
		var origin sql.NullString
		var n int
		if err := rows.Scan(&mode, &origin, &n); err != nil {
			return nil, err
		}
		out[Of(mode, origin.String)] += n
	}
	return out, rows.Err()
}

// OfChat reads one chat's kind straight from the table.
//
// `sql.ErrNoRows` when the pairing does not exist, which is also the caller's
// tenant check: a (chat, workspace) that does not match is not a chat this
// caller may reason about.
func OfChat(ctx context.Context, db *sql.DB, chatID, workspaceID string) (Kind, error) {
	var mode string
	var origin sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT mode, origin FROM chats WHERE id = ? AND workspace_id = ?`,
		chatID, workspaceID).Scan(&mode, &origin)
	if err != nil {
		return "", err
	}
	return Of(mode, origin.String), nil
}
