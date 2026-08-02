package orchestrator

import (
	"strings"
	"testing"
)

// #1669 — [MEMORY INSTRUCTIONS] has to teach the declarative rule, and
// demonstrate it with both a positive and a negative example.
//
// Hermes's rule, and why it matters here: a memory entry written as an
// instruction ("Always respond concisely") is re-read as a standing
// directive at the start of every later session and can override what
// the person is asking for right now. A memory entry written as a
// description ("prefers concise responses") is context the agent weighs.
//
// The block used to instruct exclusively in the imperative and said
// nothing at all about the voice of what it was asking the agent to write
// — so the tier the agent maintains itself made the exact mistake the
// operator-model extractor now refuses to make.
//
// Honest limit of this test: it asserts the rule is TAUGHT, which is a
// presence check, and presence checks are weak. Whether a model obeys a
// prompt is not something a unit test can decide. The ENFORCED half of
// the same rule lives in internal/usermodel (Verify → ReasonImperative),
// where the writer is code rather than a model and the guarantee is
// structural rather than asked for.
func TestBuildMemoryInstructions_TeachesDeclarativeNotImperative(t *testing.T) {
	instructions := buildMemoryInstructions("2026-02-19")

	for _, want := range []string{
		"declarative",
		// A worked pair. A rule without exemplars is a category name,
		// which is the failure mode Graphiti's anti-emotion rule avoids
		// by enumerating instead of naming.
		"prefers concise responses",
		"Always respond concisely",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("[MEMORY INSTRUCTIONS] does not teach the declarative rule; missing %q\n%s",
				want, instructions)
		}
	}

	// And it must no longer tell the agent to write a request verbatim
	// into its own long-term memory, which is the anti-pattern it now
	// warns about: "remember this" is most often a request about the
	// current turn, and copying it in verbatim is how an imperative gets
	// into AGENT.md in the first place.
	if strings.Contains(instructions, "write it to AGENT.md immediately") {
		t.Error("the block still instructs an immediate verbatim write; it should describe what gets recorded")
	}
}

// The staleness rule — a fact that will be wrong in a week is not a
// memory — is the other half of what keeps AGENT.md a profile rather
// than a transcript.
func TestBuildMemoryInstructions_TeachesTheStalenessRule(t *testing.T) {
	instructions := buildMemoryInstructions("2026-02-19")
	if !strings.Contains(instructions, "stale in a week") {
		t.Errorf("[MEMORY INSTRUCTIONS] does not state the staleness rule:\n%s", instructions)
	}
}
