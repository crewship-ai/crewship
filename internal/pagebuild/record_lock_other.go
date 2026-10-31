//go:build !unix

package pagebuild

import "os"

const lockOpenNoFollow = 0

func lockFile(*os.File) error { return nil }
