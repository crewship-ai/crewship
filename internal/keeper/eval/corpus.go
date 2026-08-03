package eval

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// LabelSource says where a corpus row's reference label came from. It exists
// because the two sources answer different questions and must never be pooled:
// a human label says whether the access SHOULD have been granted, while the
// incumbent's own past decision only says what the previously configured model
// happened to say. Scoring a candidate against the latter measures agreement
// with a predecessor — a model that is consistently wrong scores perfectly.
type LabelSource string

const (
	// LabelHuman: a named person resolved the request one way or the other.
	// This is the only label a correctness claim may rest on.
	LabelHuman LabelSource = "human"
	// LabelReference: an AI reference model adjudicated this row — a stronger
	// model than the one under test, ruling deliberately so the corpus has
	// labels at all. It is a legitimate technique and it answers a real question
	// ("can a 9B model on a laptop match a frontier model's judgement?"), which
	// scales and reproduces where twenty human clicks do not.
	//
	// It is NOT ground truth, and the distinction is not pedantry: recorded as
	// human, the eval would report "agrees with the person 0.81" about a number
	// that measured "agrees with the reference model 0.81" — a false claim inside the figure a
	// security decision rests on. IsHuman() is false for it, so every caller that
	// gates on ground truth excludes it by default.
	//
	// P3 (precedent few-shot) must NEVER draw on it. Precedent is a learning
	// loop, and feeding a model's own judgement back as worked examples is how a
	// wrong call becomes house style.
	LabelReference LabelSource = "reference"
	// LabelIncumbent: no human ever ruled on this row, so the label falls back
	// to keeper_requests.decision. Kept (rather than dropped) because a large
	// incumbent-labelled segment still detects gross behavioural drift, but it
	// is reported separately and never as correctness.
	LabelIncumbent LabelSource = "incumbent"
)

// IsHuman reports whether a row carries genuine ground truth — a PERSON's
// ruling. Deliberately false for LabelReference: an AI adjudication is a useful
// label and it is not ground truth, and every caller that gates on this excludes
// it by default rather than having to remember to.
func (s LabelSource) IsHuman() bool { return s == LabelHuman }

// keeperActorReference is the ledger actor_type an AI reference adjudication
// writes. It lives here rather than in internal/api because this package is what
// gives it meaning — the API only records it.
const keeperActorReference = "reference"

// LabelOrigin names the exact column a label was read from, so an operator
// auditing a surprising score can go straight to the row that produced it.
type LabelOrigin string

const (
	// OriginInbox: inbox_items.resolved_action on the item whose source_id IS
	// this keeper_requests.id. The keeper writes that item on ESCALATE
	// (internal/api/keeper_request.go) and it is source-less, so the operator
	// resolves it on the inbox row itself. Exact, per-request, and the strongest
	// link available.
	OriginInbox LabelOrigin = "inbox_items.resolved_action"
	// OriginEscalation: escalations.action on a row the same agent raised about
	// the same credential and a human resolved. Pair-level rather than
	// per-request (see humanEscalationSQL for why that is still sound).
	OriginEscalation LabelOrigin = "escalations.action"
	// OriginIncumbentDecision: keeper_requests.decision — the fallback, not truth.
	OriginIncumbentDecision LabelOrigin = "keeper_requests.decision"
)

// CorpusRow is one recorded keeper decision to replay: the exact prompt
// production sent, the reference label to score against, and the decision the
// incumbent actually shipped.
//
// Label and Incumbent are deliberately separate fields. Before P4 they were the
// same value, which is how the harness came to measure agreement-with-the-
// predecessor and call it accuracy. Keeping both means a run can report "the
// candidate matches the human 0.81 of the time and the old model 0.62 of the
// time" — two numbers that mean different things and now look different.
type CorpusRow struct {
	ID          string
	RequestType string
	Prompt      string // keeper_requests.ollama_prompt — replayed verbatim

	// Label is what the candidate is scored against; LabelSource says whether
	// that is ground truth or a fallback, and LabelOrigin names the column.
	Label       Decision
	LabelSource LabelSource
	LabelOrigin LabelOrigin

	// Incumbent is the normalized keeper_requests.decision — what production
	// shipped, made by whatever model was configured then. Equal to Label on
	// LabelIncumbent rows by definition.
	Incumbent Decision
	// IncumbentRisk is keeper_requests.risk_score clamped to [1,10]; a NULL risk
	// on a settled row degrades to 1 (the clamp floor production already applies
	// on write), so scoring never sees an out-of-range value.
	//
	// There is no human counterpart: an operator approves or rejects, they never
	// hand back a 1–10 score. Risk error is therefore always measured against the
	// incumbent and is a drift signal, never a correctness one.
	IncumbentRisk int

	// SecurityLevel is the credential's tier, or 0 when none could be resolved.
	// Carried because keeper_requests.decision is the POST-tier-floor decision:
	// without it a replay compares a raw model verdict against a recorded one
	// that policy already modified, and reports the difference as the model
	// disagreeing with itself. See applyRecordedPolicy.
	SecurityLevel int
}

