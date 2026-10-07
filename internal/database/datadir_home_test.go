package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallationRootSelectionCreatesNothing(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	for _, tc := range []struct {
		name, flag, home, legacy, want, err string
	}{
		{"home", "", a, "", a, ""},
		{"legacy", "", "", b, b, ""},
		{"same aliases", "", a, a + string(filepath.Separator), a, ""},
		{"conflicting aliases", "", a, b, "", "different installations"},
		{"flag wins", b, a, b, b, ""},
		{"relative flag", "relative", a, "", "", "absolute"},
		{"relative home", "", "relative", "", "", "absolute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_HOME", tc.home)
			t.Setenv("CREWSHIP_DATA_DIR", tc.legacy)
			got, err := ResolveDataDirRoot(tc.flag)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %q, %v; want error %q", got, err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if _, err := os.Stat(got); !os.IsNotExist(err) {
				t.Fatalf("root resolution created a directory: %v", err)
			}
		})
	}
}

func TestDefaultDataDirUsesHomeAlias(t *testing.T) {
	root := filepath.Join(t.TempDir(), "installation")
	t.Setenv("CREWSHIP_HOME", root)
	t.Setenv("CREWSHIP_DATA_DIR", "")
	d, err := ResolveDefaultDataDir()
	if err != nil || d.Root != root {
		t.Fatalf("resolve: %v, %v", d, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("readonly resolution created installation: %v", err)
	}
	d, err = DefaultDataDir()
	if err != nil || d.Root != root {
		t.Fatalf("create: %v, %v", d, err)
	}
	if _, err := os.Stat(d.OutputDir()); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRootRejectsRelativeUserHome(t *testing.T) {
	t.Setenv("CREWSHIP_HOME", "")
	t.Setenv("CREWSHIP_DATA_DIR", "")
	t.Setenv("HOME", "relative")
	// On Windows os.UserHomeDir reads USERPROFILE instead of HOME.
	t.Setenv("USERPROFILE", "relative")
	if _, err := ResolveDataDirRoot(""); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative user home accepted: %v", err)
	}
}

func TestInstallationRootAcceptsAliasBeforeCreation(t *testing.T) {
	base := t.TempDir()
	real, alias := filepath.Join(base, "real"), filepath.Join(base, "alias")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("CREWSHIP_HOME", filepath.Join(alias, "new"))
	t.Setenv("CREWSHIP_DATA_DIR", filepath.Join(real, "new"))
	got, err := ResolveDataDirRoot("")
	if err != nil || got != filepath.Join(alias, "new") {
		t.Fatalf("equivalent uncreated roots: %q, %v", got, err)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("resolution created directory: %v", err)
	}
}
