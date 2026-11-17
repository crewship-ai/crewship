package bedrock

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAccountPolicyAndAtomicDocument(t *testing.T) {
	good := Account{AuthMethod: "bedrock_api_key", APIKey: "synthetic-key", Region: "eu-central-1", Models: []string{"eu.anthropic.claude-opus-5-5"}, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), EUOnly: true}
	for _, mutate := range []func(*Account){nil, func(a *Account) { a.Region = "evil.example/?x=" }, func(a *Account) { a.Models = []string{"global.anthropic.haiku"} }, func(a *Account) { a.Models = []string{"eu.allowed/../../admin"} }, func(a *Account) { a.APIKey = "secret\r\ninjected" }, func(a *Account) { a.AuthMethod = "aws_root" }, func(a *Account) { a.ExpiresAt = "invalid" }} {
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

func TestEndpointSelectsOnlyFixedRuntimeDestinations(t *testing.T) {
	for _, region := range []string{"eu-central-1", "eu-central-2", "us-east-1", "ap-northeast-3"} {
		a := Account{Region: region}
		if a.Endpoint() != "https://bedrock-runtime."+region+".amazonaws.com" {
			t.Fatalf("missing supported runtime %s", region)
		}
	}
	for _, region := range []string{"eu-northwest-99", "127.0.0.1", "eu-central-1.amazonaws.com@evil.example", "eu-central-1/path", "us-gov-east-1", ""} {
		if (Account{Region: region}).Endpoint() != "" {
			t.Fatalf("unapproved runtime %s", region)
		}
	}
}

func TestAccountOptionalExpiry(t *testing.T) {
	a := Account{AuthMethod: "bedrock_api_key", APIKey: "synthetic-key", Region: "eu-central-1", Models: []string{"eu.anthropic.claude-opus-5-5"}}
	raw, _ := json.Marshal(a)
	got, err := Parse(string(raw))
	if err != nil || got.Expired(time.Now().Add(365*24*time.Hour)) {
		t.Fatal("omitted expiry rejected", err)
	}
	a.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if !a.Expired(time.Now()) {
		t.Fatal("expired key accepted")
	}
}
