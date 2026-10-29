package api

import (
	"strings"
	"testing"
)

func TestArtifactProjectAnchorUnicodeQuotePrefix(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"Czech", strings.Repeat("Žluťoučký kůň ", 60)},
		{"emoji at a whitespace boundary", strings.Repeat("🐳 ", 350)},
		{"Unicode whitespace", strings.Repeat("Český\u0085text\u00a0s\nmezerami ", 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Join(strings.Fields(tc.text), " ")
			quote := string([]rune(text)[:500])
			content := []byte(`<h1 id="hero">` + tc.text + `</h1>`)
			if !artifactProjectAnchor(content, "#hero", quote) {
				t.Fatal("canonical500-codepoint selection rejected")
			}
			if !artifactProjectAnchor(content, "#hero", text) {
				t.Fatal("exact full selection rejected")
			}
			if artifactProjectAnchor(content, "#hero", "Wrong "+quote) {
				t.Fatal("different selection accepted")
			}
		})
	}
}
