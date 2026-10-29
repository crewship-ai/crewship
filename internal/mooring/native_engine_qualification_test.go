//go:build craft_native_engine_qualification

package mooring

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

// Explicit release qualification supplies a pinned CLI and two different native
// browser exports. This tagged test is not evidence of a server/browser soak.
func TestNativeStorePinnedEngineWholeVersionQualification(t *testing.T) {
	required := func(name string) string {
		t.Helper()
		v := os.Getenv(name)
		if v == "" {
			t.Fatalf("qualification input %s is required", name)
		}
		return v
	}
	app := required("CRAFT_QUALIFICATION_APP")
	first, err := os.ReadFile(required("CRAFT_QUALIFICATION_FIRST"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(required("CRAFT_QUALIFICATION_SECOND"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) <= 4<<20 || len(second) <= 4<<20 || bytes.Equal(first, second) {
		t.Fatal("qualification requires different native documents larger than4MiB")
	}
	base, _, db, _, scope, principal := nativeStoreFixture(t)
	cfg := base.config
	cfg.Application = app
	cfg.EngineRevision = required("CRAFT_QUALIFICATION_REVISION")
	cfg.EngineSHA256 = required("CRAFT_QUALIFICATION_SHA256")
	cfg.MaxHistoryBytes = DefaultNativeHistoryBytes
	validator := NativeCLIValidator{Application: app, BinaryPath: required("CRAFT_QUALIFICATION_ENGINE"), SHA256: cfg.EngineSHA256}
	cfg.Validate = validator.Validate
	store := NewNativeStore(db, cfg)
	lease, initial, err := store.Initialize(t.Context(), scope, principal, first)
	if err != nil || initial.Version != 1 {
		t.Fatalf("nativeinitialize: %+v %v", initial, err)
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(second))
	receipt, err := store.SaveSnapshot(t.Context(), lease, 1, "qualified-native-edit", hash, second)
	if err != nil || receipt.Version != "2" {
		t.Fatalf("native save: %+v %v", receipt, err)
	}
	reopened := NewNativeStore(db, cfg)
	state, err := reopened.Read(t.Context(), scope)
	if err != nil || state.LatestVersion != 2 || !bytes.Equal(state.Manifest.Files["document.json"].Content, second) {
		t.Fatalf("reconstructed store lost native bytes: %v", err)
	}
	confirmed, err := reopened.ReconcileSnapshot(t.Context(), lease, 1, "qualified-native-edit", hash)
	if err != nil || confirmed != receipt {
		t.Fatalf("exact native reconciliation: %+v %v", confirmed, err)
	}
	restored, err := reopened.Restore(t.Context(), lease, 2, 1)
	if err != nil || restored.Version != 3 {
		t.Fatalf("native engine restore: %+v %v", restored, err)
	}
	state, err = reopened.Read(t.Context(), scope)
	if err != nil || !bytes.Equal(state.Manifest.Files["document.json"].Content, first) {
		t.Fatalf("restored original native bytes changed: %v", err)
	}
	t.Logf("%s original=%dB edited=%dB hash=%s initialize1/save2/reconcile2/restore3 exact", app, len(first), len(second), hash)
}
