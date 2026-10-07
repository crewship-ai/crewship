package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var pathEnvironment = map[string]string{
	"storage.base_path":   "CREWSHIP_STORAGE_BASE_PATH",
	"storage.log_path":    "CREWSHIP_LOG_PATH",
	"storage.memory_root": "CREWSHIP_STORAGE_MEMORY_ROOT",
	"state.bolt_path":     "CREWSHIP_BOLT_PATH",
	"ipc.socket_path":     "CREWSHIP_SOCKET_PATH",
}

func capturePathEnvSources(cfg *Config) {
	if cfg.pathSources == nil {
		cfg.pathSources = make(map[string]string)
	}
	for key, name := range pathEnvironment {
		if value, exists := os.LookupEnv(name); exists && value != "" {
			cfg.pathSources[key] = "env"
		}
	}
}

// ResolvedPaths records effective locations and their origins without creating
// files. Legacy contains only paths the previous startup would have used for
// the same inputs, so validation never scans unrelated historical layouts.
type ResolvedPaths struct {
	DatabaseURL string
	Sources     map[string]string
	Paths       map[string]string
	Legacy      map[string]string
}

// ResolvePaths applies explicit config before deriving defaults from root.
// databaseFlag is the existing --db value; no other path has a CLI flag.
// Empty environment values retain the config value, as config.Load does.
func ResolvePaths(cfg *Config, root, databaseFlag string) (*ResolvedPaths, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("installation data root must be absolute: %q", root)
	}
	result := &ResolvedPaths{Sources: make(map[string]string), Paths: make(map[string]string), Legacy: make(map[string]string)}
	result.DatabaseURL = databaseFlag
	result.Sources["database"] = "flag"
	if result.DatabaseURL == "" {
		result.DatabaseURL = os.Getenv("DATABASE_URL")
		result.Sources["database"] = "env"
	}
	explicitDB := result.DatabaseURL != ""
	if !explicitDB {
		result.DatabaseURL = "file:" + filepath.Join(root, "crewship.db")
		result.Sources["database"] = "derived"
	}
	result.Paths["database"] = result.DatabaseURL
	fields := map[string]*string{
		"storage.base_path":   &cfg.Storage.BasePath,
		"storage.log_path":    &cfg.Storage.LogPath,
		"storage.memory_root": &cfg.Storage.MemoryRoot,
		"state.bolt_path":     &cfg.State.BoltPath,
		"ipc.socket_path":     &cfg.IPC.SocketPath,
	}
	defaults := map[string]string{
		"storage.base_path":   filepath.Join(root, "output"),
		"storage.log_path":    filepath.Join(root, "logs"),
		"storage.memory_root": filepath.Join(root, "memory"),
		"state.bolt_path":     filepath.Join(root, "state.db"),
		"ipc.socket_path":     DefaultSocketPathFor(root),
	}
	for key, field := range fields {
		legacy := *field
		// Mirror the old startup's DB-dependent rewriting exactly. The new
		// behavior must not silently abandon persistent data it previously used.
		if !explicitDB {
			switch key {
			case "storage.base_path", "storage.log_path", "storage.memory_root":
				legacy = defaults[key]
			case "state.bolt_path":
				if strings.TrimSpace(os.Getenv("CREWSHIP_BOLT_PATH")) == "" && (legacy == "" || legacy == DefaultBoltPath() || legacy == "/var/lib/crewship/state.db") {
					legacy = defaults[key]
				}
			case "ipc.socket_path":
				if strings.TrimSpace(os.Getenv("CREWSHIP_SOCKET_PATH")) == "" && (legacy == "" || legacy == DefaultSocketPath() || legacy == "/tmp/crewship.sock") {
					legacy = defaults[key]
				}
			}
		}
		source := cfg.pathSources[key]
		if source == "" {
			source = "derived"
			*field = defaults[key]
		}
		if key != "storage.memory_root" && strings.TrimSpace(*field) == "" {
			return nil, fmt.Errorf("%s must not be empty (source: %s)", key, source)
		}
		result.Sources[key], result.Paths[key] = source, *field
		// Also guard stores the old startup used while ignoring explicit values.
		// Sockets are runtime files, not persistent migration content.
		if key != "ipc.socket_path" && legacy != "" {
			result.Legacy[key] = legacy
		}
	}
	return result, nil
}

// CheckLegacyData refuses any implicit move away from persistent content,
// including a split layout where the new path already contains data. It must
// run before bootstrap/store creation and database migrations.
func (p *ResolvedPaths) CheckLegacyData() error {
	for _, key := range []string{"storage.base_path", "storage.log_path", "storage.memory_root", "state.bolt_path"} {
		old := p.Legacy[key]
		if old == "" {
			continue
		}
		oldCanonical, err := canonicalPath(old)
		if err != nil {
			return fmt.Errorf("inspect previous %s at %s: %w; explicitly set %s after checking its data", key, old, err, pathEnvironment[key])
		}
		newCanonical, err := canonicalPath(p.Paths[key])
		if err != nil {
			return fmt.Errorf("resolve %s: %w", key, err)
		}
		if oldCanonical == newCanonical {
			continue
		}
		populated, err := persistentContent(old)
		if err != nil {
			return fmt.Errorf("inspect previous %s at %s: %w; explicitly set %s after checking its data", key, old, err, pathEnvironment[key])
		}
		if populated {
			return fmt.Errorf("refusing to abandon existing %s data at %s for %s; set %s=%s to retain it, or migrate the data while stopped", key, old, p.Paths[key], pathEnvironment[key], old)
		}
	}
	return nil
}

// Resolve existing ancestors as well as the leaf, so a not-yet-created path
// below a symlink compares equal to its canonical spelling.
func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for candidate := absolute; ; candidate = filepath.Dir(candidate) {
		resolved, err := filepath.EvalSymlinks(candidate)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		// A dangling symlink is not an absent store that is safe to ignore.
		if info, statErr := os.Lstat(candidate); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", err
		}
		suffix = append(suffix, filepath.Base(candidate))
	}
}

func persistentContent(path string) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return info.Mode().IsRegular(), nil
	}
	populated := false
	err = filepath.WalkDir(path, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Stat(name)
			if err != nil {
				return err
			}
			// A directory symlink can contain data; conservatively require an
			// operator decision rather than traversing outside this store.
			if target.IsDir() {
				populated = true
				return fs.SkipAll
			}
			if !target.Mode().IsRegular() {
				return nil
			}
		} else if !entry.Type().IsRegular() {
			return nil
		}
		populated = true
		return fs.SkipAll
	})
	return populated, err
}
