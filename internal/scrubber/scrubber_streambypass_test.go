package scrubber

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// These tests cover finding SC1 from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): the stdout scrubber was applied
// per streamed event (internal/orchestrator/orchestrator_run_status.go) with no
// cross-event carry buffer, and matched only literal credential forms.
//
// The fix adds two reusable primitives in this package:
//   - StreamScrubber: a stateful scrubber with an overlap buffer that catches a
//     secret split across Write calls and across the chunk boundary.
//   - Scrubber.AddSecretValues / StreamScrubber.AddSecretValues: value-aware
//     redaction of known secret values AND their common encodings
//     (base64 std/url, url-escape, hex, reversed).
//
// These tests now assert the SECURE behavior: they FAIL if either bypass
// regresses.

// scrubPerEvent mimics the OLD, insecure per-event scrubbing: each delta is
// scrubbed independently and concatenated, with no buffer carrying an
// in-progress match across event boundaries. Retained to document the contrast.
func scrubPerEvent(s *Scrubber, events []string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(s.Scrub(e))
	}
	return b.String()
}

// streamScrub feeds events through a StreamScrubber (one Write per event) and
// returns the reassembled, scrubbed output including the flushed tail.
func streamScrub(ss *StreamScrubber, events []string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(ss.Write(e))
	}
	b.WriteString(ss.Flush())
	return b.String()
}

func TestScrub_SingleEvent_CatchesWholeKey(t *testing.T) {
	// Sanity: when the whole key lands in one event, the stateless scrubber works.
	s := New()
	key := "sk-ant-api03-AAAABBBBCCCCDDDDEEEE1234"
	out := s.Scrub("here is the key: " + key + " done")
	if strings.Contains(out, key) {
		t.Fatalf("baseline scrub failed — whole-key in one event must be redacted, got %q", out)
	}
}

func TestStreamScrubber_SplitToken_Redacted(t *testing.T) {
	// SC1 (split arm): a secret streamed one piece per event must be reassembled
	// across the overlap buffer and redacted — it must NOT survive to the sink.
	key := "sk-ant-api03-AAAABBBBCCCCDDDDEEEE1234"

	cases := [][]string{
		// Boundary falls inside the key.
		{"sk-ant-", "api03-AAAABBBBCCCCDDDDEEEE1234"},
		// Many tiny deltas (realistic for LLM streaming).
		{"sk", "-a", "nt-", "api", "03-", "AAAA", "BBBB", "CCCC", "DDDD", "EEEE", "1234"},
		// Key surrounded by other text, split mid-token.
		{"the secret is sk-ant-api0", "3-AAAABBBBCCCCDDDDEEEE1234 do not log it"},
	}

	for i, events := range cases {
		ss := NewStreamScrubber(New())
		out := streamScrub(ss, events)
		if strings.Contains(out, key) {
			t.Errorf("case %d: split key leaked across Write calls — got %q", i, out)
		}
		if !strings.Contains(out, "REDACTED") {
			t.Errorf("case %d: expected a [REDACTED] marker in output, got %q", i, out)
		}
	}
}

func TestStreamScrubber_NoSecret_PassesThrough(t *testing.T) {
	// Regression: benign streamed text must come through unchanged once flushed.
	ss := NewStreamScrubber(New())
	parts := []string{"hello ", "world, ", "this is a perfectly ", "ordinary log line.\n"}
	out := streamScrub(ss, parts)
	want := strings.Join(parts, "")
	if out != want {
		t.Fatalf("benign stream mangled: got %q want %q", out, want)
	}
}

func TestStreamScrubber_DemonstratesOldBypass(t *testing.T) {
	// Documents WHY the StreamScrubber is needed: the old per-event approach
	// leaks the split key. This guards that the contrast (and thus the value of
	// the fix) remains real — the stateless path still cannot catch a split key.
	s := New()
	key := "sk-ant-api03-AAAABBBBCCCCDDDDEEEE1234"
	events := []string{"sk-ant-", "api03-AAAABBBBCCCCDDDDEEEE1234"}
	if !strings.Contains(scrubPerEvent(s, events), key) {
		t.Skip("stateless per-event scrub now catches split keys — the StreamScrubber tests are the real guard")
	}
}

func TestScrubber_EncodingAware_Redacted(t *testing.T) {
	// SC1 (encoding arm): given the known secret value, the value-aware helper
	// must redact the literal AND its base64/url/hex/reversed encodings, which
	// the literal credential patterns alone miss.
	key := "sk-ant-api03-AAAABBBBCCCCDDDDEEEE1234"

	s := New()
	if n := s.AddSecretValues(key); n == 0 {
		t.Fatalf("AddSecretValues registered nothing for a %d-byte secret", len(key))
	}

	encoders := map[string]string{
		"literal":    key,
		"base64-std": base64.StdEncoding.EncodeToString([]byte(key)),
		"base64-url": base64.URLEncoding.EncodeToString([]byte(key)),
		"hex":        hex.EncodeToString([]byte(key)),
		"reversed":   reverse(key),
	}

	for name, enc := range encoders {
		out := s.Scrub("exfil attempt: " + enc + " end")
		if strings.Contains(out, enc) {
			t.Errorf("[%s] encoded secret survived scrubbing — got %q", name, out)
		}
	}
}

func TestStreamScrubber_EncodingAware_SplitRedacted(t *testing.T) {
	// Combined arm: an ENCODED secret, streamed split across the chunk boundary,
	// must still be redacted. This exercises both the overlap buffer and the
	// value-aware encodings together.
	key := "sk-ant-api03-AAAABBBBCCCCDDDDEEEE1234"
	hexEnc := hex.EncodeToString([]byte(key))

	ss := NewStreamScrubber(New())
	ss.AddSecretValues(key)

	mid := len(hexEnc) / 2
	events := []string{"leak> " + hexEnc[:mid], hexEnc[mid:] + " <leak"}
	out := streamScrub(ss, events)
	if strings.Contains(out, hexEnc) {
		t.Fatalf("hex-encoded secret split across writes leaked — got %q", out)
	}
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
