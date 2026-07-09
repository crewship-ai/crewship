package update

import (
	"strings"
	"testing"
)

// #945: self-update is gated off on Windows until zip extraction +
// rename-self-aside land (#946). The gate must be an actionable error —
// pointing at the release zip — not a mid-flight failure on a .tar.gz
// asset name or an ERROR_ACCESS_DENIED rename over the running exe.
func TestErrSelfUpdateUnsupported(t *testing.T) {
	err := errSelfUpdateUnsupported("windows")
	if err == nil {
		t.Fatal("windows must be reported unsupported")
	}
	for _, want := range []string{"Windows", "zip", "releases"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}

	for _, goos := range []string{"linux", "darwin"} {
		if err := errSelfUpdateUnsupported(goos); err != nil {
			t.Errorf("%s must stay supported, got %v", goos, err)
		}
	}
}
