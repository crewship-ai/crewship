package pages

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Wake gates and on_failure — the sensor half of a panel (PRD §5, §4 rule 4).
//
// §0 names this as the feature's entire payoff: "a cheap script pushes, a
// threshold wakes an agent, and the agent writes its analysis back onto the
// same page". Without it a Page is a read-only dashboard, which §2.1 documents
// as the reason the push-to-panel genre lost.
//
// This file is the PURE half, and it is deliberately the whole of the
// decision-making:
//
//   - parsing `when` into a predicate over one pushed payload;
//   - deciding whether the predicate has held long enough for `for`;
//   - checking, at authoring time, that a gate can ever fire at all.
//
// Nothing here reads a database, opens a socket or looks at the wall clock,
// for the same reason the freshness contract does not: "the condition held for
// exactly 5m" is only testable if the test owns the clock, and a predicate
// nobody can evaluate offline is a predicate nobody can audit.
//
// WHY THE PREDICATE IS NOT AN EXPRESSION LANGUAGE.
// PanelWake.When's doc comment says it: the gate is matched against a payload
// whose shape is one of five closed schemas, and an author reading somebody
// else's page has to be able to tell at a glance what wakes whom. The grammar
// below is two forms, both taken verbatim from the PRD's own example
// vocabulary, and anything else is refused AT SAVE TIME with a message naming
// what is accepted — never accepted-and-never-matched, which is the worst
// failure this genre has (internal/automation/types.go says the same thing
// about payload_equals, and Explain/Preview exist there because of it).

const (
	// MaxWakeGatesPerPanel bounds how many gates one panel may declare.
	//
	// Each gate compiles to one `automations` row, so this multiplied by
	// MaxPanelsPerPage (24) is the most rules one page can add to the matcher
	// that runs on the journal write path. Four is past what anybody has
	// wanted and small enough that the arithmetic stays boring: 96 rows for a
	// maximal page, against a matcher whose cost is a map lookup per entry.
	MaxWakeGatesPerPanel = 4

	// wakeMaxFor bounds `for`. Past a day the gate is not debouncing a bad
	// scrape any more, it is a report nobody asked for — and a panel silent
	// that long has already been picked up by on_failure, which is the
	// escalation path §4 rule 4 actually names.
	wakeMaxFor = 24 * time.Hour
)

// WakeQuantifier is how a status.v1 predicate folds over the payload's items.
type WakeQuantifier string

const (
	// WakeAny — at least one item satisfies the comparison. This is the PRD's
	// own example, `any(state == "critical")`.
	WakeAny WakeQuantifier = "any"
	// WakeAll — every item satisfies it, and an empty payload satisfies
	// nothing. "All clear" is a real thing to wake on (a recovery narrative),
	// and vacuous truth over zero items is not it.
	WakeAll WakeQuantifier = "all"
)

// wakeSubject is which of the two payload shapes a predicate reads.
type wakeSubject string

const (
	subjectStatusState wakeSubject = "status.state"
	subjectMetricValue wakeSubject = "metric.value"
)

// WakePredicate is one parsed `when`.
//
// The zero value matches nothing and reports so: a gate whose predicate failed
// to parse is refused at save time, so a zero predicate can only exist in a
// caller that ignored an error.
type WakePredicate struct {
	subject wakeSubject
	quant   WakeQuantifier
	op      string
	// state is the right-hand side for a status predicate.
	state StatusState
	// number is the right-hand side for a metric predicate.
	number float64
	// source is the text as authored, echoed in messages and stored on the
	// compiled gate so a reader of the `automations` row sees the sentence the
	// page author wrote rather than a reconstruction of it.
	source string
}

// String returns the predicate as authored.
func (p WakePredicate) String() string { return p.source }

// Schema reports which panel schema this predicate can read.
func (p WakePredicate) Schema() PanelSchema {
	if p.subject == subjectMetricValue {
		return SchemaMetric
	}
	return SchemaStatus
}

// ParseWakePredicate parses `when` and checks it against the panel's schema.
//
// The schema check is half the value of parsing here: `value > 10` on a
// status.v1 panel is well-formed and can never match, which is exactly the
// silent-forever rule this file refuses to let anybody save.
func ParseWakePredicate(schema PanelSchema, when string) (WakePredicate, error) {
	src := strings.TrimSpace(when)
	if src == "" {
		return WakePredicate{}, fmt.Errorf("wake gate declares no `when`; a gate with no threshold would wake on every push")
	}

	p, err := parseWakeForm(src)
	if err != nil {
		return WakePredicate{}, err
	}
	if want := p.Schema(); want != schema {
		return WakePredicate{}, fmt.Errorf(
			"`when: %s` reads a %s payload, but this panel declares %s; the gate could never match",
			src, want, schema)
	}
	return p, nil
}

