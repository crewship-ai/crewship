package mooring

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func nativeStoreFixture(t *testing.T) (*NativeStore, *EditLeaseStore, *sql.DB, *ownerTestClock, EditLeaseScope, EditLeasePrincipal) {
	t.Helper()
	_, db, clock, key := ownerFixture(t)
	scope := EditLeaseScope{key.WorkspaceID, key.ArtifactID}
	p := EditLeasePrincipal{"u", "native-tab-a"}
	leases := NewEditLeaseStore(db.DB, EditLeaseConfig{Enabled: true, Clock: clock, TTL: 5 * time.Second, Authority: func(context.Context, *sql.Tx, EditLeaseAccess) error { return nil }})
	s := NewNativeStore(db.DB, NativeStoreConfig{
		Enabled: true, Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Leases: leases,
		Authorize: func(context.Context, *sql.Tx, EditLeaseScope, string) (EditLeasePrincipal, error) { return p, nil },
		Validate: func(_ context.Context, b []byte) error {
			if !json.Valid(b) {
				return ErrInvalid
			}
			return nil
		},
		MaxHistoryBytes: 64 << 20,
	})
	return s, leases, db.DB, clock, scope, p
}
func TestNativeStoreDefaultOff(t *testing.T) {
	s := NewNativeStore(nil, NativeStoreConfig{})
	if _, err := s.Save(t.Context(), EditLease{}, 0, []byte(`{}`)); !errors.Is(err, ErrNativeDisabled) {
		t.Fatal(err)
	}
}
func TestNativeStoreInitializeWholeVersionAndReopen(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	bytes := []byte(`{"body":[{"text":"whole native document"}]}`)
	l, r, err := s.Initialize(t.Context(), scope, p, bytes)
	if err != nil || l.Generation != 1 || r.Version != 1 {
		t.Fatalf("initialize: %+v %+v %v", l, r, err)
	}
	state, err := s.Read(t.Context(), scope)
	if err != nil || string(state.Manifest.Files["document.json"].Content) != string(bytes) {
		t.Fatalf("read: %+v %v", state, err)
	}
	if _, _, err = s.Initialize(t.Context(), scope, p, bytes); err == nil {
		t.Fatal("reinitialized immutable profile")
	}
}
func TestNativeStoreSaveAndRestoreFenceLeaseAndCAS(t *testing.T) {
	for _, path := range []string{"save", "restore"} {
		t.Run(path, func(t *testing.T) {
			s, leases, db, clock, scope, p := nativeStoreFixture(t)
			first, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Save(t.Context(), first, 1, []byte(`{"v":2}`)); err != nil {
				t.Fatal(err)
			}
			clock.advance(5 * time.Second)
			other := EditLeasePrincipal{"u", "native-tab-b"}
			if _, err = leases.Acquire(t.Context(), scope, other); err != nil {
				t.Fatal(err)
			}
			if path == "restore" {
				_, err = s.Restore(t.Context(), first, 2, 1)
			} else {
				_, err = s.Save(t.Context(), first, 2, []byte(`{"v":3}`))
			}
			if !errors.Is(err, ErrEditLeaseStale) {
				t.Fatalf("%s bypassed lease: %v", path, err)
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='p'`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("late write altered history: %d %v", count, err)
			}
		})
	}
}
func TestNativeStoreEngineValidationPrecedesPublishAndIsRechecked(t *testing.T) {
	s, _, db, clock, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	invalid := errors.New("engine rejected native model")
	s.config.Validate = func(context.Context, []byte) error { return invalid }
	if _, err = s.Save(t.Context(), l, 1, []byte(`{"v":2}`)); !errors.Is(err, invalid) {
		t.Fatal(err)
	}
	s.config.Validate = func(context.Context, []byte) error { clock.advance(s.config.Leases.config.TTL); return nil }
	if _, err = s.Save(t.Context(), l, 1, []byte(`{"v":2}`)); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal("validation delay revived write", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='p'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
func TestNativeStoreCannotBypassThroughGenericArtifactStore(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	_, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	generic := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true})
	_, err = generic.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: scope.WorkspaceID, PageID: scope.ArtifactID}, artifacts.VersionRequest{BaseVersion: 1, EntryPoint: "index.html", Files: map[string]artifacts.VersionFile{"index.html": {Content: []byte("bypass")}}}, func(context.Context, *sql.Tx) (string, error) { return p.ActorID, nil })
	if err == nil {
		t.Fatal("generic publication replaced native document")
	}
}

func TestNativeStorePublicationRechecksLeaseBeforeCommit(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER native_expire_publication AFTER INSERT ON artifact_versions WHEN NEW.version>1 BEGIN UPDATE mooring_edit_leases SET expires_ns=0 WHERE page_id=NEW.page_id; END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(t.Context(), l, 1, []byte(`{"v":2}`)); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatalf("expired at publication: %v", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='p'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("expired publication escaped rollback: %d %v", count, err)
	}
}
func TestNativeStoreInitializeRollsBackProfileAndLease(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	if _, err := db.Exec(`CREATE TRIGGER native_fail_publish BEFORE INSERT ON artifact_versions BEGIN SELECT RAISE(ABORT,'injected publication failure'); END`); err != nil {
		t.Fatal(err)
	}
	l, r, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err == nil || l.Generation != 0 || r.Version != 0 {
		t.Fatalf("failed create returned authority: %+v %+v %v", l, r, err)
	}
	for _, table := range []string{"craft_native_documents", "mooring_edit_leases", "artifact_versions"} {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s escaped atomic initialization: %d %v", table, count, err)
		}
	}
}
func TestNativeStoreWholeStateBeyondCheckpointAndSaveCountLimits(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	raw := []byte(`{"body":"` + strings.Repeat("x", (4<<20)+1) + `"}`)
	l, r, err := s.Initialize(t.Context(), scope, p, raw)
	if err != nil {
		t.Fatal(err)
	}
	// Identical blobs are deduplicated, but every explicit save is a full manifest
	// version. This tests count and size bounds, not changing-document disk growth
	// or native engine/browser qualification (those have separate acceptance).
	for range 2000 {
		r, err = s.Save(t.Context(), l, r.Version, raw)
		if err != nil {
			t.Fatal("document reached a save-count limit", r.Version, err)
		}
	}
	if r.Version != 2001 {
		t.Fatal(r.Version)
	}
	state, err := s.Read(t.Context(), scope)
	if err != nil || !bytes.Equal(state.Manifest.Files["document.json"].Content, raw) {
		t.Fatal("whole native bytes lost", err)
	}
}
