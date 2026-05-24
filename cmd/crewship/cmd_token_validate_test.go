package main

import (
	"testing"
)

// TestTokenValidateFlag_JSON guards the flag wiring on
// 'crewship token validate'. A refactor that drops the flag would
// silently break every CI script that parses validate output.
func TestTokenValidateFlag_JSON(t *testing.T) {
	t.Parallel()
	f := tokenValidateCmd.Flags().Lookup("json")
	if f == nil {
		t.Fatal("crewship token validate missing --json flag")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--json type = %s, want bool", f.Value.Type())
	}
	if f.DefValue != "false" {
		t.Errorf("--json default = %s, want false (human output is the default)", f.DefValue)
	}
}

// TestTokenValidateLong_DocumentsExitCodes pins the documented exit
// code contract to the command's Long description so a refactor
// that changes one without the other fails this test. CI consumers
// rely on exit codes more than they rely on the JSON shape.
func TestTokenValidateLong_DocumentsExitCodes(t *testing.T) {
	t.Parallel()
	long := tokenValidateCmd.Long
	for _, mustContain := range []string{
		"Exit codes",
		"0  token is valid",
		"1  token is invalid",
		"2  network or server error",
		"--json",
	} {
		if !contains(long, mustContain) {
			t.Errorf("token validate Long missing %q — contract drift", mustContain)
		}
	}
}

// contains is a tiny strings.Contains-equivalent so this file has no
// extra imports — keeps the test focused on the flag wiring rather
// than on auxiliary string utilities.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
