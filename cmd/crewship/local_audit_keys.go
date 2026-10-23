//go:build !clionly

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
)

// localAuditSigningContext loads the keyring of the instance that owns the
// target database. It is read-only: local recovery never mints a keyring,
// because a key the server does not hold would sign entries it cannot verify
// (or be adopted by its next boot). The standalone CLI has no access to a
// running server's in-process revocation notification registry.
func localAuditSigningContext(ctx context.Context, target localDBTarget) (context.Context, error) {
	path, err := localAuditKeyringPath(target)
	if err != nil {
		return nil, err
	}
	if err := adoptPersistedMasterKeys(filepath.Dir(path)); err != nil {
		return nil, err
	}
	keys, err := journal.LoadSigningKeys(path)
	if err != nil {
		return nil, fmt.Errorf("audit keyring %s: %w", path, err)
	}
	return journal.WithSigningKeys(ctx, keys), nil
}

// localAuditKeyringPath mirrors where `crewship start` keeps the keyring: an
// explicit CREWSHIP_AUDIT_KEYRING_PATH, else <data dir>/audit-keyring.json.
// The data dir is the one holding the target database (the standard layout)
// or, when the operator names one with CREWSHIP_HOME/CREWSHIP_DATA_DIR or the
// database is the default data dir's own, that root. A DATABASE_URL never
// borrows the unrelated default ~/.crewship keyring.
func localAuditKeyringPath(target localDBTarget) (string, error) {
	if p := strings.TrimSpace(os.Getenv(journal.SigningKeysPathEnv)); p != "" {
		return p, nil
	}
	var candidates []string
	if target.Path != "" {
		abs, err := filepath.Abs(target.Path)
		if err != nil {
			return "", err
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(abs), journal.SigningKeysFile))
	}
	namedRoot := strings.TrimSpace(os.Getenv("CREWSHIP_HOME")) != "" || strings.TrimSpace(os.Getenv("CREWSHIP_DATA_DIR")) != ""
	if target.Origin != "DATABASE_URL" || namedRoot {
		root, err := database.ResolveDataDirRoot("")
		if err != nil {
			return "", err
		}
		if root, err = filepath.Abs(root); err != nil {
			return "", err
		}
		if c := filepath.Join(root, journal.SigningKeysFile); len(candidates) == 0 || c != candidates[0] {
			candidates = append(candidates, c)
		}
	}
	var found []string
	for _, c := range candidates {
		if _, err := os.Lstat(c); err == nil {
			found = append(found, c)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect audit keyring %s: %w", c, err)
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) == 0:
		return "", fmt.Errorf("no audit keyring for the database at %s (looked for %s): start that instance once so it creates its keyring, or set %s to the keyring its server uses; local recovery does not create one",
			target.Path, strings.Join(candidates, " and "), journal.SigningKeysPathEnv)
	}
	if same, err := sameSigningKeys(found[0], found[1]); err != nil || !same {
		return "", fmt.Errorf("two different audit keyrings could belong to the database at %s (%s): set %s to the one its server uses",
			target.Path, strings.Join(found, " and "), journal.SigningKeysPathEnv)
	}
	return found[0], nil
}

// sameSigningKeys compares two candidate keyrings by their key sets. Master
// keys already in the environment win over either data dir's secrets.env, and
// adoptPersistedMasterKeys only fills variables that are still empty: the
// first candidate's secrets.env supplies the keys for both. A second keyring
// sealed under a different master key therefore fails to open, and the caller
// reports the ambiguity instead of guessing.
func sameSigningKeys(a, b string) (bool, error) {
	var docs [2][]byte
	for i, p := range []string{a, b} {
		if err := adoptPersistedMasterKeys(filepath.Dir(p)); err != nil {
			return false, err
		}
		keys, err := journal.LoadSigningKeys(p)
		if err != nil {
			return false, err
		}
		if docs[i], err = journal.ExportSigningKeys(keys); err != nil {
			return false, err
		}
	}
	return bytes.Equal(docs[0], docs[1]), nil
}
