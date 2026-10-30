//go:build !unix

package pagebuild

func matchDirOwner(string, string) error { return nil }
