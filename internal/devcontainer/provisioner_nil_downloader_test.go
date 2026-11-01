package devcontainer

import (
	"context"
	"strings"
	"testing"
)

// A provisioner without a feature downloader (tests in internal/api build one
// with nil) used to dereference nil in a download goroutine, which no recover
// can catch and which killed the whole test binary (#4742).
func TestResolveFeaturesWithoutDownloaderFailsTheJob(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    *Provisioner
	}{
		{"commit provisioner", NewProvisioner(nil, nil, nil, nil)},
		{"build-only provisioner", NewBuildOnlyProvisioner(nil, nil, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Features: map[string]map[string]any{"ghcr.io/devcontainers/features/node:1": {}}}
			sorted, opts, err := tc.p.resolveFeatures(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), "no feature downloader") {
				t.Fatalf("resolveFeatures error = %v, want the missing-downloader error", err)
			}
			if sorted != nil || opts != nil {
				t.Fatalf("resolveFeatures returned features %v / %v without a downloader", sorted, opts)
			}
		})
	}

	// No features declared: nothing to download, so no downloader is needed.
	if _, _, err := NewProvisioner(nil, nil, nil, nil).resolveFeatures(context.Background(), &Config{}); err != nil {
		t.Fatalf("resolveFeatures without features: %v", err)
	}
}
