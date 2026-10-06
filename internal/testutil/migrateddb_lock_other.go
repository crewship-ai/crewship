//go:build !unix

package testutil

import "errors"

// Without flock the shared template is disabled and every process builds its
// own, which is what the helper always did.
const sharedTemplateLockSupported = false

func lockFile(string, bool) (func(), error) {
	return nil, errors.New("file locks unsupported on this platform")
}
