package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// persona_writefailure_test.go pins the crash-safety fix for WritePersona
// in persona.go, which used to call os.WriteFile directly on the final
// PERSONA.md path instead of going through writeFileDurable's
// write-temp+fsync+rename dance. (BackfillFromLegacy got the identical
// fix — same file, same pattern — but its write path only fires the
// first time a layer is populated, i.e. on a path that does not exist
// yet; a not-yet-existing path needs directory-create permission under
// BOTH the old and the new implementation, so the discriminator below
// would not distinguish buggy-vs-fixed for that call site. Its content
// correctness is already covered by TestBackfillFromLegacy in
// persona_test.go.)
//
// The discriminator this test relies on: os.WriteFile on an EXISTING
// file opens that file directly with O_TRUNC — which only needs write
// permission on the file's own mode bits, not on its parent directory.
// writeFileDurable instead creates a NEW sibling tempfile first, which
// DOES need write/create permission on the parent directory. So making
// the parent directory read-only (while the target file itself stays
// mode 0644, perfectly writable in isolation) is a failure that the
// buggy os.WriteFile call would never even hit — it would happily
// truncate-and-rewrite the existing, individually-writable file. Only
// the durable path fails here, because it needs to create a tempfile
// in a directory that no longer allows creation.
//
// That makes this a real regression guard for this exact bug class: if
// a future edit reverts WritePersona back to a direct os.WriteFile
// call, this test stops observing an error and fails on the "expected
// write to fail" assertion below.
//
// What this test does NOT prove: it is not a simulated process crash or
// power loss (we cannot inject a fault between fsync and rename without
// hooking the syscall layer). It proves the documented contract that
// depends on: the target is never touched until the tempfile is fully
// written, so a failure before that point cannot corrupt or truncate
// the file that was already there.
func TestWritePersonaAgentLayerSurvivesDirectoryWriteFailure(t *testing.T) {
	// SKIP-WAIVER: permanent platform guard, not deferred work. A read-only
	// directory cannot deny root, so the failure this test injects is
	// unreachable when euid is 0 — the test would pass without exercising
	// anything. No tracking issue: there is nothing to come back and fix.
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; skip the RO-dir failure injection")
	}
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agent", ".memory")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	paths := PersonaPaths{AgentDir: agentDir}

	if err := WritePersona(paths, PersonaAgent, "version-one"); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if err := os.Chmod(agentDir, 0o555); err != nil {
		t.Fatalf("chmod agent dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(agentDir, 0o755) })

	if err := WritePersona(paths, PersonaAgent, "version-two-must-not-land"); err == nil {
		t.Fatal("expected WritePersona to fail against a read-only parent directory " +
			"(if this now succeeds, WritePersona regressed to a direct os.WriteFile call)")
	}

	if err := os.Chmod(agentDir, 0o755); err != nil {
		t.Fatalf("restore perms: %v", err)
	}

	got, err := os.ReadFile(paths.AgentPath())
	if err != nil {
		t.Fatalf("read persona after failed write: %v", err)
	}
	if string(got) != "version-one" {
		t.Fatalf("prior content corrupted after failed write: got %q, want %q", got, "version-one")
	}

	entries, _ := os.ReadDir(agentDir)
	for _, e := range entries {
		if hasTmpMarker(e.Name()) {
			t.Fatalf("leftover tempfile after failed write: %s", e.Name())
		}
	}
}
