//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package mooring

import "os"

func openOriginNodeLock(*os.Root) (*os.File, error) { return nil, ErrOriginNodeData }
