//go:build windows

package docker

import "os"

func safeLauncherOwner(os.FileInfo) bool { return false }

func safeLauncherDirectory(os.FileInfo) bool { return false }
