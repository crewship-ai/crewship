package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// These tests cover migrateLegacyCrewResources — the data-preserving
// auto-migration that replaced the fail-only C1 guard. Pre-C1 crew Docker
// resources were keyed by slug only ("crewship-{team,home,tools}-<slug>"); C1
// re-keyed them to also carry the globally-unique crew id
// ("crewship-{team,home,tools}-<slug>-<id>"). Upgrading across the C1 boundary
// used to wedge provisioning and force a destructive nuke+reseed. Now, on the
// first provision after upgrade, the legacy slug-scoped container is removed
// (ephemeral) and the legacy home/tools volume *data* is copied into the new
// id-scoped volume before the legacy one is pruned. A copy that cannot complete
// is fail-safe: the legacy volume is left untouched and an error is returned.
//
// ContainerWait-vs-inspect decision: we use ContainerWait. The docker SDK's
// ContainerWait POSTs /containers/{id}/wait and decodes a container.WaitResponse
// ({"StatusCode": N}); the fake server below answers that route directly, which
// is cleaner to fake than polling ContainerInspect for State.Running/ExitCode.

// fakeDaemon is a tiny, configurable Docker REST stand-in for the migration
// tests. It records the mutating calls migrateLegacyCrewResources makes so each
// test can assert on them.
type fakeDaemon struct {
	mu sync.Mutex

	// inputs
	containerNames []string // names returned by ContainerList (without leading "/")
	volumeNames    []string // names returned by VolumeList
	volumeListErr  bool     // VolumeList returns 500
	waitExit       int64    // exit code returned by ContainerWait
	createFails    bool     // ContainerCreate returns 500

	// recorded calls
	stoppedContainers []string
	removedContainers []string
	createdVolumes    []string
	removedVolumes    []string
	helperCreates     int
	lastCreateBody    map[string]any
}

func (f *fakeDaemon) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/json"):
			out := make([]map[string]any, 0, len(f.containerNames))
			for i, n := range f.containerNames {
				out = append(out, map[string]any{
					"Id":    "cid" + n,
					"Names": []string{"/" + n},
					"State": "running",
				})
				_ = i
			}
			writeJSON(w, http.StatusOK, out)

		case r.Method == http.MethodPost && strings.Contains(path, "/containers/create"):
			f.helperCreates++
			body, _ := decodeBody(r)
			f.lastCreateBody = body
			if f.createFails {
				http.Error(w, `{"message":"create boom"}`, http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"Id": "helper-1", "Warnings": []string{}})

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/wait"):
			writeJSON(w, http.StatusOK, map[string]any{"StatusCode": f.waitExit})

		case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
			f.stoppedContainers = append(f.stoppedContainers, lastPathSeg(path, "stop"))
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete && strings.Contains(path, "/containers/"):
			f.removedContainers = append(f.removedContainers, lastSeg(path))
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodGet && strings.HasSuffix(path, "/volumes"):
			if f.volumeListErr {
				http.Error(w, `{"message":"vol list boom"}`, http.StatusInternalServerError)
				return
			}
			vols := make([]map[string]any, 0, len(f.volumeNames))
			for _, n := range f.volumeNames {
				vols = append(vols, map[string]any{"Name": n})
			}
			writeJSON(w, http.StatusOK, map[string]any{"Volumes": vols, "Warnings": nil})

		case r.Method == http.MethodPost && strings.Contains(path, "/volumes/create"):
			body, _ := decodeBody(r)
			name, _ := body["Name"].(string)
			f.createdVolumes = append(f.createdVolumes, name)
			writeJSON(w, http.StatusCreated, map[string]any{"Name": name})

		case r.Method == http.MethodDelete && strings.Contains(path, "/volumes/"):
			f.removedVolumes = append(f.removedVolumes, lastSeg(path))
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeBody(r *http.Request) (map[string]any, error) {
	var m map[string]any
	err := json.NewDecoder(r.Body).Decode(&m)
	return m, err
}

func lastSeg(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	return parts[len(parts)-1]
}

