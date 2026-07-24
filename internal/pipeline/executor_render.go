package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// secretRedactionMarker replaces a resolved {{ secrets.<type> }} value
// anywhere it would otherwise leak out of a step — the downstream step
// output, an error message, a journaled preview. Distinct, greppable, and
// stable so operators recognise "a secret was here, and it was scrubbed."
const secretRedactionMarker = "[REDACTED:secret]"

// secretScrub holds the exact decrypted values a step injected via
// {{ secrets.<type> }} so the runner can strip them from any string that
// leaves it. We scrub by LITERAL value (not by credential-shape regex):
// a workspace secret can be any bytes — an opaque bearer, a DB password,
// a PEM blob — so only exact-match replacement can guarantee it never
// survives on an author-visible surface (step_outputs_json, journal, error
// text). A step that referenced no secrets carries an empty set and every
// scrub is a no-op, so existing routines see byte-for-byte identical output.
type secretScrub struct {
	values []string
}

// active reports whether there is anything to scrub.
func (s *secretScrub) active() bool { return s != nil && len(s.values) > 0 }

// scrub replaces every resolved secret value in str with the marker.
func (s *secretScrub) scrub(str string) string {
	if !s.active() || str == "" {
		return str
	}
	for _, v := range s.values {
		if v == "" {
			continue
		}
		str = strings.ReplaceAll(str, v, secretRedactionMarker)
	}
	return str
}

// scrubErr scrubs an error's message. When the message carries no secret
// (the common case — egress/SSRF/wiring errors), the ORIGINAL error is
// returned untouched so typed errors (e.g. *EgressBlockedError) still
// satisfy errors.As at the call site. Only when a secret is actually
// present is the error flattened to a redacted plain error — losing the
// type is the correct trade when the alternative is leaking the value.
func (s *secretScrub) scrubErr(err error) error {
	if err == nil || !s.active() {
		return err
	}
	msg := err.Error()
	cleaned := s.scrub(msg)
	if cleaned == msg {
		return err
	}
	return errors.New(cleaned)
}

// secretTypesInStep scans a step's template-bearing fields for
// {{ secrets.<type> }} references and returns the distinct set of types.
// Scoped to the step kinds that carry secrets to a non-agent runtime
// (code | script | notify | http) — the same kinds resolveStepSecrets
// enriches. An empty result means no vault lookup happens at all.
func secretTypesInStep(step Step) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(s string) {
		if s == "" {
			return
		}
		for _, m := range templateRE.FindAllStringSubmatch(s, -1) {
			parts := strings.SplitN(strings.TrimSpace(m[1]), ".", 3)
			if len(parts) >= 2 && parts[0] == "secrets" {
				if t := strings.TrimSpace(parts[1]); t != "" {
					out[t] = struct{}{}
				}
			}
		}
	}
	if step.HTTP != nil {
		add(step.HTTP.URL)
		add(step.HTTP.Body)
		for _, v := range step.HTTP.Headers {
			add(v)
		}
	}
	if step.Code != nil {
		add(step.Code.Code)
		for _, v := range step.Code.Env {
			add(v)
		}
	}
	if step.Script != nil {
		for _, a := range step.Script.Args {
			add(a)
		}
		for _, v := range step.Script.Env {
			add(v)
		}
	}
	if step.Notify != nil {
		// Notify.To is deliberately NOT scanned. `to:` is a routing address
		// (workspace / user:<id> / role:<ROLE> / crew:<slug> / trigger),
		// never a place for a vault value. If a secret resolved there it
		// would render into toRaw — which runNotifyStep logs and persists as
		// a run warning when a secret-shaped target fails to resolve — a leak
		// the deferred output/error scrub can't reach. Only Title/Body may
		// legitimately carry a secret, and both are scrubbed from the
		// delivered notice.
		add(step.Notify.Title)
		add(step.Notify.Body)
	}
	return out
}

// secretResolveTimeout bounds a single vault lookup during step-secret
// resolution. Unlike the http credential_ref path — which inherits the
// step's own request timeout — code/script/notify steps have no
// per-step deadline, so a stalled resolver could otherwise hold the run
// open indefinitely. Best-effort semantics are preserved: a timeout is
// just another lookup error, so the type renders empty rather than
// failing the step.
const secretResolveTimeout = 5 * time.Second

// resolveStepSecrets enriches parentRender with the {{ secrets.<type> }}
// values a step references, resolved from the workspace vault via the
// wired credential resolver (workspace + author-crew scoped, ACTIVE-only —
// the SAME path http's credential_ref uses). It returns the enriched
// context plus a scrubber loaded with the resolved values so the runner
// can strip them from its output/error before returning.
//
// The returned RenderContext is a shallow copy: only .Secrets is set, so
// the caller's context (and any sibling step sharing it) is untouched.
// A step that references no secrets — or an executor with no resolver
// wired — gets parentRender back unchanged and an empty (no-op) scrubber,
// so there is zero overhead and zero behaviour change for existing steps.
//
// Resolution is best-effort per type, mirroring credential_ref: a type
// with no ACTIVE credential renders empty rather than failing the step
// (public endpoints / optional secrets keep working). Turning an
// unresolvable-but-required credential into a hard failure is the job of
// credentials_required enforcement (the API layer's gateMissingCredentials),
// not this hot path.
func (e *Executor) resolveStepSecrets(ctx context.Context, step Step, parentRender RenderContext, in RunInput) (RenderContext, *secretScrub) {
	types := secretTypesInStep(step)
	if len(types) == 0 || e.credentialByType == nil {
		return parentRender, &secretScrub{}
	}
	scope := RunScope{WorkspaceID: in.WorkspaceID, AuthorCrewID: in.AuthorCrewID}
	resolved := make(map[string]string, len(types))
	scrub := &secretScrub{}
	for t := range types {
		val, err := func() (string, error) {
			lookupCtx, cancel := context.WithTimeout(ctx, secretResolveTimeout)
			defer cancel()
			return e.credentialByType(lookupCtx, scope, t)
		}()
		if err != nil || val == "" {
			continue
		}
		resolved[t] = val
		scrub.values = append(scrub.values, val)
	}
	enriched := parentRender // shallow copy — only Secrets diverges
	enriched.Secrets = resolved
	return enriched, scrub
}

