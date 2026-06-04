package api

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// ExtractClientIP honours XFF only behind a trusted proxy; with no
// trusted-proxy config it ignores XFF and uses RemoteAddr.

func TestExtractClientIP_RemoteAddrWhenNoTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.7:9999"
	req.Header.Set("X-Forwarded-For", "1.1.1.1") // must be ignored (untrusted)
	if got := ExtractClientIP(req); got != "203.0.113.7" {
		t.Errorf("ExtractClientIP=%q want 203.0.113.7 (XFF ignored without trusted proxy)", got)
	}
}

// TokenScopesFromContext returns nil for JWT/unscoped callers and the
// scope slice when a CLI token stamped one.

func TestTokenScopesFromContext_NilWhenAbsent(t *testing.T) {
	if got := TokenScopesFromContext(context.Background()); got != nil {
		t.Errorf("TokenScopesFromContext=%v want nil", got)
	}
}

func TestTokenScopesFromContext_ReturnsScopes(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxTokenScopes, stringSet{"agents:read": {}})
	got := TokenScopesFromContext(ctx)
	if len(got) != 1 || got[0] != "agents:read" {
		t.Errorf("TokenScopesFromContext=%v want [agents:read]", got)
	}
}

// MustNotDisableRateLimitInProd panics only when rate limiting is off
// AND the env is production.

func TestMustNotDisableRateLimitInProd(t *testing.T) {
	orig := rateLimitDisabled
	origEnv := os.Getenv("CREWSHIP_ENV")
	t.Cleanup(func() {
		rateLimitDisabled = orig
		_ = os.Setenv("CREWSHIP_ENV", origEnv)
	})

	// Disabled + prod → panic.
	rateLimitDisabled = true
	_ = os.Setenv("CREWSHIP_ENV", "production")
	func() {
		defer func() {
			if recover() == nil {
				t.Errorf("expected panic when rate limit disabled in production")
			}
		}()
		MustNotDisableRateLimitInProd()
	}()

	// Disabled + non-prod → no panic.
	_ = os.Setenv("CREWSHIP_ENV", "dev")
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("unexpected panic in dev env: %v", r)
			}
		}()
		MustNotDisableRateLimitInProd()
	}()

	// Enabled → no panic regardless of env.
	rateLimitDisabled = false
	_ = os.Setenv("CREWSHIP_ENV", "production")
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("unexpected panic when rate limiting enabled: %v", r)
			}
		}()
		MustNotDisableRateLimitInProd()
	}()
}

// isEmptyRequirements is true only when every provisioning field is its
// zero value.

func TestIsEmptyRequirements(t *testing.T) {
	if !isEmptyRequirements(devcontainer.AggregatedRequirements{}) {
		t.Errorf("zero-value requirements should be empty")
	}
	if isEmptyRequirements(devcontainer.AggregatedRequirements{Privileged: true}) {
		t.Errorf("Privileged=true should not be empty")
	}
	if isEmptyRequirements(devcontainer.AggregatedRequirements{ContainerEnv: map[string]string{"K": "V"}}) {
		t.Errorf("non-empty ContainerEnv should not be empty")
	}
}
