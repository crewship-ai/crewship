package database

import (
	"fmt"
	"sort"
)

// migrations is the full ordered set this binary can apply: the legacy
// sequential block and the Go-only entries from legacyMigrations, merged with
// every .sql file under migrations/.
//
// Built once at init. A malformed embedded migration is a programming error
// caught by TestMigrationRegistryBuildsCleanly on every build, but init cannot
// return an error and panicking in a library init would take down unrelated
// callers with a stack that says nothing useful. So the error is kept and
// Migrate refuses with it — a broken registry must never migrate anything.
var (
	migrations           []migration
	migrationRegistryErr error
)

func init() {
	migrations, migrationRegistryErr = buildMigrationRegistry(legacyMigrations)
}

func buildMigrationRegistry(legacy []migration) ([]migration, error) {
	files, err := loadFileMigrations()
	if err != nil {
		return nil, fmt.Errorf("load file migrations: %w", err)
	}

	merged := make([]migration, 0, len(legacy)+len(files))
	merged = append(merged, legacy...)
	merged = append(merged, files...)

	sort.Slice(merged, func(i, j int) bool { return merged[i].version < merged[j].version })

	// The same two collisions the file loader checks within migrations/, now
	// across the boundary — a file colliding with a legacy entry is the case
	// the loader alone cannot see.
	byVersion := make(map[int]string, len(merged))
	byName := make(map[string]int, len(merged))
	for _, m := range merged {
		if other, dup := byVersion[m.version]; dup {
			return nil, fmt.Errorf("version %d is claimed twice: %q and %q", m.version, other, m.name)
		}
		if otherVersion, dup := byName[m.name]; dup {
			return nil, fmt.Errorf("migration name %q is used at versions %d and %d",
				m.name, otherVersion, m.version)
		}
		byVersion[m.version] = m.name
		byName[m.name] = m.version
	}

	return merged, nil
}

// pendingPostDeploy returns the post-deploy migrations this binary declares,
// in order. Used by the background runner and by migration-status reporting.
func pendingPostDeployDeclared() []migration {
	var out []migration
	for _, m := range migrations {
		if m.postDeploy {
			out = append(out, m)
		}
	}
	return out
}
