package backupplan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/offsite"
)

// instanceFileExec writes real (unencrypted, test-only) instance bundles an
// hour apart, so rotation can read their manifests from disk. Each bundle
// may name environment blobs it needs from the shared store.
type instanceFileExec struct {
	db    *sql.DB
	dir   string
	mu    sync.Mutex
	n     int
	blobs func(n int) []string // digests the n-th bundle needs (1-based)
	paths []string
}

func (e *instanceFileExec) Run(ctx context.Context, spec RunSpec, progress func(string)) (*RunResult, error) {
	e.mu.Lock()
	e.n++
	n := e.n
	e.mu.Unlock()
	for _, p := range []string{"copy", "pack", "encrypt"} {
		progress(p)
	}
	created := time.Now().UTC().Add(time.Duration(n-10) * time.Hour).Truncate(time.Second)
	m := &backup.Manifest{
		FormatVersion: backup.FormatVersion, Scope: backup.ScopeInstance, CreatedAt: created, CreatedBy: spec.Actor,
		CompatibleTargets: []backup.Target{backup.TargetAnyInstance},
		Encryption:        backup.Encryption{Enabled: true},
	}
	var digests []string
	if e.blobs != nil {
		digests = e.blobs(n)
		m.Contents.Environments = []backup.EnvironmentSummary{{Crew: "c", ID: "env", Blobs: digests}}
	}
	path := filepath.Join(e.dir, backup.BundleFileName(backup.ScopeInstance, "all", created))
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := backup.WriteBundle(f, m, strings.NewReader("payload "+spec.RunID), backup.WriteBundleOptions{NoEncrypt: true}); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if len(digests) > 0 {
		if err := backup.AddEnvironmentRefs(ctx, e.db, backup.EnvironmentStoreFor(e.dir), path, digests); err != nil {
			return nil, err
		}
	}
	st, _ := os.Stat(path)
	e.mu.Lock()
	e.paths = append(e.paths, path)
	e.mu.Unlock()
	return &RunResult{Path: path, Size: st.Size(), SHA256: "sha-" + spec.RunID, Manifest: m}, nil
}

