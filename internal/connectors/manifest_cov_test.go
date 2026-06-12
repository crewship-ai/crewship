// Internal-package tests (package connectors, not connectors_test) so
// the unexported placeholder helpers can be exercised directly where
// the exported surface can't reach a branch.
package connectors

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// validStdioManifest returns a minimal manifest that passes Validate
// for AuthModeNone with a stdio MCP block. Tests mutate it per case.
func validStdioManifest() *Manifest {
	return &Manifest{
		ID:       "demo",
		Name:     "Demo",
		AuthMode: AuthModeNone,
		MCP:      MCPConfig{Transport: "stdio", Command: "demo-mcp"},
	}
}

func TestValidate_NilManifest(t *testing.T) {
	t.Parallel()
	var m *Manifest
	if err := m.Validate(); !errors.Is(err, ErrManifestEmpty) {
		t.Fatalf("nil manifest: want ErrManifestEmpty, got %v", err)
	}
}

func TestValidate_ConnStringRequiresFields(t *testing.T) {
	t.Parallel()
	m := validStdioManifest()
	m.AuthMode = AuthModeConnString
	m.Fields = nil
	err := m.Validate()
	if !errors.Is(err, ErrManifestMissingField) {
		t.Fatalf("want ErrManifestMissingField, got %v", err)
	}
	if !strings.Contains(err.Error(), "conn_string") {
		t.Errorf("error should mention conn_string, got: %v", err)
	}
}

func TestValidate_DerivedRefToUndeclaredKeyIsNotACycle(t *testing.T) {
	t.Parallel()
	// "a" references a derived key that is never declared. Cycle
	// detection must skip the dangling edge (Resolve catches it at
	// materialize time), so Validate passes.
	m := validStdioManifest()
	m.Derived = map[string]string{"a": "x-${derived.missing}-y"}
	if err := m.Validate(); err != nil {
		t.Fatalf("dangling derived ref must not fail Validate, got %v", err)
	}
}

func TestExtractDerivedRefs_UnclosedPlaceholder(t *testing.T) {
	t.Parallel()
	// First ref parses, then an unclosed `${` ends the scan early.
	got := extractDerivedRefs("${derived.a} tail ${derived.b")
	want := []string{"a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractDerivedRefs = %v, want %v", got, want)
	}
}

func TestResolve_UnclosedPlaceholder(t *testing.T) {
	t.Parallel()
	m := validStdioManifest()
	_, err := m.Resolve("prefix ${field.host", ResolveContext{})
	if !errors.Is(err, ErrManifestPlaceholder) {
		t.Fatalf("want ErrManifestPlaceholder, got %v", err)
	}
	if !strings.Contains(err.Error(), "unclosed placeholder") {
		t.Errorf("error should mention unclosed placeholder, got: %v", err)
	}
}

func TestResolve_MalformedPlaceholder(t *testing.T) {
	t.Parallel()
	m := validStdioManifest()
	// Closing brace exists but the inner token starts with a digit, so
	// the placeholder grammar rejects it as malformed (distinct from
	// the unsupported-namespace case, which is grammatical).
	_, err := m.Resolve("${1bad}", ResolveContext{})
	if !errors.Is(err, ErrManifestPlaceholder) {
		t.Fatalf("want ErrManifestPlaceholder, got %v", err)
	}
	if !strings.Contains(err.Error(), `malformed "${1bad}"`) {
		t.Errorf("error should mention the malformed placeholder, got: %v", err)
	}
}

func TestResolve_UnsupportedNamespace(t *testing.T) {
	t.Parallel()
	m := validStdioManifest()
	for _, s := range []string{"${bogus.key}", "${field}", "${derived}"} {
		_, err := m.Resolve(s, ResolveContext{})
		if !errors.Is(err, ErrManifestPlaceholder) {
			t.Fatalf("Resolve(%q): want ErrManifestPlaceholder, got %v", s, err)
		}
		if !strings.Contains(err.Error(), "unsupported namespace") {
			t.Errorf("Resolve(%q): error should mention unsupported namespace, got: %v", s, err)
		}
	}
}

