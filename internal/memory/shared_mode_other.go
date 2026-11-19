//go:build !unix

package memory

import "os"

// Non-unix platforms have no setgid directories, so inSharedTree is never
// true there and these are never reached with work to do.
func chmodDirNoFollow(string, os.FileMode) error { return nil }

func shareConfinedChild(_, _ *confinedDir) error { return nil }
