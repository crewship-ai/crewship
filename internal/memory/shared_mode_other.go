//go:build !unix

package memory

import "os"

// Non-unix platforms have no setgid directories, so inSharedTree is never
// true there and these never have work to do.
func shareNewDir(string) error { return nil }

func shareConfinedChild(_, _ *confinedDir) error { return nil }

func canKeepSetgid(os.FileInfo) bool { return false }
