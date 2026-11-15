package devcontainer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestImageAcquisitionUsesNormalizedCacheTag(t *testing.T) {
	cfg := &Config{Image: "synthetic/base:1", Features: map[string]map[string]any{commonUtilsFeatureRef: {"uid": "1001", "gid": "1001", "username": "agent"}}}
	p := NewProvisioner(&mockCommitClient{}, nil, nil, testLogger())
	oldTag := p.cacheTag(configHash(cfg.Image, cfg, "", dockerfileGenFingerprint(cfg.Image, cfg)))
	stop := errors.New("stop before touching Docker")
	captured := ""
	_, err := p.Provision(t.Context(), cfg.Image, cfg, "", WithImageAcquisition(func(_ context.Context, refs ...string) error { captured = refs[len(refs)-1]; return stop }))
	if !errors.Is(err, stop) {
		t.Fatalf("guard not entered before Docker: %v", err)
	}
	expected := p.cacheTag(configHash(cfg.Image, cfg, "", dockerfileGenFingerprint(cfg.Image, cfg)))
	if captured == oldTag || captured != expected {
		t.Fatalf("lease does not cover normalized acquisition: old=%s held=%s actual=%s", oldTag, captured, expected)
	}
}

func TestFeatureIntermediateAcquisitionBeforeCacheLookup(t *testing.T) {
	p := NewProvisioner(&mockCommitClient{}, nil, nil, testLogger())
	p.SetInstanceID(installationA)
	stop := errors.New("stop before image lookup")
	acquired := ""
	_, err := p.buildFeatureImage(t.Context(), "synthetic/base:1", nil, nil, nil, func(string) {}, nil, false, func(_ context.Context, refs ...string) error { acquired = refs[0]; return stop })
	if !errors.Is(err, stop) || !strings.HasPrefix(acquired, FeatureImageTagPrefix+installationA+"-") {
		t.Fatalf("feature cache not protected: ref=%s err=%v", acquired, err)
	}
}
