package docker

// Image-drift regression for EnsureCrewRuntime.
//
// Before this fix, EnsureCrewRuntime found the running team container
// by name, checked its mounts, and — if everything looked OK — short-
// circuited with `return c.ID, nil`. It never compared the running
// image to the image the manifest now wants. The fallout in the field
// was that a `crewship crew provision <slug>` rebuild produced a fresh
// image tag, but the old container kept running with the OLD code; the
// operator's only workaround was `docker rm -f crewship-<n>-team-<slug>`.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/docker/docker/client"
)

// driftCallLog records the verbs+IDs the fake daemon sees, in order.
// The drift test only needs to know whether a remove happened on the
// stale container ID before a create happened; finer-grained matching
// (timeouts, force flags) is left to the integration tier.
type driftCallLog struct {
	mu    sync.Mutex
	calls []string
}

func (d *driftCallLog) add(s string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, s)
}

func (d *driftCallLog) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make([]string, len(d.calls))
	copy(cp, d.calls)
	return cp
}

// newDriftFixture stands up a fake daemon that pretends one team
// container already exists under `containerName`, running image
// `runningImage`. EnsureCrewRuntime should see the mismatch against
// the desired image, stop+remove the stale ID, then create a new one.
func newDriftFixture(t *testing.T, containerName, runningImage string) (*Provider, *driftCallLog) {
	t.Helper()
	calls := &driftCallLog{}
	const staleID = "stale-cid-0123456789ab"

	handler := func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		switch {
		// GET /v*/containers/json — return ONE matching container so the
		// existing-container loop fires.
		case strings.HasSuffix(path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"Id":    staleID,
					"Names": []string{"/" + containerName},
					"State": "running",
					"Image": runningImage,
				},
			})

		// GET /v*/containers/{id}/json — full inspect. Returns Mounts the
		// drift path expects to be "complete" (so we exercise the image
		// branch, not the missing-mount branch).
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":    staleID,
				"State": map[string]any{"Status": "running"},
				"Config": map[string]any{
					"Image": runningImage,
				},
				"Mounts": []map[string]any{
					{"Destination": "/crew"},
					{"Destination": "/home/agent"},
					{"Destination": "/opt/crew-tools"},
				},
			})

		// Stop the stale container.
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/stop"):
			calls.add(fmt.Sprintf("stop %s", extractContainerID(path)))
			w.WriteHeader(http.StatusNoContent)

		// Remove the stale container.
		case strings.Contains(path, "/containers/") && r.Method == http.MethodDelete:
			calls.add(fmt.Sprintf("remove %s", extractContainerID(path)))
			w.WriteHeader(http.StatusNoContent)

		// Create the replacement container.
		case strings.HasSuffix(path, "/containers/create"):
			calls.add("create")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "new-cid-0123456789ab"})

		// Volumes — accept blindly so the create path can proceed.
		case strings.HasSuffix(path, "/volumes/create"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "fake-volume"})

		// Network list — pretend the configured network exists.
		case strings.HasSuffix(path, "/networks"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Name": "crewship-test-net", "Id": "net-existing"},
			})

		// Image inspect — say the image is local so ensureImage doesn't pull.
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:fakeimg",
				"RepoDigests": []string{},
				"Config":      map[string]any{"Env": []string{}},
			})

		// Container start — accept.
		case strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)

		// Exec create / inspect — needed for the post-create hooks.
		case strings.HasSuffix(path, "/exec") && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "fake-exec-id"})

		case strings.Contains(path, "/exec/") && strings.HasSuffix(path, "/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Running":  false,
				"ExitCode": 0,
			})

		default:
			w.WriteHeader(http.StatusOK)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(handler))

	cli, err := client.NewClientWithOpts(
		client.WithHost(srv.URL),
		client.WithVersion("1.43"),
	)
	if err != nil {
		srv.Close()
		t.Fatalf("docker client: %v", err)
	}

	p := &Provider{
		client: cli,
		cfg: Config{
			RuntimeImage:      "default/runtime:latest",
			Network:           "crewship-test-net",
			OutputBasePath:    t.TempDir(),
			SidecarBinaryPath: "/fake/sidecar",
			EntrypointPath:    "/fake/entrypoint.sh",
		},
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		digestResolver: dockerutil.NewDigestResolver(0, 0),
	}

	t.Cleanup(func() {
		_ = cli.Close()
		srv.Close()
	})

	return p, calls
}

