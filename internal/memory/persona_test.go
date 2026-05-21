package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPersonaLayering: agent overrides crew, crew default seen when
// agent layer empty, default fallback when both empty.
func TestPersonaLayering(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agents", "alice", ".memory")
	crewDir := filepath.Join(dir, "shared", ".memory")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent: %v", err)
	}
	if err := os.MkdirAll(crewDir, 0o755); err != nil {
		t.Fatalf("mkdir crew: %v", err)
	}
	p := PersonaPaths{AgentDir: agentDir, CrewDir: crewDir}

	// Both empty: load returns zero value (no FromDefault set, that's
	// the caller's job via DefaultPersona).
	got, err := LoadPersona(p)
	if err != nil {
		t.Fatalf("LoadPersona empty: %v", err)
	}
	if got.Content != "" {
		t.Errorf("expected empty content when no files exist, got %q", got.Content)
	}

	// Crew only: returned with Layer=crew.
	if err := WritePersona(p, PersonaCrew, "We are blunt and prefer Czech."); err != nil {
		t.Fatalf("WritePersona crew: %v", err)
	}
	got, err = LoadPersona(p)
	if err != nil {
		t.Fatalf("LoadPersona crew: %v", err)
	}
	if got.Layer != PersonaCrew || !strings.Contains(got.Content, "blunt") {
		t.Errorf("expected crew layer Czech content, got %+v", got)
	}

	// Agent override wins outright.
	if err := WritePersona(p, PersonaAgent, "Stay gentle even when the team is blunt."); err != nil {
		t.Fatalf("WritePersona agent: %v", err)
	}
	got, err = LoadPersona(p)
	if err != nil {
		t.Fatalf("LoadPersona agent: %v", err)
	}
	if got.Layer != PersonaAgent || !strings.Contains(got.Content, "gentle") {
		t.Errorf("expected agent layer gentle content, got %+v", got)
	}

	// Reset agent layer: crew layer reappears.
	if err := ResetPersona(p, PersonaAgent); err != nil {
		t.Fatalf("ResetPersona: %v", err)
	}
	got, err = LoadPersona(p)
	if err != nil {
		t.Fatalf("LoadPersona after reset: %v", err)
	}
	if got.Layer != PersonaCrew {
		t.Errorf("expected crew layer to resurface after agent reset, got %+v", got)
	}
}

func TestPersonaCapEnforced(t *testing.T) {
	dir := t.TempDir()
	p := PersonaPaths{AgentDir: dir}
	big := strings.Repeat("x", PersonaCapBytes+1)
	if err := WritePersona(p, PersonaAgent, big); err == nil {
		t.Errorf("expected cap rejection on oversize write")
	}
	// At cap exactly: allowed.
	atCap := strings.Repeat("y", PersonaCapBytes)
	if err := WritePersona(p, PersonaAgent, atCap); err != nil {
		t.Errorf("at-cap write rejected: %v", err)
	}
}

func TestPersonaEmptyRejected(t *testing.T) {
	dir := t.TempDir()
	p := PersonaPaths{AgentDir: dir}
	if err := WritePersona(p, PersonaAgent, "   \n\t"); err == nil {
		t.Errorf("expected empty-content rejection")
	}
}

func TestPersonaCrewLayerRequiresCrewDir(t *testing.T) {
	dir := t.TempDir()
	p := PersonaPaths{AgentDir: dir} // no CrewDir
	if err := WritePersona(p, PersonaCrew, "anything"); err == nil {
		t.Errorf("expected error when writing crew layer without CrewDir")
	}
}

func TestDefaultPersona(t *testing.T) {
	got := DefaultPersona("LEAD", "Captain")
	if !got.FromDefault {
		t.Errorf("expected FromDefault=true")
	}
	if !strings.Contains(got.Content, "Captain") || !strings.Contains(got.Content, "LEAD") {
		t.Errorf("default did not include role title + role bucket: %q", got.Content)
	}
	// Fallback when both inputs are empty.
	bare := DefaultPersona("", "")
	if !strings.Contains(bare.Content, "Agent") {
		t.Errorf("bare default missing Agent fallback: %q", bare.Content)
	}
}

func TestBackfillFromLegacy(t *testing.T) {
	dir := t.TempDir()
	p := PersonaPaths{AgentDir: dir}

	// Empty legacy → no-op.
	wrote, err := BackfillFromLegacy(p, "")
	if err != nil || wrote {
		t.Errorf("empty backfill should no-op; wrote=%v err=%v", wrote, err)
	}

	// Non-empty legacy → writes agent layer.
	wrote, err = BackfillFromLegacy(p, "You are helpful.")
	if err != nil || !wrote {
		t.Fatalf("backfill failed: wrote=%v err=%v", wrote, err)
	}
	got, _ := LoadPersona(p)
	if !strings.Contains(got.Content, "helpful") {
		t.Errorf("backfill did not land: %q", got.Content)
	}

	// Idempotent: re-running with same value does not overwrite the
	// existing file (which is exactly what we want — operator may
	// have edited PERSONA.md since the first backfill).
	wrote, err = BackfillFromLegacy(p, "You are NEW prompt.")
	if err != nil || wrote {
		t.Errorf("backfill should skip when agent layer non-empty; wrote=%v err=%v", wrote, err)
	}
	got, _ = LoadPersona(p)
	if !strings.Contains(got.Content, "helpful") {
		t.Errorf("second backfill clobbered existing content: %q", got.Content)
	}

	// Oversize legacy: truncated to cap, but still written.
	dir2 := t.TempDir()
	p2 := PersonaPaths{AgentDir: dir2}
	big := strings.Repeat("z", PersonaCapBytes+500)
	wrote, err = BackfillFromLegacy(p2, big)
	if err != nil || !wrote {
		t.Fatalf("oversize backfill failed: wrote=%v err=%v", wrote, err)
	}
	got, _ = LoadPersona(p2)
	if len(got.Content) != PersonaCapBytes {
		t.Errorf("oversize backfill not truncated to cap: got %d bytes", len(got.Content))
	}
}
