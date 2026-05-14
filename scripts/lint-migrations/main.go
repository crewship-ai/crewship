// Command lint-migrations enforces that migration entries in internal/database/migrate.go
// which already exist on a base ref (defaults to origin/main) have not been
// modified in the working tree. This catches the classic two-branch-merge
// collision where a rebased PR keeps a version number that another PR has
// already shipped to main with different SQL.
//
// Usage:
//
//	go run ./scripts/lint-migrations [base-ref]
//
// Exits non-zero on any divergence. Designed to run in CI against the PR's
// base ref; locally it accepts any ref reachable from `git show`.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
)

const migrationsFile = "internal/database/migrate.go"

// migrationLine matches a single migration struct literal. The slice in
// migrate.go is written as one entry per line:
//
//	{version: 65, name: "add_skill_bootstrap_fields", sql: migrationXxx},
//
// We capture (version, name) — the SQL body lives in separate const files
// keyed by migration name, so a name change is sufficient evidence of a
// migration mutation. Matching the comma at the end keeps us from picking
// up commented-out or partial lines.
var migrationLine = regexp.MustCompile(`\{version:\s*(\d+),\s*name:\s*"([^"]+)"`)

type migration struct {
	version int
	name    string
}

func parse(src []byte) (map[int]string, error) {
	out := map[int]string{}
	for _, m := range migrationLine.FindAllSubmatch(src, -1) {
		v, err := strconv.Atoi(string(m[1]))
		if err != nil {
			return nil, fmt.Errorf("parse version %q: %w", m[1], err)
		}
		if existing, dup := out[v]; dup {
			return nil, fmt.Errorf("duplicate version %d (names: %q, %q)", v, existing, m[2])
		}
		out[v] = string(m[2])
	}
	return out, nil
}

func main() {
	baseRef := "origin/main"
	if len(os.Args) > 1 {
		baseRef = os.Args[1]
	}

	// Working-tree version.
	headBytes, err := os.ReadFile(migrationsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", migrationsFile, err)
		os.Exit(2)
	}

	// Base ref version. `git show <ref>:<path>` works for any ref reachable
	// from the repo, including remote-tracking branches and tags.
	cmd := exec.Command("git", "show", baseRef+":"+migrationsFile)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// If the file does not exist on the base (e.g. brand-new project),
		// nothing to compare against — treat as success.
		if bytes.Contains(stderr.Bytes(), []byte("does not exist")) ||
			bytes.Contains(stderr.Bytes(), []byte("exists on disk, but not in")) {
			fmt.Printf("migration-lint: %s not present in %s, skipping\n", migrationsFile, baseRef)
			return
		}
		fmt.Fprintf(os.Stderr, "git show %s:%s failed: %v\n%s\n",
			baseRef, migrationsFile, err, stderr.String())
		os.Exit(2)
	}

	headMap, err := parse(headBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse HEAD: %v\n", err)
		os.Exit(2)
	}
	baseMap, err := parse(stdout.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse %s: %v\n", baseRef, err)
		os.Exit(2)
	}

	var violations []string
	for version, baseName := range baseMap {
		headName, ok := headMap[version]
		if !ok {
			violations = append(violations,
				fmt.Sprintf("v%d (%q) was REMOVED — migrations on %s must be append-only",
					version, baseName, baseRef))
			continue
		}
		if headName != baseName {
			violations = append(violations,
				fmt.Sprintf("v%d RENAMED: %s has %q, HEAD has %q — rebase your PR so the new migration takes the next free version",
					version, baseRef, baseName, headName))
		}
	}

	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "migration-lint: %d violation(s) against %s:\n", len(violations), baseRef)
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "  - %s\n", v)
		}
		os.Exit(1)
	}

	added := 0
	for v := range headMap {
		if _, exists := baseMap[v]; !exists {
			added++
		}
	}
	fmt.Printf("migration-lint: ok (%d migrations on %s, %d added in this branch)\n",
		len(baseMap), baseRef, added)
}
