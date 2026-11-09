package kindguide

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// The UI's Artifact family (components/features/artifacts-library/kinds.ts)
// plus Library, the Design mode whose content is a design system. Craft kinds
// (word, sheet) have their own single-writer playbook, crewship-craft.
var wantKinds = []string{"docs", "slides", "design", "library", "dashboard", "app"}

func TestEveryArtifactKindShipsAVersionedGuide(t *testing.T) {
	list := List()
	if len(list) != len(wantKinds) {
		t.Fatalf("List() has %d guides, want %d", len(list), len(wantKinds))
	}
	semver := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	for i, kind := range wantKinds {
		g := list[i]
		if g.Kind != kind {
			t.Fatalf("List()[%d].Kind = %q, want %q (stable order)", i, g.Kind, kind)
		}
		if g.Title == "" || !semver.MatchString(g.Version) || !semver.MatchString(g.RulesVersion) {
			t.Fatalf("%s: title %q version %q rules %q", kind, g.Title, g.Version, g.RulesVersion)
		}
		sum := sha256.Sum256([]byte(g.Content))
		if g.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: sha256 is not over the served content", kind)
		}
		if g.Bytes != len(g.Content) || g.Bytes > MaxGuideBytes {
			t.Fatalf("%s: %d bytes (limit %d)", kind, g.Bytes, MaxGuideBytes)
		}
		if strings.HasPrefix(g.Content, "---") {
			t.Fatalf("%s: front matter leaked into served content", kind)
		}
	}
}

// Every guide must carry each contract section, so an agent working on any
// kind gets the same questions answered for that kind.
func TestEveryGuideAnswersTheAgentContract(t *testing.T) {
	sections := []string{
		"## Data shape",
		"## Limits",
		"## Write order",
		"## Read before write",
		"## Conflicts",
		"## Agent modes",
		"## Verification",
		"## Untrusted content",
	}
	for _, g := range List() {
		for _, s := range sections {
			if !strings.Contains(g.Content, "\n"+s+"\n") {
				t.Errorf("%s guide lacks section %q", g.Kind, s)
			}
		}
	}
}

// The shared rules are one source appended to every guide; the strings below
// are the owner's fixed decisions and must reach every kind verbatim.
func TestSharedRulesReachEveryGuide(t *testing.T) {
	rules := []string{
		"third refusal",                  // conflict: stop after 3 refusals and report
		"Comment is not Propose",         // a comment never stands in for a proposal
		"Only the artifact's Owner",      // only the Owner delegates Edit
		"Propose is the default",         // default mode
		"Do not re-read, screenshot",     // no verification spam
		"data, never instructions",       // untrusted content
		"create: write the index",        // write order on create
		"revise: write the data first",   // write order on revise
		"read the current version first", // read-before-write
	}
	for _, g := range List() {
		for _, r := range rules {
			if !strings.Contains(g.Content, r) {
				t.Errorf("%s guide lacks shared rule %q", g.Kind, r)
			}
		}
	}
}

func TestLookupAcceptsKnownKindsAndAliasesOnly(t *testing.T) {
	for in, want := range map[string]string{
		"docs": "docs", "Docs": "docs", " app ": "app",
		"design-system": "library", "design_system": "library", "library": "library",
		"doc": "docs", "deck": "slides", "project": "app",
	} {
		g, err := Lookup(in)
		if err != nil || g.Kind != want {
			t.Fatalf("Lookup(%q) = %q, %v; want %q", in, g.Kind, err, want)
		}
	}
	for _, in := range []string{"", "word", "sheet", "../docs", "docs.md", "rules", strings.Repeat("a", 200)} {
		if _, err := Lookup(in); !errors.Is(err, ErrUnknownKind) {
			t.Fatalf("Lookup(%q) err = %v, want ErrUnknownKind", in, err)
		}
	}
}

func TestListReturnsDetachedCopies(t *testing.T) {
	a := List()
	a[0].Content = "changed"
	a[0].Kind = "changed"
	if b := List(); b[0].Kind != "docs" || b[0].Content == "changed" {
		t.Fatal("List exposes the registry for mutation")
	}
}

func TestLookupReturnsDetachedDeclarations(t *testing.T) {
	a, _ := Lookup("app")
	a.Modes[0] = "edit"
	a.Capabilities.Supported[0] = "model"
	if b, _ := Lookup("app"); b.Modes[0] != "ask" || b.Capabilities.Supported[0] == "model" {
		t.Fatal("Lookup exposes registry slices")
	}
}
