package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

func TestImageChangePreservesRunningCrew(t *testing.T) {
	crew := provider.CrewConfig{ID: "safe-image", Slug: "safe-image", CachedImage: "crewship-cache:replacement"}
	p, calls := newDriftFixture(t, "crewship-team-safe-image-safe-image", "crewship-cache:previous")
	_, err := p.EnsureCrewRuntime(context.Background(), crew)
	if !errors.Is(err, provider.ErrRuntimeImageUpdatePending) {
		t.Fatalf("running image change was not refused: %v", err)
	}
	if got := calls.snapshot(); len(got) != 0 {
		t.Fatalf("pending update mutated a running runtime: %v", got)
	}
}

// Confirmed restart backoff is covered separately by
// TestRuntimeUsePreservesRestartBackoffRecovery: it must remain recoverable.
func TestImageChangeRejectsUncertainOrBusyState(t *testing.T) {
	for _, state := range []map[string]any{nil, {}, {"Status": "paused", "Paused": true}, {"Status": "exited", "Running": true}, {"Status": "dead"}} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			p, calls := newDriftFixtureState(t, "crewship-team-safe-safe", "old", state)
			_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "safe", Slug: "safe", Image: "new"})
			if !errors.Is(err, provider.ErrRuntimeImageUpdatePending) || len(calls.snapshot()) != 0 {
				t.Fatalf("state allowed destructive update: %v %v", err, calls.snapshot())
			}
		})
	}
}

func TestImageChangeDoesNotTrustWarmCacheForAnotherImage(t *testing.T) {
	p, calls := newDriftFixture(t, "crewship-team-safe-safe", "old")
	crew := provider.CrewConfig{ID: "safe", Slug: "safe", Image: "old"}
	if _, err := p.EnsureCrewRuntime(context.Background(), crew); err != nil {
		t.Fatal(err)
	}
	if _, warm := p.warmHit(crew.ID, "old"); !warm {
		t.Fatal("fixture did not warm runtime")
	}
	crew.Image = "new"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.EnsureCrewRuntime(context.Background(), crew); !errors.Is(err, provider.ErrRuntimeImageUpdatePending) {
				t.Errorf("new-image admission=%v", err)
			}
		}()
	}
	wg.Wait()
	if len(calls.snapshot()) != 0 {
		t.Fatalf("concurrent image change destroyed runtime: %v", calls.snapshot())
	}
}

func TestImageChangeRemovalCannotForceAConcurrentStart(t *testing.T) {
	var deletes, stops int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "DELETE":
			deletes++
			if force := r.URL.Query().Get("force"); force == "true" || force == "1" {
				t.Error("image update used force removal")
			}
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"message":"container started after inspection"}`)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stops++
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "existing", "Names": []string{"/fixture"}, "State": "exited"}})
		case strings.HasSuffix(r.URL.Path, "/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "existing", "State": map[string]any{"Status": "exited", "Running": false}, "Config": map[string]any{"Image": "old"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	cli, err := client.New(client.WithHost(srv.URL), client.WithAPIVersion("1.43"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	p := &Provider{client: cli, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, _, err = p.reconcileExistingContainer(context.Background(), provider.CrewConfig{ID: "safe"}, "fixture", "new", true, func(devcontainer.ProvisionEvent) {})
	if !errors.Is(err, provider.ErrRuntimeImageUpdatePending) || deletes != 1 || stops != 0 {
		t.Fatalf("start race mishandled: err=%v deletes=%d stops=%d", err, deletes, stops)
	}
}