// parseWakeForm recognises the two accepted shapes and refuses everything else
// with the grammar spelled out. Hand-written rather than regexp-driven so the
// error can say WHICH part of the sentence it stopped at.
func parseWakeForm(src string) (WakePredicate, error) {
	if quant, inner, ok := cutQuantifier(src); ok {
		field, op, rhs, err := cutComparison(inner, src)
		if err != nil {
			return WakePredicate{}, err
		}
		if field != "state" {
			return WakePredicate{}, wakeGrammarError(src,
				fmt.Sprintf("%s(%s …) reads no field called %q", quant, field, field))
		}
		if op != "==" && op != "!=" {
			return WakePredicate{}, wakeGrammarError(src,
				fmt.Sprintf("state is a word, so it compares with == or !=, not %s", op))
		}
		state, err := parseWakeState(rhs, src)
		if err != nil {
			return WakePredicate{}, err
		}
		return WakePredicate{
			subject: subjectStatusState,
			quant:   quant,
			op:      op,
			state:   state,
			source:  src,
		}, nil
	}

	field, op, rhs, err := cutComparison(src, src)
	if err != nil {
		return WakePredicate{}, err
	}
	if field != "value" {
		return WakePredicate{}, wakeGrammarError(src, fmt.Sprintf("there is no field called %q", field))
	}
	n, cerr := strconv.ParseFloat(strings.TrimSpace(rhs), 64)
	if cerr != nil {
		return WakePredicate{}, wakeGrammarError(src,
			fmt.Sprintf("value compares against a number, and %q is not one", strings.TrimSpace(rhs)))
	}
	return WakePredicate{subject: subjectMetricValue, op: op, number: n, source: src}, nil
}

// cutQuantifier splits `any( … )` / `all( … )` into its parts.
func cutQuantifier(src string) (WakeQuantifier, string, bool) {
	for _, q := range []WakeQuantifier{WakeAny, WakeAll} {
		prefix := string(q) + "("
		if strings.HasPrefix(src, prefix) && strings.HasSuffix(src, ")") {
			return q, strings.TrimSpace(src[len(prefix) : len(src)-1]), true
		}
	}
	return "", "", false
}

// cutComparison splits "<field> <op> <rhs>". The two-character operators are
// tried first, or `>=` would parse as `>` followed by a right-hand side of
// "= 5".
func cutComparison(expr, src string) (field, op, rhs string, err error) {
	for _, candidate := range []string{"==", "!=", ">=", "<=", ">", "<"} {
		if i := strings.Index(expr, candidate); i >= 0 {
			return strings.TrimSpace(expr[:i]), candidate, expr[i+len(candidate):], nil
		}
	}
	return "", "", "", wakeGrammarError(src, "there is no comparison in it")
}

// parseWakeState reads the quoted right-hand side of a state comparison and
// checks it against the closed set. A typo'd state is the other way a gate
// becomes permanently silent, and StatusState is closed precisely so this can
// be caught.
func parseWakeState(rhs, src string) (StatusState, error) {
	text := strings.TrimSpace(rhs)
	if len(text) >= 2 {
		if (text[0] == '"' && text[len(text)-1] == '"') || (text[0] == '\'' && text[len(text)-1] == '\'') {
			text = text[1 : len(text)-1]
		}
	}
	switch StatusState(text) {
	case StatusOK, StatusWarning, StatusCritical:
		return StatusState(text), nil
	}
	return "", wakeGrammarError(src, fmt.Sprintf(
		"%q is not a status.v1 state; the set is closed: %q, %q, %q",
		text, StatusOK, StatusWarning, StatusCritical))
}

func wakeGrammarError(src, why string) error {
	return fmt.Errorf("`when: %s` is not a wake predicate — %s. "+
		"The grammar is two forms and nothing else: "+
		`any(state == "critical") / all(state == "ok") over a status.v1 panel, `+
		"or value > 90 (also >=, <, <=, ==, !=) over a metric.v1 panel", src, why)
}

// Eval reports whether the predicate holds for one pushed payload.
//
// A payload of the wrong type is false rather than an error: the panel's
// schema is checked at save time and again on every push, so a mismatch here
// can only mean the panel's schema was edited under a stored gate, and the
// honest answer to "is the threshold crossed" for a payload the gate cannot
// read is no.
func (p WakePredicate) Eval(payload Payload) bool {
	switch p.subject {
	case subjectStatusState:
		sp, ok := payload.(*StatusPayload)
		if !ok || sp == nil || len(sp.Items) == 0 {
			return false
		}
		for _, item := range sp.Items {
			hit := item.State == p.state
			if p.op == "!=" {
				hit = !hit
			}
			if p.quant == WakeAny && hit {
				return true
			}
			if p.quant == WakeAll && !hit {
				return false
			}
		}
		return p.quant == WakeAll
	case subjectMetricValue:
		mp, ok := payload.(*MetricPayload)
		if !ok || !mp.HasValue() {
			// A null metric is "no basis to compute" (§9b.4), and no basis to
			// compute is no basis to wake anybody either.
			return false
		}
		v := *mp.Value
		switch p.op {
		case ">":
			return v > p.number
		case ">=":
			return v >= p.number
		case "<":
			return v < p.number
		case "<=":
			return v <= p.number
		case "==":
			return v == p.number
		case "!=":
			return v != p.number
		}
	}
	return false
}

