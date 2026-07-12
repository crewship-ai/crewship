package main

import (
	"encoding/json"
	"testing"
)

// #961 Feature A — `credential create --type ENDPOINT_URL --auth-token/--header`
// folds the URL + auth into one JSON credential object.

func TestBuildEndpointCredentialValue(t *testing.T) {
	t.Run("no auth → bare URL", func(t *testing.T) {
		v, err := buildEndpointCredentialValue("http://h:11434/v1", "", nil)
		if err != nil || v != "http://h:11434/v1" {
			t.Fatalf("v=%q err=%v", v, err)
		}
	})

	t.Run("token + headers → JSON object", func(t *testing.T) {
		v, err := buildEndpointCredentialValue("https://h/v1", "sk-1", []string{"X-Tenant=acme", "X-Env = prod"})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		var obj struct {
			BaseURL string            `json:"baseURL"`
			APIKey  string            `json:"apiKey"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal([]byte(v), &obj); err != nil {
			t.Fatalf("not JSON: %v (%s)", err, v)
		}
		if obj.BaseURL != "https://h/v1" || obj.APIKey != "sk-1" {
			t.Errorf("baseURL=%q apiKey=%q", obj.BaseURL, obj.APIKey)
		}
		if obj.Headers["X-Tenant"] != "acme" || obj.Headers["X-Env"] != "prod" {
			t.Errorf("headers = %v", obj.Headers)
		}
	})

	t.Run("malformed header → error", func(t *testing.T) {
		if _, err := buildEndpointCredentialValue("http://h", "", []string{"noequals"}); err == nil {
			t.Error("expected error for KEY-only header")
		}
		if _, err := buildEndpointCredentialValue("http://h", "", []string{"=v"}); err == nil {
			t.Error("expected error for empty key")
		}
	})
}
