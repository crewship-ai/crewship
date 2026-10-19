package api

import (
	"encoding/base64"
	"net/url"
	"strings"
	"time"
)

// Server-side search, filters and paging for the Inbox. The Harbor 2 Inbox
// used to download every page of GET /api/v1/inbox and filter in the browser;
// past the first page that made older history unreachable and made search and
// facet counts cover only the rows that happened to be loaded. The list and
// the counts endpoint now share ONE parser and ONE set of predicates, so the
// number a facet shows is the number of rows it lists.
//
// Each predicate mirrors the client function it replaces (inbox-v2-derive.ts)
// and the counts test pins them branch for branch:
//
//	space      isActionableInboxItem + resolved   (inboxActionableSQL)
//	attention  entryAttention
//	deadline   deadlineBucket, and the "soon" facet
//	crew       entryCrewId
//	agent      entryAgentRef
//	q          matchesSearch + the crew and agent names the page resolves

const (
	inboxAttentionDecision = "decision"
	inboxAttentionInput    = "input"
	inboxAttentionReview   = "review"
	inboxAttentionRepair   = "repair"
)

// inboxPayloadText is payload key as a string, or NULL when it is absent or not
// a JSON string (the client's payloadString ignores other types).
func inboxPayloadText(key string) string {
	return `(CASE WHEN json_type(payload_json, '$.` + key + `') = 'text' THEN json_extract(payload_json, '$.` + key + `') END)`
}

// inboxCrewIDSQL is entryCrewId for an inbox row: invoking_crew_id, else crew_id.
var inboxCrewIDSQL = `(CASE WHEN ` + inboxTextNonEmpty("invoking_crew_id") + ` THEN ` + inboxPayloadText("invoking_crew_id") +
	` WHEN ` + inboxTextNonEmpty("crew_id") + ` THEN ` + inboxPayloadText("crew_id") + ` END)`

// inboxAttentionSQL is entryAttention for an active inbox row, NULL for one that
// needs no attention (and for every resolved row).
var inboxAttentionSQL = `(CASE
	WHEN state = 'resolved' THEN NULL
	WHEN NULLIF(attention_class, '') IS NOT NULL THEN attention_class
	WHEN kind = 'waitpoint' THEN 'decision'
	WHEN kind = 'run_needs_human' THEN 'input'
	WHEN kind = 'memory_consolidation' THEN 'review'
	WHEN kind IN ('failed_run', 'schedule_missed', 'schedule_circuit_breaker_tripped', 'webhook_fire_failed', 'automation_enqueue_failed') THEN 'repair'
	WHEN kind = 'escalation' AND ` + inboxActionableSQL + ` THEN (CASE
		WHEN ` + inboxPayloadText("kind") + ` IN ('skill_proposal', 'routine_proposal') THEN 'review'
		WHEN ` + inboxPayloadText("escalation_type") + ` IN ('TEXT', 'CREDENTIAL') THEN 'input'
		ELSE 'decision' END)
END)`

// inboxDeadlineAtSQL is the row's payload.timeout_at as a julian day, NULL when
// absent or unparseable (the client folds both into "no deadline").
const inboxDeadlineAtSQL = `julianday(` + `CASE WHEN json_type(payload_json, '$.timeout_at') = 'text' THEN json_extract(payload_json, '$.timeout_at') END)`

// inboxClock fixes the instants deadline buckets are measured from, so a list
// and a counts request made together agree. now defaults to the server clock;
// dayEnd is the end of the CALLER's local day (the server does not know its
// time zone) and defaults to the end of the UTC day.
type inboxClock struct{ now, dayEnd time.Time }

func (c inboxClock) deadlineSQL(bucket string) (string, []interface{}) {
	f := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	switch bucket {
	case "none":
		return inboxDeadlineAtSQL + ` IS NULL`, nil
	case "hour": // due within the hour, or already past
		return inboxDeadlineAtSQL + ` <= julianday(?)`, []interface{}{f(c.now.Add(time.Hour))}
	case "today": // later than an hour out, but before the day ends
		return `(` + inboxDeadlineAtSQL + ` > julianday(?) AND ` + inboxDeadlineAtSQL + ` <= julianday(?))`,
			[]interface{}{f(c.now.Add(time.Hour)), f(c.dayEnd)}
	case "soon": // not yet due, due within 24 hours
		return `(` + inboxDeadlineAtSQL + ` >= julianday(?) AND ` + inboxDeadlineAtSQL + ` <= julianday(?))`,
			[]interface{}{f(c.now), f(c.now.Add(24 * time.Hour))}
	}
	return "", nil
}

// inboxFilters is the parsed, validated filter set of one request.
type inboxFilters struct {
	clause string // starts with " AND " per predicate, or empty
	args   []interface{}
	clock  inboxClock
}

