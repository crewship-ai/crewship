package main

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

// TestInitProviders_UnwiredStorageProviderErrors is the defence-in-depth half
// of the #1768 item 7 storage fix. config.Validate() now refuses anything but
// `localfs`, so in a real boot this branch is unreachable — which is exactly
// why it is worth pinning.
//
// The bug being prevented is not "an operator typed s3". It is "somebody adds
// a value to validStorageProviders and forgets the switch in initProviders" —
// the precise mistake that shipped s3. A warn-and-continue default turns that
// omission into a nil deps.Storage that only surfaces when a feature
// dereferences it, at which point the stack trace points at the feature rather
// than the wiring. Failing at init names the actual fault.
//
// Container and State keep their tolerant defaults on purpose: an unknown
// container provider is how `--no-docker` and the k8s placeholder legitimately
// run with deps.Container nil, and callers of both already nil-check. Storage
// has no such caller contract.
func TestInitProviders_UnwiredStorageProviderErrors(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.Provider = "s3"

	deps, err := initProviders(context.Background(), cfg, nil, covLogger(), true)
	if err == nil {
		if deps != nil {
			t.Cleanup(deps.Close)
		}
		t.Fatal("a storage provider with no wiring must fail init, not leave deps.Storage nil")
	}
	if !strings.Contains(err.Error(), "s3") {
		t.Errorf("error must name the offending provider so the operator can fix the config, got: %v", err)
	}
}

// TestInitProviders_EmptyStorageProviderTolerated keeps the zero-value config
// path working. Several tests construct &config.Config{} to exercise one
// provider and leave the others unset; an empty string is "not configured",
// not "configured wrongly", and must stay a no-op.
func TestInitProviders_EmptyStorageProviderTolerated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.Provider = ""

	deps, err := initProviders(context.Background(), cfg, nil, covLogger(), true)
	if err != nil {
		t.Fatalf("an unset storage provider must stay a no-op, got: %v", err)
	}
	t.Cleanup(deps.Close)
	if deps.Storage != nil {
		t.Error("an unset storage provider must wire nothing")
	}
}
