package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #3789 — DiceBear 10's voxel styles are never stored. The client skips them
// (lib/agent-avatar-persist.ts) because the allowlist below refuses their
// markup, and the allowlist is not widened for them: `<use href>` is one of
// the elements it exists to keep out, and a stored <img> render would freeze
// the animation anyway. This pins the premise of that decision against real
// generator output (testdata from scripts/gen-avatar-fixtures.mjs). If it goes
// red, DiceBear 10 started emitting storable markup and the choice can be
// revisited — do not "fix" it by widening the allowlist without that review.
func TestValidateAgentAvatarSVG_RefusesDiceBear10Voxel(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "dicebear10_voxel_avatars.json"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures map[string]string
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	if len(fixtures) != 12 {
		t.Fatalf("got %d voxel fixtures, want 12 (2 styles x 3 seeds x still/animated)", len(fixtures))
	}
	for name, svg := range fixtures {
		t.Run(name, func(t *testing.T) {
			err := validateAgentAvatarSVG(svg)
			if err == nil {
				t.Fatal("voxel output passed the stored-avatar allowlist; revisit the never-store decision (#3789)")
			}
			// The first refusal is the inert aria-hidden attribute, but that is
			// not the reason: even with it allowed, the markup is built on
			// <use href> and carries an XML comment.
			if !strings.Contains(svg, "<use href=\"#") || !strings.Contains(svg, "<!--") {
				t.Errorf("voxel output no longer relies on <use href> and a comment (refused for: %v)", err)
			}
		})
	}
}
