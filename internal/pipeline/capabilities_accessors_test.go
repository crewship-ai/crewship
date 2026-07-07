package pipeline

import (
	"reflect"
	"testing"
)

func TestWiredAndReservedCodeRuntimes(t *testing.T) {
	if got := WiredCodeRuntimes(); !reflect.DeepEqual(got, []string{"cel", "expr"}) {
		t.Errorf("WiredCodeRuntimes() = %v, want [cel expr]", got)
	}
	// Reserved = known minus wired, sorted. python/go/bash are reserved-unwired.
	if got := ReservedCodeRuntimes(); !reflect.DeepEqual(got, []string{"bash", "go", "python"}) {
		t.Errorf("ReservedCodeRuntimes() = %v, want [bash go python]", got)
	}
	// Every wired runtime must also be known (no orphan wiring).
	for _, rt := range WiredCodeRuntimes() {
		if !IsKnownCodeRuntime(rt) {
			t.Errorf("wired runtime %q is not known", rt)
		}
	}
}

func TestScriptInterpreterExtensions_IsACopy(t *testing.T) {
	m := ScriptInterpreterExtensions()
	if m[".py"] != "python3" || m[".sh"] != "bash" || m[".go"] != "go run" {
		t.Errorf("unexpected interpreter map: %v", m)
	}
	// Mutating the returned copy must not affect the internal table.
	m[".py"] = "TAMPERED"
	if scriptInterpreterByExt[".py"] != "python3" {
		t.Error("ScriptInterpreterExtensions returned a live reference, not a copy")
	}
}
