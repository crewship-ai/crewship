package resourcelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var retentionNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

const week = 7 * 24 * time.Hour

type fakeRetentionRuntime struct {
	containers map[string]RetentionContainer
	images     []CacheImage
	removed    []string
	untagged   []string
	// startOnRemove simulates a start that wins the race: by the time the
	// removal re-inspects under the crew lock, the container is running.
	startOnRemove bool
	refuseImage   map[string]bool
}

func (f *fakeRetentionRuntime) ListContainers(context.Context) ([]RetentionContainer, error) {
	out := []RetentionContainer{}
	for _, c := range f.containers {
		c.FinishedAt = time.Time{} // the list view never carries it
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeRetentionRuntime) InspectContainer(_ context.Context, id string) (RetentionContainer, error) {
	c, ok := f.containers[id]
	if !ok {
		return RetentionContainer{}, ErrNotFound
	}
	return c, nil
}

func (f *fakeRetentionRuntime) RemoveIdleRuntime(_ context.Context, id, crew string, verify func(RetentionContainer) error) error {
	if f.startOnRemove {
		c := f.containers[id]
		c.State = "running"
		c.FinishedAt = time.Time{}
		f.containers[id] = c
	}
	fresh, ok := f.containers[id]
	if !ok {
		return ErrNotFound
	}
	if err := verify(fresh); err != nil {
		return err
	}
	f.removed = append(f.removed, id)
	delete(f.containers, id)
	return nil
}

func (f *fakeRetentionRuntime) ListCacheImages(context.Context) ([]CacheImage, error) {
	return f.images, nil
}

func (f *fakeRetentionRuntime) RemoveImage(_ context.Context, ref string) error {
	if f.refuseImage[ref] {
		return errors.New("conflict: image is being used by a container")
	}
	f.untagged = append(f.untagged, ref)
	return nil
}

func retentionFixture(t *testing.T) (*Retention, *fakeRetentionRuntime, *time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"20260930164209_container_cleanup_diagnostics.sql", "20261001090000_idle_retention_images.sql"} {
		b, err := os.ReadFile("../database/migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE crews(id TEXT PRIMARY KEY, deleted_at TEXT, cached_image TEXT);
INSERT INTO crews(id,deleted_at) VALUES('live',NULL),('deleted','2026-09-01')`); err != nil {
		t.Fatal(err)
	}
	now := retentionNow
	f := &fakeRetentionRuntime{containers: map[string]RetentionContainer{}, refuseImage: map[string]bool{}}
	r := &Retention{DB: db, InstanceID: "installation-a", Runtime: f, RuntimeAfter: week, EvictCache: true,
		Now: func() time.Time { return now }}
	return r, f, &now
}

func runtimeContainer(id, crew, instance, state string, stoppedFor time.Duration) RetentionContainer {
	c := RetentionContainer{ID: id, CrewID: crew, InstanceID: instance, Kind: "crew", State: state, ImageID: "img-" + id,
		Mounts: []Mount{{Type: "volume", Name: "home-" + id, Destination: "/home/agent"}}}
	if stoppedFor > 0 {
		c.FinishedAt = retentionNow.Add(-stoppedFor)
	}
	return c
}

func TestRetentionSevenDayBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stopped time.Duration
		removed bool
	}{
		{"just under seven days", week - time.Minute, false},
		{"exactly seven days", week, true},
		{"over seven days", week + time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, f, _ := retentionFixture(t)
			f.containers["x"] = runtimeContainer("x", "live", "installation-a", "exited", tc.stopped)
			r.EvictCache = false
			r.Tick(context.Background())
			if got := len(f.removed) == 1; got != tc.removed {
				t.Fatalf("removed=%v want %v", f.removed, tc.removed)
			}
		})
	}
}

// Volumes are not this controller's to remove; the evidence of what was
// mounted is recorded before the container goes.
func TestRetentionRecordsMountsBeforeRemoval(t *testing.T) {
	r, f, _ := retentionFixture(t)
	f.containers["x"] = runtimeContainer("x", "live", "installation-a", "exited", week+time.Hour)
	r.Tick(context.Background())
	var n int
	if err := r.DB.QueryRow(`SELECT count(*) FROM resource_cleanup_mounts WHERE container_id='x' AND mounts_json LIKE '%home-x%'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("mount evidence %d %v", n, err)
	}
}

func TestRetentionKeepsWhatItCannotProve(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    RetentionContainer
	}{
		{"unknown stop time", runtimeContainer("x", "live", "installation-a", "exited", 0)},
		{"created but never started", runtimeContainer("x", "live", "installation-a", "created", 0)},
		{"running", runtimeContainer("x", "live", "installation-a", "running", 0)},
		{"another installation", runtimeContainer("x", "live", "installation-b", "exited", 30*24*time.Hour)},
		{"legacy without installation label", runtimeContainer("x", "live", "", "exited", 30*24*time.Hour)},
		{"deleted owner is the deletion controller's", runtimeContainer("x", "deleted", "installation-a", "exited", 30*24*time.Hour)},
		{"owner unknown to this database", runtimeContainer("x", "absent", "installation-a", "exited", 30*24*time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, f, _ := retentionFixture(t)
			f.containers["x"] = tc.c
			r.Tick(context.Background())
			if len(f.removed) != 0 {
				t.Fatalf("removed %v", f.removed)
			}
		})
	}
	t.Run("sidecar service", func(t *testing.T) {
		r, f, _ := retentionFixture(t)
		c := runtimeContainer("x", "live", "installation-a", "exited", 30*24*time.Hour)
		c.Kind = "sidecar"
		f.containers["x"] = c
		r.Tick(context.Background())
		if len(f.removed) != 0 {
			t.Fatalf("removed a managed service %v", f.removed)
		}
	})
}

