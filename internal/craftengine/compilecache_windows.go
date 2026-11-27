//go:build windows

package craftengine

// privateDir: Windows servers do not use the compilation cache.
func privateDir(string) error { return errNotPrivate }