// WakeGate is one compiled gate: everything the API layer needs to build its
// `automations` row and to decide, on a push, whether to arm it.
type WakeGate struct {
	// Index is the gate's 1-based position in the panel's `wake` list. It is
	// the gate's identity — the key in the journal payload, the suffix of the
	// automation id, and the gate_key of its alert row — so reordering a
	// panel's gates renames them, which is why the reconcile rewrites every
	// rule for a panel on every save rather than diffing them.
	Index int
	// PanelID is the panel the gate is declared on.
	PanelID string
	// When is the parsed predicate.
	When WakePredicate
	// For is how long the condition must hold before the gate fires. Zero is
	// "fire on the first push that satisfies it", which is a legal and useful
	// choice for a panel that is already debounced by its producer.
	For time.Duration
	// CrewSlug is who gets woken, parsed out of `agent: crew/<slug>`.
	CrewSlug string
	// Writes is the panel the woken agent is expected to write. A declaration,
	// not a grant (PanelWake.Writes): it is checked to exist on this page so a
	// gate cannot instruct an agent to write a panel nobody has authored, and
	// it is carried into the issue so the agent is told where to answer.
	Writes string
}

// PayloadKey is the journal-payload key that says this gate is armed.
//
// The key is per gate, not per panel, because internal/automation's matcher is
// exact-equality only (deliberately: a regex on the journal write path is a
// backtracking incident waiting for the first user who pastes one in). One key
// per gate is what lets two gates on the same panel be two independent rules
// rather than one rule that has to disambiguate itself at fire time.
func (g WakeGate) PayloadKey() string { return "wake_" + strconv.Itoa(g.Index) }

// CompileWakeGates parses every gate declared on a panel.
//
// The panel's own IDs are not resolved here — whether crew/devops exists is
// the handler's question, exactly as it is for `owner` and `producer`.
func CompileWakeGates(p PanelSpec) ([]WakeGate, error) {
	if len(p.Wake) == 0 {
		return nil, nil
	}
	if len(p.Wake) > MaxWakeGatesPerPanel {
		return nil, fmt.Errorf("declares %d wake gates; the cap is %d", len(p.Wake), MaxWakeGatesPerPanel)
	}
	out := make([]WakeGate, 0, len(p.Wake))
	for i := range p.Wake {
		w := p.Wake[i]
		pred, err := ParseWakePredicate(p.Schema, w.When)
		if err != nil {
			return nil, fmt.Errorf("wake gate %d: %v", i+1, err)
		}
		hold, err := parseWakeFor(w.For)
		if err != nil {
			return nil, fmt.Errorf("wake gate %d: %v", i+1, err)
		}
		crew, err := crewRefSlug("agent", w.Agent)
		if err != nil {
			return nil, fmt.Errorf("wake gate %d: %v", i+1, err)
		}
		out = append(out, WakeGate{
			Index:    i + 1,
			PanelID:  p.ID,
			When:     pred,
			For:      hold,
			CrewSlug: crew,
			Writes:   strings.TrimSpace(w.Writes),
		})
	}
	return out, nil
}

func parseWakeFor(raw string) (time.Duration, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("`for: %s` is not a duration (try 30s, 5m, 1h): %v", raw, err)
	}
	switch {
	case d < 0:
		return 0, fmt.Errorf("`for: %s` is negative; a gate cannot require the future to have already happened", raw)
	case d > wakeMaxFor:
		return 0, fmt.Errorf("`for: %s` is longer than %s; a panel silent that long is on_failure's job, not a wake gate's",
			raw, wakeMaxFor)
	}
	return d, nil
}

// crewRefSlug parses "crew/<slug>" the way PanelSpec.OwnerCrewSlug does.
//
// Both halves of this file point at a crew and never at a user or a single
// agent, and that is the same decision §2.3 makes for `owner`: the crew is the
// durable subject. An agent that leaves takes its gate with it otherwise, and
// the page would quietly stop waking anybody.
func crewRefSlug(field, ref string) (string, error) {
	kind, slug, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || kind != "crew" {
		return "", fmt.Errorf("%s %q must be crew/<slug>", field, ref)
	}
	if !crewSlugRE.MatchString(slug) {
		return "", fmt.Errorf("%s %q: %q is not a crew slug", field, ref, slug)
	}
	return slug, nil
}

