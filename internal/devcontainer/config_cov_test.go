package devcontainer

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeCommand_AllForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"empty string", "", nil},
		{"string", "echo hi", []string{"echo hi"}},
		{"string slice", []string{"a", "b"}, []string{"a", "b"}},
		{"any slice mixed", []any{"a", 42, "b", true}, []string{"a", "b"}},
		{"any slice empty", []any{}, []string{}},
		{
			"string map sorted by key",
			map[string]string{"z": "last", "a": "first", "m": "middle"},
			[]string{"first", "middle", "last"},
		},
		{
			"any map sorted, non-strings dropped",
			map[string]any{"b": "two", "a": "one", "c": 3},
			[]string{"one", "two"},
		},
		{"unknown type", 42, nil},
		{"unknown struct type", struct{ X int }{1}, nil},
	}
	for _, tt := range tests {
		got := NormalizeCommand(tt.in)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: NormalizeCommand(%#v) = %#v, want %#v", tt.name, tt.in, got, tt.want)
		}
	}
}

// covErrReader always fails, to exercise Parse's read-error branch.
type covErrReader struct{}

func (covErrReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestParse_ReadError(t *testing.T) {
	t.Parallel()

	_, err := Parse(covErrReader{})
	if err == nil {
		t.Fatal("expected error from failing reader")
	}
	if !strings.Contains(err.Error(), "reading input") {
		t.Errorf("error = %v, want mention of reading input", err)
	}
}

func TestParseBytes_ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantSub string
	}{
		{"invalid json", `not json at all`, "invalid JSON"},
		{"postCreateCommand bad type", `{"image":"x","postCreateCommand":42}`, "postCreateCommand must be string"},
		{"postStartCommand bad type", `{"image":"x","postStartCommand":42}`, "postStartCommand must be string"},
		{"postCreateCommand bad array", `{"image":"x","postCreateCommand":[1,2]}`, "postCreateCommand must be string"},
		{"validation failure no image", `{"postCreateCommand":"echo hi"}`, "image is required"},
	}
	for _, tt := range tests {
		_, err := ParseBytes([]byte(tt.input))
		if err == nil {
			t.Errorf("%s: expected error, got nil", tt.name)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: error = %v, want substring %q", tt.name, err, tt.wantSub)
		}
	}
}

func TestParseBytes_NullLifecycleCommandsIgnored(t *testing.T) {
	t.Parallel()

	cfg, err := ParseBytes([]byte(`{"image":"ubuntu:22.04","postCreateCommand":null,"postStartCommand":null}`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if cfg.PostCreateCommand != nil || cfg.PostStartCommand != nil {
		t.Errorf("null lifecycle commands should remain nil, got %v / %v",
			cfg.PostCreateCommand, cfg.PostStartCommand)
	}
}

func TestParsePolymorphicCommand_Forms(t *testing.T) {
	t.Parallel()

	// string form
	v, err := parsePolymorphicCommand(json.RawMessage(`"echo hi"`), "f")
	if err != nil || v != "echo hi" {
		t.Errorf("string form: got %v, %v", v, err)
	}
	// []string form
	v, err = parsePolymorphicCommand(json.RawMessage(`["a","b"]`), "f")
	if err != nil {
		t.Fatalf("array form: %v", err)
	}
	if arr, ok := v.([]string); !ok || len(arr) != 2 || arr[0] != "a" {
		t.Errorf("array form: got %#v", v)
	}
	// map form
	v, err = parsePolymorphicCommand(json.RawMessage(`{"k":"v"}`), "f")
	if err != nil {
		t.Fatalf("map form: %v", err)
	}
	if m, ok := v.(map[string]string); !ok || m["k"] != "v" {
		t.Errorf("map form: got %#v", v)
	}
	// invalid form
	_, err = parsePolymorphicCommand(json.RawMessage(`42`), "myField")
	if err == nil || !strings.Contains(err.Error(), "myField") {
		t.Errorf("invalid form: expected error naming the field, got %v", err)
	}
}

func TestIsCommonUtilsRef_UnversionedAndCaseVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref  string
		want bool
	}{
		{"ghcr.io/devcontainers/features/common-utils", true}, // unversioned
		{"GHCR.IO/devcontainers/features/common-utils:1", true},
		{"ghcr.io/devcontainers/features/common-utils@sha256:" + strings.Repeat("a", 64), true},
		{"ghcr.io/devcontainers/features/common-utils-extra:1", false},
		{"ghcr.io/other/features/common-utils:1", false},
	}
	for _, tt := range tests {
		if got := isCommonUtilsRef(tt.ref); got != tt.want {
			t.Errorf("isCommonUtilsRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

func TestMarshalJSON_IncludesRemoteUserAndPostStart(t *testing.T) {
	t.Parallel()

	c := &Config{
		Image:            "ubuntu:22.04",
		RemoteUser:       "agent",
		PostStartCommand: "service start",
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if m["remoteUser"] != "agent" {
		t.Errorf("remoteUser = %v, want agent", m["remoteUser"])
	}
	psc, ok := m["postStartCommand"].([]any)
	if !ok || len(psc) != 1 || psc[0] != "service start" {
		t.Errorf("postStartCommand = %#v, want normalized [service start]", m["postStartCommand"])
	}
}

func TestParseFeatureRef_DigestErrors(t *testing.T) {
	t.Parallel()

	hex64 := strings.Repeat("ab", 32)
	tests := []struct {
		name    string
		ref     string
		wantSub string
	}{
		{"non sha256 digest", "ghcr.io/x/y@md5:abcdef", "only sha256"},
		{"short hex", "ghcr.io/x/y@sha256:abc", "malformed sha256"},
		{"uppercase hex rejected", "ghcr.io/x/y@sha256:" + strings.Repeat("AB", 32), "malformed sha256"},
		{"missing registry in digest form", "noslash@sha256:" + hex64, "missing registry"},
		{"empty registry in digest form", "/repo@sha256:" + hex64, "empty registry"},
		{"empty repo in digest form", "reg.io/@sha256:" + hex64, "empty repo"},
		{"empty registry tag form", "/repo:1", "empty registry"},
		{"empty repo tag form", "reg.io/:1", "empty repo"},
	}
	for _, tt := range tests {
		_, _, _, _, err := ParseFeatureRef(tt.ref)
		if err == nil {
			t.Errorf("%s: expected error for %q", tt.name, tt.ref)
			continue
		}
		if !errors.Is(err, ErrInvalidFeatureRef) {
			t.Errorf("%s: error %v is not ErrInvalidFeatureRef", tt.name, err)
		}
		if !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: error = %v, want substring %q", tt.name, err, tt.wantSub)
		}
	}
}

func TestParseFeatureRef_DigestFormLowercasesRegistry(t *testing.T) {
	t.Parallel()

	hex64 := strings.Repeat("cd", 32)
	registry, repo, tag, digest, err := ParseFeatureRef("GHCR.io/Owner/Feature@sha256:" + hex64)
	if err != nil {
		t.Fatalf("ParseFeatureRef: %v", err)
	}
	if registry != "ghcr.io" {
		t.Errorf("registry = %q, want lowercased ghcr.io", registry)
	}
	if repo != "Owner/Feature" {
		t.Errorf("repo = %q, want case preserved", repo)
	}
	if tag != "" {
		t.Errorf("tag = %q, want empty for digest form", tag)
	}
	if digest != "sha256:"+hex64 {
		t.Errorf("digest = %q", digest)
	}
}
