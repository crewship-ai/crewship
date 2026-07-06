// Package untrusted provides the ingress trust fence: the single chokepoint
// that neutralizes external, lower-trust content (webhook payloads, issue
// bodies, tool/Composio output) before it is concatenated into an agent
// prompt.
//
// The problem it closes is prompt injection at ingress (OWASP LLM01): raw
// external bytes interpolated into a prompt can carry "ignore previous
// instructions"-style directives the model may then follow. Detect-at-egress
// cannot help on the agent-CLI path — in OAuth mode the request to the model
// is an opaque TLS tunnel the sidecar cannot scan — so the fence neutralizes
// in plaintext, on our side, on 100% of paths regardless of auth mode.
//
// Fence.Wrap emits a nonce-delimited block:
//
//	<untrusted source="webhook" id="<nonce>" suspicion="high">
//	…content…
//	</untrusted id="<nonce>">
//
// The nonce is random per call and stripped from the content, so an attacker
// cannot forge the id-matching closing tag to break out of the fence: a bare
// </untrusted> inside the payload is inert — it lacks the nonce. lookout
// scans the content and annotates the block's suspicion level rather than
// blocking, so a legitimate issue that quotes an injection example is
// fenced-and-flagged, not dropped (blocking it would be its own bug).
//
// One line in the base system prompt (orchestrator.crewshipSystemPreamble)
// tells the model to treat <untrusted …> blocks as pure data, never as
// instructions — that is the other half of this contract.
package untrusted

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/lookout"
)

// nonceBytes is the length of the random per-block nonce. 16 bytes (128 bits)
// makes a guess-the-nonce forgery of the closing tag computationally
// infeasible, matching the entropy used elsewhere for capability tokens.
const nonceBytes = 16

// Fence wraps untrusted content in a nonce-delimited block and annotates it
// with a suspicion level from the injection scanner. The zero value is not
// usable — construct with New. The scan and newNonce fields are injectable so
// tests can pin a deterministic nonce; production always uses the defaults.
type Fence struct {
	scan     func(string) lookout.ScanResult
	newNonce func() string
}

// New returns a Fence backed by the real lookout scanner and a crypto/rand
// nonce generator.
func New() *Fence {
	return &Fence{
		scan:     lookout.ScanInput,
		newNonce: randomNonce,
	}
}

// defaultFence backs the package-level Wrap convenience. It is stateless
// beyond its function fields and safe for concurrent use (Wrap holds no
// shared mutable state; lookout.ScanInput is documented concurrency-safe).
var defaultFence = New()

// Wrap fences content from the named source using the default Fence. source
// identifies the ingress channel (e.g. "webhook", "github_issue") and MUST be
// trusted/caller-derived — never a field from the untrusted payload itself,
// or an attacker could label their content "system".
func Wrap(source, content string) string {
	return defaultFence.Wrap(source, content)
}

// Wrap emits the nonce-delimited fenced block for content. See the package
// doc for the contract the model is told to honor.
func (f *Fence) Wrap(source, content string) string {
	nonce := f.newNonce()

	// Defense in depth: strip any literal occurrence of the nonce from the
	// content. The nonce is random and unknown to the attacker, but this
	// covers the astronomically-unlikely collision and any future oracle
	// leak — with the nonce gone from the body, a forged </untrusted
	// id="<nonce>"> cannot appear inside the fence.
	content = strings.ReplaceAll(content, nonce, "")

	suspicion := "none"
	if res := f.scan(content); len(res.Findings) > 0 {
		suspicion = string(res.HighestSeverity())
	}

	var b strings.Builder
	b.Grow(len(content) + 96)
	// %q quotes and escapes each attribute value, so a sanitized source and
	// the hex nonce can never break out of the tag.
	fmt.Fprintf(&b, "<untrusted source=%q id=%q suspicion=%q>\n", sanitizeSource(source), nonce, suspicion)
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "</untrusted id=%q>", nonce)
	return b.String()
}

// randomNonce returns a hex-encoded 128-bit random nonce. On the vanishingly
// unlikely crypto/rand failure it panics rather than emit a low-entropy or
// empty nonce — a predictable nonce would silently defeat the fence, which is
// worse than a loud failure at the ingress boundary.
func randomNonce() string {
	b := make([]byte, nonceBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("untrusted: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// sanitizeSource reduces a source label to [A-Za-z0-9_-] so a caller-supplied
// (or misconfigured) source can never inject extra tag attributes or an early
// tag close. Source is expected to be a trusted constant; this is belt-and-
// suspenders for the one field the caller controls.
func sanitizeSource(source string) string {
	var b strings.Builder
	b.Grow(len(source))
	for _, r := range source {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "external"
	}
	return b.String()
}
