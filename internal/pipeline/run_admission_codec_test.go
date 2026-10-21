package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Structural codec fixtures only: no SQL, historical receipt, real admission,
// account, ceiling validation, permission or runtime execution is established.
func admissionCodecFixture(kind uint8) runAdmissionV1 {
	n := func(s string) admissionNativeBytes { return admissionNativeBytes{Kind: 1, Bytes: []byte(s)} }
	d := runAdmissionDataV1{
		ID: []byte("receipt"), RunID: []byte("run"), AdmissionEpoch: []byte("epoch"),
		Selection: runAdmissionSelectionV1{Kind: 1, WorkspaceID: []byte("workspace"), PipelineID: []byte("pipeline"),
			Archive: &runAdmissionArchiveV1{VersionID: []byte("archive"), Version: 1, AuthoredDefinition: []byte("authored raw\n"), AuthorityFingerprint: []byte("legacy-trusted:v1"), ArchivedOrigin: n("archived-origin")}},
		Bindings:  runAdmissionBindingsV1{Source: 1, RootCrew: n("crew"), DeclaredAuthorAgent: n("declared"), ActingAgent: n("acting"), Tier: n("tier")},
		Principal: runAdmissionPrincipalV1{Kind: 1, User: n("human"), Crew: n("caller-crew"), Agent: n("caller-agent"), InvocationPolicy: []byte("policy")},
		Source: runAdmissionSourceV1{TriggeredVia: n("manual"), TriggeredByID: n("trigger"), InvocationOrigin: n("invocation-origin"), AdmissionCurrentOrigin: n("current-origin"),
			InvocationSourceRunID: n("source-run"), ChainOrigin: n("chain"), ChainDepth: 1, ParentAdmissionID: []byte("parent"), ParentExecutionPath: []byte("/parent"), CallsiteStepID: []byte("call")},
		EffectiveDefinition: []byte("effective raw\n"), AdmissionInputsJSON: []byte("{}"), AdmissionMetadataJSON: []byte("{}"),
	}
	if kind == 2 {
		d.Selection.Archive.AuthorityFingerprint = []byte(strings.Repeat("b", 64))
		d.Bindings.Source, d.Bindings.StoredCeilingID = 2, []byte("stored-ceiling")
		d.Bindings.Tier = admissionNativeBytes{}
	}
	if kind == 3 {
		d.Selection.Kind, d.Selection.Archive = 2, nil
		d.Selection.Head0 = &runAdmissionHead0V1{CutoverReceiptID: []byte("existing-cutover"), AuthoredDefinition: []byte("head0 raw\n"), Crew: n("old-crew"), DeclaredAgent: n("old-declared"), Tier: n("old-tier"), Origin: n("old-origin")}
	}
	v := runAdmissionV1{data: d}
	admissionCodecRehash(&v)
	return v
}

func admissionCodecRehash(v *runAdmissionV1) {
	hash := func(raw []byte) []byte { s := sha256.Sum256(raw); return append([]byte{}, s[:]...) }
	v.data.EffectiveDefinitionHash = hash(v.data.EffectiveDefinition)
	if a := v.data.Selection.Archive; a != nil {
		a.DefinitionHash = hash(a.AuthoredDefinition)
	}
	if h := v.data.Selection.Head0; h != nil {
		h.DefinitionHash = hash(h.AuthoredDefinition)
	}
}

