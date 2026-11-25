package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
)

// A recovered tree is built inside a private stage that starts as a copy of
// the existing data directory, symlinks included. Its content is not
// trusted: a workspace file can be replaced by a link after the backup. No
// path recovery writes may pass through a link, so a link can never carry a
// write out of the stage (or into the stage's own other directories).

// refuseLinkedPath reports an error when any existing component of path
// below root, including the last, is a symlink. Components that do not exist
// yet are fine: they are created as real directories.
func refuseLinkedPath(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("recovery path %s is outside the recovery directory", path)
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ := filepath.Rel(root, cur)
			return fmt.Errorf("recovery would write %s through the link %s; replace the link with a real directory or move it aside", rel, link)
		}
	}
	return nil
}

// refuseLinkedRecoveryPaths names, in one error, every fixed location
// recovery writes that is a link or sits below one. Links that no recovery
// path goes through stay as they are.
func refuseLinkedRecoveryPaths(dataDir string) error {
	fixed := []string{
		"crewship.db", "crewship.db-wal", "crewship.db-shm", "crewship.db-journal",
		recoveredSecretsFile, RecoveredKeysFile, journal.SigningKeysFile,
		RecoveredServicesDir, RecoveredCrewsDir, RecoveredEnvironmentsDir,
	}
	for _, store := range InstanceStores {
		// The full path below dataDir, so a store placed deeper is guarded too.
		rel, err := filepath.Rel(dataDir, RecoveredStoreDir(dataDir, store))
		if err != nil {
			return err
		}
		fixed = append(fixed, rel)
	}
	var bad []string
	for _, name := range fixed {
		if err := refuseLinkedPath(dataDir, filepath.Join(dataDir, name)); err != nil {
			bad = append(bad, err.Error())
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("recovery cannot use the current data directory: %d path(s) go through a link: %s", len(bad), strings.Join(bad, "; "))
}
