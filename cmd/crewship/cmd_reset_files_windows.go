//go:build !clionly && windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// os.File.Sync does not support directory handles on Windows.
func resetSyncDirectory(_ *os.File) error { return nil }

func resetSingleLink(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("reset refuses multiply linked database/state file: %s", path)
	}
	return nil
}
