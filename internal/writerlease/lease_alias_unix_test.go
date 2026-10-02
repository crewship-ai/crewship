//go:build linux || darwin

package writerlease

import "path/filepath"

func leaseTestAliases(dir, path string) []string {
	return []string{path, filepath.Join(dir, "symlink.db"), filepath.Join(dir, "hardlink.db")}
}
