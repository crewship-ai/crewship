package main

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/crewship-ai/crewship/internal/api"
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
