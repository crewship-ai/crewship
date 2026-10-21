package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
)

func TestDemoRBACMatrixCoversCapabilityInventory(t *testing.T) {
	expected := append([]string{"chat"}, demoMatrixCapabilities...)
	slices.Sort(expected)
	if !slices.Equal(expected, api.AllCapabilities()) {
		t.Fatalf("shipped capability inventory changed: add explicit demo profiles and API expectations: %v", api.AllCapabilities())
	}
	users := demoCapabilityUsers()
	if len(users) != 40 {
		t.Fatalf("want 40 named variants, got %d", len(users))
	}
	seen := map[string]bool{}
	for _, u := range users {
		if seen[u.Email] {
			t.Fatalf("duplicate profile %s", u.Email)
		}
		seen[u.Email] = true
		if !slices.Contains([]string{"VIEWER", "MEMBER", "MANAGER", "ADMIN"}, u.Role) || !strings.HasPrefix(u.Email, strings.ToLower(u.Role)+"-") {
			t.Fatalf("invalid profile: %+v", u)
		}
		if !slices.Contains(u.Capabilities, "chat") {
			t.Fatalf("profile missing baseline: %s", u.Email)
		}
	}
}

func TestSeedRBACMatrixRequiresExplicitTarget(t *testing.T) {
	oldServer, oldWorkspace := flagServer, flagWorkspace
	t.Cleanup(func() { flagServer, flagWorkspace = oldServer, oldWorkspace })
	cmd := newSeedRBACMatrixCmd()
	for _, tc := range []struct{ server, workspace, want string }{
		{"", "", "explicit --workspace"},
		{"", "ws", "explicit --server"},
		{"https://user:password@example.invalid", "ws", "absolute HTTP(S) URL"},
	} {
		flagServer, flagWorkspace = tc.server, tc.workspace
		if err := cmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("target guard: %v, want %q", err, tc.want)
		}
	}
}

func TestDemoRBACRosterUsesEnglishRoleLabels(t *testing.T) {
	roster := append(append([]demoUser{}, demoUsers...), demoCapabilityUsers()...)
	if len(roster) != 44 {
		t.Fatalf("want four baseline accounts and forty profiles, got %d", len(roster))
	}
	for _, user := range roster {
		if !strings.HasPrefix(user.FullName, "RBAC "+user.Role+" · ") {
			t.Fatalf("fixture must identify its role and profile: %q", user.FullName)
		}
		for _, r := range user.FullName {
			if unicode.IsLetter(r) && r > unicode.MaxASCII {
				t.Fatalf("demo role labels must use English ASCII letters: %q", user.FullName)
			}
		}
	}
}

func TestDemoRBACBaselineUsesEnglishRoleProfiles(t *testing.T) {
	if len(demoUsers) != 4 {
		t.Fatalf("want four baseline accounts, got %d", len(demoUsers))
	}
	for _, user := range demoUsers {
		wantEmail := strings.ToLower(user.Role) + "-baseline@crewship.local"
		if user.Email != wantEmail || user.FullName != "RBAC "+user.Role+" · baseline" || user.Password != "rbac-demo-1234" {
			t.Errorf("baseline for %s = %q, %q, %q; want English role profile %q with documented demo password", user.Role, user.Email, user.FullName, user.Password, wantEmail)
		}
	}
}

func TestSeedRBACPreservesExistingCapabilityEdits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		caps      []string
		wantError bool
	}{
		{"same profile", []string{"chat", "routine.run"}, false},
		{"operator edited profile", []string{"chat"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/workspaces" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`[{"id":"workspace","slug":"workspace"}]`))
					return
				}
				if r.URL.Path != "/api/v1/workspaces/workspace/members/user/capabilities" {
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					mutations++
					t.Errorf("existing profile must not be changed: %s", r.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": tc.caps})
			}))
			defer server.Close()
			client := cli.NewClient(server.URL, "fixture", "workspace")
			err := configureSeedCapabilities(client, "workspace", "user", []string{"chat", "routine.run"}, false)
			if (err != nil) != tc.wantError || mutations != 0 {
				t.Fatalf("preservation: err=%v mutations=%d", err, mutations)
			}
		})
	}
}
