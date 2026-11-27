package mooring

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeStoreCompatEditorOpensOldDocumentsWithoutRewritingThem(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	oldPin := s.config.EditorBundle
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	s.config.EditorBundle = strings.Repeat("e", 64) // a newer editor of the same engine
	if _, err = s.Read(t.Context(), scope); !errors.Is(err, ErrNativeProfile) {
		t.Fatalf("a different editor without a declared compatibility: %v", err)
	}
	s.config.CompatEditorBundles = []string{oldPin}
	if _, err = s.Read(t.Context(), scope); err != nil {
		t.Fatalf("compatible editor: %v", err)
	}
	if _, err = s.Save(t.Context(), l, 1, []byte(`{"v":2}`)); err != nil {
		t.Fatalf("save under the compatible editor: %v", err)
	}
	var row string
	if err = db.QueryRow(`SELECT editor_bundle FROM craft_native_documents`).Scan(&row); err != nil || row != oldPin {
		t.Fatalf("the stored pin was rewritten: %q %v", row, err)
	}
}

func TestNativeStoreCompatEditorNeverWidensToAnotherEngineOrEditor(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	oldPin := s.config.EditorBundle
	if _, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	s.config.EditorBundle = strings.Repeat("e", 64)
	s.config.CompatEditorBundles = []string{oldPin}
	s.config.Validate = func(context.Context, []byte) error { t.Fatal("mismatched profile reached the engine"); return nil }
	// another engine module: refused even though the editor pin is declared compatible
	s.config.EngineSHA256 = strings.Repeat("d", 64)
	if _, err := s.Read(t.Context(), scope); !errors.Is(err, ErrNativeProfile) {
		t.Fatalf("other engine: %v", err)
	}
	s.config.EngineSHA256 = strings.Repeat("b", 64)
	s.config.EngineRevision = strings.Repeat("f", 40)
	if _, err := s.Read(t.Context(), scope); !errors.Is(err, ErrNativeProfile) {
		t.Fatalf("other engine revision: %v", err)
	}
	// an editor that is neither current nor declared
	s.config.EngineRevision = strings.Repeat("a", 40)
	s.config.CompatEditorBundles = []string{strings.Repeat("9", 64)}
	if _, err := s.Read(t.Context(), scope); !errors.Is(err, ErrNativeProfile) {
		t.Fatalf("undeclared editor: %v", err)
	}
}

func TestNativeStoreWritesNewDocumentsWithTheEarlierPinSoAnOlderServerOpensThem(t *testing.T) {
	s, _, db, _, scope, p := nativeStoreFixture(t)
	earlier := s.config.EditorBundle
	s.config.EditorBundle = strings.Repeat("e", 64)
	s.config.CompatEditorBundles = []string{earlier}
	if _, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	var row string
	if err := db.QueryRow(`SELECT editor_bundle FROM craft_native_documents`).Scan(&row); err != nil || row != earlier {
		t.Fatalf("new document pin %q %v", row, err)
	}
}

func TestNativeStoreRefusesAMalformedCompatList(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	for _, bad := range [][]string{{"nothex"}, {strings.Repeat("c", 64)}, {strings.Repeat("1", 64), strings.Repeat("1", 64)}} {
		s.config.CompatEditorBundles = bad
		if _, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestValidationTimeoutGrowsWithTheDocumentUpToAClearCeiling(t *testing.T) {
	if got := ValidationTimeout(1 << 10); got != 30*time.Second {
		t.Fatalf("small document: %s", got)
	}
	small, mid, big := ValidationTimeout(2<<20), ValidationTimeout(16<<20), ValidationTimeout(30<<20)
	if !(small < mid && mid <= big) {
		t.Fatalf("not growing: %s %s %s", small, mid, big)
	}
	// the measured worst case for a 29.7 MB workbook (76.7 s under load) fits with room
	if big < 2*77*time.Second || big > MaxValidationTime {
		t.Fatalf("30 MiB: %s", big)
	}
	if got := ValidationTimeout(1 << 40); got != MaxValidationTime || ValidationTimeout(-5) != 30*time.Second {
		t.Fatalf("ceiling or floor: %s", got)
	}
}
