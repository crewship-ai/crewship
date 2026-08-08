package docker

// #1845 — the detection half of "this crew's image is behind".
//
// The interesting property is NOT "different digests read as behind"; it is
// that every way of NOT KNOWING reads as not-behind, with a reason. A
// freshness check that treats an unreachable registry, an air-gapped host or a
// locally built cache image as stale fires on the majority of self-hosted
// installs and gets muted inside a week — at which point the real signal is
// gone with it. That is the failure mode this file pins.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

const (
	freshRemoteDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	freshLocalDigest  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// TestClassifyImageDrift pins the classifier that decides what counts as
// "behind". Every unknown must fail OPEN (not behind) and must say why.
func TestClassifyImageDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		running    string
		resolved   string
		wantBehind bool
		wantReason string
	}{
		{
			name:     "identical digests are current, with no reason to explain",
			running:  freshRemoteDigest,
			resolved: freshRemoteDigest,
		},
		{
			name:       "both known and different is the one true positive",
			running:    freshLocalDigest,
			resolved:   freshRemoteDigest,
			wantBehind: true,
		},
		{
			name:       "registry unreachable is not evidence of staleness",
			running:    freshLocalDigest,
			resolved:   "",
			wantReason: reasonRegistryUnreachable,
		},
		{
			name:       "an image with no registry digest has nothing to compare",
			running:    "",
			resolved:   freshRemoteDigest,
			wantReason: reasonNoRunningDigest,
		},
		{
			name:       "neither side known blames the registry, the outer cause",
			running:    "",
			resolved:   "",
			wantReason: reasonRegistryUnreachable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			behind, reason := classifyImageDrift(tc.running, tc.resolved)
			if behind != tc.wantBehind {
				t.Errorf("behind = %v, want %v", behind, tc.wantBehind)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestCrewImageState_ReportsDriftFromTheLiveContainer drives the real method
// against a fake daemon + fake registry: the container was created from
// freshLocalDigest, the registry now serves freshRemoteDigest.
func TestCrewImageState_ReportsDriftFromTheLiveContainer(t *testing.T) {
	t.Parallel()

	d := &freshnessDaemon{present: true, running: true, repoDigest: freshLocalDigest}
	p, ref := newCovImageProvider(t, freshRemoteDigest, d.handler)
	d.name.Store(p.CrewContainerName("crew_1", "alpha"))
	d.repo.Store(refRepo(ref))

	st, err := p.CrewImageState(context.Background(), crewCfg(ref))
	if err != nil {
		t.Fatalf("CrewImageState: %v", err)
	}
	if !st.Behind {
		t.Errorf("Behind = false (reason %q), want true — the container carries %s and the tag now resolves to %s",
			st.Reason, freshLocalDigest, freshRemoteDigest)
	}
	if st.RunningDigest != freshLocalDigest {
		t.Errorf("RunningDigest = %q, want %q", st.RunningDigest, freshLocalDigest)
	}
	if st.ResolvedDigest != freshRemoteDigest {
		t.Errorf("ResolvedDigest = %q, want %q", st.ResolvedDigest, freshRemoteDigest)
	}
	if !st.Running {
		t.Error("Running = false, want true")
	}
}

// TestCrewImageState_MatchingDigestIsCurrent is the negative half of the same
// wire path: same fixture, but the container already carries what the registry
// serves. Without it the test above passes for a method that returns
// Behind=true unconditionally.
func TestCrewImageState_MatchingDigestIsCurrent(t *testing.T) {
	t.Parallel()

	d := &freshnessDaemon{present: true, running: true, repoDigest: freshRemoteDigest}
	p, ref := newCovImageProvider(t, freshRemoteDigest, d.handler)
	d.name.Store(p.CrewContainerName("crew_1", "alpha"))
	d.repo.Store(refRepo(ref))

	st, err := p.CrewImageState(context.Background(), crewCfg(ref))
	if err != nil {
		t.Fatalf("CrewImageState: %v", err)
	}
	if st.Behind {
		t.Errorf("Behind = true for a container already on %s", freshRemoteDigest)
	}
	if st.Reason != "" {
		t.Errorf("Reason = %q, want empty — the digests were compared and agreed", st.Reason)
	}
}

// TestCrewImageState_NoContainerIsNotBehind: a crew with nothing running
// cannot be running a stale image — the next start ensures the image first.
// Reporting it as behind would light up every idle crew on the instance.
func TestCrewImageState_NoContainerIsNotBehind(t *testing.T) {
	t.Parallel()

	d := &freshnessDaemon{}
	p, ref := newCovImageProvider(t, freshRemoteDigest, d.handler)
	d.name.Store("unused")
	d.repo.Store(refRepo(ref))

	st, err := p.CrewImageState(context.Background(), crewCfg(ref))
	if err != nil {
		t.Fatalf("CrewImageState: %v", err)
	}
	if st.Behind {
		t.Error("Behind = true for a crew with no container, want false")
	}
	if st.Reason != reasonNoContainer {
		t.Errorf("Reason = %q, want %q", st.Reason, reasonNoContainer)
	}
	if st.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty", st.ContainerID)
	}
}

// TestCrewImageState_UnreachableRegistryIsNotBehind is the air-gap invariant.
// registryDigest="" makes every manifest HEAD 404, exactly as an offline host
// behaves.
func TestCrewImageState_UnreachableRegistryIsNotBehind(t *testing.T) {
	t.Parallel()

	d := &freshnessDaemon{present: true, running: true, repoDigest: freshLocalDigest}
	p, ref := newCovImageProvider(t, "", d.handler)
	d.name.Store(p.CrewContainerName("crew_1", "alpha"))
	d.repo.Store(refRepo(ref))

	st, err := p.CrewImageState(context.Background(), crewCfg(ref))
	if err != nil {
		t.Fatalf("CrewImageState: %v", err)
	}
	if st.Behind {
		t.Error("Behind = true with an unreachable registry — an air-gapped host would alert on every crew, forever")
	}
	if st.Reason != reasonRegistryUnreachable {
		t.Errorf("Reason = %q, want %q", st.Reason, reasonRegistryUnreachable)
	}
}

// TestCrewImageState_LocalCacheImageIsExempt: crewship-cache:<hash> images are
// built and committed locally and exist in NO registry, so a HEAD against them
// can only ever fail. ensureImage short-circuits them for exactly that reason;
// the freshness check has to as well, or every provisioned crew on the
// instance reports "registry unreachable" forever.
func TestCrewImageState_LocalCacheImageIsExempt(t *testing.T) {
	t.Parallel()

	d := &freshnessDaemon{present: true, running: true, repoDigest: freshLocalDigest}
	p, ref := newCovImageProvider(t, freshRemoteDigest, d.handler)
	d.name.Store(p.CrewContainerName("crew_1", "alpha"))
	d.repo.Store(refRepo(ref))

	team := crewCfg(ref)
	team.CachedImage = localCacheImagePrefix + "abc123"

	st, err := p.CrewImageState(context.Background(), team)
	if err != nil {
		t.Fatalf("CrewImageState: %v", err)
	}
	if st.Behind {
		t.Error("Behind = true for a local cache image, want false")
	}
	if st.Reason != reasonLocalImage {
		t.Errorf("Reason = %q, want %q", st.Reason, reasonLocalImage)
	}
}

// TestRefreshCrewImage_DropsTheContainerSoTheNextStartRebuilds. The refresh is
// only useful if something actually changes: the pull alone leaves the running
// container on the old manifest.
func TestRefreshCrewImage_DropsTheContainerSoTheNextStartRebuilds(t *testing.T) {
	t.Parallel()

	d := &freshnessDaemon{present: true, running: true, repoDigest: freshLocalDigest}
	p, ref := newCovImageProvider(t, freshRemoteDigest, d.handler)
	d.name.Store(p.CrewContainerName("crew_1", "alpha"))
	d.repo.Store(refRepo(ref))

	res, err := p.RefreshCrewImage(context.Background(), crewCfg(ref))
	if err != nil {
		t.Fatalf("RefreshCrewImage: %v", err)
	}
	if !res.ContainerRemoved {
		t.Error("ContainerRemoved = false — a pull that leaves the old container running changes nothing an operator can see")
	}
	if d.removes.Load() != 1 {
		t.Errorf("ContainerRemove calls = %d, want 1", d.removes.Load())
	}
	if res.PreviousDigest != freshLocalDigest {
		t.Errorf("PreviousDigest = %q, want %q", res.PreviousDigest, freshLocalDigest)
	}
	if res.NewDigest != freshRemoteDigest {
		t.Errorf("NewDigest = %q, want %q", res.NewDigest, freshRemoteDigest)
	}
}

// TestRefreshCrewImage_NoContainerRemovesNothing — refreshing an idle crew
// must still pull (so the next start is instant) but has nothing to drop, and
// must not report that it dropped one.
func TestRefreshCrewImage_NoContainerRemovesNothing(t *testing.T) {
	t.Parallel()

	d := &freshnessDaemon{}
	p, ref := newCovImageProvider(t, freshRemoteDigest, d.handler)
	d.name.Store("unused")
	d.repo.Store(refRepo(ref))

	res, err := p.RefreshCrewImage(context.Background(), crewCfg(ref))
	if err != nil {
		t.Fatalf("RefreshCrewImage: %v", err)
	}
	if res.ContainerRemoved {
		t.Error("ContainerRemoved = true with no container present")
	}
	if d.removes.Load() != 0 {
		t.Errorf("ContainerRemove calls = %d, want 0", d.removes.Load())
	}
}

// ---- fixtures ----

func crewCfg(image string) provider.CrewConfig {
	return provider.CrewConfig{ID: "crew_1", Slug: "alpha", Image: image}
}

// refRepo strips the tag off "host/repo:tag" so RepoDigests can be qualified
// with the repository LocalRepoDigest matches on.
func refRepo(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i]
	}
	return ref
}

