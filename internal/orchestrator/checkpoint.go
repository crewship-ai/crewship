package orchestrator

// checkpoint.go — the §9.5/§11.3 structured hand-off for a whole SESSION,
// not one mission task (PRD-ISSUES-AND-ROUTINES-2026, work package B5,
// #2345).
//
// ParseCheckpoint mirrors ParseHandoff (mission.go:100-137) exactly on
// purpose: §9.5 says the HANDOFF parsing and enforcement machinery "should
// be reused as-is" — it is the storage (mission_tasks.handoff_context, one
// column, overwritten, per task) that cannot serve a session's evolving
// history, not the shape of "look for a marked block at the end of the
// output and parse simple key: value lines out of it". The document schema
// itself is different — done/plan/facts/blockers/next_step/confidence, not
// summary/confidence/artifacts — because a checkpoint is a resumable state
// snapshot (§11.1 item 3: "Latest checkpoint"), not a one-shot pipeline
// hand-off.
//
// Unlike HANDOFF's single-line fields, several of these are naturally
// multi-line free text (facts and blockers are often short lists). The
// parser therefore accumulates every line after a "key:" prefix into that
// key's value until the next recognized "key:" prefix or the closing
// marker — not "read exactly one line" the way parseHandoff's fields do.

import "strings"

// CheckpointData is the §9.5 checkpoint document: what a session-bearing
// run reports about its own state, for the NEXT run on the same session to
// resume from (§11.1 item 3) without rediscovering it by tool calls.
type CheckpointData struct {
	// Done is what this run (and, by construction, every run before it on
	// this session) has actually finished — the field a resumed agent
	// reads BEFORE deciding what to do next, so it does not repeat
	// completed work (§18 scenario 7's whole point).
	Done string `json:"done"`
	// Plan is the current intended path to the goal, as this run
	// understood it when it stopped.
	Plan string `json:"plan"`
	// Facts are identifiers, decisions and constraints worth carrying
	// forward verbatim — the same category conversationSummaryInstruction
	// asks an aux-LLM to preserve for ordinary conversation compaction
	// (orchestrator_run_conv.go), reused here for the same reason.
	Facts string `json:"facts"`
	// Blockers names anything stopping forward progress — a missing
	// credential, an unanswered question, a failing test the run could not
	// resolve. Empty means "nothing blocking", not "unreported".
	Blockers string `json:"blockers"`
	// NextStep is the single next action the resuming run (or a human
	// reading the session panel) should take.
	NextStep string `json:"next_step"`
	// Confidence mirrors HANDOFF's own field: low, medium or high.
	Confidence string `json:"confidence"`
	// Parsed is true only when the model actually emitted a well-formed
	// block. Recorded explicitly rather than left to be inferred from
	// every other field being empty — the same "measurable rather than
	// invisible" precedent mission_tasks_completion.go:98 sets for HANDOFF
	// (§11.3).
	Parsed bool `json:"parsed"`
}

const (
	checkpointStartMarker = "---CHECKPOINT---"
	checkpointEndMarker   = "---END CHECKPOINT---"
)

// checkpointFieldPrefixes is the recognized set of "key:" lines, in the
// order ParseCheckpoint tests them. A line that matches none of these is
// appended to whichever field is currently open (multi-line field
// support) — or dropped if no field has been opened yet.
var checkpointFieldPrefixes = []struct {
	prefix string
	set    func(*CheckpointData, string)
}{
	{"done:", func(c *CheckpointData, v string) { c.Done = v }},
	{"plan:", func(c *CheckpointData, v string) { c.Plan = v }},
	{"facts:", func(c *CheckpointData, v string) { c.Facts = v }},
	{"blockers:", func(c *CheckpointData, v string) { c.Blockers = v }},
	{"next_step:", func(c *CheckpointData, v string) { c.NextStep = v }},
	{"confidence:", func(c *CheckpointData, v string) { c.Confidence = v }},
}

// ParseCheckpoint extracts the §9.5 structured checkpoint from an agent's
// result text. Looks for a ---CHECKPOINT--- ... ---END CHECKPOINT--- block,
// the same LastIndex-then-Index shape ParseHandoff uses so a model that
// echoes the instruction text earlier in its own output (a common failure
// mode) still resolves to the block it actually meant to close with.
func ParseCheckpoint(resultText string) CheckpointData {
	startIdx := strings.LastIndex(resultText, checkpointStartMarker)
	if startIdx < 0 {
		return CheckpointData{Parsed: false}
	}
	endIdx := strings.Index(resultText[startIdx:], checkpointEndMarker)
	if endIdx < 0 {
		return CheckpointData{Parsed: false}
	}

	block := resultText[startIdx+len(checkpointStartMarker) : startIdx+endIdx]
	cd := CheckpointData{}

	var current func(*CheckpointData, string)
	var buf strings.Builder
	flush := func() {
		if current != nil {
			current(&cd, strings.TrimSpace(buf.String()))
		}
		buf.Reset()
	}

	for _, rawLine := range strings.Split(block, "\n") {
		line := strings.TrimSpace(rawLine)
		matched := false
		for _, f := range checkpointFieldPrefixes {
			if v, ok := cutPrefixFold(line, f.prefix); ok {
				flush()
				current = f.set
				buf.WriteString(v)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if current != nil && line != "" {
			buf.WriteByte('\n')
			buf.WriteString(line)
		}
	}
	flush()

	// A checkpoint with nothing worth resuming from is not a checkpoint —
	// require at least one of the fields that answers "what state is this
	// session in" (done or next_step), the same "partial blocks don't
	// count" rule parseHandoff applies to summary+confidence.
	cd.Parsed = cd.Done != "" || cd.NextStep != ""
	return cd
}

// cutPrefixFold is strings.CutPrefix with a case-insensitive prefix match —
// a model that capitalizes "Done:" should not silently produce an unparsed
// checkpoint. Returns the trimmed remainder and whether the prefix matched.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(s[len(prefix):]), true
}
