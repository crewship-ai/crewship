package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An inbox row names a role in TargetRole and the endpoint that resolves it
// names a role in the router. When the two disagree, the product notifies
// someone it will then refuse: a skill proposal addressed to MANAGER whose
// approve route is roleManage hands its reader a button and a 403.
//
// That drift is invisible in review — the two facts live in different packages
// and neither mentions the other — so this test reads both out of the source
// and requires them to agree.
//
// Adding a decision kind means adding a line here. That is the point: the pair
// is a contract, and a contract nobody restates is a contract that rots.
func TestInboxTargetRoleMatchesDecider(t *testing.T) {
	repo := repoRoot(t)

	cases := []struct {
		name string
		// File that writes the inbox row, and the TargetRole it must carry.
		writer     string
		targetRole string
		// A route whose handler resolves that row, and the role tier it needs.
		routeFile    string
		routeFrag    string
		requiredTier string
	}{
		{
			name:         "skill proposal",
			writer:       "internal/api/skills_author_handler.go",
			targetRole:   "ADMIN",
			routeFile:    "internal/api/router_orchestration.go",
			routeFrag:    "/api/v1/skills/proposed/approve",
			requiredTier: "roleManage",
		},
		{
			name:         "memory consolidation",
			writer:       "internal/consolidate/proposed.go",
			targetRole:   "ADMIN",
			routeFile:    "internal/api/router_orchestration.go",
			routeFrag:    "/api/v1/consolidate/proposed/{id}/approve",
			requiredTier: "roleManage",
		},
		{
			// The one this table did not cover, and the one it would have caught.
			// The credential escalation was written with TargetRole MANAGER while
			// its resolve route is roleManage — so every MANAGER and above was
			// shown "ACCESS REQUEST, decide this" about a production credential
			// and could not decide it. Exposure without authority: the reader
			// learns the credential exists, who asked, with what justification and
			// at what risk, and has no part in the ruling.
			name:         "keeper credential escalation",
			writer:       "internal/api/keeper_request.go",
			targetRole:   "ADMIN",
			routeFile:    "internal/api/router_internal.go",
			routeFrag:    "/api/v1/admin/keeper/requests/{requestId}/resolve",
			requiredTier: "roleManage",
		},
		{
			name:         "routine proposal",
			writer:       "internal/api/pipeline_governance.go",
			targetRole:   "MANAGER",
			routeFile:    "internal/api/router_pipelines.go",
			routeFrag:    "/pipelines/{slug}/approve",
			requiredTier: "roleCreate",
		},
		{
			name:         "pipeline waitpoint",
			writer:       "internal/pipeline/waitpoints.go",
			targetRole:   "MANAGER",
			routeFile:    "internal/api/router_pipelines.go",
			routeFrag:    "/pipelines/waitpoints/{token}/approve",
			requiredTier: "roleCreate",
		},
	}

	// The tier a route demands, expressed as the lowest role that satisfies it.
	// Mirrors canRole: "create" is MANAGER and up, "manage" is OWNER/ADMIN.
	lowestRoleFor := map[string]string{
		"roleCreate": "MANAGER",
		"roleManage": "ADMIN",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer := readFile(t, filepath.Join(repo, tc.writer))
			if !strings.Contains(writer, `TargetRole:  "`+tc.targetRole+`"`) &&
				!strings.Contains(writer, `TargetRole: "`+tc.targetRole+`"`) {
				t.Fatalf("%s: expected the inbox row to be addressed to %s; if the endpoint's tier changed, change both",
					tc.writer, tc.targetRole)
			}

			routes := readFile(t, filepath.Join(repo, tc.routeFile))
			line := routeLineFor(routes, tc.routeFrag)
			if line == "" {
				t.Fatalf("%s: route %q not found — did it move?", tc.routeFile, tc.routeFrag)
			}
			if !strings.Contains(line, tc.requiredTier) {
				t.Fatalf("route %q no longer uses %s:\n  %s", tc.routeFrag, tc.requiredTier, strings.TrimSpace(line))
			}

			want := lowestRoleFor[tc.requiredTier]
			if want != tc.targetRole {
				t.Fatalf("%s notifies %s but %q needs %s — the reader would get a 403",
					tc.writer, tc.targetRole, tc.routeFrag, want)
			}
		})
	}
}

func routeLineFor(src, fragment string) string {
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, fragment) && strings.Contains(line, "authedMut") {
			return line
		}
	}
	return ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// repoRoot walks up from the test's working directory to the module root, so
// the test does not depend on where `go test` was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found above the test directory")
	return ""
}