func admissionCodecWire(t *testing.T, v runAdmissionV1) []byte {
	t.Helper()
	raw, err := json.Marshal(runAdmissionWireV1{Namespace: runAdmissionNamespaceV1, Format: 1, Data: v.data})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRunAdmissionCodecExactRoundTrip(t *testing.T) {
	for _, kind := range []uint8{1, 2, 3} {
		v := admissionCodecFixture(kind)
		raw, err := encodeRunAdmissionV1(v, 1<<20)
		if err != nil || !bytes.Equal(raw, admissionCodecWire(t, v)) {
			t.Fatalf("encode structural fixture %d: %v", kind, err)
		}
		got, err := decodeRunAdmissionV1(raw, len(raw))
		if err != nil || !reflect.DeepEqual(got, v) {
			t.Fatalf("exact decode %d: %v", kind, err)
		}
		private, err := json.Marshal(map[string]any{"receipt": v})
		if err != nil || string(private) != `{"receipt":{}}` {
			t.Fatalf("private receipt leaked: %s / %v", private, err)
		}
	}
}

func TestRunAdmissionCodecNativeNullTextBlob(t *testing.T) {
	values := []admissionNativeBytes{{}, {Kind: 1, Bytes: make([]byte, 0)}, {Kind: 2, Bytes: make([]byte, 0)}, {Kind: 1, Bytes: []byte("same")}, {Kind: 2, Bytes: []byte("same")}}
	seen := map[string]bool{}
	for _, native := range values {
		v := admissionCodecFixture(1)
		v.data.Bindings.Tier = native
		raw, err := encodeRunAdmissionV1(v, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if seen[string(raw)] {
			t.Fatal("native SQL kinds collapsed")
		}
		seen[string(raw)] = true
		got, err := decodeRunAdmissionV1(raw, len(raw))
		if err != nil || !reflect.DeepEqual(got.data.Bindings.Tier, native) {
			t.Fatalf("native value lost: %#v / %v", got.data.Bindings.Tier, err)
		}
	}
	v := admissionCodecFixture(1)
	v.data.Principal.Kind, v.data.Principal.User = 2, admissionNativeBytes{}
	v.data.Source.ParentAdmissionID, v.data.Source.ParentExecutionPath, v.data.Source.CallsiteStepID = nil, nil, nil
	raw, err := encodeRunAdmissionV1(v, 1<<20)
	if err != nil {
		t.Fatalf("explicit native-NULL nonhuman root shape: %v", err)
	}
	got, err := decodeRunAdmissionV1(raw, len(raw))
	if err != nil || !reflect.DeepEqual(got, v) || got.data.Principal.User.Kind != 0 || got.data.Principal.User.Bytes != nil {
		t.Fatalf("native-NULL nonhuman roundtrip changed: %v", err)
	}
}

func admissionCodecBytePaths(v reflect.Value, prefix string) []string {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return admissionCodecBytePaths(v.Elem(), prefix)
	}
	if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
		if !v.IsNil() && !strings.HasSuffix(prefix, "Hash") && !strings.HasSuffix(prefix, "Fingerprint") {
			return []string{prefix}
		}
		return nil
	}
	var paths []string
	if v.Kind() == reflect.Struct {
		for i := 0; i < v.NumField(); i++ {
			name := v.Type().Field(i).Name
			if prefix != "" {
				name = prefix + "." + name
			}
			paths = append(paths, admissionCodecBytePaths(v.Field(i), name)...)
		}
	}
	return paths
}

func admissionCodecSetBytes(v *runAdmissionV1, path string, raw []byte) {
	x := reflect.ValueOf(&v.data).Elem()
	for _, part := range strings.Split(path, ".") {
		if x.Kind() == reflect.Pointer {
			x = x.Elem()
		}
		x = x.FieldByName(part)
	}
	x.SetBytes(raw)
}

func TestRunAdmissionCodecEveryOpaqueByteFieldAvoidsUTF8Collision(t *testing.T) {
	for _, kind := range []uint8{1, 2, 3} {
		for _, path := range admissionCodecBytePaths(reflect.ValueOf(admissionCodecFixture(kind).data), "") {
			t.Run(string(rune('0'+kind))+"/"+path, func(t *testing.T) {
				var encoded [][]byte
				for _, b := range []byte{0xff, 0xfe} {
					v := admissionCodecFixture(kind)
					admissionCodecSetBytes(&v, path, []byte{b})
					admissionCodecRehash(&v)
					raw, err := encodeRunAdmissionV1(v, 1<<20)
					if err != nil {
						t.Fatalf("opaque-byte encode: %v", err)
					}
					got, err := decodeRunAdmissionV1(raw, len(raw))
					if err != nil || !reflect.DeepEqual(got, v) {
						t.Fatalf("opaque-byte binding lost: %v", err)
					}
					encoded = append(encoded, raw)
				}
				if bytes.Equal(encoded[0], encoded[1]) {
					t.Fatal("invalid UTF-8 values normalized together")
				}
			})
		}
	}
}

