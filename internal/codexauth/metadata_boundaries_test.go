package codexauth

import (
	"testing"
	"time"
)

func TestPlanLabelsPreserveKnownAndFuturePlans(t *testing.T) {
	for _, tc := range []struct{ plan, want string }{
		{"plus", "ChatGPT Plus"}, {"PRO", "ChatGPT Pro"}, {"team", "ChatGPT Team"},
		{"business", "ChatGPT Business"}, {"enterprise", "ChatGPT Enterprise"},
		{"free", "ChatGPT Free"}, {"", "ChatGPT"}, {"future", "ChatGPT Future"},
	} {
		t.Run(tc.plan, func(t *testing.T) {
			token := fakeJWT(t, map[string]any{authClaim: map[string]any{"chatgpt_plan_type": tc.plan}})
			if got := PlanLabel(token); got != tc.want {
				t.Fatalf("label = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMalformedLoginMetadataDoesNotInventExpiryOrPlan(t *testing.T) {
	for _, raw := range []string{"{", "{}", "header.invalid!.signature", "header.bm90LWpzb24.signature", "not-a-token"} {
		t.Run(raw, func(t *testing.T) {
			if got := PlanLabel(raw); got != "ChatGPT" {
				t.Fatalf("invented plan %q", got)
			}
			if expiry, ok := AccessTokenExpiry(raw); ok || !expiry.IsZero() {
				t.Fatalf("invented expiry %v, %v", expiry, ok)
			}
		})
	}
	for _, exp := range []any{nil, 0, -1, "tomorrow"} {
		if expiry, ok := AccessTokenExpiry(fakeJWT(t, map[string]any{"exp": exp})); ok || !expiry.IsZero() {
			t.Fatalf("invalid expiry %v was accepted: %v", exp, expiry)
		}
	}
}

func TestRenderRefusesIncompleteLogins(t *testing.T) {
	for _, tokens := range []Tokens{{}, {AccessToken: "fixture"}, {AccessToken: "fixture", IDToken: "fixture"}} {
		if data, err := Render(File{Tokens: tokens}, time.Now()); err == nil || data != nil {
			t.Fatalf("incomplete login produced a runnable file: %s, %v", data, err)
		}
	}
}
