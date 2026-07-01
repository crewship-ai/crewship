package devcontainer

import (
	"strings"
	"testing"
)

// TestDockerfileGenFingerprint_CoversPerFeatureRendering pins the fix for the
// stale-cache gap: the fingerprint renders a synthetic feature so the
// per-feature COPY/RUN/install-env template is part of the hash. If that
// template (or the synthetic-feature wiring) changes, this fails — a signal to
// confirm the cache will actually bust on per-feature rendering code changes.
func TestDockerfileGenFingerprint_CoversPerFeatureRendering(t *testing.T) {
	cfg := &Config{ContainerEnv: map[string]string{"FOO": "bar"}}
	fp := dockerfileGenFingerprint("ubuntu:24.04", cfg)
	if fp == "" {
		t.Fatal("fingerprint is empty — GenerateDockerfile failed on the probe feature")
	}
	for _, want := range []string{"# feature: fingerprint-probe", "install.sh", "PROBE_PATH"} {
		if !strings.Contains(fp, want) {
			t.Errorf("fingerprint missing %q — per-feature rendering is not covered:\n%s", want, fp)
		}
	}
}

// TestConfigHash_ChangesWithDockerfileFingerprint proves a generator/template
// change (modeled as a different fingerprint) yields a different cache hash, so
// a changed Dockerfile can never silently reuse a stale crewship-cache image.
func TestConfigHash_ChangesWithDockerfileFingerprint(t *testing.T) {
	cfg := &Config{ContainerEnv: map[string]string{"FOO": "bar"}}
	const base = "ubuntu:24.04"
	fp := dockerfileGenFingerprint(base, cfg)
	h1 := configHash(base, cfg, "", fp)
	h2 := configHash(base, cfg, "", fp+"\n# template changed")
	if h1 == h2 {
		t.Error("configHash did not change when the Dockerfile fingerprint changed — stale-cache risk")
	}
}
