//go:build linux

package docker

import (
	"os"
)

func stagedOwned(info os.FileInfo) bool {
	return safeLauncherOwner(info)
}
