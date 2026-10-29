//go:build linux && mooring_rust_integration

package mooring

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestActualRuntimeStderrSurvivesFailure(t *testing.T) {
	if os.Getenv("MOORING_EVIDENCE_STDERR_CHILD") == "1" {
		_, _ = os.Stderr.WriteString("synthetic Rust failure before readiness\n")
		os.Exit(23)
	}

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOORING_SESSION_OUTPUT_DIR", root)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestActualRuntimeStderrSurvivesFailure$")
	cmd.Env = append(os.Environ(), "MOORING_EVIDENCE_STDERR_CHILD=1")
	closeEvidence, err := actualRuntimeEvidence(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		closeEvidence()
		t.Fatal(err)
	}
	closeEvidence()
	if err := cmd.Wait(); err == nil {
		t.Fatal("failed child became successful")
	}
	files, err := filepath.Glob(filepath.Join(root, "rust-stderr-*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("stderr missing: %v %v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || string(raw) != "synthetic Rust failure before readiness\n" {
		t.Fatalf("stderr lost after failure: %q %v", raw, err)
	}
	if info, err := os.Stat(files[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("stderr must stay private", err)
	}
}
