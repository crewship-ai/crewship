package literalfilter

import (
	"regexp"
	"testing"
	"unicode"
)

func TestFoldedHintsRespectGoSimpleFoldExtensions(t *testing.T) {
	for r := rune(128); r <= unicode.MaxRune; r++ {
		ascii := false
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < 128 {
				ascii = true
				break
			}
		}
		if ascii && r != 'ſ' && r != 'K' {
			t.Fatalf("new non-ASCII ASCII-fold extension U+%04X requires conservative fallback", r)
		}
	}
	for _, text := range []string{"ſecret=abcdefgh", "API_KEY=abcdefgh", "SECRET=abcdefgh", "api_key=abcdefgh"} {
		view := NewView(text)
		filter := FoldedAny("secret=", "api_key=")
		if regexp.MustCompile(`(?i)(?:SECRET|API_KEY)=[^\s]{6,}`).MatchString(text) && !view.MayMatch(filter) {
			t.Fatalf("false-negative hint for%q", text)
		}
	}
}
func TestNecessaryLiteralGroupsAndUnknownMetadata(t *testing.T) {
	tests := []struct {
		text   string
		filter Filter
		want   bool
	}{{"clean", Filter{}, true}, {"clean", ExactAll(), true}, {"a b", ExactAll("a", "b"), true}, {"a", ExactAll("a", "b"), false}, {"b", ExactAny("a", "b"), true}, {"CLEAN", FoldedAny("clean"), true}, {"clean", FoldedAll("clé"), true}, {"\xff SECRET", FoldedAll("secret"), true}, {"\xff", FoldedAll("secret"), false}, {"ſ", FoldedAll("missing"), true}, {"K", FoldedAll("missing"), true}}
	for _, c := range tests {
		view := NewView(c.text)
		if got := view.MayMatch(c.filter); got != c.want {
			t.Fatalf("text%q got%v want%v", c.text, got, c.want)
		}
	}
}
func FuzzASCIIHintsHaveNoRegexFalseNegative(f *testing.F) {
	for _, s := range []string{"", "ſecret", "Key", "\xff SECRET", "bearer"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 8192 {
			t.Skip()
		}
		for _, literal := range []string{"secret", "key", "bearer", "ignore", "instructions"} {
			view := NewView(text)
			if regexp.MustCompile("(?i)"+literal).MatchString(text) && !view.MayMatch(FoldedAll(literal)) {
				t.Fatalf("false-negative text%q literal%q", text, literal)
			}
		}
	})
}
