package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// cuidShapedSlug satisfies looksLikeCUID's shape check (21+ lowercase
// alphanumeric chars starting with "c") while being a perfectly ordinary
// slug a workspace admin might pick — the exact collision from #1075.
const cuidShapedSlug = "customersuccessemea42"

// missingCUID looks like a CUID and isn't a slug either — a genuine typo
// or stale reference, which must still fail with a clean not-found.
const missingCUID = "cmissing00000000000000"

// resolveIDCase parameterizes the CUID-short-circuit fallback behaviour
// shared by resolveAgentID, resolveCrewID, resolveSkillID, and
// resolveCredentialID — all four trust a CUID-shaped input via
// cuidExists(client, singlePath) before falling back to a list scan.
type resolveIDCase struct {
	name       string
	listPath   string
	singlePath func(id string) string
	// listBody returns the list-endpoint JSON body containing one entry
	// whose "id" is realID and whose slug/name matches slugOrID.
	listBody func(realID, slugOrID string) string
	resolve  func(c *cli.Client, slugOrID string) (string, error)
}

func resolveIDCases() []resolveIDCase {
	return []resolveIDCase{
		{
			name:       "agent",
			listPath:   "/api/v1/agents",
			singlePath: func(id string) string { return "/api/v1/agents/" + id },
			listBody: func(realID, slugOrID string) string {
				return fmt.Sprintf(`[{"id":%q,"slug":%q}]`, realID, slugOrID)
			},
			resolve: resolveAgentID,
		},
		{
			name:       "crew",
			listPath:   "/api/v1/crews",
			singlePath: func(id string) string { return "/api/v1/crews/" + id },
			listBody: func(realID, slugOrID string) string {
				return fmt.Sprintf(`[{"id":%q,"slug":%q}]`, realID, slugOrID)
			},
			resolve: resolveCrewID,
		},
		{
			name:       "skill",
			listPath:   "/api/v1/skills",
			singlePath: func(id string) string { return "/api/v1/skills/" + id },
			listBody: func(realID, slugOrID string) string {
				return fmt.Sprintf(`[{"id":%q,"slug":%q}]`, realID, slugOrID)
			},
			resolve: resolveSkillID,
		},
		{
			name:       "credential",
			listPath:   "/api/v1/credentials",
			singlePath: func(id string) string { return "/api/v1/credentials/" + id },
			listBody: func(realID, slugOrID string) string {
				// resolveCredentialID matches by "name", not "slug".
				return fmt.Sprintf(`[{"id":%q,"name":%q}]`, realID, slugOrID)
			},
			resolve: resolveCredentialID,
		},
	}
}

// TestResolveID_CUIDShapedSlugFallsBack is the #1075 regression: a slug
// that happens to match looksLikeCUID's shape (e.g. "customersuccessemea42")
// must still resolve to the right id via the slug/name list scan, instead
// of being forwarded raw and dying downstream with a confusing not-found.
func TestResolveID_CUIDShapedSlugFallsBack(t *testing.T) {
	for _, tc := range resolveIDCases() {
		t.Run(tc.name, func(t *testing.T) {
			const realID = "cagentrealid0000000000"

			s := clitest.NewStubServer()
			defer s.Close()
			// The verify GET against the single-resource endpoint misses —
			// cuidShapedSlug isn't really an id.
			s.OnGet(tc.singlePath(cuidShapedSlug), clitest.ErrorResponse(http.StatusNotFound, "not found"))
			s.OnGet(tc.listPath, rawJSONHandler(http.StatusOK, tc.listBody(realID, cuidShapedSlug)))

			c := cli.NewClient(s.URL(), "", "")
			got, err := tc.resolve(c, cuidShapedSlug)
			if err != nil {
				t.Fatalf("resolve(%q) error: %v", cuidShapedSlug, err)
			}
			if got != realID {
				t.Errorf("resolve(%q) = %q, want %q (the real id)", cuidShapedSlug, got, realID)
			}

			calls := s.Calls()
			if len(calls) != 2 {
				t.Errorf("expected exactly 2 calls (verify miss + list fallback), got %d: %+v", len(calls), calls)
			}
		})
	}
}

// TestResolveID_GenuinelyMissingCUIDStillErrors: a CUID-shaped value that
// misses BOTH the verify GET and the slug/name scan is a real not-found,
// not a silently-wrong pass-through.
func TestResolveID_GenuinelyMissingCUIDStillErrors(t *testing.T) {
	for _, tc := range resolveIDCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := clitest.NewStubServer()
			defer s.Close()
			s.OnGet(tc.singlePath(missingCUID), clitest.ErrorResponse(http.StatusNotFound, "not found"))
			s.OnGet(tc.listPath, rawJSONHandler(http.StatusOK, tc.listBody("cotherrealid00000000000", "someone-else")))

			c := cli.NewClient(s.URL(), "", "")
			_, err := tc.resolve(c, missingCUID)
			if err == nil {
				t.Fatal("expected a not-found error, got nil")
			}
			if code := cli.ExitCodeFor(err); code != cli.ExitNotFound {
				t.Errorf("ExitCodeFor(%v) = %d, want %d (ExitNotFound)", err, code, cli.ExitNotFound)
			}
		})
	}
}

// TestResolveID_RealCUIDFastPathNoExtraGET: a real CUID must still resolve
// on the fast path with exactly one call (the verify GET) — no list-scan
// fallback fires when the verify GET already confirms the id exists.
func TestResolveID_RealCUIDFastPathNoExtraGET(t *testing.T) {
	for _, tc := range resolveIDCases() {
		t.Run(tc.name, func(t *testing.T) {
			const realID = "cagentrealid0000000000"

			s := clitest.NewStubServer()
			defer s.Close()
			s.OnGet(tc.singlePath(realID), clitest.JSONResponse(http.StatusOK, map[string]string{"id": realID}))
			// Deliberately do NOT stub the list endpoint — if resolve
			// falls through to it, the request hits the StubServer's
			// default 404 fallback and the test fails loudly instead of
			// silently passing.

			c := cli.NewClient(s.URL(), "", "")
			got, err := tc.resolve(c, realID)
			if err != nil {
				t.Fatalf("resolve(%q) error: %v", realID, err)
			}
			if got != realID {
				t.Errorf("resolve(%q) = %q, want %q", realID, got, realID)
			}

			calls := s.Calls()
			if len(calls) != 1 {
				t.Errorf("expected exactly 1 call (verify only, no list fallback), got %d: %+v", len(calls), calls)
			}
		})
	}
}

// rawJSONHandler serves body verbatim (already-marshalled JSON) — used
// where the fixture needs exact control over field names (e.g.
// credentials key by "name", not "slug") that clitest.JSONResponse's
// generic marshal path would otherwise need a throwaway struct for.
func rawJSONHandler(status int, body string) clitest.Handler {
	return func(_ *http.Request, _ []byte) (int, []byte, string) {
		return status, []byte(body), "application/json"
	}
}
