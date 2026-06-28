package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSidecarEnabled_AutoOnThroughLoadOrdering is the real-world regression
// the isolated applyEnvOverrides tests below could not catch: in Load() the
// binary→enable derivation ran inside applyEnvOverrides (line ordering),
// which executes BEFORE autodetectSidecarPaths populates SidecarBinaryPath
// (and before CREWSHIP_SIDECAR_PATH is applied later in that same function).
// Net effect: a present/autodetected sidecar binary never actually enabled
// the sidecar, so every deployment relying on autodetection ran with the
// sidecar silently OFF — /escalate, expose-port and MCP-memory all dead on
// :9119. The auto-enable MUST run AFTER path resolution.
func TestSidecarEnabled_AutoOnThroughLoadOrdering(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "crewship-sidecar")
	ep := filepath.Join(dir, "entrypoint.sh")
	for _, p := range []string{bin, ep} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Override TestMain's package-wide CREWSHIP_SKIP_SIDECAR=1 so Load runs
	// the real autodetect/enable path; supply explicit paths so it can't error.
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "")
	t.Setenv("CREWSHIP_SIDECAR_ENABLED", "")
	t.Setenv("CREWSHIP_SIDECAR_PATH", bin)
	t.Setenv("CREWSHIP_ENTRYPOINT_PATH", ep)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Container.SidecarBinaryPath != bin {
		t.Fatalf("SidecarBinaryPath = %q, want %q (env path not applied)", cfg.Container.SidecarBinaryPath, bin)
	}
	if !cfg.Container.SidecarEnabled {
		t.Error("SidecarEnabled = false after Load with a sidecar binary path set; auto-enable must run AFTER path resolution (Load-ordering regression of issue #541)")
	}
}

// TestSidecarEnabled_AutoOnWithBinary pins issue #541's config-side
// half: when a SidecarBinaryPath is configured (autodetect or env), the
// sidecar must be enabled by default. Pre-fix the field defaulted to
// false, leaving the bind-mounted sidecar binary live but never
// started — every MCP-memory tool call and every documented
// expose-port action hit ECONNREFUSED on :9119.
func TestSidecarEnabled_AutoOnWithBinary(t *testing.T) {
	t.Setenv("CREWSHIP_SIDECAR_ENABLED", "")

	cfg := Default()
	cfg.Container.SidecarBinaryPath = "/host/path/crewship-sidecar"
	applyEnvOverrides(cfg)
	deriveSidecarEnabled(cfg)

	if !cfg.Container.SidecarEnabled {
		t.Errorf("SidecarEnabled = false with SidecarBinaryPath set; want auto-enabled (issue #541)")
	}
}

// TestSidecarEnabled_RespectsExplicitOptOut keeps the operator escape
// hatch working — CREWSHIP_SIDECAR_ENABLED=false must beat the binary-
// detected auto-on, otherwise unit tests / hosts without docker can't
// disable the orchestrator's startSidecar branch.
func TestSidecarEnabled_RespectsExplicitOptOut(t *testing.T) {
	t.Setenv("CREWSHIP_SIDECAR_ENABLED", "false")

	cfg := Default()
	cfg.Container.SidecarBinaryPath = "/host/path/crewship-sidecar"
	applyEnvOverrides(cfg)
	deriveSidecarEnabled(cfg)

	if cfg.Container.SidecarEnabled {
		t.Errorf("SidecarEnabled = true with explicit CREWSHIP_SIDECAR_ENABLED=false; auto-on should not override")
	}
}

// TestSidecarEnabled_StaysOffWithoutBinary ensures we don't flip the
// flag for environments where the sidecar binary is genuinely missing
// (e.g. CI without `make build:sidecar`). In that case startSidecar
// would fail anyway, and a quiet `false` is better than a noisy fail.
func TestSidecarEnabled_StaysOffWithoutBinary(t *testing.T) {
	t.Setenv("CREWSHIP_SIDECAR_ENABLED", "")

	cfg := Default()
	cfg.Container.SidecarBinaryPath = ""
	applyEnvOverrides(cfg)
	deriveSidecarEnabled(cfg)

	if cfg.Container.SidecarEnabled {
		t.Errorf("SidecarEnabled = true with no binary path; should stay off")
	}
}
