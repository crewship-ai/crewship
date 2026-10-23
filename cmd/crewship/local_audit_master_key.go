//go:build !clionly

package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/secrets"
)

// adoptPersistedMasterKeys gives a host-side command the master keys the
// instance's server runs with, so it can open the sealed audit keyring in
// dataDir. Non-empty variables already in the environment win (an operator who injects
// keys externally exports them); otherwise <dataDir>/secrets.env supplies
// ENCRYPTION_KEY, any ENCRYPTION_KEY_V<N> and the active key version.
func adoptPersistedMasterKeys(dataDir string) error {
	persisted, err := secrets.ReadPersisted(filepath.Join(dataDir, "secrets.env"))
	if err != nil {
		return err
	}
	for name, value := range persisted {
		if name != "ENCRYPTION_KEY" && name != encryption.KeyVersionEnvVar && !strings.HasPrefix(name, "ENCRYPTION_KEY_V") {
			continue
		}
		if current := os.Getenv(name); current != "" || value == "" {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	return nil
}
