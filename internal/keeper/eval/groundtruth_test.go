package eval

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

// TestLoadCorpus_HumanResolutionBeatsIncumbentDecision is the P4 regression
// test: the corpus must be labelled from the human's verdict, never from the
// keeper's own past decision.
//
// Scenario: the keeper ALLOWed agent `ag1` access to credential `cr1`; a human
// then REJECTED the escalation that agent raised for that exact credential.
// The human is right by construction — the label must be DENY. Before this
// change LoadCorpus read keeper_requests.decision, so the row came back ALLOW
// and any candidate that reproduced the keeper's mistake scored a perfect
// agreement.
func TestLoadCorpus_HumanResolutionBeatsIncumbentDecision(t *testing.T) {
	db := newCorpusDB(t)
	insertKeeperRow(t, db, keeperRow{
		id: "k1", agentID: "ag1", credID: "cr1",
		reqType: "access", prompt: "p1", decision: "ALLOW", risk: nullInt(3),
		createdAt: "2026-01-01T00:00:01Z",
	})
	insertEscalation(t, db, escalationRow{
		id: "e1", agentID: "ag1", credID: "cr1",
		status: "RESOLVED", action: "reject", resolvedBy: "user",
		resolvedAt: "2026-01-02T00:00:00Z",
	})

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(got), got)
	}
	if got[0].Label != Deny {
		t.Errorf("Label = %q, want DENY — the human rejected this pair; "+
			"the keeper's own ALLOW is not ground truth", got[0].Label)
	}
	if got[0].LabelSource != LabelHuman {
		t.Errorf("LabelSource = %q, want %q", got[0].LabelSource, LabelHuman)
	}
	if got[0].Incumbent != Allow {
		t.Errorf("Incumbent = %q, want ALLOW — the shipped decision must stay "+
			"visible so agreement-with-the-predecessor is still reportable", got[0].Incumbent)
	}
}

// TestLoadCorpus_SystemAutoResolveIsNotGroundTruth guards the trap in the
// escalations schema: resolved_by is the literal actor kind, not a user id, and
// 'system' means autoResolveEscalationsForCredential closed the row by matching
// a credential name in free-form prose. Its own doc calls that a bounded guess
// ("worst case it closes one stale row early"). Treating a guess as the human
// label would relabel the corpus with a heuristic and quietly restore the very
// defect P4 exists to remove.
func TestLoadCorpus_SystemAutoResolveIsNotGroundTruth(t *testing.T) {
	db := newCorpusDB(t)
	insertKeeperRow(t, db, keeperRow{
		id: "k1", agentID: "ag1", credID: "cr1",
		reqType: "access", prompt: "p1", decision: "ALLOW", risk: nullInt(3),
		createdAt: "2026-01-01T00:00:01Z",
	})
	insertEscalation(t, db, escalationRow{
		id: "e1", agentID: "ag1", credID: "cr1",
		status: "RESOLVED", action: "reject", resolvedBy: "system",
		resolvedAt: "2026-01-02T00:00:00Z",
	})

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].LabelSource != LabelIncumbent {
		t.Errorf("LabelSource = %q, want %q — resolved_by='system' is the "+
			"auto-resolver, not a human", got[0].LabelSource, LabelIncumbent)
	}
	if got[0].Label != Allow {
		t.Errorf("Label = %q, want the incumbent ALLOW to stand", got[0].Label)
	}
}

// TestLoadCorpus_RedirectIsNotAVerdict: 'redirect' hands the escalation to
// another agent. It answers "who should deal with this", not "should this
// access be granted" — mapping it to either ALLOW or DENY would fabricate a
// label, which is worse than having none.
func TestLoadCorpus_RedirectIsNotAVerdict(t *testing.T) {
	db := newCorpusDB(t)
	insertKeeperRow(t, db, keeperRow{
		id: "k1", agentID: "ag1", credID: "cr1",
		reqType: "access", prompt: "p1", decision: "DENY", risk: nullInt(8),
		createdAt: "2026-01-01T00:00:01Z",
	})
	insertEscalation(t, db, escalationRow{
		id: "e1", agentID: "ag1", credID: "cr1",
		status: "RESOLVED", action: "redirect", resolvedBy: "user",
		resolvedAt: "2026-01-02T00:00:00Z",
	})

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 || got[0].LabelSource != LabelIncumbent {
		t.Fatalf("redirect must not produce a human label; got %+v", got)
	}
}

