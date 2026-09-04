package api

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The icon and palette names are mirrored from lib/crew-icons.ts by hand.
// These tests parse that file so the mirror cannot drift: an icon added on
// the web side without being added here would be refused by validCrewIcon,
// and one removed there would be accepted here and render as the fallback.
func readCrewIconsTS(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "lib", "crew-icons.ts"))
	if err != nil {
		t.Fatalf("read lib/crew-icons.ts: %v", err)
	}
	return string(raw)
}

func TestCrewIcons_MirrorsWebVocabulary(t *testing.T) {
	src := readCrewIconsTS(t)
	names := regexp.MustCompile(`name: "([a-z0-9-]+)"`).FindAllStringSubmatch(src, -1)
	if len(names) == 0 {
		t.Fatal("parsed no icon names — did lib/crew-icons.ts change shape?")
	}
	if len(names) != len(crewIconNames) {
		t.Fatalf("web has %d icons, Go mirror has %d — regenerate crewIconNames", len(names), len(crewIconNames))
	}
	for i, m := range names {
		if crewIconNames[i] != m[1] {
			t.Fatalf("icon[%d] = %q in Go, %q on the web", i, crewIconNames[i], m[1])
		}
	}

	palettes := src[regexp.MustCompile(`GRADIENT_PALETTES: GradientPalette\[\] = \[`).FindStringIndex(src)[0]:]
	ids := regexp.MustCompile(`id: "([a-z]+)"`).FindAllStringSubmatch(palettes, -1)
	if len(ids) != len(crewColorPalettes) {
		t.Fatalf("web has %d palettes, Go mirror has %d", len(ids), len(crewColorPalettes))
	}
	for i, m := range ids {
		if crewColorPalettes[i] != m[1] {
			t.Fatalf("palette[%d] = %q in Go, %q on the web", i, crewColorPalettes[i], m[1])
		}
	}
}

func TestCrewIcons_MenuOnlyOffersRealIcons(t *testing.T) {
	for _, n := range crewIconMenu {
		if !crewIconSet[n] {
			t.Errorf("crewIconMenu offers %q, which lib/crew-icons.ts does not draw", n)
		}
	}
	if crewIconMenuText() == "" {
		t.Fatal("empty icon menu")
	}
}

func TestCrewIcons_Validation(t *testing.T) {
	if !validCrewIcon("code") || validCrewIcon("Code") || validCrewIcon("not-an-icon") || validCrewIcon("") {
		t.Error("validCrewIcon: wrong verdicts")
	}
	cases := map[string]string{
		"blue":      "blue",
		" rose ":    "rose",
		"#1E7BFE":   "#1E7BFE",
		"1e7bfe":    "#1e7bfe",
		"red":       "",
		"#fff":      "",
		"":          "",
		"#12345g":   "",
		"blue;drop": "",
	}
	for in, want := range cases {
		if got := normaliseCrewColor(in); got != want {
			t.Errorf("normaliseCrewColor(%q) = %q, want %q", in, got, want)
		}
	}
}
