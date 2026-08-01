package apple

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeSwVers plants a `sw_vers` stub in dir that reports productVersion,
// so a test's view of the host macOS version does not depend on the machine
// running the suite.
func writeFakeSwVers(t *testing.T, dir, productVersion string) {
	t.Helper()
	script := "#!/bin/sh\necho '" + productVersion + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "sw_vers"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sw_vers: %v", err)
	}
}

// TestCheckMacOSProductVersion pins the gate's shape: the comparison is
// numeric on the major version, so every future macOS satisfies it, and an
// unreadable version fails open rather than declaring a host unsupported on no
// evidence (#1647).
func TestCheckMacOSProductVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		version string
		wantErr bool
	}{
		{"exact minimum", "26.0", false},
		{"minimum without patch", "26", false},
		{"next release", "27.1.2", false},
		{"far future stays supported", "99.4", false},
		{"three-digit future stays supported", "104.0", false},
		{"too old", "15.6", true},
		{"single-digit older release is not lexically newer", "9.9", true},
		{"unknown format fails open", "Tahoe", false},
		{"empty fails open", "", false},
		{"whitespace is tolerated", "  26.1  ", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkMacOSProductVersion(tc.version)
			if tc.wantErr && err == nil {
				t.Fatalf("checkMacOSProductVersion(%q) = nil, want an unsupported-host error", tc.version)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkMacOSProductVersion(%q) = %v, want nil", tc.version, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.version) {
				t.Errorf("error must quote the host version, got %v", err)
			}
		})
	}
}

// TestDetectRejectsUnsupportedMacOS pins that the gate is actually wired into
// Detect: a host below the minimum is reported as unavailable even though the
// CLI is installed and its system service answers.
func TestDetectRejectsUnsupportedMacOS(t *testing.T) {
	f := installFakeContainer(t, `
case "$1 $2" in
  "system version") echo '[{"appName":"container","version":"1.0"}]'; exit 0;;
esac
exit 0`)
	writeFakeSwVers(t, f.dir, "15.6")

	_, err := Detect(context.Background())
	if err == nil {
		t.Fatal("expected Detect to reject a macOS host below the runtime's minimum")
	}
	if !strings.Contains(err.Error(), "macOS") {
		t.Errorf("error should name the macOS requirement, got %v", err)
	}
}

// TestDetectAcceptsFutureMacOS is the other half of the gate: a macOS newer
// than anything this code knows about must still detect, so the check can
// never become the reason the provider stops working on a future release.
func TestDetectAcceptsFutureMacOS(t *testing.T) {
	f := installFakeContainer(t, `
case "$1 $2" in
  "system version") echo '[{"appName":"container","version":"1.0"}]'; exit 0;;
esac
exit 0`)
	writeFakeSwVers(t, f.dir, "142.0")

	res, err := Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect on a future macOS: %v", err)
	}
	if res.Version != "1.0" {
		t.Errorf("Version = %q, want 1.0", res.Version)
	}
}

// TestDetectToleratesMissingSwVers keeps the gate fail-open: a host where the
// version cannot be read (sw_vers absent, or an output format Apple changed)
// falls through to the runtime probes instead of being declared unsupported.
func TestDetectToleratesMissingSwVers(t *testing.T) {
	f := installFakeContainer(t, `
case "$1 $2" in
  "system version") echo '[{"appName":"container","version":"1.0"}]'; exit 0;;
esac
exit 0`)
	if err := os.Remove(filepath.Join(f.dir, "sw_vers")); err != nil {
		t.Fatalf("remove fake sw_vers: %v", err)
	}
	t.Setenv("PATH", f.dir) // no real sw_vers reachable either

	if _, err := Detect(context.Background()); err != nil {
		t.Fatalf("Detect must not fail when the macOS version is unreadable: %v", err)
	}
}
