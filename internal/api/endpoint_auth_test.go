package api

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// #961 Feature A — an ENDPOINT_URL credential may store either a bare URL
// (the #957 shape, still supported) or a {baseURL,apiKey,headers} JSON object
// carrying auth for an authenticated endpoint.

func TestParseEndpointValue(t *testing.T) {
	t.Run("bare URL → baseURL only", func(t *testing.T) {
		base, key, hdrs, err := parseEndpointValue("http://host:11434/v1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if base != "http://host:11434/v1" || key != "" || hdrs != nil {
			t.Fatalf("bare URL parsed wrong: base=%q key=%q hdrs=%v", base, key, hdrs)
		}
	})

	t.Run("JSON object → all three", func(t *testing.T) {
		v := `{"baseURL":"https://llm.example.com/v1","apiKey":"sk-secret","headers":{"X-Tenant":"acme"}}`
		base, key, hdrs, err := parseEndpointValue(v)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if base != "https://llm.example.com/v1" {
			t.Errorf("baseURL = %q", base)
		}
		if key != "sk-secret" {
			t.Errorf("apiKey = %q", key)
		}
		if hdrs["X-Tenant"] != "acme" {
			t.Errorf("headers = %v", hdrs)
		}
	})

	t.Run("malformed JSON → error", func(t *testing.T) {
		if _, _, _, err := parseEndpointValue(`{"baseURL": bad}`); err == nil {
			t.Error("expected error for malformed JSON")
		}
	})

	t.Run("JSON without baseURL → error", func(t *testing.T) {
		if _, _, _, err := parseEndpointValue(`{"apiKey":"sk-x"}`); err == nil {
			t.Error("expected error for JSON missing baseURL")
		}
	})

	t.Run("empty → error", func(t *testing.T) {
		if _, _, _, err := parseEndpointValue("   "); err == nil {
			t.Error("expected error for empty value")
		}
	})
}

func TestBuildEndpointValue_RoundTrip(t *testing.T) {
	// No auth → stays a bare URL (human-readable in a raw row).
	if v, err := buildEndpointValue("http://h:11434/v1", "", nil); err != nil || v != "http://h:11434/v1" {
		t.Fatalf("bare round-trip: v=%q err=%v", v, err)
	}
	// With auth → JSON that parses back to the same parts.
	v, err := buildEndpointValue("https://h/v1", "sk-1", map[string]string{"A": "b"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	base, key, hdrs, err := parseEndpointValue(v)
	if err != nil || base != "https://h/v1" || key != "sk-1" || hdrs["A"] != "b" {
		t.Fatalf("round-trip mismatch: base=%q key=%q hdrs=%v err=%v", base, key, hdrs, err)
	}
}

func TestValidateEndpointURL_AcceptsJSONAndBare(t *testing.T) {
	cases := map[string]struct {
		value  string
		wantOK bool
	}{
		"bare http":  {"http://host:11434/v1", true},
		"bare https": {"https://host/v1", true},
		// #974: http + token on a hostname is now rejected (cleartext token);
		// use https for an authenticated JSON endpoint.
		"json with baseURL": {`{"baseURL":"https://host:11434","apiKey":"sk-x"}`, true},
		"bare non-url":      {"not a url", false},
		"bare ftp scheme":   {"ftp://host/x", false},
		"json missing base": {`{"apiKey":"sk-x"}`, false},
		"json bad baseURL":  {`{"baseURL":"ftp://x"}`, false},
		"json malformed":    {`{"baseURL":`, false},
		"empty":             {"", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := validateEndpointURL(tc.value)
			if tc.wantOK && msg != "" {
				t.Errorf("expected valid, got %q", msg)
			}
			if !tc.wantOK && msg == "" {
				t.Errorf("expected invalid, got empty message")
			}
		})
	}
}

// The read path must echo the base URL but never the token/headers.
func TestDecryptEndpointURLForRead_HidesAuth(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	logger := discardLogger()

	v := `{"baseURL":"http://host:11434/v1","apiKey":"sk-super-secret","headers":{"X-Tenant":"acme"}}`
	enc, err := encryption.Encrypt(v)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got := decryptEndpointURLForRead(CredTypeEndpointURL, enc, logger)
	if got == nil {
		t.Fatal("expected base URL echoed, got nil")
	}
	if *got != "http://host:11434/v1" {
		t.Errorf("read should show baseURL only, got %q", *got)
	}
	if *got == v || strings.Contains(*got, "sk-super-secret") || strings.Contains(*got, "X-Tenant") {
		t.Errorf("read must never surface apiKey/headers, got %q", *got)
	}
}

// resolveLocalModelEndpoint threads the auth token/headers off a JSON-valued
// workspace credential.
func TestResolveLocalModelEndpoint_CarriesAuth(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	logger := discardLogger()

	const wsID = "ws-auth"
	mustExec(t, db, `INSERT INTO users (id, email) VALUES ('u1', 'u1@ex.com')`)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'A', 'a')`, wsID)

	v := `{"baseURL":"https://llm.example.com/v1","apiKey":"sk-tenant","headers":{"X-Env":"prod"}}`
	enc, err := encryption.Encrypt(v)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	mustExec(t, db, `
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, status, created_by)
		VALUES ('cr-auth', ?, 'ollama-auth', ?, 'ENDPOINT_URL', 'OLLAMA', 'ACTIVE', 'u1')`,
		wsID, enc)

	got := resolveLocalModelEndpoint(context.Background(), db, logger, wsID, nil)
	if got.BaseURL != "https://llm.example.com/v1" {
		t.Errorf("BaseURL = %q", got.BaseURL)
	}
	if got.APIKey != "sk-tenant" {
		t.Errorf("APIKey = %q", got.APIKey)
	}
	if got.Headers["X-Env"] != "prod" {
		t.Errorf("Headers = %v", got.Headers)
	}
}
