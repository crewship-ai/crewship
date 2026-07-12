package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// newListServer returns an httptest server that answers every GET with the
// given JSON list body — enough to drive the slug→ID resolvers to a miss.
func newListServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestResolverMissesAreExitNotFound locks the exit-code contract for the
// client-side slug resolvers: a lookup miss is a not-found (exit 3), not a
// generic failure (exit 1). These short-circuit before any 404 response
// exists, so the typing must happen client-side.
func TestResolverMissesAreExitNotFound(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		resolve func(c *cli.Client) error
	}{
		{
			name: "agent slug miss with candidates",
			body: `[{"id":"cabcdefghijklmnopqrst","slug":"viktor"}]`,
			resolve: func(c *cli.Client) error {
				_, err := resolveAgentID(c, "vitkor")
				return err
			},
		},
		{
			name: "agent slug miss empty workspace",
			body: `[]`,
			resolve: func(c *cli.Client) error {
				_, err := resolveAgentID(c, "ghost")
				return err
			},
		},
		{
			name: "crew slug miss",
			body: `[{"id":"cabcdefghijklmnopqrst","slug":"uctarna"}]`,
			resolve: func(c *cli.Client) error {
				_, err := resolveCrewID(c, "nonexistent")
				return err
			},
		},
		{
			name: "integration name miss",
			body: `[{"id":"cabcdefghijklmnopqrst","name":"github"}]`,
			resolve: func(c *cli.Client) error {
				_, err := resolveIntegrationID(c, "gitlab")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newListServer(t, tt.body)
			err := tt.resolve(cli.NewClient(srv.URL, "", ""))
			if err == nil {
				t.Fatal("expected a not-found error, got nil")
			}
			if code := cli.ExitCodeFor(err); code != cli.ExitNotFound {
				t.Errorf("ExitCodeFor(%v) = %d, want %d (ExitNotFound)", err, code, cli.ExitNotFound)
			}
		})
	}
}

// TestRequireWorkspaceIsExitValidation: "no workspace set" is a precondition
// failure the caller can fix by passing --workspace — a validation error
// (exit 2), not a generic exit 1. Its sibling requireAuth already types its
// failure as ExitAuth.
func TestRequireWorkspaceIsExitValidation(t *testing.T) {
	origWorkspace, origCfg := flagWorkspace, cliCfg
	t.Cleanup(func() { flagWorkspace, cliCfg = origWorkspace, origCfg })
	t.Setenv("CREWSHIP_WORKSPACE", "")

	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{}

	err := requireWorkspace()
	if err == nil {
		t.Fatal("expected an error with no workspace configured")
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitValidation {
		t.Errorf("ExitCodeFor(%v) = %d, want %d (ExitValidation)", err, code, cli.ExitValidation)
	}
}
