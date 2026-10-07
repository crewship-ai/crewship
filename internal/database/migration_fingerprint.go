package database

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// Include Go migration implementations as well as SQL: a version-only or
// SQL-only key would reuse stale fixtures after a Go migration changes.
// Test sources matched by the glob are deliberately excluded from the hash.
//
//go:embed migrate*.go migrations
var migrationSources embed.FS

// MigrationFingerprint identifies the migration inputs compiled into this
// binary. Test fixture caches use it without depending on a source checkout.
func MigrationFingerprint() (string, error) {
	if migrationRegistryErr != nil {
		return "", migrationRegistryErr
	}
	return migrationFingerprint(migrationSources)
}

func migrationFingerprint(sources fs.FS) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(sources, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasSuffix(path, "_test.go") ||
			(!strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".sql")) {
			return nil
		}
		body, err := fs.ReadFile(sources, path)
		if err != nil {
			return err
		}
		// Lengths make paths and bodies unambiguous even with embedded NULs.
		fmt.Fprintf(h, "%d:%s%d:", len(path), path, len(body))
		_, _ = h.Write(body)
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
