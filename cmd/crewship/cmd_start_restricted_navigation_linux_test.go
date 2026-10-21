//go:build linux && !clionly

package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/restricteddispatch"
)

func TestRestrictedNavigationProfileDeclarationIsNotRuntimeHealth(t *testing.T) {
	text := &restrictedExecutor{text: &restricteddispatch.TextRunner{}}
	if !text.DeclaresWorkflowProfile("responses_text") || text.DeclaresWorkflowProfile("native_api_key") || text.DeclaresWorkflowProfile("unknown") {
		t.Fatal("text-only declaration drift")
	}
	native := &restrictedExecutor{native: &restricteddispatch.NativeRunner{}}
	if !native.DeclaresWorkflowProfile("native_api_key") || native.DeclaresWorkflowProfile("responses_text") {
		t.Fatal("production native declaration drift")
	}
	if text.SupportsWorkflowProfile("responses_text") || native.SupportsWorkflowProfile("native_api_key") {
		t.Fatal("fixture unexpectedly has a ready execution runtime")
	}
}
