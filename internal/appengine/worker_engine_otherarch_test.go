//go:build linux && !amd64

package appengine

// foreignArchRefused has no foreign architecture to probe on this build.
func foreignArchRefused() bool { return true }
