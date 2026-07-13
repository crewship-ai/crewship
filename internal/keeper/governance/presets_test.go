package governance

import (
	"sort"
	"strings"
	"testing"
)

func TestValidatePresets(t *testing.T) {
	if err := ValidatePresets(nil); err != nil {
		t.Fatalf("nil presets should be valid: %v", err)
	}
	if err := ValidatePresets([]string{"credentials", "egress"}); err != nil {
		t.Fatalf("known presets should be valid: %v", err)
	}
	if err := ValidatePresets([]string{"credentials", "bogus"}); err == nil {
		t.Fatal("unknown preset must be rejected")
	}
}

func TestCompileWatchSpecEmpty(t *testing.T) {
	if got := CompileWatchSpec(Settings{}); got != "" {
		t.Fatalf("empty settings must compile to empty string, got %q", got)
	}
	// Unknown presets are skipped, not injected as noise.
	if got := CompileWatchSpec(Settings{WatchPresets: []string{"bogus"}}); got != "" {
		t.Fatalf("unknown-only presets must compile to empty, got %q", got)
	}
}

func TestCompileWatchSpecStableOrder(t *testing.T) {
	// Presets must expand in a deterministic (sorted-key) order regardless of
	// the input slice order — the compiled prompt feeds the determinism harness.
	forward := CompileWatchSpec(Settings{WatchPresets: []string{"credentials", "egress", "memory"}})
	shuffled := CompileWatchSpec(Settings{WatchPresets: []string{"memory", "credentials", "egress"}})
	if forward != shuffled {
		t.Fatalf("preset order must not affect the compiled block:\n forward=%q\n shuffled=%q", forward, shuffled)
	}
	// Sorted-key order: credentials < egress < memory.
	iCred := strings.Index(forward, WatchPresets["credentials"])
	iEgress := strings.Index(forward, WatchPresets["egress"])
	iMem := strings.Index(forward, WatchPresets["memory"])
	if !(iCred < iEgress && iEgress < iMem) {
		t.Fatalf("presets not in sorted-key order: cred=%d egress=%d mem=%d", iCred, iEgress, iMem)
	}
}

func TestCompileWatchSpecAppendsFreeform(t *testing.T) {
	const rule = "flag any read of ~/.ssh or id_rsa"
	got := CompileWatchSpec(Settings{
		WatchPresets: []string{"credentials"},
		WatchSpec:    rule,
	})
	if !strings.Contains(got, WatchPresets["credentials"]) {
		t.Fatal("compiled block missing the preset rule")
	}
	if !strings.Contains(got, rule) {
		t.Fatal("compiled block missing the free-form rule")
	}
	// Free-form rules come after the presets.
	if strings.Index(got, rule) < strings.Index(got, WatchPresets["credentials"]) {
		t.Fatal("free-form rule must be appended after presets")
	}
}

func TestCompileWatchSpecFreeformOnly(t *testing.T) {
	const rule = "flag credential access outside 08:00-18:00"
	got := CompileWatchSpec(Settings{WatchSpec: rule})
	if !strings.Contains(got, rule) {
		t.Fatalf("free-form-only spec must compile, got %q", got)
	}
}

func TestCompileWatchSpecLengthCapped(t *testing.T) {
	huge := strings.Repeat("x", maxCompiledWatchSpecLen*2)
	got := CompileWatchSpec(Settings{WatchSpec: huge})
	if len(got) > maxCompiledWatchSpecLen+len(watchSpecTruncMarker) {
		t.Fatalf("compiled block not capped: len=%d cap=%d", len(got), maxCompiledWatchSpecLen)
	}
}

func TestResolveWatchBlockGatesOnEnabled(t *testing.T) {
	rules := Settings{
		WatchPresets: []string{"credentials"},
		WatchSpec:    "flag off-hours access",
	}
	// Disabled → inert, even with presets + free-form rules authored.
	rules.Enabled = false
	if got := ResolveWatchBlock(rules); got != "" {
		t.Fatalf("disabled workspace must resolve to no watch block, got %q", got)
	}
	// Enabled → the compiled block surfaces.
	rules.Enabled = true
	got := ResolveWatchBlock(rules)
	if got == "" || !strings.Contains(got, WatchPresets["credentials"]) || !strings.Contains(got, "flag off-hours access") {
		t.Fatalf("enabled workspace must resolve to the compiled block, got %q", got)
	}
	// Enabled but no rules → still empty (nothing to inject).
	if got := ResolveWatchBlock(Settings{Enabled: true}); got != "" {
		t.Fatalf("enabled-but-ruleless workspace must resolve to empty, got %q", got)
	}
}

func TestWatchPresetKeysSorted(t *testing.T) {
	// Guards the assumption CompileWatchSpec relies on — the catalog keys must
	// have a total order (they do, being strings); this documents intent.
	keys := PresetKeys()
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("PresetKeys must return sorted keys: %v", keys)
	}
}
