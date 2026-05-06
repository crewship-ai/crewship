// Tests for the `crewship connector` CLI subtree. Two flavors:
//
//   - Pure-local commands (validate, lint) parse YAML and call into
//     internal/connectors directly. Tested by calling the underlying
//     runValidateOne / runLintDir functions, not via cmd.Execute, so
//     parallel tests don't race on the shared cobra.Command's SetArgs.
//   - Command-tree shape (Use, Aliases, subcommand registration) is
//     verified by introspecting the command struct.
//
// API-bound paths (list, show, test) are exercised in integration via
// the existing /api/v1 test suite; covering them here would require a
// fresh httptest.Server per test plus the auth helper plumbing — out
// of proportion to the leaf-command logic, which is just JSON->table.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -------------------------------------------------------------------
// Command-tree shape
// -------------------------------------------------------------------

func TestConnectorCmdStructure(t *testing.T) {
	t.Parallel()

	if connectorCmd.Use != "connector" {
		t.Errorf("Use = %q, want connector", connectorCmd.Use)
	}
	want := map[string]bool{"cn": true, "connectors": true}
	for _, a := range connectorCmd.Aliases {
		delete(want, a)
	}
	if len(want) != 0 {
		t.Errorf("missing aliases: %v (have %v)", want, connectorCmd.Aliases)
	}
	if !strings.Contains(strings.ToLower(connectorCmd.Short), "connector") {
		t.Errorf("Short should mention connector; got %q", connectorCmd.Short)
	}

	have := map[string]bool{}
	for _, sub := range connectorCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, name := range []string{"list", "show", "validate", "lint", "test"} {
		if !have[name] {
			t.Errorf("connector missing subcommand %q; have %v", name, have)
		}
	}
}

func TestConnectorValidateFlags(t *testing.T) {
	t.Parallel()
	strict := connectorValidateCmd.Flags().Lookup("strict")
	if strict == nil {
		t.Fatal("connector validate missing --strict flag")
	}
	if strict.DefValue != "false" {
		t.Errorf("--strict default = %q, want false", strict.DefValue)
	}
	// validate <file> must accept exactly one positional arg.
	if connectorValidateCmd.Args == nil {
		t.Error("connector validate must declare Args validator")
	}
}

func TestConnectorLintFlags(t *testing.T) {
	t.Parallel()
	if connectorLintCmd.Flags().Lookup("strict") == nil {
		t.Error("connector lint missing --strict")
	}
	rec := connectorLintCmd.Flags().Lookup("recursive")
	if rec == nil {
		t.Fatal("connector lint missing --recursive")
	}
	if rec.DefValue != "false" {
		t.Errorf("--recursive default = %q, want false", rec.DefValue)
	}
}

func TestConnectorTestFlags(t *testing.T) {
	t.Parallel()
	if connectorTestCmd.Flags().Lookup("field") == nil {
		t.Error("connector test missing --field flag")
	}
}

// -------------------------------------------------------------------
// Local validate — runValidateOne is the pure function.
// -------------------------------------------------------------------

func TestRunValidateOne_GoodFixture_OK(t *testing.T) {
	t.Parallel()
	yml := `id: smoke
name: Smoke
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp:
  transport: streamable-http
  endpoint: https://example.com/mcp
`
	path := writeTempYAML(t, yml)
	if err := runValidateOne(path, false); err != nil {
		t.Errorf("good manifest must validate: %v", err)
	}
}

func TestRunValidateOne_NonexistentFile(t *testing.T) {
	t.Parallel()
	err := runValidateOne(filepath.Join(t.TempDir(), "missing.yaml"), false)
	if err == nil {
		t.Error("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error should mention read failure: %v", err)
	}
}

func TestRunValidateOne_BadAuthMode_Fails(t *testing.T) {
	t.Parallel()
	yml := `id: bad
name: Bad
auth_mode: not_a_thing
brand: {logo: x, color: "#000000"}
mcp:
  transport: streamable-http
  endpoint: https://x
`
	path := writeTempYAML(t, yml)
	if err := runValidateOne(path, false); err == nil {
		t.Error("bad manifest must fail validate")
	}
}

func TestRunValidateOne_StrictRejectsEmptyDescription(t *testing.T) {
	t.Parallel()
	yml := `id: nodescribed
name: NoDesc
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp:
  transport: streamable-http
  endpoint: https://x
`
	path := writeTempYAML(t, yml)
	// Default mode: passes (description is optional in Validate).
	if err := runValidateOne(path, false); err != nil {
		t.Errorf("non-strict must pass: %v", err)
	}
	// Strict mode: fails because description is empty.
	if err := runValidateOne(path, true); err == nil {
		t.Error("strict must reject empty description")
	}
}

