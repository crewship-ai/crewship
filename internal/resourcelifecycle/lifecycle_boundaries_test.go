package resourcelifecycle

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type closingRuntime struct {
	Runtime
	closed func()
}

func (r closingRuntime) Close() error { err := r.Runtime.Close(); r.closed(); return err }

func TestControllerRunScansImmediatelyAndReleasesRuntimeOnCancellation(t *testing.T) {
	c, f := fixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	closed := 0
	c.Connect = func(context.Context) (Runtime, error) {
		return closingRuntime{Runtime: f, closed: func() { closed++; cancel() }}, nil
	}
	c.Run(ctx)
	if closed != 1 || f.lists == 0 {
		t.Fatalf("lifecycle: closed=%d lists=%d", closed, f.lists)
	}
	rows, err := c.InstanceStatuses(t.Context())
	if err != nil || len(rows) != 1 || !rows[0].Complete {
		t.Fatalf("completed scan=%+v %v", rows, err)
	}
}

func TestInstanceDiagnosticsAreScopedAndStorageFailuresVisible(t *testing.T) {
	c, _ := fixture(t)
	if got := c.Pending(t.Context(), "ws-b", "second"); got.State != "pending" {
		t.Fatalf("pending=%+v", got)
	}
	if _, err := c.DB.Exec(`INSERT INTO resource_cleanup_status(instance_id,crew_id,workspace_id,state) VALUES('foreign','secret','ws-b','complete')`); err != nil {
		t.Fatal(err)
	}
	all, err := c.InstanceStatuses(t.Context())
	if err != nil || len(all) != 2 || all[0].CrewID != "deleted" || all[1].CrewID != "second" {
		t.Fatalf("instance scope=%+v %v", all, err)
	}
	one, err := c.Statuses(t.Context(), "ws-b")
	if err != nil || len(one) != 1 || one[0].CrewID != "second" {
		t.Fatalf("workspace scope=%+v %v", one, err)
	}
	if _, err := c.DB.Exec(`UPDATE resource_cleanup_status SET complete='corrupt' WHERE crew_id='second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstanceStatuses(t.Context()); err == nil {
		t.Fatal("malformed diagnostic row became partial success")
	}
	if err := c.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if got := c.Pending(t.Context(), "ws-b", "third"); got.State != "error" || got.Error != "diagnostic_write_failed" {
		t.Fatalf("write failure hidden: %+v", got)
	}
	if _, err := c.InstanceStatuses(t.Context()); err == nil {
		t.Fatal("closed diagnostics store became empty success")
	}
}

type retentionLifecycleProbe struct {
	*fakeRetentionRuntime
	calls  atomic.Int32
	cancel context.CancelFunc
}

func (r *retentionLifecycleProbe) ListContainers(context.Context) ([]RetentionContainer, error) {
	if r.calls.Add(1) == 2 {
		r.cancel()
	}
	return nil, errors.New("daemon unavailable")
}
func TestRetentionRunRetriesUnavailableDaemonAndHonorsCancellation(t *testing.T) {
	r, f, _ := retentionFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	probe := &retentionLifecycleProbe{fakeRetentionRuntime: f, cancel: cancel}
	r.Runtime = probe
	r.Interval = time.Millisecond
	r.EvictCache = false
	r.Now = nil
	r.Run(ctx)
	if probe.calls.Load() != 2 {
		t.Fatalf("expected immediate and periodic attempts, got %d", probe.calls.Load())
	}
	// A canceled lifecycle also terminates under the default interval.
	r.Interval = 0
	r.Run(ctx)
}

func TestInstallationIdentityNeverGrantsAuthorityAfterStorageFailure(t *testing.T) {
	c, _ := fixture(t)
	root := t.TempDir()
	if id, err := LoadIdentity(t.Context(), root, c.DB, ""); err == nil || id != nil {
		t.Fatal("unstable database location gained identity")
	}
	blocker := filepath.Join(root, "file")
	if err := os.WriteFile(blocker, []byte("ordinary file"), 0600); err != nil {
		t.Fatal(err)
	}
	if id, err := LoadIdentity(t.Context(), blocker, c.DB, "location"); err == nil || id != nil {
		t.Fatal("unusable root gained identity")
	}
	if err := c.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if id, err := LoadIdentity(t.Context(), root, c.DB, "location"); err == nil || id != nil {
		t.Fatal("closed database gained identity")
	}
	var empty *Identity
	if err := empty.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (&Identity{}).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationIdentityRejectsMalformedNonceAndUnusableIdentityFiles(t *testing.T) {
	for _, failure := range []string{"nonce", "owner path", "lock path", "id path", "rekey"} {
		t.Run(failure, func(t *testing.T) {
			c, _ := fixture(t)
			dir := t.TempDir()
			nonce, err := databaseNonce(t.Context(), c.DB)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "nonce":
				_, err = c.DB.Exec(`UPDATE resource_cleanup_installation SET db_nonce='invalid'`)
			case "owner path":
				err = os.Mkdir(filepath.Join(dir, nonce+".db"), 0700)
			case "lock path":
				err = os.Mkdir(filepath.Join(dir, nonce+".lock"), 0700)
			case "id path":
				err = os.Mkdir(filepath.Join(dir, nonce), 0700)
			case "rekey":
				err = os.WriteFile(filepath.Join(dir, nonce+".db"), []byte("other-location"), 0600)
				if err == nil {
					_, err = c.DB.Exec(`CREATE TRIGGER reject_rekey BEFORE UPDATE ON resource_cleanup_installation BEGIN SELECT RAISE(ABORT,'read only'); END`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if id, err := loadIdentity(t.Context(), dir, c.DB, "location"); err == nil || id != nil {
				t.Fatalf("%s gained identity: %+v %v", failure, id, err)
			}
		})
	}
}

func TestInstanceLabelOverridesInheritedAuthorityIncludingDisabledCleanup(t *testing.T) {
	for _, instance := range []string{"owned", ""} {
		labels := map[string]string{InstanceLabel: "foreign", "crewship.kind": "crew"}
		got := WithInstanceLabel(labels, instance)
		if got[InstanceLabel] != instance || got["crewship.kind"] != "crew" {
			t.Fatalf("label ownership=%v", got)
		}
	}
	if err := claimLocation(t.TempDir(), "location"); err == nil {
		t.Fatal("directory accepted as ownership record")
	}
	c, _ := fixture(t)
	if _, err := c.DB.Exec(`DROP TABLE resource_cleanup_status`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstanceStatuses(t.Context()); err == nil || !strings.Contains(err.Error(), "resource_cleanup_status") {
		t.Fatalf("missing diagnostic table=%v", err)
	}
}

type unavailableRetentionRuntime struct {
	*fakeRetentionRuntime
	operation string
}

func (r unavailableRetentionRuntime) ListContainers(ctx context.Context) ([]RetentionContainer, error) {
	if r.operation == "containers" {
		return nil, errors.New("daemon unavailable")
	}
	return r.fakeRetentionRuntime.ListContainers(ctx)
}
func (r unavailableRetentionRuntime) ListCacheImages(ctx context.Context) ([]CacheImage, error) {
	if r.operation == "images" {
		return nil, errors.New("daemon unavailable")
	}
	return r.fakeRetentionRuntime.ListCacheImages(ctx)
}
func (r unavailableRetentionRuntime) InspectContainer(ctx context.Context, id string) (RetentionContainer, error) {
	if r.operation == "inspect" {
		return RetentionContainer{}, errors.New("daemon unavailable")
	}
	return r.fakeRetentionRuntime.InspectContainer(ctx, id)
}

func TestRetentionPreservesResourcesWhenEvidenceCannotBeReadOrWritten(t *testing.T) {
	for _, failure := range []string{"containers", "images", "inspect", "state read", "state insert", "state delete", "mount write", "mount count", "mount reference", "owner read"} {
		t.Run(failure, func(t *testing.T) {
			r, f, _ := retentionFixture(t)
			var log bytes.Buffer
			r.Logger = slog.New(slog.NewTextHandler(&log, nil))
			f.images = []CacheImage{{ID: "cache", Refs: []string{"crewship-cache:test"}, Created: retentionNow.Add(-week)}}
			c := runtimeContainer("idle", "live", r.InstanceID, "exited", 2*week)
			r.RuntimeAfter = 0
			r.Runtime = unavailableRetentionRuntime{fakeRetentionRuntime: f, operation: failure}
			var err error
			switch failure {
			case "state read":
				_, err = r.DB.Exec(`DROP TABLE resource_retention_images`)
			case "state insert":
				_, err = r.DB.Exec(`CREATE TRIGGER reject_insert BEFORE INSERT ON resource_retention_images BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`)
			case "state delete":
				_, err = r.DB.Exec(`INSERT INTO resource_retention_images VALUES('installation-a','gone','2026-09-01T00:00:00Z'); CREATE TRIGGER reject_delete BEFORE DELETE ON resource_retention_images BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`)
			case "mount write":
				_, err = r.DB.Exec(`DROP TABLE resource_cleanup_mounts`)
			case "mount count":
				c.Mounts = make([]Mount, 257)
			case "mount reference":
				c.Mounts[0].Source = "invalid\npath"
			case "owner read":
				_, err = r.DB.Exec(`DROP TABLE crews`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(failure, "mount") || failure == "inspect" || failure == "owner read" {
				r.RuntimeAfter = week
				r.EvictCache = false
				f.containers[c.ID] = c
			}
			r.Tick(t.Context())
			if len(f.removed) != 0 || len(f.untagged) != 0 {
				t.Fatalf("removed resources without evidence: %v %v", f.removed, f.untagged)
			}
			if failure != "owner read" && !strings.Contains(log.String(), "level=WARN") {
				t.Fatalf("failure not diagnosed: %s", log.String())
			}
		})
	}
}

func TestRetentionUsesWallClockAndDisabledControllerReportsNoCleanup(t *testing.T) {
	r, f, _ := retentionFixture(t)
	r.Now = nil
	r.RuntimeAfter = 0
	f.images = []CacheImage{{ID: "old-cache", Created: time.Now().Add(-2 * week)}}
	before := time.Now().Add(-time.Second)
	r.Tick(t.Context())
	seen, err := r.firstUnused(t.Context())
	if err != nil || seen["old-cache"].Before(before) || seen["old-cache"].After(time.Now()) {
		t.Fatalf("unused window did not start now: %v %v", seen, err)
	}
	for _, c := range []*Controller{nil, {}} {
		got := c.Pending(t.Context(), "workspace", "crew")
		if got.State != "disabled" || got.CrewID != "crew" || got.Complete {
			t.Fatalf("disabled cleanup claimed progress: %+v", got)
		}
	}
}
