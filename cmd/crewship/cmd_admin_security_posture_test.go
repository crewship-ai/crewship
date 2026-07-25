package main

// CLI parity for the #1379 posture endpoint. The wording is the product here —
// a boolean printed as "true" reads as fine at a glance, which defeats the
// purpose of a posture view.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const posturePath = "/api/v1/admin/security-posture"

func runPosture(t *testing.T) string {
	t.Helper()
	covResetFlags(t, adminSecurityPostureCmd)
	return covCaptureAll(t, func() {
		if err := adminSecurityPostureCmd.RunE(adminSecurityPostureCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
}

func TestAdminSecurityPosture(t *testing.T) {
	t.Run("insecure state reads as insecure, not as 'true'", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(posturePath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"environment":"prod","encryption_key_configured":false,
				"plaintext_secrets_allowed":true,"private_endpoints_ceiling":false,
				"signup_open":true,"oauth_configured":false,"email_configured":false,
				"rate_limit_disabled":false,"rate_limit_effectively_disabled":false,
				"warnings":[{"key":"plaintext_secrets_allowed","severity":"high","message":"credentials unencrypted at rest"}]}`), "application/json"
		})
		out := runPosture(t)
		for _, want := range []string{"ALLOWED (insecure)", "NOT configured", "OPEN", "HIGH", "prod"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	})

	t.Run("prod-ignored rate limit is not reported as disabled", func(t *testing.T) {
		// Collapsing intent and effect would either invent an exposure or hide
		// a misconfiguration; both lines have to survive.
		stub := covStub(t)
		stub.OnGet(posturePath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"environment":"prod","encryption_key_configured":true,
				"rate_limit_disabled":true,"rate_limit_effectively_disabled":false,"warnings":[]}`), "application/json"
		})
		out := runPosture(t)
		if !strings.Contains(out, "IGNORED") || !strings.Contains(out, "limiter running") {
			t.Errorf("must show the flag is set but not in effect:\n%s", out)
		}
	})

	t.Run("genuinely disabled limiter says DISABLED", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(posturePath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"environment":"dev","encryption_key_configured":true,
				"rate_limit_disabled":true,"rate_limit_effectively_disabled":true,"warnings":[]}`), "application/json"
		})
		out := runPosture(t)
		if !strings.Contains(out, "DISABLED") {
			t.Errorf("want DISABLED:\n%s", out)
		}
		if strings.Contains(out, "IGNORED") {
			t.Errorf("dev must not claim the prod override:\n%s", out)
		}
	})

	t.Run("clean instance says so instead of printing an empty list", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(posturePath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"environment":"prod","encryption_key_configured":true,
				"plaintext_secrets_allowed":false,"signup_open":false,"warnings":[]}`), "application/json"
		})
		out := runPosture(t)
		if !strings.Contains(out, "No warnings") {
			t.Errorf("want an explicit all-clear:\n%s", out)
		}
	})

	t.Run("unset environment is labelled", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(posturePath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"environment":"","encryption_key_configured":true,"warnings":[]}`), "application/json"
		})
		out := runPosture(t)
		if !strings.Contains(out, "(unset)") {
			t.Errorf("an empty env should read as (unset), not as a blank:\n%s", out)
		}
	})

	t.Run("403 propagates", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(posturePath, clitest.ErrorResponse(403, "admin role required"))
		covResetFlags(t, adminSecurityPostureCmd)
		err := adminSecurityPostureCmd.RunE(adminSecurityPostureCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "admin role required") {
			t.Fatalf("got %v", err)
		}
	})
}
