package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/craftengine"
)

// The server keeps the engine's compilation cache under its data directory; it starts without
// it when that directory cannot be made, and CREWSHIP_CRAFT_COMPILE_CACHE=0 turns it off.
func TestCraftCompileCacheIsOptionalAndCanBeTurnedOff(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Cleanup(craftengine.DisableCompileCache)
	start := func(home, env string) *craftApps {
		t.Helper()
		craftengine.DisableCompileCache()
		t.Setenv("CREWSHIP_HOME", home)
		t.Setenv("CREWSHIP_CRAFT_COMPILE_CACHE", env)
		cfg := config.Default()
		cfg.CraftNative.ComponentsDir = t.TempDir()
		a := resolveCraftApps(cfg, logger)
		if a == nil {
			t.Fatal("the server did not start its Craft applications")
		}
		return a
	}

	home := t.TempDir()
	start(home, "")
	want := filepath.Join(home, "craft-compile-cache")
	if got := craftengine.CompileCacheRoot(); got != want {
		t.Fatalf("cache root = %q, want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir: %v %v", fi, err)
	}

	off := t.TempDir()
	start(off, "0")
	if got := craftengine.CompileCacheRoot(); got != "" {
		t.Fatalf("CREWSHIP_CRAFT_COMPILE_CACHE=0 left the cache on at %q", got)
	}
	if _, err := os.Stat(filepath.Join(off, "craft-compile-cache")); !os.IsNotExist(err) {
		t.Fatalf("turned off, the cache directory was made: %v", err)
	}

	// The data directory is a file: no cache directory can be made, the server starts anyway.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	start(blocked, "")
	if got := craftengine.CompileCacheRoot(); got != "" {
		t.Fatalf("no cache directory, yet the cache is on at %q", got)
	}
}
