package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

type fakeQuotaCatalog struct {
	unavailable bool
	verify      int
	owners      []quota.Owner
	calls       []string
	removeErr   error
	releaseErr  error
	trace       *[]string
}

func (f *fakeQuotaCatalog) record(op string) {
	f.calls = append(f.calls, op)
	if f.trace != nil {
		*f.trace = append(*f.trace, "catalog-"+op)
	}
}

func (f *fakeQuotaCatalog) descriptor(k quota.Key, n int64) (quota.Descriptor, error) {
	if f.unavailable {
		return quota.Descriptor{}, quota.ErrUnavailable
	}
	return quota.Descriptor{ID: "syntheticquota", Key: k, Bytes: n, Mount: "/trusted-quota/syntheticquota"}, nil
}
func (f *fakeQuotaCatalog) Ensure(_ context.Context, k quota.Key, n int64, owner quota.Owner) (quota.Descriptor, error) {
	f.owners = append(f.owners, owner)
	f.calls = append(f.calls, "ensure")
	return f.descriptor(k, n)
}
func (f *fakeQuotaCatalog) Verify(_ context.Context, k quota.Key, n int64) (quota.Descriptor, error) {
	f.verify++
	return f.descriptor(k, n)
}
func (f *fakeQuotaCatalog) Remove(context.Context, quota.Key) error {
	f.record("remove")
	return f.removeErr
}
func (*fakeQuotaCatalog) Recover(context.Context) error { return nil }
func TestQuotaServiceUnavailableNeverUsesLegacyStorage(t *testing.T) {
	svc := provider.CrewService{Name: "database", Image: "alpine:3", QuotaEnforced: true, Volumes: []provider.CrewServiceVolume{{Name: "data", Mount: "/data", QuotaBytes: 64 << 20}}}
	for _, catalog := range []quota.Catalog{nil, &fakeQuotaCatalog{unavailable: true}} {
		daemon := newFakeQuotaDaemon(t)
		p := newCovProvider(t, Config{QuotaCatalog: catalog}, daemon.ServeHTTP)
		if _, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", &svc); err == nil {
			t.Fatal("missing quota helper permitted service")
		}
		if daemon.mutatingCalls() != 0 {
			t.Fatal("quota denial mutated Docker or created unlimited volume")
		}
	}
}
func TestQuotaServiceOwnedVolumeAndReadOnlyRoot(t *testing.T) {
	svc := provider.CrewService{Name: "database", Image: "alpine:3", QuotaEnforced: true, Volumes: []provider.CrewServiceVolume{{Name: "data", Mount: "/data", QuotaBytes: 64 << 20}}}
	catalog := &fakeQuotaCatalog{}
	sawCreate := false
	rootReadOnly := false
	p := newCovProvider(t, Config{QuotaCatalog: catalog}, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/info"):
			_, _ = w.Write([]byte(`{"SwapLimit":true,"PidsLimit":true}`))
		case strings.Contains(path, "/volumes/") && r.Method == http.MethodGet:
			http.Error(w, `{"message":"not found"}`, 404)
		case strings.HasSuffix(path, "/volumes/create"):
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["Driver"] != "local" {
				t.Error("unexpected volume driver")
			}
			options := req["DriverOpts"].(map[string]any)
			if options["device"] != "/trusted-quota/syntheticquota" || options["o"] != "bind" {
				t.Error("volume not helper catalog bind")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"Name": "crewship-quota-syntheticquota"})
		case strings.HasSuffix(path, "/containers/json"):
			_, _ = w.Write([]byte("[]"))
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "image", "Config": map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}})
		case strings.HasSuffix(path, "/images/create"):
			_, _ = w.Write([]byte("{}"))
		case strings.HasSuffix(path, "/containers/create"):
			sawCreate = true
			var req container.CreateRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			rootReadOnly = req.HostConfig.ReadonlyRootfs
			if err := checkServiceQuotaProfile(req.HostConfig); err != nil {
				t.Error(err)
			}
			if len(req.HostConfig.Mounts) != 1 || req.HostConfig.Mounts[0].Source != "crewship-quota-syntheticquota" || !req.HostConfig.Mounts[0].VolumeOptions.NoCopy {
				t.Error("unclassified persistent mount")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": "quota-service"})
		case strings.HasSuffix(path, "/start"):
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, path)
			w.WriteHeader(500)
		}
	})
	if _, err := p.ensureSidecar(context.Background(), covCrewID, "alpha", &svc); err != nil {
		t.Fatal(err)
	}
	if !sawCreate || !rootReadOnly || catalog.verify != 1 {
		t.Fatalf("missing enforcement create%v readonly%v verify%d", sawCreate, rootReadOnly, catalog.verify)
	}
}
func TestQuotaServiceNoPathOrLegacyQuotaAliasing(t *testing.T) {
	for _, v := range []provider.CrewServiceVolume{{Name: "data", Mount: "/tmp/escape", QuotaBytes: 64 << 20}, {Name: "data", Mount: "/run", QuotaBytes: 64 << 20}, {Name: "data", Mount: "/", QuotaBytes: 64 << 20}, {Name: "data", Mount: "/data", QuotaBytes: 1}, {Name: "../host", Mount: "/data", QuotaBytes: 64 << 20}} {
		svc := provider.CrewService{Name: "database", QuotaEnforced: true, Volumes: []provider.CrewServiceVolume{v}}
		if !errors.Is(validateQuotaService(&svc), quota.ErrDenied) {
			t.Fatalf("unsafe quota accepted %+v", v)
		}
	}
}

func (f *fakeQuotaCatalog) Protect(context.Context, quota.Key, string) error {
	f.calls = append(f.calls, "protect")
	return nil
}
func (f *fakeQuotaCatalog) Release(context.Context, quota.Key, string) error {
	f.record("release")
	return f.releaseErr
}

func TestQuotaMountAuditRejectsUnboundedDrift(t *testing.T) {
	bounded := mount.Mount{Type: mount.TypeVolume, Source: "owned", Target: "/data", VolumeOptions: &mount.VolumeOptions{NoCopy: true}}
	if err := checkQuotaMounts([]mount.Mount{bounded}, []mount.Mount{bounded}); err != nil {
		t.Fatal(err)
	}
	cases := [][]mount.Mount{nil, {bounded, {Type: mount.TypeBind, Source: "/host", Target: "/extra"}}, {{Type: mount.TypeBind, Source: "/host", Target: "/data"}}, {{Type: mount.TypeVolume, Source: "anonymous", Target: "/data", VolumeOptions: &mount.VolumeOptions{NoCopy: true}}}}
	for _, got := range cases {
		if err := checkQuotaMounts(got, []mount.Mount{bounded}); err == nil {
			t.Fatalf("unbounded drift admitted: %+v", got)
		}
	}
}