// asciiLower lowers ASCII letters only, exactly what SQLite's lower() does, so
// the needle and the haystack are folded the same way.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// parseInboxFilters reads space, unread, attention, deadline (with now and
// day_end), crew, agent and q. A bad value is an error message for a 400.
//
// The routes declare these names by hand because the OpenAPI generator cannot
// see through this helper (see helper_parsed_params_test.go).
func parseInboxFilters(params url.Values, nowFn func() time.Time) (inboxFilters, string) {
	var f inboxFilters
	var sb strings.Builder
	add := func(sql string, args ...interface{}) {
		sb.WriteString(" AND " + sql)
		f.args = append(f.args, args...)
	}

	f.clock = inboxClock{now: nowFn().UTC()}
	if v := params.Get("now"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, "invalid now: want RFC3339"
		}
		f.clock.now = t.UTC()
	}
	f.clock.dayEnd = time.Date(f.clock.now.Year(), f.clock.now.Month(), f.clock.now.Day(), 23, 59, 59, 999_000_000, time.UTC)
	if v := params.Get("day_end"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, "invalid day_end: want RFC3339"
		}
		f.clock.dayEnd = t.UTC()
	}

	switch v := params.Get("space"); v {
	case "":
	case inboxSpaceHistory:
		add(`state = 'resolved'`)
	case inboxSpaceAction:
		add(`state != 'resolved' AND ` + inboxActionableSQL)
	case inboxSpaceUpdates:
		add(`state != 'resolved' AND NOT COALESCE(` + inboxActionableSQL + `, 0)`)
	default:
		return f, "invalid space: want action, updates or history"
	}

	switch v := params.Get("unread"); v {
	case "", "false", "0":
	case "true", "1":
		add(inboxEffectiveStateExpr + ` = 'unread'`)
	default:
		return f, "invalid unread: want true or false"
	}

	switch v := params.Get("attention"); v {
	case "":
	case inboxAttentionDecision, inboxAttentionInput, inboxAttentionReview, inboxAttentionRepair:
		add(inboxAttentionSQL+` = ?`, v)
	default:
		return f, "invalid attention: want decision, input, review or repair"
	}

	if v := params.Get("deadline"); v != "" {
		sql, args := f.clock.deadlineSQL(v)
		if sql == "" {
			return f, "invalid deadline: want hour, today, soon or none"
		}
		add(sql, args...)
	}

	if v := params.Get("crew"); v != "" {
		add(inboxCrewIDSQL+` = ?`, v)
	}
	if v := params.Get("agent"); v != "" {
		add(`(`+inboxPayloadText("agent_id")+` = ? OR `+inboxPayloadText("agent_slug")+` = ? OR `+inboxPayloadText("agent_name")+` = ?
			OR (sender_type = 'agent' AND sender_name = ?))`, v, v, v, v)
	}

	if q := asciiLower(strings.TrimSpace(params.Get("q"))); q != "" {
		// instr, not LIKE: the text is searched for, never interpreted, so a
		// % or _ typed into the box means itself.
		hay := []string{
			`lower(title)`, `lower(COALESCE(body_md, ''))`, `lower(COALESCE(sender_name, ''))`, `lower(kind)`,
			`lower(COALESCE(` + inboxPayloadText("agent_name") + `, ''))`,
			`lower(COALESCE(` + inboxPayloadText("agent_slug") + `, ''))`,
		}
		parts := make([]string, 0, len(hay)+2)
		args := make([]interface{}, 0, len(hay)+4)
		for _, h := range hay {
			parts = append(parts, `instr(`+h+`, ?) > 0`)
			args = append(args, q)
		}
		// The page also searches the crew and agent names it resolves.
		parts = append(parts, `EXISTS (SELECT 1 FROM crews c WHERE c.workspace_id = inbox_items.workspace_id AND c.id = `+inboxCrewIDSQL+
			` AND (instr(lower(c.name), ?) > 0 OR instr(lower(c.slug), ?) > 0))`)
		parts = append(parts, `EXISTS (SELECT 1 FROM agents a WHERE a.workspace_id = inbox_items.workspace_id
			AND (a.id = `+inboxPayloadText("agent_id")+` OR a.slug = `+inboxPayloadText("agent_slug")+` OR (inbox_items.sender_type = 'agent' AND a.slug = inbox_items.sender_name))
			AND (instr(lower(a.name), ?) > 0 OR instr(lower(a.slug), ?) > 0))`)
		args = append(args, q, q, q, q)
		add(`(`+strings.Join(parts, " OR ")+`)`, args...)
	}

	f.clause = sb.String()
	return f, ""
}

// inboxCursor is the keyset position of the last row of a page. The list is
// ordered (created_at DESC, id DESC), so "after" is the rows strictly below
// this pair: a row inserted or resolved mid-walk cannot shift the window the
// way an OFFSET does.
type inboxCursor struct{ createdAt, id string }

const inboxCursorSep = "\x1f"

func (c inboxCursor) encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1" + inboxCursorSep + c.createdAt + inboxCursorSep + c.id))
}

func decodeInboxCursor(s string) (inboxCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return inboxCursor{}, false
	}
	parts := strings.Split(string(raw), inboxCursorSep)
	if len(parts) != 3 || parts[0] != "v1" || parts[1] == "" || parts[2] == "" {
		return inboxCursor{}, false
	}
	return inboxCursor{createdAt: parts[1], id: parts[2]}, true
}
