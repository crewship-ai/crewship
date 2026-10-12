//go:build !clionly && !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func resetSyncDirectory(directory *os.File) error { return directory.Sync() }

func resetSingleLink(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("reset refuses multiply linked or unverified database/state file: %s", path)
	}
	return nil
}
