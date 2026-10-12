//go:build linux

package main

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/startup.sh
var quotaStartupFixture string

// The same command is exercised under an ordinary host UID and under root in
// an owned container. No test attaches a loop device, mounts a filesystem or
// accesses a real quota catalog; all catalogs and sockets are fresh fixtures.
func TestQuotaCommandStartupBoundaries(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	binary := os.Getenv("CREWSHIP_TEST_QUOTA_HELPER_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "quota-helper")
		build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build quota command: %v\n%s", err, output)
		}
	} else if !filepath.IsAbs(binary) {
		t.Fatal("CREWSHIP_TEST_QUOTA_HELPER_BINARY must be absolute")
	}
	for _, tc := range []struct {
		name    string
		args    []string
		message string
	}{
		{"namespace-required", nil, "namespace required"},
		{"server-uid-overflow", []string{"--namespace", "fixture", "--server-uid", "4294967296"}, "invalid server UID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(ctx, binary, tc.args...)
			cmd.Env = append(os.Environ(), "NOTIFY_SOCKET=")
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), tc.message) {
				t.Fatalf("startup refusal: err=%v output=%s", err, output)
			}
		})
	}
	selfMount, selfErr := os.Readlink("/proc/self/ns/mnt")
	initMount, initErr := os.Readlink("/proc/1/ns/mnt")
	if os.Geteuid() != 0 || selfErr != nil || initErr != nil || selfMount != initMount {
		cmd := exec.CommandContext(ctx, binary, "--namespace", "fixture", "--root", t.TempDir())
		cmd.Env = append(os.Environ(), "NOTIFY_SOCKET=")
		output, err := cmd.CombinedOutput()
		if err == nil || (!strings.Contains(string(output), "quota catalog unavailable") && !strings.Contains(string(output), "requires the host mount namespace")) {
			t.Fatalf("unprivileged or foreign-namespace startup was not denied: %v %s", err, output)
		}
		return
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-s")
	cmd.Stdin = strings.NewReader(quotaStartupFixture)
	cmd.Env = append(os.Environ(), "QUOTA_HELPER_BINARY="+binary, "NOTIFY_SOCKET=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("owned root startup fixture: %v\n%s", err, output)
	}
	for _, check := range []string{"missing-namespace", "oversized-uid", "missing-catalog", "invalid-namespace", "unsafe-peer-uid", "namespace-rebinding", "recovery-failure", "readiness-failure", "quarantine-readiness-graceful-shutdown"} {
		if !strings.Contains(string(output), "PASS "+check+"\n") {
			t.Errorf("missing startup assertion %s:\n%s", check, output)
		}
	}
	t.Logf("owned startup assertions:\n%s", output)
}
