package backup

import (
	"strings"
	"testing"
)

func TestIsAdminRole(t *testing.T) {
	cases := map[string]bool{
		"OWNER":   true,
		"ADMIN":   true,
		"MANAGER": false,
		"MEMBER":  false,
		"VIEWER":  false,
		"":        false,
		"owner":   false, // case-sensitive by contract
	}
	for role, want := range cases {
		if got := IsAdminRole(role); got != want {
			t.Errorf("IsAdminRole(%q) = %v, want %v", role, got, want)
		}
	}
}

func TestRequireAdmin(t *testing.T) {
	if err := RequireAdmin("OWNER"); err != nil {
		t.Errorf("OWNER should pass, got %v", err)
	}
	err := RequireAdmin("MEMBER")
	if err == nil {
		t.Fatal("MEMBER should fail")
	}
	if !strings.Contains(err.Error(), "MEMBER") {
		t.Errorf("error should include offending role, got %q", err)
	}
}
