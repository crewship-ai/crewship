//go:build !linux && !darwin && !windows

package writerlease

import "os"

func lock(*os.File) error           { return ErrUnsupportedStorage }
func supportedStorage(string) error { return ErrUnsupportedStorage }
