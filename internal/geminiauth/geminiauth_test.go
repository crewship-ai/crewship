package geminiauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// loginJSON is the file Gemini CLI writes: JSON.stringify of the
// google-auth-library Credentials object, expiry_date in milliseconds.
func loginJSON(t *testing.T, extra map[string]any) string {
	t.Helper()
	m := map[string]any{
		"access_token":  "ya29.a0fake-access",
		"refresh_token": "1//0gREAL-SECRET",
		"scope":         DefaultScope,
		"token_type":    "Bearer",
		"id_token":      fakeJWT(t, map[string]any{"email": "jana@unify.cz", "sub": "1234"}),
		"expiry_date":   int64(1757150000000),
	}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestIsLogin(t *testing.T) {
	cases := []struct {
		typ, provider string
		want          bool
	}{
		{"AI_CLI_TOKEN", "GOOGLE", true},
		{"AI_CLI_TOKEN", "google", true},
		{"AI_CLI_TOKEN", " Google ", true},
		{"AI_CLI_TOKEN", "OPENAI", false},
		{"AI_CLI_TOKEN", "ANTHROPIC", false},
		{"API_KEY", "GOOGLE", false},
		{"CLI_TOKEN", "GOOGLE", false},
	}
	for _, tc := range cases {
		if got := IsLogin(tc.typ, tc.provider); got != tc.want {
			t.Errorf("IsLogin(%q, %q) = %v, want %v", tc.typ, tc.provider, got, tc.want)
		}
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{"whole file", loginJSON(t, nil), ""},
		{"scope and token_type defaulted", loginJSON(t, map[string]any{"scope": nil, "token_type": nil}), ""},
		{"empty", "   ", "empty value"},
		{"bare token", "ya29.a0fake-access", "not a JSON object"},
		{"invalid json", "{not json", "not valid JSON"},
		{"missing access_token", loginJSON(t, map[string]any{"access_token": nil}), "no access_token"},
		{"missing refresh_token", loginJSON(t, map[string]any{"refresh_token": nil}), "no refresh_token"},
		{"missing expiry_date", loginJSON(t, map[string]any{"expiry_date": nil}), "no expiry_date"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Parse(tc.value)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if f.Scope == "" || f.TokenType == "" {
					t.Errorf("scope/token_type not defaulted: %+v", f)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if msg := ShapeError(tc.value); !strings.HasPrefix(msg, "Google login: ") {
				t.Errorf("ShapeError = %q, want the Google login prefix", msg)
			}
		})
	}
	if msg := ShapeError(loginJSON(t, nil)); msg != "" {
		t.Errorf("ShapeError(valid) = %q, want empty", msg)
	}
}

func TestRender_ReplacesRefreshTokenOnly(t *testing.T) {
	f, err := Parse(loginJSON(t, map[string]any{"plan": "pro"}))
	if err != nil {
		t.Fatal(err)
	}
	body, err := Render(f)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "1//0gREAL-SECRET") {
		t.Fatalf("rendered file carries the real refresh token:\n%s", s)
	}
	if strings.Contains(s, `"plan"`) {
		t.Errorf("Crewship's plan field must not be written into the file Gemini reads:\n%s", s)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("rendered file is not JSON: %v", err)
	}
	want := map[string]any{
		"access_token":  "ya29.a0fake-access",
		"refresh_token": PlaceholderRefreshToken,
		"scope":         DefaultScope,
		"token_type":    "Bearer",
		"expiry_date":   float64(1757150000000),
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %v, want %v", k, out[k], v)
		}
	}
	if out["id_token"] != f.IDToken {
		t.Errorf("id_token not preserved")
	}
	if !strings.HasSuffix(s, "\n") {
		t.Errorf("rendered file should end with a newline")
	}
}

func TestRender_RefusesIncomplete(t *testing.T) {
	if _, err := Render(File{AccessToken: "x"}); err == nil {
		t.Error("Render without expiry_date must fail")
	}
	if _, err := Render(File{ExpiryDate: 1}); err == nil {
		t.Error("Render without access_token must fail")
	}
}

func TestPlanLabel(t *testing.T) {
	cases := []struct {
		plan string
		want string
	}{
		{"", "Google AI"},
		{"pro", "Google AI Pro"},
		{"ULTRA", "Google AI Ultra"},
		{"enterprise", "Google AI Enterprise"},
	}
	for _, tc := range cases {
		value := loginJSON(t, map[string]any{"plan": tc.plan})
		if tc.plan == "" {
			value = loginJSON(t, nil)
		}
		if got := PlanLabel(value); got != tc.want {
			t.Errorf("PlanLabel(plan=%q) = %q, want %q", tc.plan, got, tc.want)
		}
	}
	if got := PlanLabel("not a login"); got != "Google AI" {
		t.Errorf("PlanLabel(garbage) = %q", got)
	}
}

func TestAccessTokenExpiryAndEmail(t *testing.T) {
	value := loginJSON(t, nil)
	exp, ok := AccessTokenExpiry(value)
	if !ok || !exp.Equal(time.UnixMilli(1757150000000).UTC()) {
		t.Errorf("AccessTokenExpiry = %v, %v", exp, ok)
	}
	if _, ok := AccessTokenExpiry("garbage"); ok {
		t.Error("garbage must not have an expiry")
	}
	if got := Email(value); got != "jana@unify.cz" {
		t.Errorf("Email = %q", got)
	}
	if got := Email(loginJSON(t, map[string]any{"id_token": nil})); got != "" {
		t.Errorf("Email without id_token = %q, want empty", got)
	}
}
