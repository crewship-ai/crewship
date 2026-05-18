package scrubber

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// These tests fill gaps left by scrubber_test.go / validate_test.go /
// security_test.go / zerowidth_test.go / scrubber_multicli_test.go.
// They focus on PII-leak edge cases:
//
//   - whitespace variants between a Bearer prefix and the JWT payload
//   - AWS access key IDs embedded mid-paragraph
//   - GitHub fine-grained PATs (github_pat_…)
//   - Multi-rule collisions and the deterministic hit ordering
//   - UTF-8 multi-byte text around a secret (offset bookkeeping)
//   - Very long clean input completes in linear time
//   - Block-mode rejection surfaces the FIRST hit's rule name
//
// All assertions exercise the public Scrubber API only.

// makeKey constructs a credential-shaped string at runtime so the file
// itself doesn't trip Gitleaks / pre-commit scanners. Mirrors the
// buildKey/buildTestKey helpers in the sibling test files but lives
// here so we never edit those.
func makeKey(prefix string, bodyLen int) string {
	body := strings.Repeat("abcdef1234567890ABCDEF", (bodyLen/22)+1)
	return prefix + body[:bodyLen]
}

// TestScrubber_BearerJWTWithTabAndDoubleSpace_Redacted verifies that a
// JWT Bearer token survives non-canonical whitespace (mix of tabs and
// double spaces) between `Bearer` and the payload. Real log frames in
// the wild often re-format Authorization headers when pretty-printing,
// and the regex uses `\s+` precisely to defeat that — pin the contract.
func TestScrubber_BearerJWTWithTabAndDoubleSpace_Redacted(t *testing.T) {
	s := New()
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0." + strings.Repeat("a", 24)
	inputs := []string{
		"Authorization: Bearer\t" + jwt,   // single tab
		"Authorization: Bearer  \t" + jwt, // tabs + spaces mix
		"Authorization: Bearer \n " + jwt, // newline counts as \s
		"Authorization: Bearer not-jwt-x", // NBSP + non-JWT: must NOT be redacted (no JWT)
	}
	for i, in := range inputs {
		got := s.Scrub(in)
		if i < 3 {
			if strings.Contains(got, jwt) {
				t.Errorf("case %d: JWT bearer leaked through whitespace variant: %q", i, got)
			}
			if !strings.Contains(got, "[REDACTED:bearer_token]") {
				t.Errorf("case %d: missing bearer_token marker in %q", i, got)
			}
		} else {
			// NBSP isn't in \s for Go regexp + payload isn't a JWT;
			// scrubber must NOT pretend a non-JWT was redacted.
			if strings.Contains(got, "[REDACTED:bearer_token]") {
				t.Errorf("case %d: non-JWT bearer should not be marked as bearer_token: %q", i, got)
			}
		}
	}
}

// TestScrubber_AWSKeyEmbeddedMidParagraph_Redacted verifies that an AWS
// access key ID (AKIA…) embedded inside surrounding prose is redacted
// without disturbing the surrounding text — a critical journal-write
// scenario where agent stdout glues a key into a sentence.
func TestScrubber_AWSKeyEmbeddedMidParagraph_Redacted(t *testing.T) {
	s := New()
	// AKIA + 16 uppercase alphanumeric is the canonical shape.
	aws := "AKIA" + strings.Repeat("Z", 16)
	in := "Deploy log: provisioning user with key " + aws + " and rotating in 90 days."
	got := s.Scrub(in)
	if strings.Contains(got, aws) {
		t.Fatalf("AKIA key leaked: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:aws_key]") {
		t.Fatalf("missing aws_key marker: %q", got)
	}
	// Surrounding sentence preserved.
	for _, expect := range []string{"Deploy log:", "provisioning user with key", "rotating in 90 days."} {
		if !strings.Contains(got, expect) {
			t.Errorf("surrounding text mangled — missing %q in %q", expect, got)
		}
	}
}

// TestScrubber_GitHubFineGrainedPAT_Redacted pins the github_pat_ shape
// (GitHub fine-grained personal access tokens, introduced 2022). The
// existing github tests cover ghp_/gho_/ghs_; this one closes the
// fine-grained gap so a leaked admin-scope PAT can't slip past.
func TestScrubber_GitHubFineGrainedPAT_Redacted(t *testing.T) {
	s := New()
	pat := makeKey("github_pat_11AAAA", 36)
	in := "leaked: " + pat + " (please rotate)"
	got := s.Scrub(in)
	if strings.Contains(got, pat) {
		t.Fatalf("github_pat leaked: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:github_token]") {
		t.Fatalf("expected github_token marker, got %q", got)
	}
	if !strings.Contains(got, "(please rotate)") {
		t.Errorf("trailing context dropped: %q", got)
	}
}

// TestScrubber_MultipleHeterogeneousSecretsInOneLine_AllRedacted
// confirms that when a single line carries an Anthropic key, a GitHub
// token, a Slack token, and a Google API key together, every one is
// redacted — i.e. the loop in Scrub does not short-circuit on first hit.
// The existing SecurityMultipleCredentialsSameLine test covers three;
// this widens coverage to include Google.
func TestScrubber_MultipleHeterogeneousSecretsInOneLine_AllRedacted(t *testing.T) {
	s := New()
	anth := makeKey("sk-ant-api03-", 20)
	gh := makeKey("ghp_", 36)
	slack := "xoxb-1111-2222-" + strings.Repeat("a", 16)
	goog := "AIzaSy" + strings.Repeat("B", 33)
	in := "secrets: " + anth + " / " + gh + " / " + slack + " / " + goog + " done"

	got := s.Scrub(in)
	for _, leak := range []string{anth, gh, slack, goog} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked secret %q in %q", leak, got)
		}
	}
	for _, marker := range []string{
		"[REDACTED:anthropic_key]",
		"[REDACTED:github_token]",
		"[REDACTED:slack_token]",
		"[REDACTED:google_key]",
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("missing marker %s in %q", marker, got)
		}
	}
	if !strings.Contains(got, "done") {
		t.Errorf("trailing text dropped: %q", got)
	}
}

