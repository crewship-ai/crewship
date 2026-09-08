package api

import (
	"strings"
	"testing"
)

// A subscription login is stored whole — Codex's auth.json, Gemini's
// oauth_creds.json — and a value the CLI would refuse is refused HERE, where
// the operator can read why, not at run time as a 401 that blames the key.
func TestValidateCredentialPayload_LoginShapes(t *testing.T) {
	codexLogin := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id.tok.en","access_token":"acc.tok.en","refresh_token":"rt.x","account_id":"acct-1"},"last_refresh":"2026-08-29T20:31:08Z"}`
	geminiLogin := `{"access_token":"ya29.x","refresh_token":"1//0g-x","scope":"s","token_type":"Bearer","id_token":"","expiry_date":1757150000000}`

	cases := []struct {
		name     string
		provider string
		value    string
		wantErr  string
	}{
		{"codex login stores", "OPENAI", codexLogin, ""},
		{"codex bare token refused", "OPENAI", "eyJ.bare.token", "OpenAI login: "},
		{"gemini login stores", "GOOGLE", geminiLogin, ""},
		{"gemini lower-case provider stores", "google", geminiLogin, ""},
		{"gemini bare token refused", "GOOGLE", "ya29.bare", "Google login: not a JSON object"},
		{"gemini file without refresh_token refused", "GOOGLE", `{"access_token":"ya29.x","expiry_date":1757150000000}`, "Google login: oauth_creds.json has no refresh_token"},
		{"gemini file without expiry refused", "GOOGLE", `{"access_token":"ya29.x","refresh_token":"1//0g-x"}`, "Google login: oauth_creds.json has no expiry_date"},
		{"anthropic setup-token is opaque", "ANTHROPIC", "sk-ant-oat01-x", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &createCredentialRequest{Name: "login", Type: "AI_CLI_TOKEN", Provider: tc.provider, Value: tc.value}
			msg := validateCredentialPayload(req)
			if tc.wantErr == "" {
				if msg != "" {
					t.Fatalf("validateCredentialPayload = %q, want accepted", msg)
				}
				return
			}
			if !strings.HasPrefix(msg, tc.wantErr) {
				t.Fatalf("validateCredentialPayload = %q, want prefix %q", msg, tc.wantErr)
			}
		})
	}
}
