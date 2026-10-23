package mooring

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestFingerprintSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../mooring/protocol/fingerprint-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Atomic           json.RawMessage `json:"atomic_input"`
		Canonical        string          `json:"atomic_canonical"`
		Membership       string          `json:"membership_hash"`
		Fingerprint      string          `json:"operation_fingerprint"`
		Canonicalization []struct {
			ID        string `json:"id"`
			Input     string `json:"input_json"`
			Canonical string `json:"canonical_json"`
			Hash      string `json:"sha256"`
		} `json:"canonicalization_vectors"`
		Adapters []struct {
			ID        string `json:"id"`
			Vector    string `json:"canonicalization_vector"`
			Canonical string `json:"adapter_input_canonical"`
		} `json:"adapter_semantics_vectors"`
		Checkpoints []struct {
			ID        string          `json:"id"`
			Preimage  json.RawMessage `json:"preimage"`
			Canonical string          `json:"canonical_json"`
			Digest    string          `json:"digest"`
		} `json:"checkpoint_vectors"`
		Invalid []struct {
			ID    string `json:"id"`
			Input string `json:"input_json"`
		} `json:"invalid_inputs"`
		Memberships []struct {
			ID        string          `json:"id"`
			Input     json.RawMessage `json:"input"`
			Canonical string          `json:"canonical_json"`
			Hash      string          `json:"membership_hash"`
		} `json:"membership_vectors"`
		Retries []struct {
			ID         string          `json:"id"`
			Input      json.RawMessage `json:"atomic_input"`
			Membership string          `json:"membership_hash"`
			Operations []struct {
				ID          string `json:"op_id"`
				Fingerprint string `json:"fingerprint"`
			} `json:"operation_fingerprints"`
		} `json:"retry_vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	result, err := FingerprintAtomicJSON(fixture.Atomic)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.CanonicalInput) != fixture.Canonical || result.MembershipHash != fixture.Membership || result.Operations["o1"] != fixture.Fingerprint {
		t.Fatal("atomic vector mismatch")
	}
	for _, v := range fixture.Canonicalization {
		t.Run(v.ID, func(t *testing.T) {
			normalized, err := CanonicalJSON([]byte(v.Input))
			if err != nil {
				t.Fatal(err)
			}
			if string(normalized) != v.Canonical || canonicalDigest(normalized) != v.Hash {
				t.Fatalf("canonical vector mismatch: %s", v.ID)
			}
		})
	}
	for _, v := range fixture.Checkpoints {
		t.Run(v.ID, func(t *testing.T) {
			normalized, err := CanonicalJSON(v.Preimage)
			if err != nil {
				t.Fatal(err)
			}
			if string(normalized) != v.Canonical || canonicalDigest(normalized) != v.Digest {
				t.Fatal("checkpoint preimage mismatch")
			}
		})
	}
	for _, v := range fixture.Adapters {
		t.Run(v.ID, func(t *testing.T) {
			var input string
			for _, source := range fixture.Canonicalization {
				if source.ID == v.Vector {
					input = source.Input
				}
			}
			if input == "" {
				t.Fatal("adapter fixture references missing canonicalization vector")
			}
			normalized, err := CanonicalJSON([]byte(input))
			if err != nil {
				t.Fatal(err)
			}
			// The adapter boundary observes normalized bytes, then decodes exact
			// uint64 or string semantics. It never receives the original JSON.
			observed, err := exactAdapterValue(normalized)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := exactAdapterValue([]byte(v.Canonical))
			if err != nil {
				t.Fatal(err)
			}
			if string(normalized) != v.Canonical || observed != expected {
				t.Fatalf("adapter semantic mismatch: %s", v.ID)
			}
		})
	}
	for _, v := range fixture.Invalid {
		t.Run(v.ID, func(t *testing.T) {
			if _, err := CanonicalJSON([]byte(v.Input)); !errors.Is(err, ErrCanonicalInput) {
				t.Fatalf("want rejection, got %v", err)
			}
		})
	}
	for _, v := range fixture.Memberships {
		t.Run(v.ID, func(t *testing.T) {
			result, err := FingerprintAtomicJSON(v.Input)
			if err != nil {
				t.Fatal(err)
			}
			if string(result.CanonicalInput) != v.Canonical || result.MembershipHash != v.Hash {
				t.Fatal("membership mismatch")
			}
		})
	}
	for _, v := range fixture.Retries {
		t.Run(v.ID, func(t *testing.T) {
			result, err := FingerprintAtomicJSON(v.Input)
			if err != nil {
				t.Fatal(err)
			}
			if result.MembershipHash != v.Membership {
				t.Fatal("retry changed membership")
			}
			for _, op := range v.Operations {
				if result.Operations[op.ID] != op.Fingerprint {
					t.Fatal("retry fingerprint mismatch")
				}
			}
		})
	}
}

func TestCanonicalAdmissionBounds(t *testing.T) {
	for name, input := range map[string][]byte{
		"wide-array":        []byte("[" + strings.Repeat("0,", MaxCanonicalStructure) + "0]"),
		"raw-size":          bytes.Repeat([]byte(" "), MaxCanonicalBytes+1),
		"depth":             []byte(strings.Repeat("[", MaxCanonicalDepth+1) + "0" + strings.Repeat("]", MaxCanonicalDepth+1)),
		"invalid-utf8":      {'"', 0xff, '"'},
		"escaped-duplicate": []byte(`{"x":1,"\u0078":2}`),
		"trailing":          []byte(`{} {}`),
		"truncated":         []byte(`{"a":`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalJSON(input); !errors.Is(err, ErrCanonicalInput) {
				t.Fatalf("want rejection got %v", err)
			}
		})
	}
	// Braces, escaped quotes and backslashes inside strings are not nesting.
	if _, err := CanonicalJSON([]byte(`{"text":"[[[\"\\"}`)); err != nil {
		t.Fatal(err)
	}
	atLimit := []byte(strings.Repeat("[", MaxCanonicalDepth) + "0" + strings.Repeat("]", MaxCanonicalDepth))
	if _, err := CanonicalJSON(atLimit); err != nil {
		t.Fatal(err)
	}
	ops := strings.Repeat(`{"op_id":"x"},`, MaxAtomicOperations) + `{"op_id":"last"}`
	if _, err := FingerprintAtomicJSON([]byte(`{"domain":"mooring.atomic.v0.4","operations_without_fingerprints":[` + ops + `]}`)); !errors.Is(err, ErrCanonicalInput) {
		t.Fatal("unbounded operations accepted")
	}
}

func TestNormalizedBytesAreAdapterSemantics(t *testing.T) {
	normalized, err := CanonicalJSON([]byte(`{"payload":{"value":9007199254740993,"exact":"9007199254740993"}}`))
	if err != nil {
		t.Fatal(err)
	}
	// An exact-u64 decoder consumes the returned bytes, never the raw preimage.
	var adapter struct {
		Payload struct {
			Value uint64 `json:"value"`
			Exact string `json:"exact"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(normalized, &adapter); err != nil {
		t.Fatal(err)
	}
	if adapter.Payload.Value != 9007199254740992 || adapter.Payload.Exact != "9007199254740993" {
		t.Fatal("adapter and hash disagree")
	}
}

