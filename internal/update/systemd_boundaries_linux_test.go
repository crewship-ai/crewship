//go:build linux

package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the real exec boundary with a private systemctl fixture. Nothing
// reaches the host service manager or any running Crewship instance.
func TestSystemdExecutableBoundary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if _, err := NewSystemdService("crewship-test.service"); err == nil || !strings.Contains(err.Error(), "systemctl not found") {
		t.Fatalf("missing service manager accepted: %v", err)
	}
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$CREWSHIP_TEST_SYSTEMCTL_CALLS"
if [ "$CREWSHIP_TEST_SYSTEMCTL_FAIL" = 1 ]; then
  printf 'unit refused operation\n' >&2
  exit 17
fi
if [ "$1" = show ]; then
  printf 'Environment=OTHER=value CREWSHIP_PORT=19873\n'
fi
`
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	t.Setenv("CREWSHIP_TEST_SYSTEMCTL_CALLS", calls)
	s, err := NewSystemdService("crewship-test.service")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if port := s.UnitEnvPort(t.Context()); port != 19873 {
		t.Fatalf("resolved service port = %d", port)
	}
	data, err := os.ReadFile(calls)
	if err != nil || string(data) != "stop\ncrewship-test.service\nstart\ncrewship-test.service\nshow\ncrewship-test.service\n--property=Environment\n" {
		t.Fatalf("wrong command arguments: %q %v", data, err)
	}
	t.Setenv("CREWSHIP_TEST_SYSTEMCTL_FAIL", "1")
	if err := s.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "exit status 17") || !strings.Contains(err.Error(), "unit refused operation") {
		t.Fatalf("lost service manager failure: %v", err)
	}
	if port := s.UnitEnvPort(t.Context()); port != 0 {
		t.Fatalf("failed lookup supplied port %d", port)
	}
	s.run = func(context.Context, ...string) error { return nil }
	if port := s.UnitEnvPort(t.Context()); port != 0 {
		t.Fatalf("injected runner fabricated port %d", port)
	}
}

func TestHealthCheckerRejectsMalformedURLAndCancelledRequests(t *testing.T) {
	if err := HTTPHealthChecker("http://[invalid", time.Second, 0)(t.Context()); err == nil {
		t.Fatal("malformed health endpoint accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := HTTPHealthChecker("http://127.0.0.1:1", time.Hour, time.Millisecond)(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled health check: %v", err)
	}
}
