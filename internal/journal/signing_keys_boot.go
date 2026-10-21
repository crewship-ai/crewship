package journal

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const SigningKeysFile = "audit-keyring.json"

// defaultSigningKeys keeps low-level callers versioned without writing files.
// Real server/CLI boot supplies EnsureSigningKeys' persisted random active key.
// Legacy root is retained solely for verification; even this bootstrap signing
// key has its own purpose-separated derivation and explicit v2 identity.
func defaultSigningKeys() *SigningKeys {
	return BootstrapSigningKeys(ChainKeyFromEnv())
}

// BootstrapSigningKeys upgrades an old RecoveryKit root into versioned writers
// without modifying the historical legacy-v1 verification root.
func BootstrapSigningKeys(legacy []byte) *SigningKeys {
	legacy = append([]byte(nil), legacy...)
	derive := hmac.New(sha256.New, legacy)
	_, _ = derive.Write([]byte("crewship audit bootstrap signing v2\x00"))
	return &SigningKeys{activeID: "bootstrap-v2", keys: map[string][]byte{LegacySigningKeyID: legacy, "bootstrap-v2": derive.Sum(nil)}}
}

// EnsureSigningKeys is an application boot operation, never an append-time
// key generator. Explicit external configuration is read-only. The app-owned
// default is published complete and exclusively, so concurrent boots load the
// winner rather than overwrite keys. Invalid existing files are never repaired.
func EnsureSigningKeys(dataDir, explicitPath string) (*SigningKeys, string, error) {
	if explicitPath != "" {
		keys, err := LoadSigningKeys(explicitPath)
		return keys, explicitPath, err
	}
	if !filepath.IsAbs(dataDir) {
		return nil, "", errors.New("audit keyring: absolute data directory required")
	}
	path := filepath.Join(dataDir, SigningKeysFile)
	if _, err := os.Lstat(path); err == nil {
		keys, err := LoadSigningKeys(path)
		return keys, path, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", errors.New("audit keyring: cannot inspect default file")
	}
	key, identity := make([]byte, 32), make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, "", err
	}
	if _, err := rand.Read(identity); err != nil {
		return nil, "", err
	}
	document := signingKeyDocument{ActiveID: "key-" + hex.EncodeToString(identity), Keys: []signingKeyRecord{
		{ID: LegacySigningKeyID, Key: base64.StdEncoding.EncodeToString(ChainKeyFromEnv())},
		{ID: "bootstrap-v2", Key: base64.StdEncoding.EncodeToString(defaultSigningKeys().keys["bootstrap-v2"])},
		{ID: "key-" + hex.EncodeToString(identity), Key: base64.StdEncoding.EncodeToString(key)},
	}}
	data, err := json.Marshal(document)
	if err != nil {
		return nil, "", err
	}
	if err := publishSigningKeys(dataDir, path, data); err != nil {
		return nil, "", err
	}
	keys, err := LoadSigningKeys(path)
	return keys, path, err
}

// RestoreSigningKeys publishes the encrypted RecoveryKit's exact retained key
// set to a fresh app data directory. Existing different keys are never replaced.
func RestoreSigningKeys(dataDir string, data []byte) (string, error) {
	if !filepath.IsAbs(dataDir) {
		return "", errors.New("audit keyring: absolute data directory required")
	}
	keys, err := ParseSigningKeys(data)
	if err != nil {
		return "", err
	}
	canonical, err := ExportSigningKeys(keys)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, SigningKeysFile)
	if err := publishSigningKeys(dataDir, path, canonical); err != nil {
		return "", err
	}
	actual, err := LoadSigningKeys(path)
	if err != nil {
		return "", err
	}
	actualRaw, err := ExportSigningKeys(actual)
	if err != nil || !bytes.Equal(actualRaw, canonical) {
		return "", errors.New("audit keyring: existing destination differs from recovery kit")
	}
	return path, nil
}

func publishSigningKeys(dataDir, path string, data []byte) error {
	file, err := os.CreateTemp(dataDir, ".audit-keyring-*.tmp")
	if err != nil {
		return errors.New("audit keyring: cannot create private temporary file")
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return errors.New("audit keyring: cannot write private file")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(temp, path); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("audit keyring: exclusive publication failed")
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(dataDir)
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
