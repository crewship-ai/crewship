//go:build !linux

package docker

import "os"

// The staged pilot is qualified on native Linux only; other host platforms
// retain the unselected legacy runtime until separately qualified.
func stagedOwned(os.FileInfo) bool { return false }