func TestAtomicFingerprintAdmission(t *testing.T) {
	for name, input := range map[string]string{
		"empty":              `{"domain":"mooring.atomic.v0.4","operations_without_fingerprints":[]}`,
		"wrong-domain":       `{"domain":"mooring.operation.v0.4","operations_without_fingerprints":[{"op_id":"x"}]}`,
		"duplicate-id":       `{"domain":"mooring.atomic.v0.4","operations_without_fingerprints":[{"op_id":"x"},{"op_id":"x"}]}`,
		"empty-id":           `{"domain":"mooring.atomic.v0.4","operations_without_fingerprints":[{"op_id":""}]}`,
		"duplicate-property": `{"domain":"mooring.atomic.v0.4","operations_without_fingerprints":[{"op_id":"x","op_id":"y"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := FingerprintAtomicJSON([]byte(input)); !errors.Is(err, ErrCanonicalInput) {
				t.Fatalf("want rejection got %v", err)
			}
		})
	}
}

// exactAdapterValue models an application's exact integer/string decoder at
// the boundary. Its result preserves the JSON kind and exact observed value.
func exactAdapterValue(raw []byte) (string, error) {
	var envelope struct {
		Payload struct {
			Value json.RawMessage `json:"value"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", err
	}
	if len(envelope.Payload.Value) > 0 && envelope.Payload.Value[0] == '"' {
		var value string
		if err := json.Unmarshal(envelope.Payload.Value, &value); err != nil {
			return "", err
		}
		return "string:" + value, nil
	}
	var value uint64
	if err := json.Unmarshal(envelope.Payload.Value, &value); err != nil {
		return "", err
	}
	return fmt.Sprintf("uint64:%d", value), nil
}
