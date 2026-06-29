package scrubber

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"regexp"
	"sync"
	"unicode/utf8"
)

// defaultStreamOverlap is the number of trailing bytes a StreamScrubber holds
// back between writes. It must be large enough to cover the longest *partial*
// (not-yet-complete) credential prefix that a future chunk could complete —
// e.g. an "-----BEGIN OPENSSH PRIVATE KEY-----" marker (~35 bytes) or a token
// fixed-prefix plus its minimum body. 128 bytes is a generous margin for every
// built-in token pattern. Value-aware patterns (AddSecretValues) can raise it.
const defaultStreamOverlap = 128

// maxStreamCarry bounds the internal buffer so a hostile stream that keeps a
// match perpetually straddling the emit boundary cannot grow memory without
// limit. When exceeded we force-flush the safe prefix.
const maxStreamCarry = 1 << 20 // 1 MiB

// StreamScrubber is a STATEFUL wrapper around a Scrubber for redacting a secret
// that is split across multiple streamed chunks (e.g. LLM stdout deltas).
//
// The plain Scrubber.Scrub is stateless: a secret split across two events —
// "sk-ant-" then "api03-…" — matches no pattern in either chunk and leaks. The
// per-event stdout path in the orchestrator had exactly this gap (finding SC1).
//
// StreamScrubber carries an OVERLAP BUFFER across Write calls: it holds back a
// tail of length `overlap` (and never cuts through a complete match) so a
// boundary-straddling secret is reassembled and redacted. Call Flush() once the
// stream ends to drain the final tail.
//
// A StreamScrubber instance is NOT safe for concurrent use by multiple
// goroutines (it holds per-stream state); use one per stream. The underlying
// Scrubber it wraps is itself concurrency-safe.
type StreamScrubber struct {
	mu      sync.Mutex
	s       *Scrubber
	carry   string
	overlap int
}

// NewStreamScrubber returns a StreamScrubber wrapping s. If s is nil a default
// Scrubber (New()) is used.
func NewStreamScrubber(s *Scrubber) *StreamScrubber {
	if s == nil {
		s = New()
	}
	return &StreamScrubber{s: s, overlap: defaultStreamOverlap}
}

// Write feeds a chunk of streamed output into the scrubber and returns the
// portion that is safe to emit downstream. A trailing window is buffered
// internally so a credential straddling this and the next chunk is still
// caught. The returned string is already scrubbed.
func (ss *StreamScrubber) Write(chunk string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	buf := ss.carry + chunk

	// Not enough buffered yet to safely emit anything: hold it all back. The
	// exception is the memory-safety cap, handled below.
	if len(buf) <= ss.overlap {
		ss.carry = buf
		return ""
	}

	cut := len(buf) - ss.overlap
	cut = ss.safeBoundary(buf, cut)
	cut = alignRuneStart(buf, cut)

	// Memory-safety: if a match keeps straddling the boundary the carry can
	// grow without bound. Once it crosses the cap, force the cut forward so we
	// drain everything except the minimum overlap window. This may split a
	// genuine match, but only under adversarial unbounded input.
	if cut <= 0 && len(buf) > maxStreamCarry {
		cut = alignRuneStart(buf, len(buf)-ss.overlap)
	}
	if cut <= 0 {
		ss.carry = buf
		return ""
	}

	out := ss.s.Scrub(buf[:cut])
	ss.carry = buf[cut:]
	return out
}

// Flush scrubs and returns any buffered tail. Call exactly once after the final
// Write when the stream has ended.
func (ss *StreamScrubber) Flush() string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.carry == "" {
		return ""
	}
	out := ss.s.Scrub(ss.carry)
	ss.carry = ""
	return out
}

// safeBoundary pulls the proposed cut point back so it never falls inside a
// complete credential match. A match whose [start,end) straddles `cut` would,
// if the prefix were scrubbed in isolation, be truncated and slip through; we
// keep the whole match in the carry buffer to be redacted once more context (or
// Flush) arrives.
func (ss *StreamScrubber) safeBoundary(buf string, cut int) int {
	for _, b := range ss.s.matchBounds(buf) {
		start, end := b[0], b[1]
		if start < cut && end > cut {
			cut = start
		}
	}
	if cut < 0 {
		cut = 0
	}
	return cut
}

// alignRuneStart moves i back to the nearest UTF-8 rune boundary so we never
// emit a prefix that ends mid-rune (the split bytes stay in the carry and
// recombine on the next Write).
func alignRuneStart(s string, i int) int {
	if i <= 0 || i >= len(s) {
		return i
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// AddSecretValues registers value-aware redaction for the StreamScrubber and
// widens the overlap window to cover the longest encoded form, so a secret that
// is base64/hex/url-encoded or reversed before being printed is still caught
// even when streamed in pieces. See Scrubber.AddSecretValues.
func (ss *StreamScrubber) AddSecretValues(values ...string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	maxLen := ss.s.AddSecretValues(values...)
	if need := maxLen + 8; need > ss.overlap {
		ss.overlap = need
	}
}

// minSecretValueLen guards against catastrophic over-redaction: very short
// credential values (and common words) are not registered as literal patterns.
const minSecretValueLen = 5

// AddSecretValues registers exact-match redaction patterns for a set of known
// secret values AND their common encodings — base64 (std + URL), URL-escape,
// hex, and reversed — mirroring the encoding-aware scrub in
// internal/api/keeper_execute.go. This catches exfiltration attempts like
// `echo $TOKEN | base64` / `printf %s "$TOKEN" | xxd -p` / `… | rev` that the
// literal-only credential patterns would miss.
//
// It returns the byte length of the longest pattern registered, which callers
// (e.g. StreamScrubber) use to size an overlap window. Values shorter than
// minSecretValueLen are skipped to avoid redacting common short strings.
func (s *Scrubber) AddSecretValues(values ...string) int {
	maxLen := 0
	seen := make(map[string]struct{})
	add := func(v string) {
		if len(v) < minSecretValueLen {
			return
		}
		if _, dup := seen[v]; dup {
			return
		}
		seen[v] = struct{}{}
		if err := s.AddPattern("secret-value", regexp.QuoteMeta(v)); err != nil {
			return
		}
		if len(v) > maxLen {
			maxLen = len(v)
		}
	}
	for _, v := range values {
		if len(v) < minSecretValueLen {
			continue
		}
		add(v)
		add(base64.StdEncoding.EncodeToString([]byte(v)))
		add(base64.URLEncoding.EncodeToString([]byte(v)))
		add(url.QueryEscape(v))
		add(hex.EncodeToString([]byte(v)))
		add(reverseString(v))
	}
	return maxLen
}

// reverseString returns s with its runes in reverse order.
func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
