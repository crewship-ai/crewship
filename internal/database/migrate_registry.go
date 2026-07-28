package database

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The registry — why migrations are files now.
//
// v1..v169 were declared in one hand-maintained slice in migrate.go. Every PR
// adding a migration edited the same region of the same file, so two open
// branches conflicted textually on top of racing for the same version number.
// The timestamp scheme fixes the number; only moving the registry out of a
// central list fixes the conflict. Two people adding a migration now add two
// files and have nothing to disagree about.
//
// Rails, GitLab, goose, golang-migrate and atlas all work this way, for this
// reason.
//
// The legacy slice stays exactly as it is. Those 169 entries are applied in
// databases nobody controls, and rewriting them buys nothing while risking a
// transcription error in the one part of the system that must never drift.
// It also still holds the migrations that need Go rather than SQL — schema
// discovery at apply time, SQLite table rebuilds — which cannot be a .sql
// file. New Go migrations still go there, with a timestamp version.

//go:embed migrations
var migrationFS embed.FS

const (
	migrationDir  = "migrations"
	postDeployDir = "migrations/post_deploy"
)

// migrationFileRE matches "<version>_<name>.sql". The version is validated
// against the two-era scheme separately (validateMigrationVersion), so this
// only has to split the parts.
var migrationFileRE = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.sql$`)

// parseMigrationFilename splits and validates "<version>_<name>.sql". Split
// out of the walker so the rules can be tested directly against the mistakes
// an author actually makes, rather than by planting files in the embedded
// tree — which cannot be done at test time, since embedding happens at build.
func parseMigrationFilename(file string) (int, string, error) {
	m := migrationFileRE.FindStringSubmatch(file)
	if m == nil {
		return 0, "", fmt.Errorf("migration filename %q does not match <version>_<name>.sql "+
			"with a lower_snake_case name; see %s/README.md", file, migrationDir)
	}
	version, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", fmt.Errorf("migration %q: version %q is not a number: %w", file, m[1], err)
	}
	if version <= legacySequentialCeiling {
		return 0, "", fmt.Errorf("migration %q claims version %d, which is inside the closed "+
			"legacy block (<= v%d). Those numbers are applied in databases we do not control; "+
			"use a timestamp", file, version, legacySequentialCeiling)
	}
	if err := validateMigrationVersion(version); err != nil {
		return 0, "", fmt.Errorf("migration %q: %w", file, err)
	}
	return version, m[2], nil
}

// checkMigrationFilename reports whether a filename is acceptable, discarding
// the parsed parts. Exists for the rule table in the tests.
func checkMigrationFilename(file string) error {
	_, _, err := parseMigrationFilename(file)
	return err
}

// loadFileMigrations reads every .sql file under migrations/, returning them
// sorted by version. Files directly in migrations/ run at boot; files in
// migrations/post_deploy/ are marked postDeploy and run after the server is
// serving. Returns an error rather than panicking so the caller — a test that
// runs on every build — can report it usefully.
func loadFileMigrations() ([]migration, error) {
	var out []migration
	seenVersion := map[int]string{}
	seenName := map[string]int{}

	err := fs.WalkDir(migrationFS, migrationDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".sql") {
			return nil
		}

		dir, file := path.Split(p)
		dir = strings.TrimSuffix(dir, "/")
		if dir != migrationDir && dir != postDeployDir {
			return fmt.Errorf("migration %s is in an unrecognised directory — put it in %s/ or %s/",
				p, migrationDir, postDeployDir)
		}

		version, name, nameErr := parseMigrationFilename(file)
		if nameErr != nil {
			return nameErr
		}

		if other, dup := seenVersion[version]; dup {
			return fmt.Errorf("two migrations claim version %d: %q and %q", version, other, name)
		}
		if otherVersion, dup := seenName[name]; dup {
			return fmt.Errorf("two migrations are named %q (versions %d and %d) — names appear "+
				"in error messages and the ledger, so they have to be unique", name, otherVersion, version)
		}
		seenVersion[version] = name
		seenName[name] = version

		body, readErr := migrationFS.ReadFile(p)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", p, readErr)
		}
		if strings.TrimSpace(string(body)) == "" {
			return fmt.Errorf("migration %q is empty — it would consume a version number "+
				"and do nothing", file)
		}

		out = append(out, migration{
			version:    version,
			name:       name,
			sql:        string(body),
			postDeploy: dir == postDeployDir,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
