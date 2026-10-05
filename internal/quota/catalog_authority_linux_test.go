//go:build linux

package quota

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogNamespaceIsImmutableAndCannotAdoptUnboundImages(t *testing.T) {
	u := newUnitBackend(t)
	if err := u.BindNamespace("installation-a"); err != nil {
		t.Fatal(err)
	}
	u.writeEntry(t, Key{"crew", "database", "data", 1}, MinBytes, true, true)
	if err := u.BindNamespace("installation-a"); err != nil {
		t.Fatalf("same bound namespace cannot reopen populated catalog: %v", err)
	}
	for _, namespace := range []string{"installation-b", "", "../foreign", strings.Repeat("a", 129)} {
		if err := u.BindNamespace(namespace); !errors.Is(err, ErrDenied) {
			t.Fatalf("namespace changed to %q: %v", namespace, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(u.root, "namespace"))
	if err != nil || string(raw) != "installation-a" {
		t.Fatalf("failed binding changed identity: %q %v", raw, err)
	}
	unbound := newUnitBackend(t)
	unbound.writeEntry(t, Key{"crew", "database", "data", 1}, MinBytes, true, true)
	if err := unbound.BindNamespace("installation-a"); !errors.Is(err, ErrDenied) {
		t.Fatalf("unattributed images adopted by namespace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(unbound.root, "namespace")); !os.IsNotExist(err) {
		t.Fatalf("failed binding published identity: %v", err)
	}
}

func TestCatalogRejectsUnsafeNamespaceRecords(t *testing.T) {
	for _, kind := range []string{"symlink", "readable by other users", "wrong owner"} {
		t.Run(kind, func(t *testing.T) {
			u := newUnitBackend(t)
			target := filepath.Join(u.root, "namespace")
			if kind == "symlink" {
				foreign := filepath.Join(t.TempDir(), "foreign")
				if err := os.WriteFile(foreign, []byte("installation-a"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, target); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(target, []byte("installation-a"), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "wrong owner" {
					u.owner++
				} else if err := os.Chmod(target, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := u.BindNamespace("installation-a"); err == nil {
				t.Fatal("unsafe namespace record admitted")
			}
		})
	}
}

func TestReleasePreservesOtherVolumeReferences(t *testing.T) {
	u := newUnitBackend(t)
	k := Key{"crew", "database", "data", 1}
	u.writeEntry(t, k, MinBytes, true, true)
	path := filepath.Join(u.root, "images", k.id()+".references")
	if err := os.WriteFile(path, []byte(`["docker-a","docker-b","docker-a"]`), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := u.Release(t.Context(), k, "docker-a"); err != nil {
			t.Fatal(err)
		}
		refs, err := u.references(k)
		if err != nil || !reflect.DeepEqual(refs, []string{"docker-b"}) {
			t.Fatalf("release lost another runtime reference: %v %v", refs, err)
		}
	}
	if err := u.Remove(t.Context(), k); !errors.Is(err, ErrDenied) {
		t.Fatalf("remaining runtime reference did not prevent removal: %v", err)
	}
	if len(u.commands) != 0 {
		t.Fatalf("referenced filesystem reached destructive tools: %v", u.commands)
	}
}

func TestCorruptReferencesFailClosedBeforeUnmountOrExport(t *testing.T) {
	tooMany, _ := json.Marshal(make([]string, 17))
	for _, raw := range []string{"not-json", `{}`, string(tooMany), strings.Repeat(" ", 8192) + `[]`} {
		t.Run(raw[:min(len(raw), 20)], func(t *testing.T) {
			u := newUnitBackend(t)
			k := Key{"crew", "database", "data", 1}
			u.writeEntry(t, k, MinBytes, true, true)
			if err := os.WriteFile(filepath.Join(u.root, "images", k.id()+".references"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := u.Remove(t.Context(), k); !errors.Is(err, ErrDenied) {
				t.Fatalf("corrupt references allowed removal: %v", err)
			}
			var dst strings.Builder
			if err := u.Export(t.Context(), k, MinBytes, &dst); !errors.Is(err, ErrDenied) {
				t.Fatalf("corrupt references allowed export: %v", err)
			}
			if dst.Len() != 0 || len(u.commands) != 0 {
				t.Fatal("untrusted references reached image copy or destructive tools")
			}
		})
	}
}

func TestCloseCannotReleaseCatalogWithAnActiveSnapshot(t *testing.T) {
	u := newUnitBackend(t)
	k := Key{"crew", "database", "data", 1}
	u.activeSnapshots = map[string]bool{k.id(): true}
	if err := u.Close(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed active catalog: %v", err)
	}
	if u.lock == nil {
		t.Fatal("active snapshot lost exclusive catalog ownership")
	}
	for name, operation := range map[string]func() error{
		"verify":  func() error { _, err := u.Verify(t.Context(), k, MinBytes); return err },
		"protect": func() error { return u.Protect(t.Context(), k, "docker-a") },
		"release": func() error { return u.Release(t.Context(), k, "docker-a") },
		"export":  func() error { return u.Export(t.Context(), k, MinBytes, &strings.Builder{}) },
	} {
		if err := operation(); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s stole active snapshot: %v", name, err)
		}
	}
	delete(u.activeSnapshots, k.id())
	for i := 0; i < 2; i++ {
		if err := u.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := u.Verify(t.Context(), k, MinBytes); !errors.Is(err, ErrDenied) {
		t.Fatalf("closed catalog admitted verification: %v", err)
	}
	if err := u.BindNamespace("installation-a"); !errors.Is(err, ErrDenied) {
		t.Fatalf("closed catalog changed authority: %v", err)
	}
}
