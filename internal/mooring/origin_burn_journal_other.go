//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package mooring

import "os"

// An unsupported platform must not silently weaken locking/durability.
const originBurnSupported = false

func openOriginBurnFile(*os.Root, int) (*os.File, error) { return nil, ErrOriginBurnJournal }
func lockOriginBurnFile(*os.File) error                  { return ErrOriginBurnJournal }
func originBurnSafeFile(os.FileInfo) bool                { return false }
