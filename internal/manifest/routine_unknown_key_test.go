package manifest

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/kinds"
)

// RoutineSpec now passes unmodelled `spec:` keys through to the server
// instead of dropping them. That is the right trade — the DSL grows
// faster than the manifest struct, and pipeline.Parse is the authority
// on what a key means — but it moves where a typo goes to die: it used
// to be dropped by the manifest, now it is dropped by the server. Both
// are silent.
//
// So the plan says it out loud. `guardrail:` for `guardrails:` is a
// one-character mistake that costs a routine its safety config, and the
// only moment anyone is looking is the dry-run.
func TestRoutinePlanWarnings_NamesKeysTheDSLDoesNotHave(t *testing.T) {
	doc := &kinds.RoutineDocument{}
	doc.Metadata.Slug = "msn-etn-podklady"
	doc.Spec.Rest = map[string]any{
		"guardrail":       map[string]any{"input": "log"},
		"concurrency_key": "acct",
	}

	warnings := strings.Join(routinePlanWarnings(doc), "\n")

	if !strings.Contains(warnings, `"guardrail"`) {
		t.Errorf("a misspelled DSL key produced no warning:\n%s", warnings)
	}
	if strings.Contains(warnings, `"concurrency_key"`) {
		t.Errorf("a real DSL key was warned about — the channel stops being read:\n%s", warnings)
	}
}

// Determinism: warnings are printed, diffed and pasted into issues, so
// two runs over the same manifest must not reorder them.
func TestRoutinePlanWarnings_StableOrder(t *testing.T) {
	doc := &kinds.RoutineDocument{}
	doc.Metadata.Slug = "r"
	doc.Spec.Rest = map[string]any{"zzz": 1, "aaa": 1, "mmm": 1}

	first := strings.Join(routinePlanWarnings(doc), "\n")
	for i := 0; i < 20; i++ {
		if got := strings.Join(routinePlanWarnings(doc), "\n"); got != first {
			t.Fatalf("warning order is not stable:\n%s\n---\n%s", first, got)
		}
	}
	if !strings.Contains(first, `"aaa"`) || !strings.Contains(first, `"zzz"`) {
		t.Errorf("unknown keys missing from warnings:\n%s", first)
	}
}
