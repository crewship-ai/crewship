package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIRejectsInvalidSigningRequestsAndOutputFailures(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "license-keygen")
	args := []string{"build", "-o", binary}
	coverageDir := os.Getenv("CREWSHIP_TEST_LICENSE_KEYGEN_COVERAGE_DIR")
	if coverageDir != "" {
		if !filepath.IsAbs(coverageDir) {
			t.Fatal("coverage directory must be absolute")
		}
		if err := os.MkdirAll(coverageDir, 0700); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-cover", "-coverpkg=github.com/crewship-ai/crewship/tools/license-keygen")
	}
	args = append(args, ".")
	if output, err := exec.Command("go", args...).CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	// A deterministic fixture key is never used outside this test.
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	key := base64.StdEncoding.EncodeToString(private)
	valid := []string{"sign", "--key", key, "--id", "test-license", "--name", "Test"}
	for _, tc := range []struct {
		name string
		args []string
		want string
		code int
	}{
		{"no command", nil, "Usage:", 1},
		{"unknown command", []string{"unknown"}, "Usage:", 1},
		{"missing fields", []string{"sign"}, "Required: --key, --id, --name", 1},
		{"malformed key", []string{"sign", "--key", "%", "--id", "test", "--name", "Test"}, "Invalid private key", 1},
		{"short key", []string{"sign", "--key", "eA==", "--id", "test", "--name", "Test"}, "Invalid private key", 1},
		{"unknown flag", []string{"sign", "--unsupported"}, "flag provided but not defined", 2},
		{"invalid number", []string{"sign", "--max-crews", "many"}, "invalid value", 2},
		{"missing output directory", append(append([]string{}, valid...), "--out", filepath.Join(dir, "absent", "license.json")), "Error writing file:", 1},
		{"output is directory", append(append([]string{}, valid...), "--out", dir), "Error writing file:", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binary, tc.args...)
			cmd.Dir = dir
			if coverageDir != "" {
				cmd.Env = append(os.Environ(), "GOCOVERDIR="+coverageDir)
			}
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tc.code {
				t.Fatalf("exit: %v want %d output=%s", err, tc.code, output)
			}
			if !strings.Contains(string(output), tc.want) || strings.Contains(string(output), "License written") {
				t.Fatalf("wrong failure diagnostic: %s", output)
			}
			if strings.Contains(string(output), key) {
				t.Fatal("signing key leaked to diagnostics")
			}
			if _, err := os.Stat(filepath.Join(dir, "license.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid request created output: %v", err)
			}
		})
	}
}
