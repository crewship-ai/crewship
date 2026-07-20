package docker

import (
	"os"
	"testing"
)

// TestMain isolates the whole package from the developer's Docker credential
// state. Several tests here drive ensureImage against a loopback httptest
// registry; with the real ~/.docker/config.json in scope the digest lookup
// shells out to the host `credsStore` helper, and a slow or broken helper makes
// the HEAD time out. The digest then comes back "" and ensureImage takes the
// "remote unknown → trust local, skip pull" branch, so pull-count assertions go
// red on developer machines and stay green in CI (which has no config.json).
//
// This has to be TestMain rather than t.Setenv: the affected tests call
// t.Parallel(), and t.Setenv panics in a parallel test.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "crewship-docker-config-*")
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
