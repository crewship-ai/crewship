package memdiff

import (
	"bytes"
	"errors"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "empty", in: "", want: ""},
		{name: "no line endings at all", in: "abc", want: "abc"},
		{name: "lf is left alone", in: "a\nb\n", want: "a\nb\n"},
		{name: "crlf becomes lf", in: "a\r\nb\r\n", want: "a\nb\n"},
		{name: "mixed crlf and lf", in: "a\r\nb\nc\r\n", want: "a\nb\nc\n"},
		{name: "trailing newline is preserved", in: "a\r\n", want: "a\n"},
		{name: "absent trailing newline is not invented", in: "a\r\nb", want: "a\nb"},
		{
			// A lone CR is content. Treating it as a terminator would split a
			// line the client never split, and every removal hash after it
			// would move.
			name: "bare cr survives as content",
			in:   "a\rb\n",
			want: "a\rb\n",
		},
		{
			// One left-to-right pass. The first CR is content and stays; only
			// the CRLF becomes an LF. The result therefore still contains the
			// byte pair \r\n — a content CR that happens to sit before the
			// terminator — see TestNormalizeIsNotIdempotent.
			name: "cr immediately before a crlf",
			in:   "a\r\r\nb\n",
			want: "a\r\nb\n",
		},
		{
			name: "lone cr at end of file",
			in:   "a\n\r",
			want: "a\n\r",
		},
		{
			name: "multi-byte utf-8 is untouched",
			in:   "příliš žluťoučký kůň\r\nüber\r\n\U0001F680\r\n",
			want: "příliš žluťoučký kůň\nüber\n\U0001F680\n",
		},
		{
			name:    "invalid utf-8: lone continuation byte",
			in:      "a\n\x80\n",
			wantErr: ErrInvalidUTF8,
		},
		{
			name:    "invalid utf-8: truncated multi-byte sequence",
			in:      "a\n\xc3",
			wantErr: ErrInvalidUTF8,
		},
		{
			name:    "invalid utf-8: surrogate half",
			in:      "\xed\xa0\x80",
			wantErr: ErrInvalidUTF8,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize([]byte(tc.in))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Normalize(%q) error = %v, want %v", tc.in, err, tc.wantErr)
				}
				if got != nil {
					t.Fatalf("Normalize(%q) returned %q alongside an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize(%q) unexpected error: %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeDoesNotAliasInput(t *testing.T) {
	in := []byte("a\nb\n")
	got, err := Normalize(in)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	in[0] = 'z'
	if string(got) != "a\nb\n" {
		t.Fatalf("Normalize aliased its input: got %q after mutating the source", got)
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		// The five cases the package contract names explicitly.
		{name: "empty has zero lines", in: "", want: nil},
		{name: "bare newline is one terminated empty line", in: "\n", want: []string{"\n"}},
		{name: "unterminated single line", in: "a", want: []string{"a"}},
		{name: "terminated single line", in: "a\n", want: []string{"a\n"}},
		{name: "blank line after content", in: "a\n\n", want: []string{"a\n", "\n"}},

		{name: "two terminated lines", in: "a\nb\n", want: []string{"a\n", "b\n"}},
		{name: "last line unterminated", in: "a\nb", want: []string{"a\n", "b"}},
		{name: "leading blank line", in: "\na\n", want: []string{"\n", "a\n"}},
		{name: "only blank lines", in: "\n\n\n", want: []string{"\n", "\n", "\n"}},
		{name: "bare cr stays inside its line", in: "a\rb\nc\n", want: []string{"a\rb\n", "c\n"}},
		{name: "multi-byte content", in: "kůň\n🚀\n", want: []string{"kůň\n", "🚀\n"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitLines([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("SplitLines(%q) = %d lines %q, want %d lines %q",
					tc.in, len(got), asStrings(got), len(tc.want), tc.want)
			}
			for i := range got {
				if string(got[i]) != tc.want[i] {
					t.Fatalf("SplitLines(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
			// The split must be lossless, because the removal hash is taken
			// over exactly these bytes.
			if rejoined := string(bytes.Join(got, nil)); rejoined != tc.in {
				t.Fatalf("SplitLines(%q) does not rejoin: got %q", tc.in, rejoined)
			}
		})
	}
}

func TestNormalizeLines(t *testing.T) {
	norm, lines, err := NormalizeLines([]byte("a\r\nb\r\n"))
	if err != nil {
		t.Fatalf("NormalizeLines: %v", err)
	}
	if string(norm) != "a\nb\n" {
		t.Fatalf("normalized = %q, want %q", norm, "a\nb\n")
	}
	if got := asStrings(lines); len(got) != 2 || got[0] != "a\n" || got[1] != "b\n" {
		t.Fatalf("lines = %q, want [a\\n b\\n]", got)
	}

	if _, _, err := NormalizeLines([]byte("\xff")); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("NormalizeLines on invalid UTF-8 = %v, want ErrInvalidUTF8", err)
	}
}

func asStrings(lines [][]byte) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = string(l)
	}
	return out
}

// TestNormalizeIsNotIdempotent pins a hazard that follows from §8 rather than
// from this implementation, and that §8 does not mention.
//
// CRLF normalization is not injective: "a\n" and "a\r\n" must both normalize
// to "a\n". So no normalizer can be both content-preserving for a bare CR and
// idempotent. Given content whose last character is a literal CR and that ends
// with a CRLF terminator, one pass yields "a\r\n" — the CR is content, the LF
// is the terminator — and a *second* pass would eat the content CR.
//
// The contract this package therefore commits to: Normalize is applied exactly
// once, at the ingest boundary, and canonical stored content is never
// re-normalized. §8's "Existující soubory projdou explicitním importem s novou
// revizí, nikoli tichou normalizací při čtení" is the rule that keeps this
// safe; this test is here so nobody "fixes" Normalize into a loop.
func TestNormalizeIsNotIdempotent(t *testing.T) {
	once, err := Normalize([]byte("a\r\r\n"))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if string(once) != "a\r\n" {
		t.Fatalf("first pass = %q, want %q", once, "a\r\n")
	}
	twice, err := Normalize(once)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if string(twice) != "a\n" {
		t.Fatalf("second pass = %q, want %q", twice, "a\n")
	}
	if string(once) == string(twice) {
		t.Fatal("expected the double pass to differ; the hazard this test documents is gone, " +
			"which means Normalize changed semantics")
	}
}

// TestNormalizeIsIdempotentWithoutBareCR is the reassuring half: for content
// with no bare CR — every real memory file — normalization is a fixpoint.
func TestNormalizeIsIdempotentWithoutBareCR(t *testing.T) {
	for _, in := range []string{"", "a", "a\n", "a\r\nb\r\n", "a\nb", "kůň\r\n🚀"} {
		once, err := Normalize([]byte(in))
		if err != nil {
			t.Fatalf("Normalize(%q): %v", in, err)
		}
		twice, err := Normalize(once)
		if err != nil {
			t.Fatalf("Normalize(Normalize(%q)): %v", in, err)
		}
		if !bytes.Equal(once, twice) {
			t.Fatalf("Normalize(%q) is not a fixpoint: %q then %q", in, once, twice)
		}
	}
}