// evalIfCondition decides whether a step.If render result counts as
// "true". Empty + the obvious falsey strings short-circuit to false;
// everything else is true. Case-insensitive to match how YAML/JSON
// values flow through templates ("False" from a Python service still
// reads as falsey).
//
// Mirrors GitHub Actions' `if:` evaluator on the easy cases (no full
// expression language — that's a deeper rabbit hole and Render
// already covers the substitution side).
func evalIfCondition(rendered string) bool {
	s := rendered
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		last := s[len(s)-1]
		if last != ' ' && last != '\t' && last != '\n' && last != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	if s == "" {
		return false
	}
	// ASCII fold for the falsey-literal check
	low := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		low[i] = c
	}
	switch string(low) {
	case "false", "0", "null", "nil", "no", "off":
		return false
	}
	return true
}

// renderConcurrencyKey renders the DSL's concurrency_key template
// against the inputs map. We only support `{{ inputs.X }}` here —
// the full Render pipeline isn't reachable yet (no step outputs at
// reservation time).
//
// Three outcomes:
//
//   - template == ""                 → ("", false, nil)  unset by author, no gate
//   - non-empty template, renders OK → (key, true, nil)  gate engaged
//   - non-empty template, empty out  → ("", true, err)   author asked for a gate
//     but a referenced input is missing/empty — that's a silent-bypass bug if
//     we let it through, so caller MUST fail the run.
//
// Why: a routine that declares `concurrency_key: "{{ inputs.account_id }}"`
// is asking the platform to serialise runs per tenant. If account_id is
// missing, returning empty key (no gate) would silently allow unlimited
// parallelism — a Denial-of-Self via unintended fan-out. The fail-fast
// here treats an unresolved-empty concurrency key as a config error,
// not "no gate."
func renderConcurrencyKey(_ context.Context, template string, inputs map[string]any) (string, bool, error) {
	if template == "" {
		return "", false, nil
	}
	rc := RenderContext{Inputs: inputs, StepOutputs: map[string]string{}, Env: map[string]string{}}
	rendered := Render(template, rc)
	if rendered == "" {
		return "", true, ErrConcurrencyKeyEmpty
	}
	return rendered, true, nil
}

// estimateStepCost returns a coarse cost guess for a dry-run step.
// MVP uses a flat per-step number; Phase 2 will read pricing from
// internal/llm and produce model-aware estimates with token counts.
func estimateStepCost(_ Step, prompt string) float64 {
	// Rough heuristic: $1/M input tokens, ~4 chars/token. Output
	// guess at 25% of input. This is order-of-magnitude only — the
	// dry-run report explicitly labels it "estimated" so users
	// don't mistake it for a quote.
	tokensIn := float64(len(prompt)) / 4
	tokensOut := tokensIn * 0.25
	return (tokensIn + tokensOut) / 1_000_000
}

// mergeInputs takes the caller-supplied inputs and merges in the DSL's
// declared defaults so templates can reference any input the DSL
// promised, even when the caller omitted optional fields.
func mergeInputs(supplied map[string]any, dsl *DSL) map[string]any {
	out := make(map[string]any, len(dsl.Inputs))
	for _, spec := range dsl.Inputs {
		if v, ok := supplied[spec.Name]; ok {
			out[spec.Name] = v
			continue
		}
		if spec.Default != nil {
			out[spec.Name] = spec.Default
		}
	}
	// Preserve any extra inputs the caller passed that the DSL
	// didn't declare — useful for ad-hoc test runs.
	for k, v := range supplied {
		if _, already := out[k]; !already {
			out[k] = v
		}
	}
	return out
}

// NewRunID mints a run id in the canonical "run_" shape. Exported for
// dispatch paths that must know the id BEFORE the run executes — the
// async webhook handler 202-responds with the id, reserves it against
// the idempotency key, then starts the run in the background with
// RunIDOverride so the sender's polling handle matches the run that
// actually executes.
func NewRunID() string { return generateRunID() }

// generateRunID mints a "run_" CUID for journaling. Distinct from
// generatePipelineID so journal queries can pattern-match either
// kind without ambiguity.
func generateRunID() string {
	ts := time.Now().UnixMilli()
	c := runIDCounter.Add(1)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		b[0] = byte(c)
	}
	var buf [40]byte
	out := append(buf[:0], 'r', 'u', 'n', '_', 'c')
	out = strconv.AppendInt(out, ts, 36)
	tail := c % 65536
	const hexdigits = "0123456789abcdef"
	out = append(out,
		hexdigits[(tail>>12)&0xf],
		hexdigits[(tail>>8)&0xf],
		hexdigits[(tail>>4)&0xf],
		hexdigits[tail&0xf],
	)
	out = append(out, hex.EncodeToString(b)...)
	return string(out)
}

var runIDCounter atomic.Uint64