// A start that wins the race against the removal: the re-check under the
// crew's start lock sees it running and the container stays.
func TestRetentionConcurrentStartKeepsContainer(t *testing.T) {
	r, f, _ := retentionFixture(t)
	f.containers["x"] = runtimeContainer("x", "live", "installation-a", "exited", week+time.Hour)
	f.startOnRemove = true
	r.Tick(context.Background())
	if len(f.removed) != 0 {
		t.Fatalf("removed a container that had just started: %v", f.removed)
	}
	if _, ok := f.containers["x"]; !ok {
		t.Fatal("container gone")
	}
	var n int
	if err := r.DB.QueryRow(`SELECT count(*) FROM resource_cleanup_mounts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("mount evidence written for a container that was kept: %d %v", n, err)
	}
}

// The owner deleted or the container stopped again (new FinishedAt) between
// the list and the locked re-check: nothing is removed on stale facts.
func TestRetentionRecheckUnderLock(t *testing.T) {
	r, f, _ := retentionFixture(t)
	f.containers["x"] = runtimeContainer("x", "live", "installation-a", "exited", week+time.Hour)
	inner := r.Runtime
	r.Runtime = &hookRuntime{RetentionRuntime: inner, beforeRemove: func() {
		if _, err := r.DB.Exec(`UPDATE crews SET deleted_at='2026-10-01' WHERE id='live'`); err != nil {
			t.Fatal(err)
		}
	}}
	r.Tick(context.Background())
	if len(f.removed) != 0 {
		t.Fatalf("removed after owner deletion: %v", f.removed)
	}
}

type hookRuntime struct {
	RetentionRuntime
	beforeRemove func()
}

func (h *hookRuntime) RemoveIdleRuntime(ctx context.Context, id, crew string, verify func(RetentionContainer) error) error {
	h.beforeRemove()
	return h.RetentionRuntime.RemoveIdleRuntime(ctx, id, crew, verify)
}

func cacheImage(id string, age time.Duration) CacheImage {
	return CacheImage{ID: id, Refs: []string{"crewship-cache:" + id}, Created: retentionNow.Add(-age), Size: 1000}
}

func TestCacheEvictionNeedsAContinuousUnusedWindow(t *testing.T) {
	r, f, now := retentionFixture(t)
	r.RuntimeAfter = 0
	f.images = []CacheImage{cacheImage("old", 48*time.Hour)}
	r.Tick(context.Background()) // first sighting starts the window
	if len(f.untagged) != 0 {
		t.Fatalf("evicted on first sighting: %v", f.untagged)
	}
	*now = now.Add(30 * time.Minute)
	r.Tick(context.Background())
	if len(f.untagged) != 0 {
		t.Fatalf("evicted inside the window: %v", f.untagged)
	}
	*now = now.Add(31 * time.Minute)
	r.Tick(context.Background())
	if len(f.untagged) != 1 || f.untagged[0] != "old" {
		t.Fatalf("not evicted after the window: %v", f.untagged)
	}
}

// Any container on the daemon — any installation, stopped too — keeps the
// image, and a use inside the window restarts it.
func TestCacheEvictionKeepsImagesInUse(t *testing.T) {
	r, f, now := retentionFixture(t)
	r.RuntimeAfter = 0
	f.images = []CacheImage{cacheImage("shared", 48*time.Hour)}
	r.Tick(context.Background())
	f.containers["other"] = RetentionContainer{ID: "other", State: "exited", ImageID: "shared", InstanceID: "installation-b"}
	*now = now.Add(2 * time.Hour)
	r.Tick(context.Background())
	delete(f.containers, "other")
	*now = now.Add(30 * time.Minute)
	r.Tick(context.Background())
	if len(f.untagged) != 0 {
		t.Fatalf("evicted although used within the window: %v", f.untagged)
	}
}

func TestCacheEvictionSkipsWhileAnyInstallationBuilds(t *testing.T) {
	r, f, now := retentionFixture(t)
	r.RuntimeAfter = 0
	f.images = []CacheImage{cacheImage("old", 48*time.Hour)}
	r.Tick(context.Background())
	*now = now.Add(2 * time.Hour)
	f.containers["build"] = RetentionContainer{ID: "build", State: "running", ImageID: "base", Provisioning: true}
	r.Tick(context.Background())
	if len(f.untagged) != 0 {
		t.Fatalf("evicted during a build: %v", f.untagged)
	}
}

func TestCacheEvictionProtectsFreshImages(t *testing.T) {
	r, f, now := retentionFixture(t)
	r.RuntimeAfter = 0
	f.images = []CacheImage{cacheImage("fresh", time.Hour)}
	for i := 0; i < 5; i++ {
		r.Tick(context.Background())
		*now = now.Add(2 * time.Hour)
	}
	if len(f.untagged) != 0 {
		t.Fatalf("evicted an image younger than a day: %v", f.untagged)
	}
}

// Docker refusing the removal (a container created since the list) keeps
// the image and restarts its window.
func TestCacheEvictionRefusedRemovalRestartsWindow(t *testing.T) {
	r, f, now := retentionFixture(t)
	r.RuntimeAfter = 0
	f.images = []CacheImage{cacheImage("old", 48*time.Hour)}
	f.refuseImage["old"] = true
	r.Tick(context.Background())
	*now = now.Add(2 * time.Hour)
	r.Tick(context.Background())
	var n int
	if err := r.DB.QueryRow(`SELECT count(*) FROM resource_retention_images`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("window not restarted after a refused removal: %d %v", n, err)
	}
}

func TestRetentionDisabledByDefault(t *testing.T) {
	r, f, now := retentionFixture(t)
	r.RuntimeAfter, r.EvictCache = 0, false
	f.containers["x"] = runtimeContainer("x", "live", "installation-a", "exited", 30*24*time.Hour)
	f.images = []CacheImage{cacheImage("old", 48*time.Hour)}
	r.Tick(context.Background())
	*now = now.Add(3 * time.Hour)
	r.Tick(context.Background())
	if len(f.removed)+len(f.untagged) != 0 {
		t.Fatalf("acted while disabled: %v %v", f.removed, f.untagged)
	}
}
