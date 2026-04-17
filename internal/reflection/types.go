// Package reflection implements two multi-agent research patterns for
// self-improving agent outputs:
//
//  1. Role-Based Reflection (MAR — Multi-Agent Reflexion). Three specialist
//     personas critique an output independently; an ensemble judge
//     synthesizes the critiques into a single keep/revise/reject plan.
//     Heterogeneous reviewers catch different failure modes than a single
//     self-critique pass (published results show +6% on HumanEval and +3%
//     on HotPotQA over single-agent reflexion).
//
//  2. Evaluator-Optimizer Loop. A Generator produces an output, a Verifier
//     tests it, and failure feedback is folded back into the next Generate
//     call. The loop terminates on first pass or after maxIters (default 5),
//     at which point the caller is expected to escalate (e.g. to Harbor
//     Master approval).
//
// The package is provider-neutral: no Anthropic / Ollama / OpenAI imports.
// LLM clients are local interfaces; callers wire adapters at the edges.
// The judge is reused from internal/quartermaster (JudgeInterface,
// EnsembleJudge) rather than reimplemented so bias-mitigation logic stays
// in one place.
package reflection

import (
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quartermaster"
)

// Persona identifies the critique role a reviewer plays. Each persona has
// a distinct system prompt (see personas.go) designed to surface a
// different class of failure. The enum is deliberately small — more
// personas dilute the signal and blow up latency/cost without adding
// coverage according to the MAR ablation results.
type Persona string

const (
	// PersonaLogician checks logical consistency: unjustified leaps,
	// missing premises, contradictions between claims, invalid inferences.
	PersonaLogician Persona = "logician"

	// PersonaSkeptic challenges every factual assertion that lacks
	// evidence. Treats the subject as a suspect, not a colleague.
	PersonaSkeptic Persona = "skeptic"

	// PersonaDomainExpert applies practitioner heuristics: patterns
	// experienced people would avoid, red flags a beginner wouldn't
	// notice, domain-specific correctness.
	PersonaDomainExpert Persona = "domain_expert"
)

// CritiqueSeverity is a coarse rating of how damaging the identified issues
// are. Synthesis uses this to decide whether a critique's points land in
// Revise or Reject.
type CritiqueSeverity string

const (
	CritiqueSeverityLow      CritiqueSeverity = "low"
	CritiqueSeverityMedium   CritiqueSeverity = "medium"
	CritiqueSeverityHigh     CritiqueSeverity = "high"
	CritiqueSeverityCritical CritiqueSeverity = "critical"
)

// Critique is one reviewer's assessment of a subject.
//
// RawText is populated when the LLM's structured output could not be
// parsed — instead of discarding the review we keep the raw text so the
// synthesizer still has signal and an operator can inspect the failure.
// On parse failure Severity defaults to CritiqueSeverityLow so a mangled
// critique can't single-handedly trigger a reject.
type Critique struct {
	Persona     Persona
	Severity    CritiqueSeverity
	Issues      []string
	Suggestions []string
	RawText     string
}

// RevisionPoint is a single concrete change the synthesizer is asking the
// author to make. What is the surface-level fix; Why is the reasoning
// that motivates it. Keeping them separate lets a UI show a diff
// alongside the rationale.
type RevisionPoint struct {
	What string
	Why  string
}

// Synthesis is the judge's consolidated plan for acting on the critiques.
//
// Keep is prose describing what is already correct and should not change.
// Revise is the ordered list of targeted edits. Reject is a list of
// critique points the judge discarded (either because they contradicted
// each other, were out of scope, or were unsupported).
//
// Confidence reflects the judge's own certainty. If the upstream
// ensemble reported high disagreement (stddev > 0.25) the Synthesize
// helper further dampens this value so callers can treat low-confidence
// outputs as requiring human review.
type Synthesis struct {
	Keep       string
	Revise     []RevisionPoint
	Reject     []string
	Confidence float64
}

// ReflectionRequest is the input to Reflect. Subject is the text being
// reviewed; Context is any supporting material the personas need to
// evaluate it (task description, relevant source files, prior
// conversation). Personas picks the reviewer set; nil/empty means use
// AllPersonas.
type ReflectionRequest struct {
	Subject  string
	Context  string
	Personas []Persona
}

// ReflectionResult bundles every output of a reflection run: the raw
// per-persona critiques, the synthesis, and the judge verdict the
// synthesis was built from. Callers commonly forward JudgeVerdict into
// their existing evaluation pipelines.
type ReflectionResult struct {
	Critiques    []Critique
	Synthesis    Synthesis
	JudgeVerdict quartermaster.JudgeVerdict
}

// Scope is re-exported as a type alias so callers of this package don't
// have to import internal/journal just to construct a scope. The underlying
// type is unchanged.
type Scope = journal.Scope
