package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiffLimitsResolve(t *testing.T) {
	got, err := (DiffConfig{}).Resolve()
	if err != nil || got != (DiffConfig{256 << 10, 2000, 2}) {
		t.Fatalf("defaults %+v %v", got, err)
	}
	for _, bad := range []DiffConfig{{-1, 1, 1}, {1, -1, 1}, {1, 1, -1}, {(8 << 20) + 1, 1, 1}, {1, 20001, 1}, {1, 1, 65}} {
		if _, err := bad.Resolve(); err == nil {
			t.Fatalf("invalid config accepted: %+v", bad)
		}
	}
	explicit := DiffConfig{512, 32, 3}
	got, err = explicit.Resolve()
	if err != nil || got != explicit {
		t.Fatalf("explicit config %+v %v", got, err)
	}
}
func TestDiffLimitsYAMLAndEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("diff:\n  max_input_bytes: 8192\n  max_lines: 100\n  max_concurrent: 3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Diff != (DiffConfig{8192, 100, 3}) {
		t.Fatalf("YAML diff limits=%+v", cfg.Diff)
	}
	t.Setenv("CREWSHIP_DIFF_MAX_INPUT_BYTES", "16384")
	t.Setenv("CREWSHIP_DIFF_MAX_LINES", "200")
	t.Setenv("CREWSHIP_DIFF_MAX_CONCURRENT", "1")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Diff != (DiffConfig{16384, 200, 1}) {
		t.Fatalf("env diff limits=%+v", cfg.Diff)
	}
	for _, bad := range []string{"-1", "invalid", "65"} {
		t.Setenv("CREWSHIP_DIFF_MAX_CONCURRENT", bad)
		if _, err := Load(path); err == nil {
			t.Fatalf("invalid env%q accepted", bad)
		}
	}
}
