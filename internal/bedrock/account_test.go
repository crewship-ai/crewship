package bedrock

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAccountPolicyAndAtomicDocument(t *testing.T) {
	good := Account{AuthMethod: "bedrock_api_key", APIKey: "synthetic-key", Region: "eu-central-1", Models: []string{"eu.anthropic.claude-opus-5-5"}, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), EUOnly: true}
	for _, mutate := range []func(*Account){nil, func(a *Account) { a.Region = "evil.example/?x=" }, func(a *Account) { a.Models = []string{"global.anthropic.haiku"} }, func(a *Account) { a.Models = []string{"eu.allowed/../../admin"} }, func(a *Account) { a.APIKey = "secret\r\ninjected" }, func(a *Account) { a.AuthMethod = "aws_root" }, func(a *Account) { a.ExpiresAt = "" }} {
		a := good
		if mutate != nil {
			mutate(&a)
		}
		raw, _ := json.Marshal(a)
		parsed, err := Parse(string(raw))
		if mutate == nil {
			if err != nil || parsed.Endpoint() != "https://bedrock-runtime.eu-central-1.amazonaws.com" || !parsed.Allows(good.Models[0]) || parsed.Expired(time.Now()) {
				t.Fatalf("valid account rejected: %v", err)
			}
		} else if err == nil {
			t.Fatal("invalid account accepted")
		}
	}
}