// lastPathSeg returns the path segment immediately before the given action
// suffix, e.g. lastPathSeg("/v1.43/containers/abc/stop", "stop") == "abc".
func lastPathSeg(path, action string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == action && i > 0 {
			return parts[i-1]
		}
	}
	return ""
}

const (
	migPrefix   = "crewship"
	migSlug     = "backend"
	migCrewID   = "ckcrew0001"
	migImage    = "busybox:latest"
	legacyTeam  = migPrefix + "-team-" + migSlug
	legacyHome  = migPrefix + "-home-" + migSlug
	legacyTools = migPrefix + "-tools-" + migSlug
	targetHome  = migPrefix + "-home-" + migSlug + "-" + migCrewID
	targetTools = migPrefix + "-tools-" + migSlug + "-" + migCrewID
)

func newMigProvider(t *testing.T, f *fakeDaemon) (*Provider, func()) {
	p, cleanup := newFakeDockerProvider(t, f.handler(t))
	p.cfg.ContainerPrefix = migPrefix
	return p, cleanup
}

// Clean post-C1 daemon: no legacy container, no legacy volumes => no-op.
func TestMigrateLegacy_NoOpOnCleanDaemon(t *testing.T) {
	f := &fakeDaemon{
		containerNames: []string{migPrefix + "-team-" + migSlug + "-" + migCrewID}, // already id-scoped
		volumeNames:    []string{targetHome, targetTools},
	}
	p, cleanup := newMigProvider(t, f)
	defer cleanup()

	if err := p.migrateLegacyCrewResources(context.Background(), migCrewID, migSlug, migImage); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(f.stoppedContainers) != 0 || len(f.removedContainers) != 0 {
		t.Errorf("clean daemon should touch no containers: stopped=%v removed=%v", f.stoppedContainers, f.removedContainers)
	}
	if len(f.createdVolumes) != 0 || len(f.removedVolumes) != 0 {
		t.Errorf("clean daemon should touch no volumes: created=%v removed=%v", f.createdVolumes, f.removedVolumes)
	}
	if f.helperCreates != 0 {
		t.Errorf("clean daemon should not create helper containers, got %d", f.helperCreates)
	}
}

// Legacy slug-scoped container present => stopped + removed.
func TestMigrateLegacy_RemovesLegacyContainer(t *testing.T) {
	f := &fakeDaemon{
		containerNames: []string{legacyTeam},
		volumeNames:    []string{}, // no legacy volumes
	}
	p, cleanup := newMigProvider(t, f)
	defer cleanup()

	if err := p.migrateLegacyCrewResources(context.Background(), migCrewID, migSlug, migImage); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(f.stoppedContainers) != 1 {
		t.Fatalf("expected legacy container stopped once, got %v", f.stoppedContainers)
	}
	if len(f.removedContainers) != 1 {
		t.Fatalf("expected legacy container removed once, got %v", f.removedContainers)
	}
}

// Legacy home volume present, target absent => create target, run helper copy,
// remove helper, remove legacy. Helper must mount legacy=>/from and target=>/to.
func TestMigrateLegacy_MigratesHomeVolume(t *testing.T) {
	f := &fakeDaemon{
		containerNames: []string{},
		volumeNames:    []string{legacyHome},
		waitExit:       0,
	}
	p, cleanup := newMigProvider(t, f)
	defer cleanup()

	if err := p.migrateLegacyCrewResources(context.Background(), migCrewID, migSlug, migImage); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !contains(f.createdVolumes, targetHome) {
		t.Errorf("expected target volume %q created, got %v", targetHome, f.createdVolumes)
	}
	if f.helperCreates != 1 {
		t.Errorf("expected 1 helper container created, got %d", f.helperCreates)
	}
	if !contains(f.removedContainers, "helper-1") {
		t.Errorf("expected helper container removed, got %v", f.removedContainers)
	}
	if !contains(f.removedVolumes, legacyHome) {
		t.Errorf("expected legacy volume %q removed after copy, got %v", legacyHome, f.removedVolumes)
	}

	// Assert the helper mounts the right volume names.
	mounts := mountsFromCreateBody(t, f.lastCreateBody)
	gotFrom, gotTo := "", ""
	for _, m := range mounts {
		switch m["Target"] {
		case "/from":
			gotFrom, _ = m["Source"].(string)
		case "/to":
			gotTo, _ = m["Source"].(string)
		}
	}
	if gotFrom != legacyHome {
		t.Errorf("helper /from source = %q, want legacy volume %q", gotFrom, legacyHome)
	}
	if gotTo != targetHome {
		t.Errorf("helper /to source = %q, want target volume %q", gotTo, targetHome)
	}
}