// corpusRequestTypes are the live-activity request types the harness scores.
// skill_review / memory_health / negative_learning are lower value for
// governance-model selection (spec §3), so they are excluded from the corpus.
//
// `behavior` is deliberately EXCLUDED for now even though M1 targets it. The
// replay path normalizes model output with gatekeeper.NormalizeRawResponse,
// whose closed set is ALLOW/DENY/ESCALATE (WARN → DENY). But the LIVE behavior
// path records decisions via classifyBehaviorDecision, which keeps WARN as a
// first-class outcome. Scoring behavior rows here would (a) mis-score any
// candidate that legitimately answers WARN as a DENY disagreement, and (b) the
// decision filter below already silently drops behavior rows recorded as WARN —
// both skew the governance-model selection for that one type. access/execute
// both map cleanly onto NormalizeRawResponse, so they stay. Follow-up: route
// behavior replay through classifyBehaviorDecision, then add it back.
var corpusRequestTypes = []string{"access", "execute"}

// humanInboxSQL resolves the exact, per-request human label.
//
// A keeper ESCALATE writes an inbox item with source_id = keeper_requests.id.
// That item has no backing escalations row, so the inbox PATCH path lets the
// operator resolve it directly — resolved_by_user_id names them, resolved_action
// records what they chose. (kind, source_id) is UNIQUE, so at most one row
// matches.
//
// Only 'approved' and 'denied' are verdicts. The action vocabulary also carries
// retried / cancelled / acknowledged / dismissed / archived, all of which mean
// "I cleared my inbox" — reading any of them as a decision would invent a label,
// and an invented label is worse than a missing one because it looks like data.
// referenceLedgerSQL reads the provenance of the terminal decision.
//
// keeper_request_events is already the record of WHO decided — actor_type
// carries keeper / user / system / agent — so an adjudication does not need a
// new column anywhere, only a new actor. A request whose terminal transition was
// made by a `reference` actor is labelled reference, never human.
//
// Empty for a resolution with no ledger entry, which is what every pre-ledger
// row looks like. Those keep reading as human: downgrading them would erase
// every human label the product collected before the ledger existed.
const referenceLedgerSQL = `
	COALESCE((
		SELECT LOWER(e.actor_type)
		FROM keeper_request_events e
		WHERE e.request_id = kr.id
		  AND UPPER(e.state) IN ('ALLOW','DENY')
		ORDER BY e.seq DESC
		LIMIT 1
	), '')`

const humanInboxSQL = `
	COALESCE((
		SELECT LOWER(i.resolved_action)
		FROM inbox_items i
		WHERE i.kind = 'escalation'
		  AND i.source_id = kr.id
		  AND i.state = 'resolved'
		  AND COALESCE(i.resolved_by_user_id, '') != ''
		  AND LOWER(COALESCE(i.resolved_action, '')) IN ('approved', 'denied')
	), '')`