// TestScrubber_PatternCollision_ListsBothHitsAndOuterWins documents the
// observed contract when a private-key PEM block has an
// Anthropic-shaped key embedded inside it:
//
//   - Validate emits BOTH hits in deterministic order
//     (private_key first because patterns are registered specific→generic
//     with private_key earlier than anthropic_key in New()).
//   - Scrub redacts the OUTER private_key span first, which consumes the
//     inner anthropic_key — so the final Scrub output only contains
//     [REDACTED:private_key].
//
// This pins the priority so a future refactor can't silently invert it
// (which would leak the inner key after the outer block is stripped).
func TestScrubber_PatternCollision_ListsBothHitsAndOuterWins(t *testing.T) {
	s := New()
	innerKey := makeKey("sk-ant-api03-", 20)
	pem := "-----BEGIN RSA PRIVATE KEY-----\n" + innerKey + "\n-----END RSA PRIVATE KEY-----"

	res := s.Validate(pem, ModeBlock)
	if res.Decision != DecisionReject {
		t.Fatalf("expected DecisionReject on PEM with embedded key, got %v", res.Decision)
	}
	if len(res.Hits) < 2 {
		t.Fatalf("expected at least two hits for collision case, got %+v", res.Hits)
	}
	if res.Hits[0].Pattern != "private_key" {
		t.Errorf("expected first hit to be private_key (registration order), got %q", res.Hits[0].Pattern)
	}
	foundAnth := false
	for _, h := range res.Hits[1:] {
		if h.Pattern == "anthropic_key" {
			foundAnth = true
			break
		}
	}
	if !foundAnth {
		t.Errorf("expected anthropic_key hit in addition to private_key, got %+v", res.Hits)
	}

	got := s.Scrub(pem)
	if strings.Contains(got, innerKey) {
		t.Errorf("inner anthropic key leaked after private_key redaction: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:private_key]") {
		t.Errorf("expected private_key marker, got %q", got)
	}
}

// TestScrubber_BlockModeFirstHitSurfacesOuterRule is the
// rejection-metadata variant of the collision test: when a caller
// shows the user "your input was rejected because of X", X must be the
// FIRST pattern that hit (deterministic, registration-order). Without
// this guarantee the rejection UX could flap between rule names and
// confuse operators trying to allowlist a placeholder.
func TestScrubber_BlockModeFirstHitSurfacesOuterRule(t *testing.T) {
	s := New()
	innerKey := makeKey("sk-ant-api03-", 20)
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + innerKey + "\n-----END OPENSSH PRIVATE KEY-----"

	res := s.Validate(pem, ModeBlock)
	if res.Decision != DecisionReject {
		t.Fatalf("expected reject, got %v", res.Decision)
	}
	if len(res.Hits) == 0 {
		t.Fatalf("expected hits, got none")
	}
	if res.Hits[0].Pattern != "ssh_private_key" {
		t.Errorf("first hit must be ssh_private_key (registered before anthropic_key); got %q (full hits: %+v)",
			res.Hits[0].Pattern, res.Hits)
	}
}

// TestScrubber_UTF8WrappedSecret_OffsetsAreByteAccurate verifies that
// when a secret is surrounded by multi-byte UTF-8 characters, the Hit
// Index/Length values are correct BYTE offsets into the normalised
// string (not rune offsets) — and that slicing back into the input at
// those offsets recovers the exact key. A bug here would silently
// corrupt downstream consumers that store offset metadata for audit.
func TestScrubber_UTF8WrappedSecret_OffsetsAreByteAccurate(t *testing.T) {
	s := New()
	aws := "AKIA" + strings.Repeat("Q", 16)
	prefix := "héllo 文字 "     // multi-byte UTF-8
	suffix := " ümlaut after" // multi-byte UTF-8
	in := prefix + aws + suffix
	if !utf8.ValidString(in) {
		t.Fatalf("test setup: input not valid UTF-8")
	}

	res := s.Validate(in, ModeBlock)
	if res.Decision != DecisionReject {
		t.Fatalf("expected reject, got %v", res.Decision)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("expected exactly one hit, got %+v", res.Hits)
	}
	h := res.Hits[0]
	if h.Pattern != "aws_key" {
		t.Errorf("expected aws_key, got %q", h.Pattern)
	}
	// Length is byte length, must equal len(aws).
	if h.Length != len(aws) {
		t.Errorf("Length = %d, want %d (byte length of key)", h.Length, len(aws))
	}
	// Slice in (== normalised, since no zero-width chars) by the
	// reported offsets and check we get back the key verbatim.
	if h.Index+h.Length > len(in) {
		t.Fatalf("Index+Length %d exceeds input length %d", h.Index+h.Length, len(in))
	}
	if got := in[h.Index : h.Index+h.Length]; got != aws {
		t.Errorf("slicing by reported offsets returned %q, want %q", got, aws)
	}
}

// TestScrubber_VeryLongCleanInput_CompletesUnderOneSecond is a coarse
// sanity check that the regex engine doesn't go quadratic on ~1 MB of
// plain text containing no secrets. The SecurityReDoSResistance test
// covers an adversarial pattern; this one covers the boring-payload
// path that dominates real journal writes (megabytes of agent stdout).
func TestScrubber_VeryLongCleanInput_CompletesUnderOneSecond(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping linear-time sanity check in -short mode")
	}
	s := New()
	// ~1 MB of clean prose. Use varied characters to avoid pathological
	// runs of identical bytes that might short-circuit the regex engine.
	chunk := "the quick brown fox jumps over the lazy dog 1234567890 "
	var b strings.Builder
	b.Grow(1 << 20)
	for b.Len() < (1 << 20) {
		b.WriteString(chunk)
	}
	in := b.String()

	start := time.Now()
	got := s.Scrub(in)
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Errorf("Scrub of ~1 MB clean input took %v (want <1s) — possible quadratic regex behaviour", elapsed)
	}
	if got != in {
		t.Errorf("clean input mutated by Scrub (len got=%d, want=%d)", len(got), len(in))
	}
}