func TestResolve_UnknownDerivedKey(t *testing.T) {
	t.Parallel()
	m := validStdioManifest() // no Derived map at all
	_, err := m.Resolve("${derived.nope}", ResolveContext{})
	if !errors.Is(err, ErrManifestPlaceholder) {
		t.Fatalf("want ErrManifestPlaceholder, got %v", err)
	}
	if !strings.Contains(err.Error(), `unknown derived "nope"`) {
		t.Errorf("error should name the unknown derived key, got: %v", err)
	}
}

func TestResolve_CyclicDerivedAtResolveTime(t *testing.T) {
	t.Parallel()
	// Validate would reject this manifest, but Resolve must defend
	// itself too — a cyclic template surfaces ErrManifestPlaceholder
	// instead of a stack overflow.
	m := validStdioManifest()
	m.Derived = map[string]string{
		"a": "${derived.b}",
		"b": "${derived.a}",
	}
	_, err := m.Resolve("${derived.a}", ResolveContext{})
	if !errors.Is(err, ErrManifestPlaceholder) {
		t.Fatalf("want ErrManifestPlaceholder, got %v", err)
	}
	if !strings.Contains(err.Error(), "cyclic derived reference") {
		t.Errorf("error should mention cyclic derived reference, got: %v", err)
	}
}

func TestMaterializeMCP_DerivedReferencesUnknownKey(t *testing.T) {
	t.Parallel()
	m := validStdioManifest()
	m.Derived = map[string]string{"conn": "${derived.missing}"}
	_, err := m.MaterializeMCP(nil, "https://crew.example")
	if !errors.Is(err, ErrManifestPlaceholder) {
		t.Fatalf("want ErrManifestPlaceholder, got %v", err)
	}
	// The failure must be attributed to the derived key that failed.
	if !strings.Contains(err.Error(), `derived "conn"`) {
		t.Errorf("error should name the failing derived key, got: %v", err)
	}
}

func TestMaterializeMCP_PlaceholderErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(m *Manifest)
	}{
		{"command", func(m *Manifest) {
			m.MCP.Command = "run-${field.unknown}"
		}},
		{"endpoint", func(m *Manifest) {
			m.MCP = MCPConfig{Transport: "streamable-http", Endpoint: "https://x/${field.unknown}"}
		}},
		{"args", func(m *Manifest) {
			m.MCP.Args = []string{"--token=${field.unknown}"}
		}},
		{"env", func(m *Manifest) {
			m.MCP.Env = map[string]string{"TOKEN": "${field.unknown}"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := validStdioManifest()
			tc.mutate(m)
			_, err := m.MaterializeMCP(nil, "")
			if !errors.Is(err, ErrManifestPlaceholder) {
				t.Fatalf("want ErrManifestPlaceholder, got %v", err)
			}
			if !strings.Contains(err.Error(), `unknown field "unknown"`) {
				t.Errorf("error should name the unknown field, got: %v", err)
			}
		})
	}
}

func TestMaterializeMCP_DerivedChainResolvesThroughFields(t *testing.T) {
	t.Parallel()
	// Happy-path companion to the error tests above: a derived chain
	// (url → host) that feeds env + args resolves end-to-end.
	m := &Manifest{
		ID:       "db",
		Name:     "DB",
		AuthMode: AuthModeConnString,
		Fields: []Field{
			{Key: "host", Type: FieldTypeText, Required: true},
			{Key: "port", Type: FieldTypeNumber, Default: "5432"},
		},
		Derived: map[string]string{
			"hostport": "${field.host}:${field.port}",
			"url":      "postgres://${derived.hostport}/app",
		},
		MCP: MCPConfig{
			Transport: "stdio",
			Command:   "db-mcp",
			Args:      []string{"--url", "${derived.url}"},
			Env:       map[string]string{"DB_URL": "${derived.url}", "CB": "${instance_url}/cb"},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("fixture must validate: %v", err)
	}
	out, err := m.MaterializeMCP(map[string]string{"host": "db.internal"}, "https://crew.example")
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	wantURL := "postgres://db.internal:5432/app"
	if got := out.Args[1]; got != wantURL {
		t.Errorf("args[1] = %q, want %q", got, wantURL)
	}
	if got := out.Env["DB_URL"]; got != wantURL {
		t.Errorf("env DB_URL = %q, want %q", got, wantURL)
	}
	if got := out.Env["CB"]; got != "https://crew.example/cb" {
		t.Errorf("env CB = %q, want instance_url substitution", got)
	}
}
