package api

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The folder icon and colour vocabularies (pages_folder_icons.go) are mirrors
// of the client's registries. The client owns them — an icon is a glyph and
// the glyph lives in the bundle — so the test reads the client's files and
// fails when this package's copy disagrees in membership OR order.

func readClientRegistry(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

func TestPageFolderIcons_MirrorTheCrewIconRegistry(t *testing.T) {
	t.Parallel()
	src := readClientRegistry(t, filepath.Join("lib", "crew-icons.ts"))
	start := strings.Index(src, "export const CREW_ICONS")
	if start < 0 {
		t.Fatal("lib/crew-icons.ts no longer declares CREW_ICONS; the mirror has nothing to mirror")
	}
	end := strings.Index(src[start:], "\n]")
	if end < 0 {
		t.Fatal("CREW_ICONS block does not close")
	}
	block := src[start : start+end]
	var want []string
	for _, m := range regexp.MustCompile(`name:\s*"([^"]+)"`).FindAllStringSubmatch(block, -1) {
		want = append(want, m[1])
	}
	if len(want) < 8 {
		t.Fatalf("parsed only %d icon names from CREW_ICONS; the parser is wrong, not the registry", len(want))
	}
	if !reflect.DeepEqual(pageFolderIcons, want) {
		t.Errorf("pageFolderIcons drifted from lib/crew-icons.ts CREW_ICONS:\n got %d names %v\nwant %d names %v",
			len(pageFolderIcons), pageFolderIcons, len(want), want)
	}
}

func TestPageFolderColors_MirrorTheCrewPalette(t *testing.T) {
	t.Parallel()
	src := readClientRegistry(t, filepath.Join("lib", "colors.ts"))
	start := strings.Index(src, "export const CREW_COLORS")
	if start < 0 {
		t.Fatal("lib/colors.ts no longer declares CREW_COLORS")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatal("CREW_COLORS block does not close")
	}
	var want []string
	for _, m := range regexp.MustCompile(`(?m)^\s*([a-z]+):\s*"#`).FindAllStringSubmatch(src[start:start+end], -1) {
		want = append(want, m[1])
	}
	if !reflect.DeepEqual(pageFolderColors, want) {
		t.Errorf("pageFolderColors drifted from lib/colors.ts CREW_COLORS: got %v, want %v", pageFolderColors, want)
	}
}

func TestPageFolderIconValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		icon  string
		color string
		okI   bool
		okC   bool
	}{
		{"empty is the default and valid", "", "", true, true},
		{"a registry icon and a palette colour", "rocket", "amber", true, true},
		{"a panel icon is not a crew icon", "memory", "", false, true},
		{"a hex colour is not a palette key", "", "#f59e0b", true, false},
		{"case matters", "Rocket", "Amber", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validPageFolderIcon(tc.icon); got != tc.okI {
				t.Errorf("validPageFolderIcon(%q) = %v, want %v", tc.icon, got, tc.okI)
			}
			if got := validPageFolderColor(tc.color); got != tc.okC {
				t.Errorf("validPageFolderColor(%q) = %v, want %v", tc.color, got, tc.okC)
			}
		})
	}
	if !strings.Contains(pageFolderIconRefusal("memory"), "345 names") && !strings.Contains(pageFolderIconRefusal("memory"), "names") {
		t.Error("the icon refusal does not say how large the set is")
	}
	for _, c := range pageFolderColors {
		if !strings.Contains(pageFolderColorRefusal("x"), c) {
			t.Errorf("the colour refusal does not list %q", c)
		}
	}
}