func TestRunAdmissionCodecBoundsAndOwnedBytes(t *testing.T) {
	v := admissionCodecFixture(1)
	raw := admissionCodecWire(t, v)
	for _, bound := range []int{-1, 0, 1, len(raw) - 1} {
		out, err := encodeRunAdmissionV1(v, bound)
		if out != nil || !errors.Is(err, errRunAdmissionUnavailable) {
			t.Fatalf("encode bound %d: %v", bound, err)
		}
		got, err := decodeRunAdmissionV1(raw, bound)
		if !reflect.DeepEqual(got, runAdmissionV1{}) || !errors.Is(err, errRunAdmissionUnavailable) {
			t.Fatalf("decode bound %d: %v", bound, err)
		}
	}
	encoded, err := encodeRunAdmissionV1(v, len(raw))
	if err != nil {
		t.Fatal(err)
	}
	v.data.ID[0] = 'X'
	got, err := decodeRunAdmissionV1(encoded, len(encoded))
	if err != nil || string(got.data.ID) != "receipt" {
		t.Fatalf("encoding aliased input: %v", err)
	}
	for i := range encoded {
		encoded[i] = 0
	}
	if string(got.data.ID) != "receipt" {
		t.Fatal("decoder retained caller wire buffer")
	}
}

func TestRunAdmissionCodecMalformedValuesZeroResult(t *testing.T) {
	cases := []struct {
		name   string
		change func(*runAdmissionV1)
	}{
		{"missing-id", func(v *runAdmissionV1) { v.data.ID = nil }},
		{"unknown-selection", func(v *runAdmissionV1) { v.data.Selection.Kind = 9 }},
		{"both-selection-forms", func(v *runAdmissionV1) { v.data.Selection.Head0 = admissionCodecFixture(3).data.Selection.Head0 }},
		{"missing-archive", func(v *runAdmissionV1) { v.data.Selection.Archive = nil }},
		{"zero-version", func(v *runAdmissionV1) { v.data.Selection.Archive.Version = 0 }},
		{"authored-hash", func(v *runAdmissionV1) { v.data.Selection.Archive.DefinitionHash[0] ^= 1 }},
		{"effective-hash", func(v *runAdmissionV1) { v.data.EffectiveDefinitionHash[0] ^= 1 }},
		{"unknown-fingerprint", func(v *runAdmissionV1) { v.data.Selection.Archive.AuthorityFingerprint = []byte("legacy-trusted:v2") }},
		{"NUL-fingerprint", func(v *runAdmissionV1) {
			v.data.Selection.Archive.AuthorityFingerprint = append([]byte(strings.Repeat("b", 64)), 0)
		}},
		{"upper-fingerprint", func(v *runAdmissionV1) {
			v.data.Selection.Archive.AuthorityFingerprint = []byte(strings.Repeat("B", 64))
		}},
		{"source-mismatch", func(v *runAdmissionV1) { v.data.Bindings.Source = 2 }},
		{"null-native-with-bytes", func(v *runAdmissionV1) { v.data.Bindings.Tier = admissionNativeBytes{Bytes: make([]byte, 0)} }},
		{"text-native-with-nil", func(v *runAdmissionV1) { v.data.Bindings.Tier = admissionNativeBytes{Kind: 1} }},
		{"unknown-native", func(v *runAdmissionV1) { v.data.Bindings.Tier.Kind = 3 }},
		{"erased-human", func(v *runAdmissionV1) { v.data.Principal.User = admissionNativeBytes{} }},
		{"unknown-principal", func(v *runAdmissionV1) { v.data.Principal.Kind = 0 }},
		{"nonhuman-empty-TEXT", func(v *runAdmissionV1) {
			v.data.Principal.Kind = 2
			v.data.Principal.User = admissionNativeBytes{Kind: 1, Bytes: make([]byte, 0)}
		}},
		{"nonhuman-empty-BLOB", func(v *runAdmissionV1) {
			v.data.Principal.Kind = 2
			v.data.Principal.User = admissionNativeBytes{Kind: 2, Bytes: make([]byte, 0)}
		}},
		{"negative-depth", func(v *runAdmissionV1) { v.data.Source.ChainDepth = -1 }},
		{"partial-parent", func(v *runAdmissionV1) { v.data.Source.CallsiteStepID = nil }},
		{"missing-input-snapshot", func(v *runAdmissionV1) { v.data.AdmissionInputsJSON = nil }},
		{"missing-epoch", func(v *runAdmissionV1) { v.data.AdmissionEpoch = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := admissionCodecFixture(1)
			tc.change(&v)
			raw, err := encodeRunAdmissionV1(v, 1<<20)
			if raw != nil || !errors.Is(err, errRunAdmissionUnavailable) {
				t.Fatalf("invalid encode accepted: %v", err)
			}
			got, err := decodeRunAdmissionV1(admissionCodecWire(t, v), 1<<20)
			if !reflect.DeepEqual(got, runAdmissionV1{}) || !errors.Is(err, errRunAdmissionUnavailable) {
				t.Fatalf("invalid decode returned partial value: %v", err)
			}
		})
	}
}

