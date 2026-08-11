//go:build unix

package server

import (
	"strings"
	"testing"
)

// TestWSAECONNREFUSEDIsNotAPosixErrno guards the one way the portable match
// could misfire: if some unix ever defined errno 10061, isConnRefused would
// start reading that unrelated failure as proof of deadness and unlink a path
// it could not verify. Errno numbering on every unix Go supports tops out
// around 150, so the check is a cheap standing assertion rather than a
// hypothetical.
//
// It is a whole-file build tag rather than a runtime.GOOS skip because the
// assertion is meaningless on windows — there 10061 *is* the errno being
// matched, so the test would have to assert the opposite of itself. A skip
// would report `ok` on the platform where it does not hold; the tag says so at
// compile time. The unix side is where the collision would have to happen, and
// that is where this runs.
func TestWSAECONNREFUSEDIsNotAPosixErrno(t *testing.T) {
	if msg := wsaeconnrefused.Error(); !strings.Contains(strings.ToLower(msg), "errno 10061") &&
		!strings.Contains(strings.ToLower(msg), "unknown") {
		t.Errorf("errno 10061 has a real meaning on this platform (%q); the portable "+
			"connection-refused match could misclassify it", msg)
	}
}
