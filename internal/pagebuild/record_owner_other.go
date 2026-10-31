//go:build !unix

package pagebuild

import "os"

func matchDirOwner(string, string) error { return nil }

func matchFileOwner(*os.File, string) error { return nil }