// Plans with scope instance apply their keep rules after a good run, like
// workspace plans: the keep_min floor and pins protect, everything else
// beyond them goes — its file, its environment layers (unless another
// kept bundle still needs them) and its off-site copies.
func TestService_InstancePlanRotatesItsBundles(t *testing.T) {
	h := newHarness(t, "2026-09-30T02:00:00Z")
	ctx := context.Background()
	dir := t.TempDir()
	h.svc.BackupsDir = func() (string, error) { return dir, nil }

	store := backup.EnvironmentStoreFor(dir)
	put := func(s string) string {
		d, _, err := store.Put(strings.NewReader(s), "")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	shared, own1, own2, own3 := put("base layer every bundle shares"), put("layer of bundle 1"), put("layer of bundle 2"), put("layer of bundle 3")
	own := map[int]string{1: own1, 2: own2, 3: own3}
	exec := &instanceFileExec{db: h.db, dir: dir, blobs: func(n int) []string {
		if d, ok := own[n]; ok {
			return []string{shared, d}
		}
		return []string{shared}
	}}
	h.svc.Instance = exec

	remote := newMemDest()
	h.svc.Destinations = func(_ context.Context, id string) (offsite.Destination, *Destination, error) {
		return remote, &Destination{ID: id, Name: "remote"}, nil
	}
	d := &Destination{Name: "remote", Endpoint: "https://s3.example.com", Bucket: "b", AccessKeyID: "AK"}
	if err := InsertDestination(ctx, h.db, d, "enc", "u1", h.clock.Now()); err != nil {
		t.Fatal(err)
	}

	p := h.plan(func(p *Plan) {
		*p = Defaults(backup.PresetComplete)
		p.RecipientIDs = []string{h.key}
		p.KeepMin, p.KeepDaily, p.KeepWeekly, p.KeepMonthly = 1, 0, 0, 0
		p.Destinations = []string{"local", d.ID}
	})
	if p.Scope != ScopeInstance {
		t.Fatalf("plan scope = %s", p.Scope)
	}

	manual := func() string {
		t.Helper()
		ids, err := h.svc.StartManual(ctx, ManualRequest{PlanID: p.ID}, "")
		if err != nil || len(ids) != 1 {
			t.Fatalf("StartManual = %v, %v", ids, err)
		}
		h.svc.Wait()
		r, err := GetRun(ctx, h.db, ids[0])
		if err != nil || r.Status != StatusDone {
			t.Fatalf("run = %+v, %v", r, err)
		}
		return r.BundlePath
	}
	b1 := manual()
	if err := backup.PinCatalogEntry(ctx, h.db, b1); err != nil {
		t.Fatal(err)
	}
	b2 := manual()
	if _, err := os.Stat(b2); err != nil {
		t.Fatalf("the floor (keep_min 1) must keep the newest bundle: %v", err)
	}
	b3 := manual()

	// keep_min 1 keeps b3, the pin keeps b1; b2 goes.
	for _, keep := range []string{b1, b3} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s deleted: %v", filepath.Base(keep), err)
		}
	}
	if _, err := os.Stat(b2); !os.IsNotExist(err) {
		t.Fatalf("b2 survived rotation: %v", err)
	}
	if _, err := backup.GetCatalogEntry(ctx, h.db, b2); err == nil {
		t.Errorf("b2 is still in the catalog")
	}
	// Its own layer went; the shared one and the kept bundles' layers stay.
	if store.Has(own2) {
		t.Error("the dropped bundle's own environment layer was not collected")
	}
	for _, d := range []string{shared, own1, own3} {
		if !store.Has(d) {
			t.Errorf("layer %s a kept bundle needs was collected", d[:15])
		}
	}
	var refs int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM bundle_environment_refs WHERE bundle_ref = ?`, b2).Scan(&refs)
	if refs != 0 {
		t.Errorf("b2 still holds %d environment refs", refs)
	}
	// Its off-site copy went too; the kept bundles' copies stay.
	if copies, _ := CopiesOf(ctx, h.db, b2); len(copies) != 0 {
		t.Errorf("b2's copy record survived: %+v", copies)
	}
	b2Key := ObjectKey(ScopeInstance, "", b2)
	if _, ok := remote.objects[b2Key]; ok {
		t.Errorf("b2's remote copy survived")
	}
	for _, keep := range []string{b1, b3} {
		if _, ok := remote.objects[ObjectKey(ScopeInstance, "", keep)]; !ok {
			t.Errorf("remote copy of kept %s deleted", filepath.Base(keep))
		}
	}
	// The layers went off-site with the bundles; b2's own layer left the
	// destination with it, the ones the kept copies need stayed.
	layerKey := func(d string) string { k, _ := offsite.EnvironmentBlobKey("", d); return k }
	if _, ok := remote.objects[layerKey(own2)]; ok {
		t.Error("b2's own layer is still at the destination")
	}
	for _, d := range []string{shared, own1, own3} {
		if _, ok := remote.objects[layerKey(d)]; !ok {
			t.Errorf("layer %s a kept remote bundle needs was deleted", d[:15])
		}
	}

	// Restore from off-site onto a server that has neither the bundle nor
	// its layers: both come back, and the layers are the bundle's refs.
	fresh := t.TempDir()
	got, tr, err := FetchOffsiteCopy(ctx, h.db, h.svc.Destinations, d.ID, ObjectKey(ScopeInstance, "", b3), fresh)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Environments.Transferred != 2 {
		t.Fatalf("fetch = %+v", tr)
	}
	if m, err := backup.Inspect(ctx, got); err != nil || m.Scope != backup.ScopeInstance {
		t.Fatalf("fetched bundle: %+v, %v", m, err)
	}
	freshStore := backup.EnvironmentStoreFor(fresh)
	for _, d := range []string{shared, own3} {
		if !freshStore.Has(d) {
			t.Errorf("layer %s not restored from off-site", d[:15])
		}
	}
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM bundle_environment_refs WHERE bundle_ref = ?`, got).Scan(&refs)
	if refs != 2 {
		t.Errorf("fetched bundle holds %d refs, want 2", refs)
	}
	if _, _, err := FetchOffsiteCopy(ctx, h.db, h.svc.Destinations, d.ID, ObjectKey(ScopeInstance, "", b3), fresh); err == nil {
		t.Error("fetching over an existing bundle must be refused")
	}
}
