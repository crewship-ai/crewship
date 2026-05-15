package scrubber

import (
	"strings"
	"testing"
)

// Concrete F-009 / Agent F bypass payloads. The fix is the new
// stripZeroWidth pre-pass in Scrub/ContainsSecret — without it the
// scrubber regex `sk-ant-[a-zA-Z0-9_-]{10,}` doesn't match because the
// invisible code point is not in the char class.
func TestScrub_StripsZeroWidthInsideKey(t *testing.T) {
	zwsp := "\u200b"
	zwj := "\u200d"
	bom := "\ufeff"
	cases := []struct {
		name   string
		input  string
		hidden string
	}{
		{"zwsp_after_prefix", "leak: sk-ant-" + zwsp + "abcdef1234567890XYZ trailing", "sk-ant-" + zwsp + "abcdef1234567890XYZ"},
		{"zwj_inside_payload", "key=sk-ant-abcd" + zwj + "ef1234567890XYZ", "sk-ant-abcd" + zwj + "ef1234567890XYZ"},
		{"bom_at_start", bom + "sk-ant-abcdef1234567890XYZ", bom + "sk-ant-abcdef1234567890XYZ"},
	}
	s := New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := s.Scrub(tc.input)
			if strings.Contains(out, tc.hidden) {
				t.Errorf("scrubber emitted the obfuscated key intact: %q", out)
			}
			if !strings.Contains(out, "[REDACTED:anthropic_key]") {
				t.Errorf("expected anthropic_key redaction marker; got %q", out)
			}
		})
	}
}

func TestContainsSecret_DetectsZeroWidthObfuscation(t *testing.T) {
	s := New()
	zwsp := "\u200b"
	if !s.ContainsSecret("sk-ant-" + zwsp + "abcdef1234567890XYZ") {
		t.Errorf("ContainsSecret missed a zero-width-obfuscated key — F-009 regression")
	}
}

func TestStripZeroWidth_FastPathPreservesText(t *testing.T) {
	in := "ordinary text with no zero-width chars"
	if got := stripZeroWidth(in); got != in {
		t.Errorf("fast path mutated benign input: %q vs %q", got, in)
	}
}

func TestStripZeroWidth_KeepsRealWhitespaceAndNonAscii(t *testing.T) {
	// Tab, newline, NBSP (U+00A0), accented chars must survive. We only
	// strip the explicit zero-width set.
	in := "abc\tdef\nghi jkl ÄÖÜẞŸÇ"
	out := stripZeroWidth(in)
	if out != in {
		t.Errorf("expected %q, got %q", in, out)
	}
}