// humanEscalationSQL resolves the pair-level human label: a human's standing
// position on "may this agent hold this credential".
//
// Three details in the escalations schema decide whether this is truth or noise:
//
//  1. resolved_by holds the actor KIND ('user' or 'system'), not a user id.
//     'system' is autoResolveEscalationsForCredential, which closes a row by
//     whole-word-matching a credential name in free-form prose and whose own
//     doc concedes "worst case it closes one stale row early". Accepting it
//     would relabel the corpus with a heuristic — precisely the defect P4
//     removes. Only 'user' counts.
//  2. action is approve | reject | redirect. Redirect hands the ask to another
//     agent; it answers "who deals with this", not "should this be granted", so
//     it yields no label.
//  3. credential_id (v119) is the only structural link between an escalation and
//     a keeper request. It is NULL on every legacy / plain escalation, which is
//     why the human-labelled segment is small — the honest consequence of
//     refusing to name-match. Requiring BOTH the agent and the credential to
//     match is what keeps the pair-level join sound: a verdict about a different
//     agent, or a different credential, is not about this request.
//
// Newest resolution wins: an operator may reject a pair and later approve it as
// the agent's remit changes, and scoring against a superseded verdict would
// penalise a candidate for agreeing with the operator's current position.
const humanEscalationSQL = `
	COALESCE((
		SELECT LOWER(e.action)
		FROM escalations e
		WHERE e.from_agent_id = kr.requesting_agent_id
		  AND e.credential_id = kr.credential_id
		  AND COALESCE(e.credential_id, '') != ''
		  AND e.status = 'RESOLVED'
		  AND e.resolved_by = 'user'
		  AND LOWER(COALESCE(e.action, '')) IN ('approve', 'reject')
		ORDER BY e.resolved_at DESC
		LIMIT 1
	), '')`

