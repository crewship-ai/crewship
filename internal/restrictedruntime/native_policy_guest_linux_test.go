//go:build linux && restrictedruntime_guest

package restrictedruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This gate changes a kernel policy only inside an explicitly provisioned,
// disposable VM. It is excluded from both ordinary and Docker live suites.
func TestGuestNativeKernelPolicyBinding(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("CREWSHIP_NATIVE_POLICY_GUEST") != "1" {
		t.Fatal("requires explicitly isolated native policy guest")
	}
	profile := filepath.Join(t.TempDir(), "profile")
	load := func(raw []byte) {
		t.Helper()
		if err := os.WriteFile(profile, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.CommandContext(t.Context(), "/usr/sbin/apparmor_parser", "--replace", "--skip-read-cache", profile).CombinedOutput(); err != nil {
			t.Fatalf("load guest policy: %v %s", err, output)
		}
	}
	load(nativeAppArmor)
	if err := NativeSandboxReady(t.Context()); err != nil {
		t.Fatalf("exact loaded kernel policy rejected: %v", err)
	}
	p := Plan{NativeSandbox: NativeSandboxFingerprint()}
	options, cleanup, err := nativeSecurityOptions(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(options) != 4 || options[0] != "--security-opt" || options[2] != "--security-opt" || options[3] != "apparmor="+nativeAppArmorName {
		t.Fatalf("incorrect native options: %v", options)
	}
	policyFile := strings.TrimPrefix(options[1], "seccomp=")
	data, err := os.ReadFile(policyFile)
	if err != nil || string(data) != string(nativeSeccomp) {
		t.Fatalf("seccomp bytes changed: %v", err)
	}
	cleanup()
	if _, err := os.Stat(policyFile); !os.IsNotExist(err) {
		t.Fatalf("temporary policy retained: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(NativeSandboxReady(ctx), ErrDenied) {
		t.Fatal("cancelled readiness accepted")
	}
	a := &fixtureAuthority{plans: map[string]Plan{}, denied: map[string]bool{}}
	docker := Docker{Image: "sha256:" + strings.Repeat("a", 64)}
	m, err := NewNative(filepath.Join(t.TempDir(), "manager"), docker, a, catalogMap{}, NativeLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !m.nativeOnly {
		t.Fatal("native constructor admitted ordinary dispatch")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	docker.Image = "sha256:" + strings.Repeat("z", 64)
	if _, err := NewNative(filepath.Join(t.TempDir(), "invalid"), docker, a, catalogMap{}, NativeLimits()); !errors.Is(err, ErrDenied) {
		t.Fatalf("invalid digest accepted: %v", err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"complain", []byte(strings.Replace(string(nativeAppArmor), "flags=(", "flags=(complain,", 1))},
		{"changed hash", []byte(strings.Replace(string(nativeAppArmor), "  network,", "  network,\n  deny /tmp/guest-policy-drift-canary w,", 1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			load(tc.raw)
			defer load(nativeAppArmor)
			if err := NativeSandboxReady(t.Context()); !errors.Is(err, ErrDenied) {
				t.Fatalf("changed loaded policy admitted: %v", err)
			}
			if _, cleanup, err := nativeSecurityOptions(t.Context(), p); !errors.Is(err, ErrDenied) {
				cleanup()
				t.Fatalf("changed policy produced launch options: %v", err)
			}
		})
	}
	if output, err := exec.CommandContext(t.Context(), "/usr/sbin/apparmor_parser", "--remove", profile).CombinedOutput(); err != nil {
		t.Fatalf("remove guest policy: %v %s", err, output)
	}
	if err := NativeSandboxReady(t.Context()); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed policy still trusted: %v", err)
	}
}
