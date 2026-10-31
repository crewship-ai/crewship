//go:build !unix

package pagebuild

import "os"

func lockFile(*os.File) error { return nil }