// LoadCorpus reads the recorded keeper_requests corpus for replay: rows with a
// non-empty ollama_prompt and a *settled* decision (ALLOW/DENY/ESCALATE —
// PENDING and NULL are excluded), filtered to the live-activity request types,
// each relabelled from human ground truth where a human ruled on it.
// limit <= 0 means no limit.
//
// Ordering puts every human-labelled row first, then falls back to newest-first.
// Recency alone would be wrong under a limit: human labels are the scarce
// resource (see humanEscalationSQL on why), so a `--limit 200` run ordered by
// created_at could drop every row worth scoring and hand back a corpus that
// measures nothing but agreement with the predecessor — with no visible sign
// that it had.
//
// The query is intentionally server-wide: keeper_requests has no workspace_id
// column (it is crew-scoped), and the harness picks a single curated *global*
// default model, so scoping to one workspace would only shrink the corpus.
func LoadCorpus(ctx context.Context, db *sql.DB, limit int) ([]CorpusRow, error) {
	// keeper_requests.request_type is a closed CHECK set; build the IN list
	// from corpusRequestTypes so the two can't drift.
	placeholders := make([]string, len(corpusRequestTypes))
	args := make([]any, 0, len(corpusRequestTypes)+1)
	for i, rt := range corpusRequestTypes {
		placeholders[i] = "?"
		args = append(args, rt)
	}

	q := fmt.Sprintf(`
		SELECT kr.id, kr.request_type, kr.ollama_prompt, kr.decision, kr.risk_score,
		       kr.requesting_agent_id, kr.credential_id,
		       %s AS human_inbox_action,
		       %s AS human_escalation_action,
		       %s AS terminal_actor,
		       COALESCE((SELECT c.security_level FROM credentials c
		                  WHERE c.id = kr.credential_id), 0) AS security_level
		FROM keeper_requests kr
		WHERE kr.request_type IN (%s)
		  AND kr.ollama_prompt IS NOT NULL AND kr.ollama_prompt != ''
		  AND UPPER(kr.decision) IN ('ALLOW','DENY','ESCALATE')
		ORDER BY (human_inbox_action != '' OR human_escalation_action != '') DESC,
		         kr.created_at DESC`,
		humanInboxSQL, humanEscalationSQL, referenceLedgerSQL, strings.Join(placeholders, ","))
	if limit > 0 {
		q += "\n\t\tLIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load keeper corpus: %w", err)
	}
	defer rows.Close()

	var out []CorpusRow
	// Pairs whose single escalation verdict has already been spent on a row.
	labelledPairs := map[string]bool{}
	for rows.Next() {
		var (
			id, reqType, prompt, decision string
			agentID, credID               string
			inboxAction, escAction        string
			terminalActor                 string
			securityLevel                 int
			risk                          sql.NullInt64
		)
		if err := rows.Scan(&id, &reqType, &prompt, &decision, &risk, &agentID, &credID, &inboxAction, &escAction, &terminalActor, &securityLevel); err != nil {
			return nil, fmt.Errorf("scan keeper corpus row: %w", err)
		}
		incumbent := Decision(strings.ToUpper(decision))
		row := CorpusRow{
			ID:            id,
			RequestType:   reqType,
			Prompt:        prompt,
			Incumbent:     incumbent,
			IncumbentRisk: clampRisk(risk),
			SecurityLevel: securityLevel,
			Label:         incumbent,
			LabelSource:   LabelIncumbent,
			LabelOrigin:   OriginIncumbentDecision,
		}
		// Per-request beats pair-level: the inbox resolution is a human ruling on
		// THIS request, while an escalation match is a ruling on the agent/
		// credential pair that may predate it.
		if d, ok := decisionFromInboxAction(inboxAction); ok {
			// Per-request: an inbox resolution links to THIS request, so N of them
			// really are N human decisions. No cap.
			//
			// WHO decided comes from the ledger, not from the inbox row: the inbox
			// only records that somebody resolved it, and an AI adjudication and a
			// person's ruling look identical there. Labelling both `human` would
			// make the eval report agreement with a person about a number that
			// measured agreement with a model.
			src := LabelHuman
			if terminalActor == keeperActorReference {
				src = LabelReference
			}
			row.Label, row.LabelSource, row.LabelOrigin = d, src, OriginInbox
		} else if d, ok := decisionFromEscalationAction(escAction); ok {
			// Pair-level, so it must be counted ONCE. The join matches on
			// (agent, credential), which means one operator decision would
			// otherwise label every request that pair ever made — and the corpus
			// would then report "120 human-labelled rows, benchmark grade" on the
			// strength of a single click. A ratchet that can be fooled is worse
			// than no ratchet, because it is trusted.
			//
			// Rows are ordered human-first then newest-first, so the row that
			// keeps the label is the most recent request the verdict plausibly
			// ruled on. The rest are not dropped — they stay as incumbent-labelled
			// corpus, which is what they honestly are.
			pair := agentID + "\x00" + credID
			if !labelledPairs[pair] {
				labelledPairs[pair] = true
				row.Label, row.LabelSource, row.LabelOrigin = d, LabelHuman, OriginEscalation
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate keeper corpus: %w", err)
	}
	return out, nil
}

// decisionFromInboxAction maps the verdict-bearing inbox actions onto keeper
// decisions. Note what is absent: ESCALATE. A human resolving the item IS the
// escalation being carried out, so their answer is always the terminal one —
// mapping anything here to ESCALATE would score a candidate as correct for
// deferring a question a person already answered.
func decisionFromInboxAction(action string) (Decision, bool) {
	switch action {
	case "approved":
		return Allow, true
	case "denied":
		return Deny, true
	default:
		return "", false
	}
}

// decisionFromEscalationAction maps escalations.action onto keeper decisions.
// 'redirect' is intentionally unmapped — see humanEscalationSQL.
func decisionFromEscalationAction(action string) (Decision, bool) {
	switch action {
	case "approve":
		return Allow, true
	case "reject":
		return Deny, true
	default:
		return "", false
	}
}

// CountBySource tallies a corpus by label source. Every caller that prints a
// percentage needs these two numbers next to it: a run whose human segment is
// three rows is an anecdote, and the only thing stopping it being read as a
// benchmark is that the count is on screen.
func CountBySource(corpus []CorpusRow) (human, incumbent int) {
	for _, r := range corpus {
		if r.LabelSource.IsHuman() {
			human++
			continue
		}
		incumbent++
	}
	return human, incumbent
}

// clampRisk maps a nullable recorded risk_score into the valid [1,10] range,
// mirroring the clamp the gatekeeper applies on write. A NULL or sub-floor
// value degrades to 1 rather than being dropped — the decision is what the
// scorer's safety metric turns on; risk MAE is secondary.
func clampRisk(n sql.NullInt64) int {
	if !n.Valid || n.Int64 < 1 {
		return 1
	}
	if n.Int64 > 10 {
		return 10
	}
	return int(n.Int64)
}