func TestRunAdmissionCodecRejectsNoncanonicalEnvelope(t *testing.T) {
	raw := admissionCodecWire(t, admissionCodecFixture(1))
	cases := map[string][]byte{
		"namespace":          bytes.Replace(raw, []byte(runAdmissionNamespaceV1), []byte("other"), 1),
		"version":            bytes.Replace(raw, []byte(`"format":1`), []byte(`"format":2`), 1),
		"unknown-field":      append([]byte(`{"secret-input":"must-not-leak",`), raw[1:]...),
		"duplicate-field":    append([]byte(`{"format":1,`), raw[1:]...),
		"trailing-value":     append(append([]byte{}, raw...), []byte(`{}`)...),
		"trailing-space":     append(append([]byte{}, raw...), ' '),
		"missing-field":      bytes.Replace(raw, []byte(`"format":1,`), nil, 1),
		"bad-base64":         bytes.Replace(raw, []byte(`"id":"cmVjZWlwdA=="`), []byte(`"id":"!"`), 1),
		"byte-array-alias":   bytes.Replace(raw, []byte(`"id":"cmVjZWlwdA=="`), []byte(`"id":[114,101,99,101,105,112,116]`), 1),
		"fractional-version": bytes.Replace(raw, []byte(`"format":1`), []byte(`"format":1.0`), 1),
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(invalid, raw) {
				t.Fatal("invalid fixture did not change envelope")
			}
			got, err := decodeRunAdmissionV1(invalid, 1<<20)
			if !reflect.DeepEqual(got, runAdmissionV1{}) || !errors.Is(err, errRunAdmissionUnavailable) || err.Error() != "routine run admission unavailable" {
				t.Fatalf("noncanonical input accepted or leaked: %v", err)
			}
		})
	}
}

func TestRunAdmissionCodecSelectionCoherence(t *testing.T) {
	tests := []struct {
		name   string
		kind   uint8
		change func(*runAdmissionV1)
	}{
		{"head0-number", 3, func(v *runAdmissionV1) { v.data.Selection.Head0.HeadVersion = 1 }},
		{"head0-witness", 3, func(v *runAdmissionV1) { v.data.Selection.Head0.CutoverReceiptID = nil }},
		{"head0-hash", 3, func(v *runAdmissionV1) { v.data.Selection.Head0.DefinitionHash[0] ^= 1 }},
		{"head0-source", 3, func(v *runAdmissionV1) { v.data.Bindings.Source = 2 }},
		{"bounded-ceiling", 2, func(v *runAdmissionV1) { v.data.Bindings.StoredCeilingID = nil }},
		{"bounded-acting", 2, func(v *runAdmissionV1) { v.data.Bindings.ActingAgent = admissionNativeBytes{} }},
		{"legacy-ceiling", 1, func(v *runAdmissionV1) { v.data.Bindings.StoredCeilingID = []byte("not-legacy") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := admissionCodecFixture(tc.kind)
			tc.change(&v)
			raw, err := encodeRunAdmissionV1(v, 1<<20)
			if raw != nil || !errors.Is(err, errRunAdmissionUnavailable) {
				t.Fatalf("incoherent encode: %v", err)
			}
			got, err := decodeRunAdmissionV1(admissionCodecWire(t, v), 1<<20)
			if !reflect.DeepEqual(got, runAdmissionV1{}) || !errors.Is(err, errRunAdmissionUnavailable) {
				t.Fatalf("incoherent decode returned partial value: %v", err)
			}
		})
	}
	v := admissionCodecFixture(2)
	v.data.Bindings.DeclaredAuthorAgent = admissionNativeBytes{}
	raw, err := encodeRunAdmissionV1(v, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeRunAdmissionV1(raw, len(raw))
	if err != nil || !reflect.DeepEqual(got, v) || len(got.data.Bindings.DeclaredAuthorAgent.Bytes) != 0 || string(got.data.Bindings.ActingAgent.Bytes) != "acting" {
		t.Fatalf("optional declared author replaced by acting deputy: %v", err)
	}
}
