package eval

import "testing"

// Measured 2026-08-02 on dev2: qwen3.5:9b replayed against its own recorded
// decisions agreed with them 0.625 of the time across 3 passes. The judge
// contradicts ITSELF in roughly a third of runs, on the same prompt at the same
// settings.
//
// That reframes the question. Before "is model A better than model B" can mean
// anything, a model has to agree with itself — and this one does not. It also
// explains why the corpus is hard to label: the thing being labelled is noisy.
//
// The obvious hypothesis is prompt volume. The operator put it plainly long
// before the measurement — feed it too much and it gets lost — and it is the
// premise behind every retrieval-on-demand memory architecture: pull what the
// decision needs instead of pushing everything you have.
//
// It is a hypothesis, not a finding, and it is cheap to test on data already
// collected: if long prompts flip more than short ones, prompt volume is a
// driver and retrieval-on-demand is worth building. If they flip the same,
// it is not, and a memory subsystem would have been an expensive answer to the
// wrong question.
//
// SelfConsistency measures agreement of a row's replays WITH EACH OTHER, never
// with a label. That distinction is the point: a label says whether the judge
// was right, and this says whether it was stable. A judge can be consistently
// wrong (measurable, fixable) or unstable (not measurable at all, because there
// is no "it" to measure).

func replays(decisions ...Decision) []Replay {
	out := make([]Replay, 0, len(decisions))
	for _, d := range decisions {
		out = append(out, Replay{Decision: d, Risk: 5})
	}
	return out
}

func TestSelfConsistency_UnanimousRowIsFullyConsistent(t *testing.T) {
	got := SelfConsistency(Row{Replays: replays(Deny, Deny, Deny)})
	if got != 1 {
		t.Errorf("got %v, want 1 — three identical verdicts are as stable as it gets", got)
	}
}

// The majority share, not "did the majority win". A 2-1 split is 2/3 stable and
// reporting it as 1 would hide exactly the wobble this exists to find.
func TestSelfConsistency_SplitRowReportsTheMajorityShare(t *testing.T) {
	got := SelfConsistency(Row{Replays: replays(Deny, Deny, Allow)})
	if got < 0.66 || got > 0.67 {
		t.Errorf("got %v, want ~0.667 — a 2-1 split is two thirds stable, not stable", got)
	}
}

// Three different answers to one prompt is the worst case and must read as such
// rather than as "a third agreed", which sounds like partial success.
func TestSelfConsistency_ThreeWaySplitIsTheFloor(t *testing.T) {
	got := SelfConsistency(Row{Replays: replays(Allow, Deny, Escalate)})
	if got < 0.33 || got > 0.34 {
		t.Errorf("got %v, want ~0.333", got)
	}
}

// A single pass says nothing about stability. Reporting 1.0 would let a
// --passes 1 run claim a perfectly stable judge, which is the reading this
// measurement exists to prevent.
func TestSelfConsistency_OnePassIsNotEvidenceOfStability(t *testing.T) {
	if got := SelfConsistency(Row{Replays: replays(Deny)}); got != 0 {
		t.Errorf("got %v, want 0 — one sample cannot show consistency, and 1.0 would "+
			"let a --passes 1 run report a perfectly stable judge", got)
	}
	if got := SelfConsistency(Row{}); got != 0 {
		t.Errorf("got %v, want 0 for no replays at all", got)
	}
}

// The bucketing is the experiment. Rows are split by prompt size so the two
// halves can be compared: if the long half is less stable, prompt volume is a
// driver.
func TestConsistencyByPromptSize_SplitsAtTheMedian(t *testing.T) {
	rows := []Row{
		{PromptChars: 100, Replays: replays(Deny, Deny, Deny)},
		{PromptChars: 200, Replays: replays(Deny, Deny, Deny)},
		{PromptChars: 8000, Replays: replays(Deny, Allow, Escalate)},
		{PromptChars: 9000, Replays: replays(Deny, Allow, Escalate)},
	}
	short, long := ConsistencyByPromptSize(rows)

	if short.Rows != 2 || long.Rows != 2 {
		t.Fatalf("split = %d short / %d long, want 2/2", short.Rows, long.Rows)
	}
	if short.MeanConsistency <= long.MeanConsistency {
		t.Errorf("short=%v long=%v — this fixture is built so the long half is "+
			"less stable; if the split does not show it, the bucketing is wrong",
			short.MeanConsistency, long.MeanConsistency)
	}
}

// An odd row count must not silently drop a row. Losing the median row is how a
// small corpus quietly becomes a smaller one.
func TestConsistencyByPromptSize_KeepsEveryRow(t *testing.T) {
	rows := []Row{
		{PromptChars: 10, Replays: replays(Deny, Deny)},
		{PromptChars: 20, Replays: replays(Deny, Deny)},
		{PromptChars: 30, Replays: replays(Deny, Deny)},
	}
	short, long := ConsistencyByPromptSize(rows)
	if short.Rows+long.Rows != 3 {
		t.Errorf("split kept %d of 3 rows", short.Rows+long.Rows)
	}
}

// Rows with a single pass carry no consistency signal and must not be averaged
// in as zeros — that would manufacture instability out of a --passes 1 run and
// make prompt size look like it mattered when nothing was sampled twice.
func TestConsistencyByPromptSize_IgnoresRowsThatCannotShowConsistency(t *testing.T) {
	rows := []Row{
		{PromptChars: 10, Replays: replays(Deny)},
		{PromptChars: 20, Replays: replays(Deny, Deny)},
	}
	short, long := ConsistencyByPromptSize(rows)
	total := short.Rows + long.Rows
	if total != 1 {
		t.Errorf("counted %d rows, want 1 — a single-pass row cannot show consistency "+
			"and averaging it as 0 invents instability", total)
	}
}
