package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pagebuild"
)

func TestResponseUsesProductionDenyWorkerAndDistinctSites(t *testing.T) {
	response := bootstrap()
	if response.Status != 200 || !strings.Contains(response.Body, "crewship.pages.boot/v1") {
		t.Fatal("production bootstrap required")
	}
	policy := strings.Join(response.Headers["Content-Security-Policy"], ";")
	if !strings.Contains(policy, "worker-src 'none'") || !strings.Contains(policy, "frame-ancestors "+studio) {
		t.Fatal("production worker denial and parent binding required")
	}
	if pagebuild.ValidateRuntimeOrigin(runtime, studio) != nil || pagebuild.ValidateRuntimeOrigin(studio, studio) == nil {
		t.Fatal("fixture must use distinct sites without development exception")
	}
}