// extractContainerID pulls the ID out of /v*/containers/{id}/... so
// the call log can encode which container was acted on.
func extractContainerID(path string) string {
	const marker = "/containers/"
	idx := strings.Index(path, marker)
	if idx < 0 {
		return ""
	}
	rest := path[idx+len(marker):]
	if slash := strings.Index(rest, "/"); slash >= 0 {
		return rest[:slash]
	}
	return rest
}

// TestEnsureCrewRuntime_RecreatesOnImageDrift is the load-bearing
// regression test for the fix: a running container whose image tag
// no longer matches the manifest's desired image MUST be torn down
// and recreated. Before the fix the call log was just "create" (the
// short-circuit returned the stale ID without any remove), the test
// here asserts the remove-then-create order so an accidental revert
// would fail loudly instead of silently leaking stale containers.
func TestEnsureCrewRuntime_RecreatesOnImageDrift(t *testing.T) {
	t.Parallel()

	const slug = "eng"
	const oldImg = "crewship-cache:OLD-sha"
	const newImg = "crewship-cache:NEW-sha"

	p, calls := newDriftFixture(t, "crewship-team-"+slug, oldImg)

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:          "crew-id-1",
		Slug:        slug,
		MemoryMB:    1024,
		CPUs:        1.0,
		CachedImage: newImg, // post-provision rebuild handed us a new tag
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	snap := calls.snapshot()
	// Three things must hold:
	//   1. the stale container ID was removed,
	//   2. a fresh one was created,
	//   3. (1) happened BEFORE (2).
	// Presence alone (the pre-CodeRabbit assertion) was too loose:
	// a regression that called ContainerCreate first and then
	// ContainerRemove on the stale ID would still satisfy it,
	// leaving Docker briefly with two containers under the same
	// name (one of which the daemon would have rejected, but that
	// rejection happens at a different layer the test doesn't see).
	// Stop is still best-effort — the production code ignores its
	// error — so we don't require it in the ordering check, only
	// remove + create.
	removeIdx := -1
	createIdx := -1
	for i, c := range snap {
		if removeIdx == -1 && strings.HasPrefix(c, "remove ") && strings.Contains(c, "stale-cid") {
			removeIdx = i
		}
		if createIdx == -1 && c == "create" {
			createIdx = i
		}
	}
	if removeIdx < 0 {
		t.Errorf("expected the stale container to be removed; got call log: %v", snap)
	}
	if createIdx < 0 {
		t.Errorf("expected a ContainerCreate after the remove; got call log: %v", snap)
	}
	if removeIdx >= 0 && createIdx >= 0 && removeIdx > createIdx {
		t.Errorf("remove (idx=%d) must happen before create (idx=%d); got call log: %v",
			removeIdx, createIdx, snap)
	}
}

// TestEnsureCrewRuntime_NoRecreateOnSameImage is the negative
// half: when the running container's image already matches what
// the manifest wants, EnsureCrewRuntime must short-circuit
// without any remove/create churn. This pins the no-op path so
// a future change to the drift check can't accidentally turn
// every dispatch into a container rebuild.
func TestEnsureCrewRuntime_NoRecreateOnSameImage(t *testing.T) {
	t.Parallel()

	const slug = "eng"
	const img = "crewship-cache:SAME-sha"

	p, calls := newDriftFixture(t, "crewship-team-"+slug, img)

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID:          "crew-id-1",
		Slug:        slug,
		MemoryMB:    1024,
		CPUs:        1.0,
		CachedImage: img, // matches what's running
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if id != "stale-cid-0123456789ab" {
		t.Errorf("expected the existing container ID to be reused, got %q", id)
	}
	for _, c := range calls.snapshot() {
		if strings.HasPrefix(c, "remove ") || c == "create" {
			t.Errorf("no remove/create expected when image is identical; got %q", c)
		}
	}
}