// TestScrubber_ContainsSecretAndScrubAgreeAcrossModes guards a subtle
// invariant: ContainsSecret(x) == true  iff  Scrub(x) != x. If those
// ever disagree, callers that gate on ContainsSecret (the fast pre-check
// before journal write) can land an unredacted string on disk because
// the slow path saw no hits. Tested across a representative grid.
func TestScrubber_ContainsSecretAndScrubAgreeAcrossModes(t *testing.T) {
	s := New()
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJ4Ijoxfq." + strings.Repeat("z", 24)
	cases := []struct {
		name      string
		input     string
		hasSecret bool
	}{
		{"clean_prose", "just normal output, nothing sensitive", false},
		{"empty", "", false},
		{"anthropic", "key " + makeKey("sk-ant-api03-", 20), true},
		{"aws", "id " + "AKIA" + strings.Repeat("X", 16), true},
		{"bearer_jwt", "Authorization: Bearer " + jwt, true},
		{"ghpat", "tok " + makeKey("github_pat_11AAAA", 30), true},
		{"json_pw", `{"password":"hunter2hunter2"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contains := s.ContainsSecret(tc.input)
			scrubbed := s.Scrub(tc.input)
			mutated := scrubbed != tc.input
			if contains != tc.hasSecret {
				t.Errorf("ContainsSecret(%q) = %v, want %v", tc.input, contains, tc.hasSecret)
			}
			if contains != mutated {
				t.Errorf("invariant broken: ContainsSecret=%v but Scrub mutated=%v (in=%q out=%q)",
					contains, mutated, tc.input, scrubbed)
			}
		})
	}
}

// TestScrubber_AllowlistDoesNotRescuePartialOverlap closes a subtle
// allowlist hole: a regex that matches a SUBSTRING of the hit span must
// NOT rescue the hit. ValidateWithAllowlist anchors on span boundaries
// (FindStringIndex span[0]==0 && span[1]==len(matched)). Verify that a
// substring-only allowlist still rejects.
func TestScrubber_AllowlistDoesNotRescuePartialOverlap(t *testing.T) {
	s := New()
	key := makeKey("sk-ant-api03-", 20)
	res := s.ValidateWithAllowlist("leak: "+key, ModeBlock, `sk-ant-api03`)
	if res.Decision != DecisionReject {
		t.Fatalf("substring-only allowlist must NOT rescue real key; got decision=%v hits=%+v",
			res.Decision, res.Hits)
	}
}
