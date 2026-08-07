package server

// The container-status payload has to carry the runtime-contract verdict, or
// the drift #1642 is about is known only to the daemon and a log line.
//
// This endpoint is proxied verbatim by the workspace API
// (CrewHandler.ContainerStatus re-serialises the IPC body and only overwrites
// crew_id), so a key added here reaches `crewship crew container-status`
// without any further plumbing.

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type covContractContainer struct {
	mockContainer
	contract string
	memoryMB int
	cpus     float64
}

func (c *covContractContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{
		ID: id, State: "running", Uptime: "1h", RuntimeContract: c.contract,
		MemoryMB: c.memoryMB, CPUs: c.cpus,
	}, nil
}

func TestHandleContainerStatus_ReportsRuntimeContract(t *testing.T) {
	s := newTestServerWithDeps(t)
	ctr := &covContractContainer{contract: provider.RuntimeContractStale}
	s.container = ctr

	req := httptest.NewRequest("GET", "/crews/c1/container/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["runtime_contract"] != provider.RuntimeContractStale {
		t.Errorf("runtime_contract = %v, want %q — an operator has no other way to learn that a crew is running with the configuration of an older build",
			resp["runtime_contract"], provider.RuntimeContractStale)
	}
}

// A provider with no opinion (the Apple provider today, or a docker provider
// that could not compute its own contract) must not have one invented for it.
// An absent key is "unknown"; emitting "current" would be a lie in exactly the
// direction that hides the defect.
func TestHandleContainerStatus_OmitsUnknownRuntimeContract(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &covContractContainer{contract: ""}

	req := httptest.NewRequest("GET", "/crews/c1/container/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	resp := parseJSON(t, w.Body.Bytes())
	if v, ok := resp["runtime_contract"]; ok {
		t.Errorf("runtime_contract = %v, want the key absent when the provider has no opinion", v)
	}
}

// The limits the container actually runs under travel the same way (#1681).
// The workspace API compares them against the crews row — it is the only side
// that holds both numbers — so if they stop at the daemon, `crewship crew
// container-status` goes back to reporting only what was asked for.
func TestHandleContainerStatus_ReportsEffectiveResourceLimits(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &covContractContainer{memoryMB: 4096, cpus: 2}

	req := httptest.NewRequest("GET", "/crews/c1/container/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	resp := parseJSON(t, w.Body.Bytes())
	if resp["effective_memory_mb"] != float64(4096) {
		t.Errorf("effective_memory_mb = %v, want 4096 — the limit the running container carries", resp["effective_memory_mb"])
	}
	if resp["effective_cpus"] != float64(2) {
		t.Errorf("effective_cpus = %v, want 2", resp["effective_cpus"])
	}
}

// Same rule as the contract verdict: a provider with nothing to say says
// nothing. A container that declares no limit, or a provider that does not
// track limits at all, must not report 0 — the comparison on the other side
// would read that as a crew running under no memory at all and call it drift.
func TestHandleContainerStatus_OmitsUnknownResourceLimits(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &covContractContainer{}

	req := httptest.NewRequest("GET", "/crews/c1/container/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	resp := parseJSON(t, w.Body.Bytes())
	for _, k := range []string{"effective_memory_mb", "effective_cpus"} {
		if v, ok := resp[k]; ok {
			t.Errorf("%s = %v, want the key absent when the provider reports no limit", k, v)
		}
	}
}
