package config

import (
	"testing"
)

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

	if cfg.Container.SidecarEnabled {
		t.Errorf("SidecarEnabled = true with no binary path; should stay off")
	}
}