func TestRunValidateOne_StrictRejectsEmptyBrandColor(t *testing.T) {
	t.Parallel()
	yml := `id: nocolor
name: NoColor
description: Test
auth_mode: none
brand: {logo: x, color: ""}
mcp:
  transport: streamable-http
  endpoint: https://x
`
	path := writeTempYAML(t, yml)
	if err := runValidateOne(path, false); err != nil {
		t.Errorf("non-strict must pass: %v", err)
	}
	if err := runValidateOne(path, true); err == nil {
		t.Error("strict must reject empty brand.color")
	}
}

// -------------------------------------------------------------------
// Local lint — runLintDir walks a dir.
// -------------------------------------------------------------------

func TestRunLintDir_AllPass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFileAt(t, filepath.Join(dir, "a.yaml"), `id: a
name: A
description: Test
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://x}
`)
	writeFileAt(t, filepath.Join(dir, "b.yaml"), `id: b
name: B
description: Test
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://y}
`)
	if err := runLintDir(dir, false, false); err != nil {
		t.Errorf("clean dir must lint clean: %v", err)
	}
}

func TestRunLintDir_OneBad_Fails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFileAt(t, filepath.Join(dir, "good.yaml"), `id: good
name: Good
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://x}
`)
	writeFileAt(t, filepath.Join(dir, "bad.yaml"), `id: bad
name: Bad
auth_mode: bogus_mode
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://x}
`)
	if err := runLintDir(dir, false, false); err == nil {
		t.Error("dir with one bad fixture must fail lint")
	}
}

func TestRunLintDir_IgnoresNonYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFileAt(t, filepath.Join(dir, "good.yaml"), `id: good
name: Good
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://x}
`)
	writeFileAt(t, filepath.Join(dir, "notes.md"), "# README — should be ignored\n")
	writeFileAt(t, filepath.Join(dir, ".DS_Store"), "\x00")
	if err := runLintDir(dir, false, false); err != nil {
		t.Errorf("non-yaml siblings must be ignored: %v", err)
	}
}

func TestRunLintDir_EmptyDirIsWarning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := runLintDir(dir, false, false); err != nil {
		t.Errorf("empty dir must not be hard error: %v", err)
	}
}

func TestRunLintDir_RecursiveFlag(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFileAt(t, filepath.Join(dir, "top.yaml"), `id: top
name: Top
auth_mode: none
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://x}
`)
	writeFileAt(t, filepath.Join(dir, "sub", "nested.yaml"), `id: nested
name: Nested
auth_mode: bogus_mode
brand: {logo: x, color: "#000000"}
mcp: {transport: streamable-http, endpoint: https://x}
`)
	// Without --recursive: only top.yaml seen, lint clean.
	if err := runLintDir(dir, false, false); err != nil {
		t.Errorf("flat lint should ignore subdir, got: %v", err)
	}
	// With --recursive: nested.yaml fails Validate, lint reports.
	if err := runLintDir(dir, false, true); err == nil {
		t.Error("recursive lint must surface nested bad fixture")
	}
}

func TestRunLintDir_RunsAgainstShippedFixtures(t *testing.T) {
	t.Parallel()
	// End-to-end: lint the actual fixtures shipped in
	// internal/connectors/fixtures. If any drifts from the schema
	// post-impl, this test catches it — without parsing the embed.
	dir := filepath.Join("..", "..", "internal", "connectors", "fixtures")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skip("fixtures dir not reachable from cmd/crewship working dir")
	}
	if err := runLintDir(dir, false, false); err != nil {
		t.Errorf("shipped fixtures must lint clean: %v", err)
	}
}

// -------------------------------------------------------------------
// splitKV — small helper, easy to break, easy to verify
// -------------------------------------------------------------------

func TestSplitKV(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    string
		k, v  string
		valid bool
	}{
		{"key=value", "key", "value", true},
		{"key=", "key", "", true},
		{"=value", "", "", false},
		{"keyvalue", "", "", false},
		{"a=b=c", "a", "b=c", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			k, v, ok := splitKV(tc.in)
			if ok != tc.valid {
				t.Errorf("ok = %v, want %v", ok, tc.valid)
			}
			if ok {
				if k != tc.k || v != tc.v {
					t.Errorf("got (%q, %q), want (%q, %q)", k, v, tc.k, tc.v)
				}
			}
		})
	}
}

// -------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------

func writeTempYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	writeFileAt(t, path, body)
	return path
}

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