// TestLoadCorpus_InboxResolutionLabelsTheExactRequest covers the one link that
// is exact rather than pair-level: a keeper ESCALATE writes an inbox item whose
// source_id IS the keeper_requests.id (keeper_request.go), and that item is
// source-less, so the operator resolves it on the inbox row itself. That
// resolution is a human verdict on THIS request, so it outranks any pair-level
// escalation match.
func TestLoadCorpus_InboxResolutionLabelsTheExactRequest(t *testing.T) {
	db := newCorpusDB(t)
	insertKeeperRow(t, db, keeperRow{
		id: "k1", agentID: "ag1", credID: "cr1",
		reqType: "access", prompt: "p1", decision: "ESCALATE", risk: nullInt(7),
		createdAt: "2026-01-01T00:00:01Z",
	})
	// Pair-level says reject; the operator approved this specific request.
	insertEscalation(t, db, escalationRow{
		id: "e1", agentID: "ag1", credID: "cr1",
		status: "RESOLVED", action: "reject", resolvedBy: "user",
		resolvedAt: "2026-01-02T00:00:00Z",
	})
	insertInboxItem(t, db, inboxRow{
		id: "i1", sourceID: "k1", state: "resolved",
		resolvedAction: "approved", resolvedByUserID: "usr_1",
	})

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].Label != Allow || got[0].LabelSource != LabelHuman {
		t.Errorf("Label/source = %q/%q, want ALLOW/human", got[0].Label, got[0].LabelSource)
	}
	if got[0].LabelOrigin != OriginInbox {
		t.Errorf("LabelOrigin = %q, want %q", got[0].LabelOrigin, OriginInbox)
	}
}

// TestLoadCorpus_NonVerdictInboxActionsAreIgnored: the inbox resolve action set
// is free-form ("approved | denied | retried | cancelled | acknowledged |
// dismissed", plus "archived"). Only approved/denied say anything about whether
// the access should have been granted; the rest mean "I cleared my inbox".
func TestLoadCorpus_NonVerdictInboxActionsAreIgnored(t *testing.T) {
	for _, action := range []string{"dismissed", "acknowledged", "archived", "retried", "cancelled", ""} {
		t.Run("action="+action, func(t *testing.T) {
			db := newCorpusDB(t)
			insertKeeperRow(t, db, keeperRow{
				id: "k1", agentID: "ag1", credID: "cr1",
				reqType: "access", prompt: "p1", decision: "DENY", risk: nullInt(6),
				createdAt: "2026-01-01T00:00:01Z",
			})
			insertInboxItem(t, db, inboxRow{
				id: "i1", sourceID: "k1", state: "resolved",
				resolvedAction: action, resolvedByUserID: "usr_1",
			})

			got, err := LoadCorpus(context.Background(), db, 0)
			if err != nil {
				t.Fatalf("LoadCorpus: %v", err)
			}
			if len(got) != 1 || got[0].LabelSource != LabelIncumbent {
				t.Fatalf("action %q must not label the row; got %+v", action, got)
			}
		})
	}
}

// TestLoadCorpus_LatestHumanResolutionWins: a human may reject a pair and later
// approve it (the agent's remit changed). The standing position is the newest
// one; scoring against a superseded verdict would penalise a candidate for
// agreeing with the operator.
func TestLoadCorpus_LatestHumanResolutionWins(t *testing.T) {
	db := newCorpusDB(t)
	insertKeeperRow(t, db, keeperRow{
		id: "k1", agentID: "ag1", credID: "cr1",
		reqType: "access", prompt: "p1", decision: "DENY", risk: nullInt(6),
		createdAt: "2026-01-01T00:00:01Z",
	})
	insertEscalation(t, db, escalationRow{
		id: "old", agentID: "ag1", credID: "cr1",
		status: "RESOLVED", action: "reject", resolvedBy: "user",
		resolvedAt: "2026-01-02T00:00:00Z",
	})
	insertEscalation(t, db, escalationRow{
		id: "new", agentID: "ag1", credID: "cr1",
		status: "RESOLVED", action: "approve", resolvedBy: "user",
		resolvedAt: "2026-03-02T00:00:00Z",
	})

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 || got[0].Label != Allow {
		t.Fatalf("newest human resolution must win; got %+v", got)
	}
}

// TestLoadCorpus_HumanRowsSurviveTheLimit: human-labelled rows are the scarce
// resource — a `--limit 1` run that ordered purely by recency would drop the
// only row worth scoring and leave a corpus that measures nothing but
// agreement with the predecessor.
func TestLoadCorpus_HumanRowsSurviveTheLimit(t *testing.T) {
	db := newCorpusDB(t)
	insertKeeperRow(t, db, keeperRow{
		id: "human", agentID: "ag1", credID: "cr1",
		reqType: "access", prompt: "p1", decision: "ALLOW", risk: nullInt(3),
		createdAt: "2026-01-01T00:00:01Z", // older
	})
	insertEscalation(t, db, escalationRow{
		id: "e1", agentID: "ag1", credID: "cr1",
		status: "RESOLVED", action: "reject", resolvedBy: "user",
		resolvedAt: "2026-01-02T00:00:00Z",
	})
	insertKeeperRow(t, db, keeperRow{
		id: "newer", agentID: "ag2", credID: "cr2",
		reqType: "access", prompt: "p2", decision: "ALLOW", risk: nullInt(3),
		createdAt: "2026-05-01T00:00:01Z", // newer, but unlabelled
	})

	got, err := LoadCorpus(context.Background(), db, 1)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 || got[0].ID != "human" {
		t.Fatalf("limit must keep the human-labelled row; got %+v", got)
	}
}