// Helper exits non-zero => error AND legacy volume NOT removed (fail-safe).
func TestMigrateLegacy_HelperNonZeroDoesNotRemoveLegacy(t *testing.T) {
	f := &fakeDaemon{
		volumeNames: []string{legacyHome},
		waitExit:    1,
	}
	p, cleanup := newMigProvider(t, f)
	defer cleanup()

	err := p.migrateLegacyCrewResources(context.Background(), migCrewID, migSlug, migImage)
	if err == nil {
		t.Fatal("expected error when helper copy exits non-zero")
	}
	if contains(f.removedVolumes, legacyHome) {
		t.Fatalf("legacy volume MUST NOT be removed when copy failed; removed=%v", f.removedVolumes)
	}
	if !strings.Contains(err.Error(), legacyHome) {
		t.Errorf("error should name the legacy volume %q: %v", legacyHome, err)
	}
}

// Helper create fails => error, legacy volume NOT removed.
func TestMigrateLegacy_HelperCreateFailureDoesNotRemoveLegacy(t *testing.T) {
	f := &fakeDaemon{
		volumeNames: []string{legacyHome},
		createFails: true,
	}
	p, cleanup := newMigProvider(t, f)
	defer cleanup()

	err := p.migrateLegacyCrewResources(context.Background(), migCrewID, migSlug, migImage)
	if err == nil {
		t.Fatal("expected error when helper create fails")
	}
	if contains(f.removedVolumes, legacyHome) {
		t.Fatalf("legacy volume MUST NOT be removed when helper create failed; removed=%v", f.removedVolumes)
	}
}

// Target volume already exists => do NOT clobber, leave legacy in place, warn,
// return nil (orphan path).
func TestMigrateLegacy_TargetExistsLeavesLegacyOrphan(t *testing.T) {
	f := &fakeDaemon{
		volumeNames: []string{legacyHome, targetHome},
	}
	p, cleanup := newMigProvider(t, f)
	defer cleanup()

	if err := p.migrateLegacyCrewResources(context.Background(), migCrewID, migSlug, migImage); err != nil {
		t.Fatalf("orphan path should not error: %v", err)
	}
	if contains(f.removedVolumes, legacyHome) {
		t.Errorf("legacy volume must NOT be removed when target already exists; removed=%v", f.removedVolumes)
	}
	if f.helperCreates != 0 {
		t.Errorf("no copy should run when target already exists, got %d helper creates", f.helperCreates)
	}
}

// image == "" with a legacy volume present => error, no VolumeRemove (fail-safe).
func TestMigrateLegacy_EmptyImageIsFailSafe(t *testing.T) {
	f := &fakeDaemon{
		volumeNames: []string{legacyHome},
	}
	p, cleanup := newMigProvider(t, f)
	defer cleanup()

	err := p.migrateLegacyCrewResources(context.Background(), migCrewID, migSlug, "")
	if err == nil {
		t.Fatal("expected error when image is empty and a legacy volume must be migrated")
	}
	if contains(f.removedVolumes, legacyHome) {
		t.Fatalf("legacy volume MUST NOT be removed when image is empty; removed=%v", f.removedVolumes)
	}
	if !strings.Contains(err.Error(), legacyHome) {
		t.Errorf("error should name the legacy volume %q: %v", legacyHome, err)
	}
}

func mountsFromCreateBody(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	hc, ok := body["HostConfig"].(map[string]any)
	if !ok {
		t.Fatalf("create body missing HostConfig: %v", body)
	}
	raw, ok := hc["Mounts"].([]any)
	if !ok {
		t.Fatalf("HostConfig missing Mounts: %v", hc)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
