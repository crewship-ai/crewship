package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

type snapshotTestCatalog struct{ fakeQuotaCatalog }

func (*snapshotTestCatalog) SnapshotNamespace() string                                 { return "local-helper" }
func (*snapshotTestCatalog) Export(context.Context, quota.Key, int64, io.Writer) error { return nil }
func (*snapshotTestCatalog) Import(_ context.Context, k quota.Key, size int64, _ io.Reader) (quota.Descriptor, error) {
	return quota.Descriptor{Key: k, Bytes: size}, nil
}

func TestQuotaSnapshotNeverRemovesAnotherInstallationsContainer(t *testing.T) {
	for _, operation := range []string{"export", "detach"} {
		for _, instance := range []string{"foreign-installation", "", "local-installation"} {
			t.Run(operation+"/"+instance, func(t *testing.T) {
				var removals atomic.Int32
				labels := resourcelifecycle.WithInstanceLabel(sidecarContainerLabels("crew", "alpha", "database", "hash"), instance)
				p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case strings.HasSuffix(r.URL.Path, "/containers/json"):
						_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "foreign-service", "State": "exited", "Labels": labels}})
					case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
						removals.Add(1)
						w.WriteHeader(http.StatusNoContent)
					case strings.HasSuffix(r.URL.Path, "/volumes"):
						_, _ = io.WriteString(w, `{"Volumes":[]}`)
					default:
						w.WriteHeader(http.StatusNotFound)
						_, _ = io.WriteString(w, `{"message":"not found"}`)
					}
				})
				defer close()
				p.cfg.InstanceID = "local-installation"
				p.cfg.QuotaCatalog = &snapshotTestCatalog{}
				p.SetServiceOperationGate(provider.ServiceOperationGate(func(context.Context, string) (func(context.Context) error, error) {
					return func(context.Context) error { return nil }, nil
				}))
				var err error
				if operation == "export" {
					err = p.ExportQuotaVolume(t.Context(), quota.Key{Crew: "crew", Service: "database", Volume: "data", Generation: 1}, 64<<20, io.Discard)
				} else {
					err = p.DetachQuotaService(t.Context(), "crew", "database")
				}
				if err != nil {
					t.Fatal(err)
				}
				if instance == "local-installation" {
					if removals.Load() != 1 {
						t.Fatalf("owned container was not removed: %d", removals.Load())
					}
				} else if removals.Load() != 0 {
					t.Fatalf("removed %d foreign or unlabelled containers", removals.Load())
				}
			})
		}
	}
}

func TestQuotaSnapshotNamespaceRequiresInstanceIdentity(t *testing.T) {
	p := &Provider{cfg: Config{QuotaCatalog: &snapshotTestCatalog{}}}
	if got := p.QuotaSnapshotNamespace(); got != "" {
		t.Fatalf("unowned namespace %q", got)
	}
	p.cfg.InstanceID = "owned"
	if got := p.QuotaSnapshotNamespace(); got != "local-helper" {
		t.Fatalf("namespace %q", got)
	}
}

type snapshotLockProbe struct {
	snapshotTestCatalog
	mu *sync.Mutex
	t  *testing.T
}

func (c *snapshotLockProbe) Release(context.Context, quota.Key, string) error {
	if c.mu.TryLock() {
		c.mu.Unlock()
		c.t.Fatal("detach released the crew lock before catalog release")
	}
	return nil
}
func (c *snapshotLockProbe) Export(context.Context, quota.Key, int64, io.Writer) error {
	if !c.mu.TryLock() {
		c.t.Fatal("stream export holds the crew lifecycle lock")
	}
	c.mu.Unlock()
	return nil
}
func (c *snapshotLockProbe) Import(_ context.Context, k quota.Key, n int64, _ io.Reader) (quota.Descriptor, error) {
	if !c.mu.TryLock() {
		c.t.Fatal("fresh generation import holds the crew lifecycle lock")
	}
	c.mu.Unlock()
	return quota.Descriptor{Key: k, Bytes: n}, nil
}
func TestQuotaSnapshotStreamingDoesNotHoldCrewLock(t *testing.T) {
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			_, _ = io.WriteString(w, `[]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	})
	defer close()
	p.cfg.InstanceID = "owned"
	p.cfg.QuotaCatalog = &snapshotLockProbe{mu: p.lockForCrew("crew"), t: t}
	p.SetServiceOperationGate(provider.ServiceOperationGate(func(context.Context, string) (func(context.Context) error, error) {
		return func(context.Context) error { return nil }, nil
	}))
	key := quota.Key{Crew: "crew", Service: "database", Volume: "data", Generation: 1}
	if err := p.ExportQuotaVolume(t.Context(), key, 64<<20, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := p.ImportQuotaVolume(t.Context(), key, 64<<20, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
}
