package avatarstyle

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

func TestValid(t *testing.T) {
	tests := []struct {
		name  string
		style string
		want  bool
	}{
		{"empty means follow the crew", "", true},
		{"voxel bot", "voxel-bot", true},
		{"voxel art", "voxel-art", true},
		{"the pre-#3789 default", "bottts-neutral", true},
		{"a 9.4 style", "adventurer", true},
		{"last of the 9.4 catalogue", "toon-head", true},
		{"a phantom label from the old picker", "robots", false},
		{"case matters: slugs are lower-case", "Voxel-Bot", false},
		{"the label is not the slug", "Voxel Bot", false},
		{"surrounding space is not trimmed", " voxel-bot", false},
		{"a DiceBear 10 style this app does not offer", "blobs", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Valid(tt.style); got != tt.want {
				t.Errorf("Valid(%q) = %v, want %v", tt.style, got, tt.want)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	if Default != "voxel-bot" {
		t.Errorf("Default = %q, want voxel-bot", Default)
	}
	if Legacy != "bottts-neutral" {
		t.Errorf("Legacy = %q, want bottts-neutral", Legacy)
	}
	if !Valid(Default) || !Valid(Legacy) {
		t.Error("both defaults must be valid slugs")
	}
}

func TestSlugsIsACopy(t *testing.T) {
	s := Slugs()
	s[0] = "tampered"
	if Slugs()[0] == "tampered" {
		t.Error("Slugs must not hand out the backing array")
	}
}

// The web catalogue (AVATAR_STYLES in lib/agent-avatar.ts) is the source of
// the picker. A slug the picker offers but this list refuses would make the
// save fail; one this list accepts but the picker cannot draw would store a
// style that silently renders as the default.
func TestSlugsMatchTheWebCatalogue(t *testing.T) {
	src, err := os.ReadFile("../../lib/agent-avatar.ts")
	if err != nil {
		t.Fatalf("read lib/agent-avatar.ts: %v", err)
	}
	block := regexp.MustCompile(`(?s)export const AVATAR_STYLES[^=]*= \{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal("AVATAR_STYLES not found in lib/agent-avatar.ts")
	}
	var web []string
	for _, m := range regexp.MustCompile(`(?m)^\s*"?([a-z-]+)"?: \{ label:`).FindAllSubmatch(block[1], -1) {
		web = append(web, string(m[1]))
	}
	got := Slugs()
	sort.Strings(web)
	sort.Strings(got)
	if len(web) == 0 {
		t.Fatal("parsed no slugs from AVATAR_STYLES")
	}
	if len(web) != len(got) {
		t.Fatalf("web catalogue has %d styles %v, Go list has %d %v", len(web), web, len(got), got)
	}
	for i := range web {
		if web[i] != got[i] {
			t.Errorf("slug %d: web %q, Go %q", i, web[i], got[i])
		}
	}
}
