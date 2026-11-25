package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

type imageProtectionFunc func(context.Context, []resourcelifecycle.ImageReference) (map[string]string, error)

func (f imageProtectionFunc) Preserve(ctx context.Context, refs []resourcelifecycle.ImageReference) (map[string]string, error) {
	return f(ctx, refs)
}

func TestRuntimeImageAdmissionHoldsResolvedIdentityUntilCallerReleases(t *testing.T) {
	db := setupTestDB(t)
	gate := resourcelifecycle.Images(db)
	id := "sha256:" + strings.Repeat("a", 64)
	gate.SetReferenceProtector(imageProtectionFunc(func(context.Context, []resourcelifecycle.ImageReference) (map[string]string, error) {
		return map[string]string{"legacy-alias": id}, nil
	}))
	resolved, release, err := acquireCrewImages(t.Context(), db, provider.CrewConfig{CachedImage: "legacy-alias"})
	if err != nil || resolved.CachedImage != id {
		t.Fatalf("acquire: %+v %v", resolved, err)
	}
	if collect, idle := gate.TryCollectImages(id); idle {
		collect()
		t.Fatal("resolved immutable image had no lifetime lease")
	}
	release()
	collect, idle := gate.TryCollectImages(id)
	if !idle {
		t.Fatal("released runtime kept a stale image lease")
	}
	collect()
}

func TestMissingSelectedEnvironmentDoesNotTriggerReplacementBuild(t *testing.T) {
	h, ws, crew := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
	if _, err := h.db.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, "sha256:"+strings.Repeat("a", 64), crew); err != nil {
		t.Fatal(err)
	}
	resourcelifecycle.Images(h.db).SetReferenceProtector(imageProtectionFunc(func(context.Context, []resourcelifecycle.ImageReference) (map[string]string, error) {
		return nil, errors.New("selected environment unavailable")
	}))
	if err := h.EnsureProvisioned(t.Context(), crew, ws, 0); err == nil {
		t.Fatal("missing exact environment silently admitted")
	}
	if _, err := h.EnqueueForCrew(t.Context(), crew, ws); err == nil {
		t.Fatal("ordinary enqueue bypassed exact selected image admission")
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.jobs) != 0 {
		t.Fatal("missing selected image triggered dependency re-resolution")
	}
}

func TestReferenceInventoryRejectsIncompleteConfigurationAndIncludesServices(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	crew := seedCrewRow(t, db, "reference-inputs", ws, "References", "references")
	id := "sha256:" + strings.Repeat("a", 64)
	if _, err := db.Exec(`UPDATE crews SET devcontainer_config='{broken',services_json=? WHERE id=?`, `[{"name":"worker","image":"`+id+`"}]`, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := resourcelifecycle.SelectedImageReferences(t.Context(), db); err == nil {
		t.Fatal("unreadable configuration authorized reference release")
	}
	if _, err := db.Exec(`UPDATE crews SET devcontainer_config='{"image":"alpine:3"}' WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	refs, err := resourcelifecycle.SelectedImageReferences(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if ref.Ref == id {
			return
		}
	}
	t.Fatal("configured service image was not retained before container creation")
}

func TestRuntimeInputsRequireMissingLocalAliasesButNotFutureBuildOutputs(t *testing.T) {
	db := setupTestDB(t)
	gate := resourcelifecycle.Images(db)
	local := "crewship-cache:borrowed-artifact"
	gate.SetReferenceProtector(imageProtectionFunc(func(_ context.Context, refs []resourcelifecycle.ImageReference) (map[string]string, error) {
		for _, ref := range refs {
			if ref.Ref == local && ref.Required {
				return nil, errors.New("selected local alias unavailable")
			}
		}
		return nil, nil
	}))
	if _, release, err := acquireCrewImages(t.Context(), db, provider.CrewConfig{Image: local}); err == nil {
		release()
		t.Fatal("runtime treated missing local environment as a remote pull")
	}
	h := &ProvisioningHandler{db: db}
	if _, release, err := h.AcquireImageReferences(t.Context(), []string{local}); err == nil {
		release()
		t.Fatal("service treated missing local environment as a remote pull")
	}
}

func TestSelectedEnvironmentDoesNotRequireItsMissingUnusedBase(t *testing.T) {
	db := setupTestDB(t)
	selected := "sha256:" + strings.Repeat("a", 64)
	missingBase := "crewship-cache:removed-base-alias"
	resourcelifecycle.Images(db).SetReferenceProtector(imageProtectionFunc(func(_ context.Context, refs []resourcelifecycle.ImageReference) (map[string]string, error) {
		for _, ref := range refs {
			if ref.Ref == missingBase && ref.Required {
				return nil, errors.New("unused base was made required")
			}
		}
		return map[string]string{selected: selected}, nil
	}))
	cfg, release, err := acquireCrewImages(t.Context(), db, provider.CrewConfig{CachedImage: selected, Image: missingBase})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if cfg.CachedImage != selected {
		t.Fatal("runnable selected environment was replaced")
	}
}

func TestColdCatalogBaseCannotOverrideServiceSelection(t *testing.T) {
	const base = "mcr.microsoft.com/devcontainers/python:3.12-bookworm"
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	first := seedCrewRow(t, db, "base-only", ws, "Base", "base")
	second := seedCrewRow(t, db, "sidecar", ws, "Sidecar", "sidecar")
	assertBaseOnly := func(want bool) {
		t.Helper()
		refs, err := resourcelifecycle.SelectedImageReferences(t.Context(), db)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range refs {
			if ref.Ref == base {
				if ref.ConfiguredBaseOnly != want {
					t.Fatalf("base-only provenance = %v, want %v", ref.ConfiguredBaseOnly, want)
				}
				return
			}
		}
		t.Fatalf("missing %q", base)
	}
	service := `[{"name":"app","image":"` + base + `"}]`
	mustExec(t, db, "UPDATE crews SET services_json=? WHERE id=?", service, second)
	assertBaseOnly(false)
	mustExec(t, db, "UPDATE crews SET runtime_image=? WHERE id=?", base, first)
	assertBaseOnly(false)
	mustExec(t, db, "UPDATE crews SET services_json=NULL WHERE id=?", second)
	assertBaseOnly(true)
	mustExec(t, db, "UPDATE crews SET previous_image=? WHERE id=?", base, second)
	assertBaseOnly(false)
}
