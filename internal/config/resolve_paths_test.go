package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearPathEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range pathEnvironment {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CREWSHIP_DATA_DIR", "")
}

func TestResolvePathsSourcesAndDatabaseIndependence(t *testing.T) {
	clearPathEnvironment(t)
	root := t.TempDir()
	for _, db := range []string{"", "file:/explicit/database.db"} {
		for _, origin := range []string{"derived", "yaml", "env"} {
			t.Run(origin+db, func(t *testing.T) {
				for _, name := range pathEnvironment {
					t.Setenv(name, "")
				}
				cfg := Default()
				if origin == "yaml" {
					filename := filepath.Join(t.TempDir(), "config.yaml")
					content := "storage:\n  base_path: /var/lib/crewship\n  log_path: /var/log/crewship\n  memory_root: ''\nstate:\n  bolt_path: /var/lib/crewship/state.db\nipc:\n  socket_path: /tmp/crewship.sock\n"
					if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
					if err := loadFromFile(cfg, filename); err != nil {
						t.Fatal(err)
					}
				}
				if origin == "env" {
					for _, name := range pathEnvironment {
						t.Setenv(name, filepath.Join(root, name))
					}
					applyEnvOverrides(cfg)
				}
				capturePathEnvSources(cfg)
				result, err := ResolvePaths(cfg, root, db)
				if err != nil {
					t.Fatal(err)
				}
				for key := range pathEnvironment {
					if result.Sources[key] != origin {
						t.Errorf("%s source = %s, want %s", key, result.Sources[key], origin)
					}
				}
				if origin == "derived" {
					if cfg.Storage.BasePath != filepath.Join(root, "output") || cfg.Storage.MemoryRoot != filepath.Join(root, "memory") || cfg.Storage.LogPath != filepath.Join(root, "logs") || cfg.State.BoltPath != filepath.Join(root, "state.db") {
						t.Fatalf("incorrect root paths: %+v", result.Paths)
					}
				}
				if origin == "yaml" && (cfg.Storage.BasePath != "/var/lib/crewship" || cfg.Storage.MemoryRoot != "" || cfg.State.BoltPath != "/var/lib/crewship/state.db") {
					t.Fatal("explicit defaults or disabled memory overwritten")
				}
			})
		}
	}
}

func TestResolvePathsEnvironmentBeatsYAMLAndFlagBeatsDatabaseEnv(t *testing.T) {
	clearPathEnvironment(t)
	cfg := Default()
	cfg.pathSources = map[string]string{"storage.base_path": "yaml"}
	cfg.Storage.BasePath = "/yaml"
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", "/env")
	t.Setenv("DATABASE_URL", "file:/env.db")
	applyEnvOverrides(cfg)
	capturePathEnvSources(cfg)
	paths, err := ResolvePaths(cfg, t.TempDir(), "file:/flag.db")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.BasePath != "/env" || paths.Sources["storage.base_path"] != "env" || paths.DatabaseURL != "file:/flag.db" || paths.Sources["database"] != "flag" {
		t.Fatal(paths)
	}
}

func TestResolvePathsIndependentOfWorkingDirectory(t *testing.T) {
	clearPathEnvironment(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		if err := os.Chdir(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		cfg := Default()
		paths, err := ResolvePaths(cfg, root, "")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Storage.BasePath != filepath.Join(root, "output") || paths.DatabaseURL != "file:"+filepath.Join(root, "crewship.db") {
			t.Fatal(paths)
		}
	}
}

func TestLegacyDataGuard(t *testing.T) {
	for _, scenario := range []string{"missing", "empty-dirs", "data", "both-data", "symlink-alias", "dangling-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			old, next := filepath.Join(root, "old"), filepath.Join(root, "next")
			if scenario != "missing" && scenario != "dangling-symlink" {
				if err := os.MkdirAll(filepath.Join(old, "empty"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "data" || scenario == "both-data" || scenario == "symlink-alias" {
				if err := os.WriteFile(filepath.Join(old, "artifact"), []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "both-data" {
				if err := os.Mkdir(next, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(next, "artifact"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "symlink-alias" {
				if err := os.Symlink(old, next); err != nil {
					t.Skip(err)
				}
			}
			if scenario == "dangling-symlink" {
				if err := os.Symlink(filepath.Join(root, "absent"), old); err != nil {
					t.Skip(err)
				}
			}
			paths := &ResolvedPaths{Paths: map[string]string{"storage.base_path": next}, Legacy: map[string]string{"storage.base_path": old}}
			err := paths.CheckLegacyData()
			wantError := scenario == "data" || scenario == "both-data" || scenario == "dangling-symlink"
			if (err != nil) != wantError {
				t.Fatalf("error = %v, want error %v", err, wantError)
			}
			if err != nil && !strings.Contains(err.Error(), "CREWSHIP_STORAGE_BASE_PATH") {
				t.Fatal("missing recovery instruction", err)
			}
			if scenario != "both-data" && scenario != "symlink-alias" {
				if _, err := os.Stat(next); !os.IsNotExist(err) {
					t.Fatal("guard created destination")
				}
			}
		})
	}
}

func TestLegacyPathsMirrorDatabaseDependentStartup(t *testing.T) {
	clearPathEnvironment(t)
	root := t.TempDir()
	cfg := Default()
	paths, err := ResolvePaths(cfg, root, "file:/database.db")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Legacy["storage.base_path"] != Default().Storage.BasePath {
		t.Fatal(paths.Legacy)
	}
	cfg = Default()
	cfg.Storage.BasePath = filepath.Join(root, "explicit")
	cfg.pathSources = map[string]string{"storage.base_path": "yaml"}
	paths, err = ResolvePaths(cfg, root, "")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Legacy["storage.base_path"] != filepath.Join(root, "output") || cfg.Storage.BasePath != filepath.Join(root, "explicit") {
		t.Fatal(paths)
	}
}
