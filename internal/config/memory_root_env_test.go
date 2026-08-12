package config

import "testing"

// storage.memory_root was the only field in its group with no environment
// binding, and that gap is load-bearing rather than cosmetic.
//
// cmd_start wires MemoryRoot (and BasePath, and LogPath) from the data
// directory — but only inside `if databaseURL == ""`. An operator who sets
// DATABASE_URL, which is every containerised and systemd deployment, skips the
// whole block. BasePath and LogPath survive that because they have
// CREWSHIP_STORAGE_BASE_PATH and CREWSHIP_LOG_PATH to fall back on.
// MemoryRoot had nothing, so it stayed empty, and empty disables:
//
//   - the workspace memory tier ([WORKSPACE MEMORY] in the agent prompt)
//   - memory versioning, whose handler then answers
//     503 "memory versioning is not configured on this server"
//
// silently, with no warning at boot. Confirmed on a live instance: dev2 runs
// with DATABASE_URL set and GET /api/v1/admin/memory/versions/{id}/content
// answers exactly that 503.
//
// This test pins the escape hatch. It does not change what an unset value
// does — an operator who configures nothing still gets no workspace tier,
// which is the documented "absence is safe" behaviour.
func TestEnvOverrides_StorageMemoryRoot(t *testing.T) {
	t.Setenv("CREWSHIP_STORAGE_MEMORY_ROOT", "/data/memory")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.MemoryRoot != "/data/memory" {
		t.Errorf("Storage.MemoryRoot = %q, want /data/memory — without this binding an "+
			"operator running with DATABASE_URL set cannot enable the workspace memory "+
			"tier or memory versioning at all", cfg.Storage.MemoryRoot)
	}
}

// TestEnvOverrides_StorageMemoryRootUnsetStaysEmpty is the other half: the
// binding must not invent a default. Deriving one from BasePath would turn a
// feature on for every existing install on upgrade, which is a behaviour
// change wearing a bugfix's clothes.
func TestEnvOverrides_StorageMemoryRootUnsetStaysEmpty(t *testing.T) {
	t.Setenv("CREWSHIP_STORAGE_MEMORY_ROOT", "")
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", "/data/base")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.MemoryRoot != "" {
		t.Errorf("Storage.MemoryRoot = %q with nothing set; it must stay empty rather than "+
			"deriving from BasePath — that would enable the workspace tier on upgrade for "+
			"installs that never asked for it", cfg.Storage.MemoryRoot)
	}
}
