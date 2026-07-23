package manifest

import (
	"reflect"
	"testing"
)

// Focused unit tests for the command-arg injection primitives that back
// the always-auth Redis path. renderCommandTemplate splices a value into
// a template; extractCommandArgValue recovers a value from a prior
// rendered command (idempotent re-apply), returning ok=false on any
// structural mismatch so the caller regenerates rather than reusing junk.

func TestRenderCommandTemplate(t *testing.T) {
	tmpl := []string{"redis-server", "--requirepass", autoCredentialValuePlaceholder}
	got := renderCommandTemplate(tmpl, "SECRET")
	want := []string{"redis-server", "--requirepass", "SECRET"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("renderCommandTemplate = %+v, want %+v", got, want)
	}
	// The template itself must not be mutated (aliasing bug guard).
	if tmpl[2] != autoCredentialValuePlaceholder {
		t.Errorf("template was mutated in place: %+v", tmpl)
	}
}

func TestRenderCommandTemplate_PlaceholderAppearsMultipleTimes(t *testing.T) {
	tmpl := []string{"--auth", autoCredentialValuePlaceholder, "--mirror", autoCredentialValuePlaceholder}
	got := renderCommandTemplate(tmpl, "X")
	want := []string{"--auth", "X", "--mirror", "X"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("renderCommandTemplate = %+v, want %+v", got, want)
	}
}

func TestExtractCommandArgValue(t *testing.T) {
	tmpl := []string{"redis-server", "--requirepass", autoCredentialValuePlaceholder}

	got, ok := extractCommandArgValue(tmpl, []string{"redis-server", "--requirepass", "ABC"})
	if !ok || got != "ABC" {
		t.Errorf("extractCommandArgValue = (%q,%v), want (ABC,true)", got, ok)
	}

	// Length mismatch → no reuse.
	if _, ok := extractCommandArgValue(tmpl, []string{"redis-server"}); ok {
		t.Errorf("length mismatch should return ok=false")
	}
	// A fixed (non-placeholder) token differs → no reuse.
	if _, ok := extractCommandArgValue(tmpl, []string{"redis-server", "--other", "ABC"}); ok {
		t.Errorf("fixed-token mismatch should return ok=false")
	}
	// Empty prior command → no reuse.
	if _, ok := extractCommandArgValue(tmpl, nil); ok {
		t.Errorf("nil prior command should return ok=false")
	}
}
