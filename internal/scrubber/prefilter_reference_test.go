package scrubber

import (
	"sync"
	"unicode/utf8"
)

func (s *Scrubber) legacyPrefilterScrub(input string) string {
	if input == "" {
		return ""
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := stripZeroWidth(input)
	for _, p := range s.patterns {
		replacement := "[REDACTED]"
		if p.name != "" {
			replacement = "[REDACTED:" + p.name + "]"
		}

		if p.name == "" {
			// Generic patterns need special handling to preserve the key name
			result = s.scrubGeneric(result, p.re)
		} else {
			result = p.re.ReplaceAllString(result, replacement)
		}
	}
	return result
}

func (s *Scrubber) legacyPrefilterBounds(input string) [][2]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out [][2]int
	for _, p := range s.patterns {
		for _, loc := range p.re.FindAllStringIndex(input, -1) {
			out = append(out, [2]int{loc[0], loc[1]})
		}
	}
	return out
}
func (s *Scrubber) legacyPrefilterContains(input string) bool {
	if input == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	normalized := stripZeroWidth(input)
	for _, p := range s.patterns {
		if p.re.MatchString(normalized) {
			return true
		}
	}
	return false
}

type legacyPrefilterStream struct {
	mu      sync.Mutex
	s       *Scrubber
	carry   string
	overlap int
}

// newLegacyPrefilterStream returns a legacyPrefilterStream wrapping s. If s is nil a default
// Scrubber (New()) is used.
func newLegacyPrefilterStream(s *Scrubber) *legacyPrefilterStream {
	if s == nil {
		s = New()
	}
	return &legacyPrefilterStream{s: s, overlap: defaultStreamOverlap}
}

// Write feeds a chunk of streamed output into the scrubber and returns the
// portion that is safe to emit downstream. A trailing window is buffered
// internally so a credential straddling this and the next chunk is still
// caught. The returned string is already scrubbed.
func (ss *legacyPrefilterStream) Write(chunk string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	// Normalize zero-width characters up front so boundary detection
	// (matchBounds) and redaction (Scrub) agree. Scrub matches on the
	// zero-width-stripped form; if we computed boundaries on raw bytes a token
	// like "sk-ant-​api03-…" would be invisible to matchBounds, the cut
	// could fall inside it, and the emitted prefix would normalize into a live
	// secret fragment downstream — reopening the zero-width bypass on the
	// streaming path (CodeRabbit). Stripping the combined buffer also
	// reassembles a zero-width char whose bytes are split across chunks. The
	// carry is therefore always already-normalized, matching Scrub's output.
	buf := stripZeroWidth(ss.carry + chunk)

	// Not enough buffered yet to safely emit anything: hold it all back. The
	// exception is the memory-safety cap, handled below.
	if len(buf) <= ss.overlap {
		ss.carry = buf
		return ""
	}

	cut := len(buf) - ss.overlap
	cut = ss.safeBoundary(buf, cut)
	cut = legacyPrefilterAlignRuneStart(buf, cut)

	// Memory-safety: if a match keeps straddling the boundary the carry can
	// grow without bound. Once it crosses the cap, force the cut forward so we
	// drain everything except the minimum overlap window. This may split a
	// genuine match, but only under adversarial unbounded input.
	if cut <= 0 && len(buf) > maxStreamCarry {
		cut = legacyPrefilterAlignRuneStart(buf, len(buf)-ss.overlap)
	}
	if cut <= 0 {
		ss.carry = buf
		return ""
	}

	out := ss.s.legacyPrefilterScrub(buf[:cut])
	ss.carry = buf[cut:]
	return out
}

// Flush scrubs and returns any buffered tail. Call exactly once after the final
// Write when the stream has ended.
func (ss *legacyPrefilterStream) Flush() string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.carry == "" {
		return ""
	}
	out := ss.s.legacyPrefilterScrub(ss.carry)
	ss.carry = ""
	return out
}

// safeBoundary pulls the proposed cut point back so it never falls inside a
// complete credential match. A match whose [start,end) straddles `cut` would,
// if the prefix were scrubbed in isolation, be truncated and slip through; we
// keep the whole match in the carry buffer to be redacted once more context (or
// Flush) arrives.
func (ss *legacyPrefilterStream) safeBoundary(buf string, cut int) int {
	for _, b := range ss.s.legacyPrefilterBounds(buf) {
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

// legacyPrefilterAlignRuneStart moves i back to the nearest UTF-8 rune boundary so we never
// emit a prefix that ends mid-rune (the split bytes stay in the carry and
// recombine on the next Write).
func legacyPrefilterAlignRuneStart(s string, i int) int {
	if i <= 0 || i >= len(s) {
		return i
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// AddSecretValues registers value-aware redaction for the legacyPrefilterStream and
// widens the overlap window to cover the longest encoded form, so a secret that
// is base64/hex/url-encoded or reversed before being printed is still caught
// even when streamed in pieces. See Scrubber.AddSecretValues.
func (ss *legacyPrefilterStream) AddSecretValues(values ...string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	maxLen := ss.s.AddSecretValues(values...)
	if need := maxLen + 8; need > ss.overlap {
		ss.overlap = need
	}
}