// freshnessDaemon fakes the daemon calls the freshness path makes:
// ContainerList, ContainerInspect, ImageInspect, ImagePull and ContainerRemove.
// Registry HEADs are answered by newCovImageProvider ahead of this handler.
type freshnessDaemon struct {
	present    bool   // a container exists for this crew
	running    bool   //
	repoDigest string // "" = the image reports no RepoDigests at all

	name    atomic.Value // container name, set after the provider exists
	repo    atomic.Value // repository the RepoDigests are qualified with
	removes atomic.Int32
}

func (d *freshnessDaemon) handler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodDelete && strings.Contains(path, "/containers/"):
		d.removes.Add(1)
		w.WriteHeader(http.StatusNoContent)

	case strings.HasSuffix(path, "/containers/json"):
		var items []map[string]any
		if d.present {
			state := "exited"
			if d.running {
				state = "running"
			}
			items = append(items, map[string]any{
				"Id":    "ctr_1",
				"Names": []string{"/" + d.name.Load().(string)},
				"State": state,
			})
		}
		writeFreshnessJSON(w, items)

	case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/json"):
		writeFreshnessJSON(w, map[string]any{
			"Id":     "ctr_1",
			"Image":  "img_local",
			"State":  map[string]any{"Running": d.running},
			"Config": map[string]any{"Image": "img_local"},
		})

	case strings.HasSuffix(path, "/images/create"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"Download complete"}`))

	case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
		if d.repoDigest == "" {
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
			return
		}
		writeFreshnessJSON(w, map[string]any{
			"Id":          "img_local",
			"RepoDigests": []string{d.repo.Load().(string) + "@" + d.repoDigest},
		})

	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}
}

func writeFreshnessJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
