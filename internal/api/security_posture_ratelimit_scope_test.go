package api

import (
	"strings"
	"testing"
)

// The rate-limit warning claimed credential stuffing was unthrottled when the
// limiter is off. It is not: CREWSHIP_RATELIMIT_DISABLED short-circuits
// RateLimiter.Middleware, which is the per-IP HTTP group only. The login
// lockout is a separate mechanism inside the signin handler
// (checkAndLockoutOnFail, nextauth.go) that never consults that flag, and so
// are the notification, provisioning and webhook limiters.
//
// An operator who knows the lockout still works reads "credential stuffing is
// unthrottled", sees it is wrong, and learns to discount the whole panel —
// which is how a real warning on the same panel gets ignored later. A
// security warning that overstates its scope is worse than a narrower one.

func TestSecurityPosture_RateLimitWarningNamesOnlyWhatIsActuallyOff(t *testing.T) {
	p := buildSecurityPosture(false, false, false, true, postureState{BackupsRecorded: 1})

	var msg string
	for _, w := range p.Warnings {
		if w.Key == "rate_limit_disabled" {
			msg = w.Message
		}
	}
	if msg == "" {
		t.Fatal("no rate_limit_disabled warning was produced")
	}

	// The login lockout is untouched by this flag, so the warning must not
	// claim login brute-forcing is open.
	if strings.Contains(strings.ToLower(msg), "credential-stuffing") ||
		strings.Contains(strings.ToLower(msg), "credential stuffing") {
		t.Errorf("warning still claims credential stuffing is unthrottled:\n%s", msg)
	}

	// It must still name the exposure that IS real — the per-IP HTTP limits,
	// which cover the credential test and reveal endpoints.
	for _, want := range []string{"per-IP", "/credentials/test"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning does not name %q:\n%s", want, msg)
		}
	}

	// And it should say what still protects the login, so the reader can tell
	// this apart from "nothing is limited".
	if !strings.Contains(strings.ToLower(msg), "lockout") {
		t.Errorf("warning does not mention the login lockout still applying:\n%s", msg)
	}
}