// TestLoadCorpus_CrossPairEscalationDoesNotLabel: the pair-level join is only
// sound because both halves must match. A human verdict about a DIFFERENT
// credential, or a different agent, says nothing about this request.
func TestLoadCorpus_CrossPairEscalationDoesNotLabel(t *testing.T) {
	db := newCorpusDB(t)
	insertKeeperRow(t, db, keeperRow{
		id: "k1", agentID: "ag1", credID: "cr1",
		reqType: "access", prompt: "p1", decision: "ALLOW", risk: nullInt(3),
		createdAt: "2026-01-01T00:00:01Z",
	})
	insertEscalation(t, db, escalationRow{ // same agent, other credential
		id: "e1", agentID: "ag1", credID: "cr9",
		status: "RESOLVED", action: "reject", resolvedBy: "user",
		resolvedAt: "2026-01-02T00:00:00Z",
	})
	insertEscalation(t, db, escalationRow{ // same credential, other agent
		id: "e2", agentID: "ag9", credID: "cr1",
		status: "RESOLVED", action: "reject", resolvedBy: "user",
		resolvedAt: "2026-01-02T00:00:00Z",
	})

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 1 || got[0].LabelSource != LabelIncumbent {
		t.Fatalf("a verdict on another pair must not label this row; got %+v", got)
	}
}

// --- fixtures -------------------------------------------------------------

type keeperRow struct {
	id, agentID, credID string
	reqType, prompt     string
	decision            string
	risk                sql.NullInt64
	createdAt           string
}

func insertKeeperRow(t *testing.T, db *sql.DB, r keeperRow) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO keeper_requests
			(id, requesting_agent_id, credential_id, request_type, ollama_prompt, decision, risk_score, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.id, r.agentID, r.credID, r.reqType, r.prompt, r.decision, r.risk, r.createdAt)
	if err != nil {
		t.Fatalf("insert keeper_requests %s: %v", r.id, err)
	}
}

type escalationRow struct {
	id, agentID, credID string
	status, action      string
	resolvedBy          string
	resolvedAt          string
}

func insertEscalation(t *testing.T, db *sql.DB, r escalationRow) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO escalations
			(id, from_agent_id, credential_id, status, action, resolved_by, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.id, r.agentID, r.credID, r.status, r.action, r.resolvedBy, r.resolvedAt)
	if err != nil {
		t.Fatalf("insert escalations %s: %v", r.id, err)
	}
}

type inboxRow struct {
	id, sourceID     string
	state            string
	resolvedAction   string
	resolvedByUserID string
}

func insertInboxItem(t *testing.T, db *sql.DB, r inboxRow) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO inbox_items
			(id, kind, source_id, state, resolved_action, resolved_by_user_id)
		VALUES (?, 'escalation', ?, ?, ?, ?)`,
		r.id, r.sourceID, r.state, r.resolvedAction, r.resolvedByUserID)
	if err != nil {
		t.Fatalf("insert inbox_items %s: %v", r.id, err)
	}
}

// One operator decision is one piece of ground truth, however many requests the
// pair accumulated. The escalation join is pair-level (agent + credential), so
// without a cap a single resolution labels EVERY request that pair ever made —
// and the corpus then reports, truthfully by its own arithmetic, "120
// human-labelled rows, benchmark grade" on the strength of one click.
//
// That is the exact failure this file exists to remove, arrived at from the
// other side: the incumbent-label defect let a consistently wrong model score
// perfectly, and this one lets an anecdote authorise a merge. A ratchet that can
// be fooled is worse than no ratchet, because it is trusted.
//
// The inbox path is deliberately NOT capped — it links to a single request, so
// N inbox resolutions really are N human decisions.
func TestLoadCorpus_OneEscalationLabelsOneRow(t *testing.T) {
	db := newCorpusDB(t)
	for i := 0; i < 5; i++ {
		insertKeeperRow(t, db, keeperRow{
			id: fmt.Sprintf("kr%d", i), agentID: "ag1", credID: "cr1",
			reqType: "access", prompt: "P", decision: "DENY",
			risk:      sql.NullInt64{Int64: 5, Valid: true},
			createdAt: fmt.Sprintf("2026-07-2%dT10:00:00Z", i),
		})
	}
	insertEscalation(t, db, escalationRow{
		id: "esc1", agentID: "ag1", credID: "cr1",
		status: "RESOLVED", action: "approve", resolvedBy: "user",
		resolvedAt: "2026-07-25T10:00:00Z",
	})

	got, err := LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	human := 0
	for _, r := range got {
		if r.LabelSource == LabelHuman {
			human++
		}
	}
	if human != 1 {
		t.Errorf("human-labelled rows = %d, want 1 — one operator decision is one label, not one per request the pair made", human)
	}
	if len(got) != 5 {
		t.Errorf("corpus size = %d, want all 5 rows retained (the surplus falls back to the incumbent label, it is not dropped)", len(got))
	}
}
