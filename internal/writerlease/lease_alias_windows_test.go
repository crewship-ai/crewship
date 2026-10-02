//go:build windows

package writerlease

import "path/filepath"

// Windows symlink creation requires a developer-mode/privilege contract that
// CI does not promise. Mandatory inode alias coverage uses a real hard link.
func leaseTestAliases(dir, path string) []string {
	return []string{path, filepath.Join(dir, "hardlink.db")}
}
