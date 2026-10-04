package docker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

const idleInspect = `{"Id":"idle-id","Image":"sha256:img","State":{"Status":"exited","FinishedAt":"2026-09-20T10:00:00.5Z"},
"Config":{"Labels":{"crewship.crew-id":"crew-a","crewship.kind":"crew","crewship.instance-id":"installation-a"}},
"Mounts":[{"Type":"volume","Name":"crewship-home-a","Destination":"/home/agent"}]}`

// The removal re-inspects under the crew lock, removes without force or
// volumes, and forgets the warm-crew fact so the next start re-reconciles.
func TestRemoveIdleRuntimeKeepsVolumesFakeAPI(t *testing.T) {
	var deletes atomic.Int32
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/idle-id/json"):
			w.Write([]byte(idleInspect))
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/idle-id"):
			q := r.URL.Query()
			if q.Get("v") == "1" || q.Get("v") == "true" || q.Get("force") == "1" || q.Get("force") == "true" {
				t.Errorf("destructive remove flags: %s", r.URL.RawQuery)
			}
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	defer close()
	p.warmCrew.Store("crew-a", "stale")
	var seen resourcelifecycle.RetentionContainer
	err := p.RemoveIdleRuntime(context.Background(), "idle-id", "crew-a", func(c resourcelifecycle.RetentionContainer) error {
		if !p.lockForCrew("crew-a").TryLock() {
			seen = c
			return nil
		}
		t.Error("verify ran without the crew start lock held")
		return errors.New("unlocked")
	})
	if err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 1 {
		t.Fatalf("deletes=%d", deletes.Load())
	}
	if seen.FinishedAt.IsZero() || seen.State != "exited" || len(seen.Mounts) != 1 {
		t.Fatalf("fresh inspect not passed to verify: %+v", seen)
	}
	if _, ok := p.warmCrew.Load("crew-a"); ok {
		t.Fatal("warm-crew fact survived the removal")
	}
}

func TestRemoveIdleRuntimeVetoMakesNoMutationFakeAPI(t *testing.T) {
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("mutation after veto: %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(idleInspect))
	})
	defer close()
	veto := errors.New("started meanwhile")
	if err := p.RemoveIdleRuntime(context.Background(), "idle-id", "crew-a", func(resourcelifecycle.RetentionContainer) error { return veto }); !errors.Is(err, veto) {
		t.Fatalf("err=%v", err)
	}
}

// Docker's zero FinishedAt means "never stopped"; retention must read it as
// unknown, not as stopped since year 1.
func TestInspectContainerZeroFinishedAtIsUnknownFakeAPI(t *testing.T) {
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Id":"c","Image":"sha256:i","State":{"Status":"created","FinishedAt":"0001-01-01T00:00:00Z"},"Config":{"Labels":{}}}`))
	})
	defer close()
	c, err := p.InspectContainer(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if !c.FinishedAt.IsZero() {
		t.Fatalf("FinishedAt=%v", c.FinishedAt)
	}
}

func TestListContainersFlagsProvisioningOfAnyInstallationFakeAPI(t *testing.T) {
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"Id":"b","Names":["/crewship-provision-abc"],"State":"running","ImageID":"sha256:base","Labels":{}},
{"Id":"r","Names":["/crewship-4-team-x"],"State":"exited","ImageID":"sha256:cache","Labels":{"crewship.kind":"crew","crewship.crew-id":"x","crewship.instance-id":"installation-b"}}]`))
	})
	defer close()
	list, err := p.ListContainers(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	if !list[0].Provisioning || list[1].Provisioning || list[1].InstanceID != "installation-b" || list[1].ImageID != "sha256:cache" {
		t.Fatalf("%+v", list)
	}
}

func TestCacheImagesAndRemoveWithoutForceFakeAPI(t *testing.T) {
	var removed []string
	p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/images/json"):
			w.Write([]byte(`[{"Id":"sha256:c1","RepoTags":["crewship-cache:aaa","other:tag"],"Created":1759000000,"Size":3000},
{"Id":"sha256:base","RepoTags":["debian:bookworm"],"Created":1759000000,"Size":100}]`))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/images/"):
			if q := r.URL.Query().Get("force"); q == "1" || q == "true" {
				t.Errorf("forced image removal: %s", r.URL.RawQuery)
			}
			removed = append(removed, r.URL.Path)
			w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	defer close()
	imgs, err := p.ListCacheImages(context.Background())
	if err != nil || len(imgs) != 1 || len(imgs[0].Refs) != 1 || imgs[0].Refs[0] != "crewship-cache:aaa" {
		t.Fatalf("%+v %v", imgs, err)
	}
	imageID := "sha256:" + strings.Repeat("a", 64)
	if err := p.RemoveImage(context.Background(), "crewship-cache:aaa"); err == nil || len(removed) != 0 {
		t.Fatalf("mutable reference accepted: %v %v", removed, err)
	}
	if err := p.RemoveImage(context.Background(), imageID); err != nil || len(removed) != 1 || !strings.HasSuffix(removed[0], "/images/"+imageID) {
		t.Fatalf("%v %v", removed, err)
	}
}
