package recipes

import (
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// recipes.go — All + FindBySlug accessors over the builtins catalog.
//
// The recipes package is a hardcoded MVP catalog. The accessors
// themselves are trivial, but they protect against:
//   1. Catalog mutation leaking back to callers (All returns a copy)
//   2. Slug collisions inside the catalog (FindBySlug breaks if two
//      entries share a slug — the API surface assumes uniqueness)
//   3. Empty/malformed catalog entries (the install flow would fail
//      mid-provisioning instead of at definition time)
//
// All three are invariants the rest of the system relies on but no
// code enforces — these tests pin them so a future "add a recipe"
// PR has to keep them true.
// ---------------------------------------------------------------------------

// ---- All ----

func TestAll_ReturnsAtLeastOneBuiltin(t *testing.T) {
	// MVP ships 3 recipes (code-review, triage, research). If All()
	// suddenly returned empty, the dashboard's recipe carousel would
	// render with no cards — pin the lower bound so a refactor that
	// accidentally cleared builtins surfaces here.
	got := All()
	if len(got) == 0 {
		t.Fatal("All() = []; dashboard recipe carousel would be empty")
	}
}

func TestAll_ReturnsCopy_MutationDoesNotLeak(t *testing.T) {
	// Source: `copy(out, builtins)`. Pin that mutating the returned
	// slice does NOT mutate the package-internal catalog. A regression
	// to `return builtins` would let a misbehaved caller break every
	// subsequent install.
	first := All()
	if len(first) == 0 {
		t.Skip("empty catalog")
	}
	originalSlug := first[0].Slug

	// Mutate the returned slice — clobber the first entry.
	first[0] = Recipe{Slug: "tainted-by-caller"}

	// Re-read the catalog; it must NOT reflect the mutation.
	second := All()
	if second[0].Slug != originalSlug {
		t.Errorf("catalog mutated through returned slice: second[0].Slug = %q, want %q (All must return a copy)",
			second[0].Slug, originalSlug)
	}
}

func TestAll_LengthMatchesBuiltins(t *testing.T) {
	// `out := make([]Recipe, len(builtins))` + `copy` — the lengths
	// MUST agree. A regression that allocated wrong size (or under-
	// copied) would silently drop entries from the dashboard.
	got := All()
	if len(got) != len(builtins) {
		t.Errorf("All() len = %d, want %d (must match builtins length)", len(got), len(builtins))
	}
}

func TestAll_PreservesOrder(t *testing.T) {
	// Source comment: "Order is the display order on the dashboard
	// empty state." A regression that reordered or shuffled would
	// change the UX without anyone noticing in code review. Pin
	// position-for-position equality with builtins.
	got := All()
	for i := range got {
		if got[i].Slug != builtins[i].Slug {
			t.Errorf("All()[%d].Slug = %q, want %q (display order must equal builtins order)",
				i, got[i].Slug, builtins[i].Slug)
		}
	}
}

// ---- FindBySlug ----

func TestFindBySlug_FindsEachBuiltin(t *testing.T) {
	// Every slug in the catalog must round-trip through FindBySlug —
	// the API handler reads `?slug=...` and pipes it through here.
	// A regression in iteration / comparison would 404 a valid slug.
	for _, want := range builtins {
		t.Run(want.Slug, func(t *testing.T) {
			got := FindBySlug(want.Slug)
			if got == nil {
				t.Fatalf("FindBySlug(%q) = nil; built-in not findable", want.Slug)
			}
			if got.Slug != want.Slug {
				t.Errorf("returned recipe slug = %q, want %q", got.Slug, want.Slug)
			}
			if got.Name != want.Name {
				t.Errorf("returned recipe name = %q, want %q (round-trip integrity)", got.Name, want.Name)
			}
		})
	}
}

func TestFindBySlug_UnknownReturnsNil(t *testing.T) {
	// Defensive: API handler relies on nil to 404. A regression to
	// "return &Recipe{}" (zero-value) would silently install an empty
	// blueprint — much worse failure mode than a 404.
	for _, in := range []string{
		"",
		"never-existed",
		"code-review", // close to "code-review-crew" but distinct — must NOT prefix-match
		"CODE-REVIEW-CREW", // case-sensitive — slugs are exact
		"code-review-crew ", // trailing space — must NOT trim
		" code-review-crew",
	} {
		t.Run(in, func(t *testing.T) {
			if got := FindBySlug(in); got != nil {
				t.Errorf("FindBySlug(%q) = %+v, want nil", in, got)
			}
		})
	}
}

func TestFindBySlug_ReturnsPointerIntoBuiltins(t *testing.T) {
	// Source: `return &builtins[i]` — caller gets a pointer into the
	// catalog itself. Pin this so a future "return a copy" refactor
	// has to update both API handlers AND this test, because handlers
	// rely on field-by-field comparability (not pointer equality, but
	// the result must remain consistent across calls).
	a := FindBySlug(builtins[0].Slug)
	b := FindBySlug(builtins[0].Slug)
	if a == nil || b == nil {
		t.Fatal("FindBySlug returned nil for known slug")
	}
	// Two consecutive lookups must return the same address (current
	// contract). If a future refactor changes this, update the test
	// AND the API handler that uses the pointer identity for caching.
	if a != b {
		t.Errorf("FindBySlug returned different addresses for same slug: a=%p b=%p", a, b)
	}
}

// ---- Catalog-shape invariants ----

func TestBuiltins_SlugsAreUnique(t *testing.T) {
	// FindBySlug returns the FIRST match — a duplicate slug would
	// shadow the later entry and make it unreachable through the API.
	// Pin uniqueness here, not in FindBySlug, so the error message
	// names the offender directly.
	seen := map[string]int{}
	for i, r := range builtins {
		if prev, ok := seen[r.Slug]; ok {
			t.Errorf("duplicate slug %q at builtins[%d] (first at builtins[%d]); FindBySlug would only ever return the first", r.Slug, i, prev)
		}
		seen[r.Slug] = i
	}
}

func TestBuiltins_RequiredFieldsPopulated(t *testing.T) {
	// Each recipe's frontend rendering AND install flow assume these
	// fields are non-empty. A blank slug would 404 immediately; a
	// blank name renders a chip with no label; a blank crew_slug
	// would 500 at install when the crews INSERT trips a NOT NULL.
	for _, r := range builtins {
		t.Run(r.Slug, func(t *testing.T) {
			if r.Slug == "" {
				t.Error("Slug is empty — API handler would 404 the recipe")
			}
			if r.Name == "" {
				t.Error("Name is empty — dashboard card would render with no label")
			}
			if r.Description == "" {
				t.Error("Description is empty — card subtitle would be blank")
			}
			if r.CrewSlug == "" {
				t.Error("CrewSlug is empty — install would 500 (crews.slug is NOT NULL)")
			}
		})
	}
}

func TestBuiltins_SlugFormat_IsURLStable(t *testing.T) {
	// Source comment: "Slug is the URL-stable identifier". Lowercase
	// kebab-case ASCII is the convention — pin so a typo like
	// "Code_Review" doesn't slip in.
	slugRE := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	for _, r := range builtins {
		t.Run(r.Slug, func(t *testing.T) {
			if !slugRE.MatchString(r.Slug) {
				t.Errorf("Slug %q is not URL-stable kebab-case", r.Slug)
			}
		})
	}
}

func TestBuiltins_CredentialEnvVarsAreUnique_PerRecipe(t *testing.T) {
	// Within one recipe, two credentials with the same EnvVarName
	// would clobber each other in the install request — the wizard
	// only collects ONE value per env var. Pin uniqueness.
	for _, r := range builtins {
		t.Run(r.Slug, func(t *testing.T) {
			seen := map[string]bool{}
			for _, c := range r.Credentials {
				if c.EnvVarName == "" {
					t.Errorf("credential has empty EnvVarName")
					continue
				}
				if seen[c.EnvVarName] {
					t.Errorf("duplicate credential EnvVarName %q within recipe", c.EnvVarName)
				}
				seen[c.EnvVarName] = true
			}
		})
	}
}

func TestBuiltins_MCPEnvMappingReferencesDeclaredCredentials(t *testing.T) {
	// EnvMapping values point to credential EnvVarNames; a typo here
	// would render an MCP server with a placeholder env var that
	// the install flow would never populate. Pin referential integrity.
	for _, r := range builtins {
		t.Run(r.Slug, func(t *testing.T) {
			declared := map[string]bool{}
			for _, c := range r.Credentials {
				declared[c.EnvVarName] = true
			}
			for _, srv := range r.MCPServers {
				for envKey, credRef := range srv.EnvMapping {
					if credRef == "" {
						t.Errorf("MCP server %q env %q has empty credRef", srv.Name, envKey)
						continue
					}
					if !declared[credRef] {
						t.Errorf("MCP server %q env %q references credential %q not declared in recipe (typo? would never get a value at install)",
							srv.Name, envKey, credRef)
					}
				}
			}
		})
	}
}

func TestBuiltins_MCPTransportIsKnown(t *testing.T) {
	// Source comment: "Transport is \"stdio\" or \"streamable-http\"".
	// Pin so a typo / new variant has to be acknowledged in both the
	// catalog AND the install handler that switches on this.
	for _, r := range builtins {
		for _, srv := range r.MCPServers {
			t.Run(r.Slug+"/"+srv.Name, func(t *testing.T) {
				switch strings.ToLower(srv.Transport) {
				case "stdio", "streamable-http":
					// known
				default:
					t.Errorf("MCP server %q transport %q is not one of stdio / streamable-http", srv.Name, srv.Transport)
				}
			})
		}
	}
}
