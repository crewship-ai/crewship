package llmroute

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/llm"
)

// TestLLMRegistryIDsAgree cross-checks this table against internal/llm's
// provider registry WHERE THEY OVERLAP.
//
// It deliberately does NOT assert set equality, and that is the point of the
// test existing rather than a shared table existing. The two registries answer
// different questions — internal/llm describes how crewshipd constructs an
// outbound provider for keeper-aux calls, this one describes how an agent's
// CLI reaches a provider through the sidecar — so GOOGLE having no llm row and
// ollama having no route row are both correct. What would be a bug is the two
// disagreeing about a provider they BOTH name: a display string an operator
// sees in two places, or an auth scheme, or the lowercase key that both send
// to paymaster as a rate-card lookup.
func TestLLMRegistryIDsAgree(t *testing.T) {
	overlap := 0
	for _, s := range Specs() {
		lower := strings.ToLower(s.ID)
		spec, ok := llm.LookupProvider(lower)
		if !ok {
			continue
		}
		overlap++

		t.Run(s.ID, func(t *testing.T) {
			if s.DisplayName != spec.DisplayName {
				t.Errorf("DisplayName: llmroute %q vs llm %q", s.DisplayName, spec.DisplayName)
			}
			// Both feed a paymaster rate-card key, so a divergence here bills
			// the same provider under two rows.
			if s.LedgerProvider != spec.ID {
				t.Errorf("LedgerProvider %q != llm provider id %q", s.LedgerProvider, spec.ID)
			}

			def := s.AuthRules[len(s.AuthRules)-1]
			switch spec.Auth {
			case llm.AuthBearer:
				assertDefaultSlot(t, def, PlaceHeader, "Authorization", "Bearer ")
			case llm.AuthAnthropicKey:
				assertDefaultSlot(t, def, PlaceHeader, "x-api-key", "")
			case llm.AuthNone:
				// A provider internal/llm dials without a credential says
				// nothing about whether the sidecar route needs one: the aux
				// slot talks to a local runtime, the route may front a hosted
				// gateway. Nothing to assert, and asserting anyway would pin a
				// coincidence.
			default:
				t.Errorf("llm.AuthScheme %q has no mapping here; a new scheme needs a case", spec.Auth)
			}
		})
	}

	if overlap == 0 {
		t.Fatal("no llmroute ID has a matching internal/llm provider; every assertion above was skipped, " +
			"which means an id was renamed on one side and this cross-check went silently vacuous")
	}
}

func assertDefaultSlot(t *testing.T, rule AuthRule, place AuthPlacement, name, prefix string) {
	t.Helper()
	for _, slot := range rule.Slots {
		if slot.Placement == place && slot.Name == name {
			if slot.Prefix != prefix {
				t.Errorf("slot %s prefix = %q, want %q", name, slot.Prefix, prefix)
			}
			return
		}
	}
	t.Errorf("default AuthRule %+v has no %s slot named %q", rule, place, name)
}

// TestPackageIsALeaf is the constraint the package doc states, enforced rather
// than asserted in prose: no non-test file here may import anything from this
// repo.
//
// The rule is load-bearing in one direction — internal/sidecar imports this
// package, and internal/llm (the obvious place someone would reach for) pulls
// paymaster, telemetry, journal, lookout and modelcatalog behind it. The first
// crewship import added below is the one that puts the sidecar on the wrong
// side of a future import cycle, and it would compile fine on the day it
// landed.
//
// This file itself imports internal/llm, which is why the parse skips _test.go.
func TestPackageIsALeaf(t *testing.T) {
	// Read the directory and parse each file rather than using parser.ParseDir,
	// which is deprecated as of Go 1.25 for not honouring build tags. Per-file
	// parsing is what this check wants anyway: it asks a question about every
	// source file individually, not about a package as the toolchain assembles
	// one, so a build-tagged file is exactly as much in scope as any other.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		files++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, "crewship") {
				t.Errorf("%s imports %q; this package must stay a std-lib-only leaf", name, path)
			}
		}
	}
	if files == 0 {
		t.Fatal("parsed no non-test files; the check above proved nothing")
	}
}
