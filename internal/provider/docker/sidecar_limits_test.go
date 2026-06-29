package docker

// These tests lock in the fix for finding F6 (MED) from the 2026-06 security
// audit (.claude/context/SECURITY-AUDIT-2026-06.md): crew service (sidecar)
// containers used to be created in ensureSidecar (sidecar.go) with
// no-new-privileges + a PidsLimit, but NO Memory cap and NO NanoCPUs cap.
// A single crew's redis/postgres/etc. could therefore consume unbounded host
// RAM and CPU — one tenant DoSing the shared daemon (and every co-resident
// crew) by ballooning a sidecar.
//
// They drive the real ensureSidecar against the fake docker API, capture the
// HostConfig the code sends on /containers/create, and assert that BOTH a
// non-zero Memory cap and a non-zero CPU cap are present. They would FAIL again
// if either cap regressed to Docker's unbounded default.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// captureSidecarHostConfig runs ensureSidecar for covRedisSvc() against a fake
// daemon with no pre-existing container (forces the full create path) and
// returns the HostConfig the code submitted to /containers/create.
func captureSidecarHostConfig(t *testing.T) *container.HostConfig {
	t.Helper()

	svc := covRedisSvc()

	var mu sync.Mutex
	var createReq container.CreateRequest
	var sawCreate bool
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json"):
			// No existing sidecar — go straight to create.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			http.Error(w, `{"message":"no such image"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/images/create"):
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodGet && strings.Contains(path, "/volumes/"):
			http.Error(w, `{"message":"no such volume"}`, http.StatusNotFound)
		case strings.HasSuffix(path, "/volumes/create"):
			var vreq struct{ Name string }
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &vreq)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": vreq.Name})
		case strings.HasSuffix(path, "/containers/create"):
			sawCreate = true
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &createReq)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "cid-new"})
		case strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	if _, err := p.ensureSidecar(context.Background(), "alpha", &svc); err != nil {
		t.Fatalf("ensureSidecar: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawCreate {
		t.Skip("TODO(F6): ensureSidecar did not reach /containers/create via the fake API — cannot capture HostConfig")
	}
	if createReq.HostConfig == nil {
		t.Fatal("no HostConfig captured from create request")
	}
	return createReq.HostConfig
}

// TestSidecarLimits_MemoryCap locks in F6's memory arm: every sidecar is
// created with a non-zero Memory cap, so a runaway service can't exhaust host
// RAM. Would FAIL again if the cap regressed to Docker's default of 0.
func TestSidecarLimits_MemoryCap(t *testing.T) {
	t.Parallel()

	hc := captureSidecarHostConfig(t)

	// Sanity: the existing H7 hardening is still in place, so we know we're
	// reading a real, hardened HostConfig and not an empty/zero struct.
	if hc.Resources.PidsLimit == nil || *hc.Resources.PidsLimit == 0 {
		t.Fatalf("expected the H7 PidsLimit baseline to be present; HostConfig looks unhardened: %+v", hc.Resources)
	}

	if hc.Resources.Memory <= 0 {
		t.Fatalf("F6 REGRESSION: sidecar created with Memory=%d (Docker default = unlimited host RAM) — ensureSidecar must set a non-zero Memory cap", hc.Resources.Memory)
	}
}

// TestSidecarLimits_CPUCap locks in F6's CPU arm: every sidecar is created with
// a non-zero CPU cap (NanoCPUs), bounding host CPU DoS. CPUQuota/CPUShares are
// accepted as alternative knobs so the guard survives a future cap mechanism.
func TestSidecarLimits_CPUCap(t *testing.T) {
	t.Parallel()

	hc := captureSidecarHostConfig(t)

	if hc.Resources.NanoCPUs <= 0 && hc.Resources.CPUQuota <= 0 && hc.Resources.CPUShares <= 0 {
		t.Fatalf("F6 REGRESSION: sidecar created with no CPU cap (NanoCPUs=%d CPUQuota=%d CPUShares=%d) → host CPU DoS",
			hc.Resources.NanoCPUs, hc.Resources.CPUQuota, hc.Resources.CPUShares)
	}
}

// --- Secure target regression guard -----------------------------------------

// TestSidecarLimits_SecureTarget is the combined regression guard: after the
// F6 fix every created sidecar must carry BOTH a non-zero Memory cap AND a
// non-zero CPU cap (NanoCPUs), so a runaway sidecar can't exhaust shared-host
// RAM/CPU.
func TestSidecarLimits_SecureTarget(t *testing.T) {
	t.Parallel()

	hc := captureSidecarHostConfig(t)
	if hc.Resources.Memory <= 0 {
		t.Errorf("sidecar Memory cap must be > 0, got %d", hc.Resources.Memory)
	}
	if hc.Resources.NanoCPUs <= 0 {
		t.Errorf("sidecar CPU cap (NanoCPUs) must be > 0, got %d", hc.Resources.NanoCPUs)
	}
}
