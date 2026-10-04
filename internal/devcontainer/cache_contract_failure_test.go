package devcontainer

import (
	"context"
	"testing"
)

func TestCachedImageCannotPublishIncompleteFeatureRequirements(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04", Features: map[string]map[string]any{"invalid feature reference": {}}}
	hash := configHash(cfg.Image, cfg, "", dockerfileGenFingerprint(cfg.Image, cfg)+requiredBinariesHashSalt([]string{"claude"}))
	mock := &mockCommitClient{existingImages: []string{cacheImageTag(hash)}}
	p := NewProvisioner(mock, nil, NewFeatureDownloader(t.TempDir(), testLogger()), testLogger())
	var events []ProvisionEvent
	result, err := p.Provision(context.Background(), cfg.Image, cfg, "", WithRequiredBinaries([]string{"claude"}), WithProvisionSink(func(e ProvisionEvent) { events = append(events, e) }))
	if err == nil || result != nil {
		t.Fatalf("incomplete cache contract published: result=%+v err=%v", result, err)
	}
	for _, e := range events {
		if e.Step == ProvStepReady {
			t.Fatal("failed inspection emitted ready")
		}
	}
	if len(mock.createdContainers) != 0 {
		t.Fatal("failed cache inspection unexpectedly rebuilt")
	}
}
