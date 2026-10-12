package docker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func TestCleanupRuntimePreservesOwnershipAndMountEvidence(t *testing.T) {
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			if r.URL.Query().Get("all") != "1" && r.URL.Query().Get("all") != "true" {
				t.Error("stopped containers omitted")
			}
			_, _ = w.Write([]byte(`[{"Id":"owned","Labels":{"crewship.instance-id":"install","crewship.crew-id":"crew","crewship.kind":"crew"}},{"Id":"legacy","Labels":{}}]`))
		case strings.HasSuffix(r.URL.Path, "/containers/owned/json"):
			_, _ = w.Write([]byte(`{"Id":"owned","Config":{"Labels":{"crewship.instance-id":"install","crewship.crew-id":"crew","crewship.kind":"crew"}},"Mounts":[{"Type":"volume","Name":"data","Source":"/volumes/data","Destination":"/data"}]}`))
		case strings.HasSuffix(r.URL.Path, "/containers/owned/stop"):
			if r.Method != "POST" || r.URL.Query().Get("t") != "10" {
				t.Errorf("unexpected stop: %s %s", r.Method, r.URL)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	})
	defer close()
	runtime := &cleanupRuntime{client: p.client}
	rows, err := runtime.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []resourcelifecycle.Container{{ID: "owned", InstanceID: "install", CrewID: "crew", Kind: "crew"}, {ID: "legacy"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("ownership evidence=%+v", rows)
	}
	inspected, err := runtime.Inspect(t.Context(), "owned")
	if err != nil {
		t.Fatal(err)
	}
	want[0].Mounts = []resourcelifecycle.Mount{{Type: "volume", Name: "data", Source: "/volumes/data", Destination: "/data"}}
	if !reflect.DeepEqual(inspected, want[0]) {
		t.Fatalf("mount evidence=%+v", inspected)
	}
	if err := runtime.Stop(t.Context(), "owned"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRuntimeDistinguishesGoneContainersFromDaemonOutage(t *testing.T) {
	for _, status := range []int{404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"unavailable"}`))
			})
			defer close()
			runtime := &cleanupRuntime{client: p.client}
			if _, err := runtime.List(t.Context()); err == nil {
				t.Fatal("failed listing became empty success")
			}
			_, inspectErr := runtime.Inspect(t.Context(), "gone")
			for name, err := range map[string]error{"inspect": inspectErr, "stop": runtime.Stop(t.Context(), "gone"), "remove": runtime.Remove(t.Context(), "gone")} {
				if err == nil || errors.Is(err, resourcelifecycle.ErrNotFound) != (status == 404) {
					t.Fatalf("%s status=%d error=%v", name, status, err)
				}
			}
		})
	}
}

func TestExecPIDReportsHostPIDAndPropagatesInspectionFailure(t *testing.T) {
	for _, tc := range []struct {
		body        string
		status, pid int
	}{{`{"Pid":321}`, 200, 321}, {`{"Pid":0}`, 200, 0}, {`{"message":"gone"}`, 404, 0}} {
		p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/exec/run/json") {
				t.Errorf("wrong endpoint: %s", r.URL)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		pid, err := p.ExecPID(t.Context(), "run")
		close()
		if pid != tc.pid || (err != nil) != (tc.status != 200) {
			t.Fatalf("PID=%d error=%v", pid, err)
		}
	}
}

func TestOwnerCheckWiringRejectsWorkBeforeDaemonAccess(t *testing.T) {
	denied := errors.New("owner removed")
	p := &Provider{}
	p.SetCrewOwnerCheck(func(_ context.Context, id string) error {
		if id != "crew" {
			t.Errorf("wrong owner: %s", id)
		}
		return denied
	})
	if _, err := p.EnsureCrewRuntime(t.Context(), provider.CrewConfig{ID: "crew"}); !errors.Is(err, denied) {
		t.Fatalf("runtime admission=%v", err)
	}
	if _, err := p.EnsureCrewServices(t.Context(), provider.CrewConfig{ID: "crew"}); !errors.Is(err, denied) {
		t.Fatalf("service admission=%v", err)
	}
}

func TestCleanupRuntimeConstructionOnlyProbesDaemon(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(map[bool]string{true: "available", false: "unavailable"}[available], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" && r.Method != "HEAD" {
					t.Errorf("cleanup construction mutated daemon: %s %s", r.Method, r.URL)
				}
				if !available {
					http.Error(w, "daemon unavailable", 500)
					return
				}
				w.Header().Set("API-Version", "1.47")
				if strings.HasSuffix(r.URL.Path, "/_ping") {
					_, _ = w.Write([]byte("OK"))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/version") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"Version":"28.0","ApiVersion":"1.47"}`))
					return
				}
				t.Errorf("unexpected construction request: %s", r.URL)
				w.WriteHeader(500)
			}))
			defer srv.Close()
			t.Setenv("DOCKER_HOST", srv.URL)
			t.Setenv("DOCKER_TLS_VERIFY", "")
			t.Setenv("DOCKER_CERT_PATH", "")
			t.Setenv("DOCKER_API_VERSION", "1.47")
			runtime, err := NewCleanupRuntime(t.Context())
			if !available {
				if err == nil || runtime != nil {
					t.Fatalf("unavailable daemon accepted: %v %v", runtime, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type removalCatalog struct {
	fakeQuotaCatalog
	key   quota.Key
	calls int
	err   error
}

func (c *removalCatalog) Remove(_ context.Context, key quota.Key) error {
	c.key = key
	c.calls++
	return c.err
}
func TestQuotaRemovalRequiresWiringAndPreservesCatalogRefusal(t *testing.T) {
	key := quota.Key{Crew: "crew", Service: "database", Volume: "data", Generation: 2}
	p := &Provider{}
	if err := p.RemoveQuotaVolume(t.Context(), key); !errors.Is(err, quota.ErrUnavailable) {
		t.Fatalf("unwired gate=%v", err)
	}
	p.SetServiceOperationGate(provider.ServiceOperationGate(func(context.Context, string) (func(context.Context) error, error) {
		return func(context.Context) error { return nil }, nil
	}))
	if err := p.RemoveQuotaVolume(t.Context(), key); !errors.Is(err, quota.ErrUnavailable) {
		t.Fatalf("unwired catalog=%v", err)
	}
	c := &removalCatalog{err: quota.ErrDenied}
	p.cfg.QuotaCatalog = c
	if err := p.RemoveQuotaVolume(t.Context(), key); !errors.Is(err, quota.ErrDenied) {
		t.Fatalf("catalog refusal=%v", err)
	}
	if c.key != key || c.calls != 1 {
		t.Fatalf("wrong removal authority: %+v calls=%d", c.key, c.calls)
	}
	c.err = nil
	if err := p.RemoveQuotaVolume(t.Context(), key); err != nil {
		t.Fatal(err)
	}
}
