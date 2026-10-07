package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// A private per-user cache contains one immutable template per migration hash
// and PID-owned fixture directories. A persistent lock file serializes template
// publication and stale-directory collection; never unlink that lock file.
func fixtureRoot() (string, error) {
	root := filepath.Join(os.TempDir(), fmt.Sprintf("crewship-test-fixtures-%d", os.Getuid()))
	if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || !fixtureRootOwned(info) {
		return "", fmt.Errorf("fixture cache must be an owned private directory: %s", root)
	}
	return root, nil
}

func sharedTemplate(root, key string, build func(string) error) (string, error) {
	unlock, err := lockFixtureCache(filepath.Join(root, ".lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	// The format prefix invalidates copies if fixture construction changes.
	dir := filepath.Join(root, "template-v1-"+key)
	path := filepath.Join(dir, "template.db")
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return path, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	// Any unpublished build here belongs to a builder that lost the lock (for
	// example through SIGKILL). It is safe to discard under the same lock.
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	staging := filepath.Join(dir, "building.db")
	if err := build(staging); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.Chmod(staging, 0o400); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.Rename(staging, path); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

var reclaimFixturesOnce sync.Once

func newMigratedTestDir() (string, error) {
	root, err := fixtureRoot()
	if err != nil {
		return "", err
	}
	// Scan once per binary, not once per test: large packages can hold hundreds
	// of fixtures concurrently. Cleanup remains best-effort for detached writers.
	reclaimFixturesOnce.Do(func() { _ = reclaimDeadFixtures(root) })
	return os.MkdirTemp(root, fmt.Sprintf("testdb-%d-", os.Getpid()))
}

func reclaimDeadFixtures(root string) error {
	unlock, err := lockFixtureCache(filepath.Join(root, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "testdb-") {
			continue
		}
		owner, _, ok := strings.Cut(strings.TrimPrefix(entry.Name(), "testdb-"), "-")
		pid, err := strconv.Atoi(owner)
		// Unknown ownership and permission errors always mean leave it alone.
		// PID reuse also keeps a directory rather than risking a live owner's DB.
		if !ok || err != nil || pid <= 0 || !fixtureProcessDead(pid) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, entry.Name()))
	}
	return nil
}