// OnFailureCrewSlug parses `on_failure: {issue: crew/<slug>}`.
func OnFailureCrewSlug(f *PanelOnFailure) (string, error) {
	if f == nil {
		return "", nil
	}
	if strings.TrimSpace(f.Issue) == "" {
		return "", fmt.Errorf("on_failure declares nothing; the one thing it can say is issue: crew/<slug>")
	}
	return crewRefSlug("on_failure.issue", f.Issue)
}

// ValidateGates checks every panel's wake gates and on_failure block.
//
// It lives here rather than inside Document.Validate's loop for one reason:
// the rules it enforces are about the SENSOR, and the sensor's vocabulary
// (predicates, hold windows, crew references) has nothing to do with the
// spec's shape. Document.Validate calls it as its last act.
func ValidateGates(d *Document) error {
	panelIDs := make(map[string]bool, len(d.Spec.Panels))
	for i := range d.Spec.Panels {
		panelIDs[d.Spec.Panels[i].ID] = true
	}
	for i := range d.Spec.Panels {
		p := &d.Spec.Panels[i]
		gates, err := CompileWakeGates(*p)
		if err != nil {
			return newError(CodeInvalidSpec, p.Schema, "panel %q: %v", p.ID, err)
		}
		for _, g := range gates {
			// `writes` is checked against the page it is declared on, so a
			// typo is caught by the author instead of by an agent that was
			// woken, went looking for a panel and found nothing to write.
			if g.Writes != "" && !panelIDs[g.Writes] {
				return newError(CodeInvalidSpec, p.Schema,
					"panel %q wake gate %d writes %q, which is not a panel on this page",
					p.ID, g.Index, g.Writes)
			}
		}
		if _, err := OnFailureCrewSlug(p.OnFailure); err != nil {
			return newError(CodeInvalidSpec, p.Schema, "panel %q: %v", p.ID, err)
		}
	}
	return nil
}

// DecodeStoredPayload decodes a payload that HAS ALREADY BEEN VALIDATED.
//
// ValidatePayload is the door: nothing reaches page_panel_data without passing
// the published JSON Schema, the size cap and the semantic checks. Re-running
// all three over the ring — once per historical row, once per push, for every
// gate with a `for` window — would make the hold window cost more than the
// condition it guards, so this decodes and nothing else.
//
// It is deliberately NOT exported as a general decoder and returns a bool
// rather than an error: the only correct answer for a stored payload that no
// longer decodes is "this row is not evidence", which is what the caller does
// with it.
func DecodeStoredPayload(schema PanelSchema, raw []byte) (Payload, bool) {
	var into Payload
	switch schema {
	case SchemaMetric:
		into = &MetricPayload{}
	case SchemaSeries:
		into = &SeriesPayload{}
	case SchemaStatus:
		into = &StatusPayload{}
	case SchemaTable:
		into = &TablePayload{}
	case SchemaNarrative:
		into = &NarrativePayload{}
	default:
		return nil, false
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return nil, false
	}
	return into, true
}

// WakeSample is one stored payload reduced to the only two facts the hold
// window needs: when the server accepted it, and whether the predicate held
// for it.
type WakeSample struct {
	ProducedAt time.Time
	Matched    bool
}

// WakeHeldFor reports whether the gate's condition has held continuously for
// at least `hold`, given the panel's ring NEWEST FIRST.
//
// The rule, stated once so the boundary is not an accident:
//
//   - the newest sample must match. A gate is about now, not about a bad
//     afternoon last week;
//   - walk back while samples keep matching. The oldest one in that unbroken
//     run is when the condition started, as far as this server can prove;
//   - it has held long enough when now - started >= hold, the same `>=` the
//     freshness contract uses for its own boundary, so "exactly 5m" fires and
//     4m59s does not;
//   - if EVERY sample in the ring matches, the ring's oldest sample is still
//     the earliest provable start. Assuming the condition predates the
//     retained history would let a gate with `for: 1h` fire on a panel we have
//     two minutes of evidence for.
//
// A panel that pushes once and goes quiet therefore never satisfies a non-zero
// `for` — which is correct, and is on_failure's case rather than this one.
func WakeHeldFor(samples []WakeSample, hold time.Duration, now time.Time) bool {
	if len(samples) == 0 || !samples[0].Matched {
		return false
	}
	if hold <= 0 {
		return true
	}
	started := samples[0].ProducedAt
	for _, s := range samples[1:] {
		if !s.Matched {
			break
		}
		if s.ProducedAt.Before(started) {
			started = s.ProducedAt
		}
	}
	return now.Sub(started) >= hold
}
