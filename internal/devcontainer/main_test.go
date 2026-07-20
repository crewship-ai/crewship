package devcontainer

import (
	"os"
	"testing"
)

// TestMain isolates the whole package from the developer's Docker credential
// state. Provisioner tests exercise ensureImage, whose digest lookup would
// otherwise read the real ~/.docker/config.json and exec the host `credsStore`
// helper; a slow or broken helper makes the lookup return "" and ensureImage
// falls through to "trust the local copy, skip the pull" — flaking pull-count
// assertions locally while staying green in CI (no config.json there).
//
// This has to be TestMain rather than t.Setenv: affected tests call
// t.Parallel(), and t.Setenv panics in a parallel test.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "crewship-devcontainer-config-*")
	if err != nil {
		panic("create temp DOCKER_CONFIG dir: " + err.Error())
	}
	if err := os.Setenv("DOCKER_CONFIG", dir); err != nil {
		panic("set DOCKER_CONFIG: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
