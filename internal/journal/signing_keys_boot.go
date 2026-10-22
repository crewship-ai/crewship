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
	"time"
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

// CheckSigningKeysReplaceable is recovery's check before it touches anything:
// the app-owned keyring in dataDir is either absent or a regular file that
// ReplaceSigningKeys can keep as a backup. A symlink or directory is refused.
func CheckSigningKeysReplaceable(dataDir string) error {
	info, err := os.Lstat(filepath.Join(dataDir, SigningKeysFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("audit keyring: cannot inspect the existing keyring")
	}
	if !info.Mode().IsRegular() {
		return errors.New("audit keyring: the existing " + SigningKeysFile + " is not a regular file; move it aside before recovering")
	}
	return nil
}

// ReplaceSigningKeys installs the encrypted RecoveryKit's exact retained key
// set as the app-owned keyring of dataDir. An identical keyring is kept as is.
// A different one (a target that already booted mints its own) is never
// deleted: it is hard-linked to <file>.replaced-<UTC time>, keeping its bytes
// and mode, before the kit's keyring is renamed over it. It returns the
// keyring path and the backup path ("" when nothing was replaced).
func ReplaceSigningKeys(dataDir string, data []byte, now time.Time) (string, string, error) {
	if !filepath.IsAbs(dataDir) {
		return "", "", errors.New("audit keyring: absolute data directory required")
	}
	keys, err := ParseSigningKeys(data)
	if err != nil {
		return "", "", err
	}
	canonical, err := ExportSigningKeys(keys)
	if err != nil {
		return "", "", err
	}
	if err := CheckSigningKeysReplaceable(dataDir); err != nil {
		return "", "", err
	}
	path, backup := filepath.Join(dataDir, SigningKeysFile), ""
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err := publishSigningKeys(dataDir, path, canonical); err != nil {
			return "", "", err
		}
	} else if same, _ := signingKeysFileEquals(path, canonical); !same {
		backup = path + ".replaced-" + now.UTC().Format("20060102T150405.000000000Z")
		if err := os.Link(path, backup); err != nil {
			return "", "", errors.New("audit keyring: cannot keep a backup of the existing keyring")
		}
		temp, err := writePrivateTemp(dataDir, canonical)
		if err != nil {
			return "", "", err
		}
		defer os.Remove(temp)
		if err := os.Rename(temp, path); err != nil {
			return "", "", errors.New("audit keyring: cannot install the recovered keyring")
		}
		if err := syncDir(dataDir); err != nil {
			return "", "", err
		}
	}
	if same, err := signingKeysFileEquals(path, canonical); err != nil || !same {
		return "", "", errors.Join(err, errors.New("audit keyring: installed keyring differs from recovery kit"))
	}
	return path, backup, nil
}

func signingKeysFileEquals(path string, canonical []byte) (bool, error) {
	actual, err := LoadSigningKeys(path)
	if err != nil {
		return false, err
	}
	raw, err := ExportSigningKeys(actual)
	if err != nil {
		return false, err
	}
	return bytes.Equal(raw, canonical), nil
}

func publishSigningKeys(dataDir, path string, data []byte) error {
	temp, err := writePrivateTemp(dataDir, data)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if err := os.Link(temp, path); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("audit keyring: exclusive publication failed")
	}
	return syncDir(dataDir)
}

// writePrivateTemp writes a complete, synced 0600 file beside the keyring.
func writePrivateTemp(dataDir string, data []byte) (string, error) {
	file, err := os.CreateTemp(dataDir, ".audit-keyring-*.tmp")
	if err != nil {
		return "", errors.New("audit keyring: cannot create private temporary file")
	}
	temp := file.Name()
	err = file.Chmod(0600)
	if err == nil {
		if _, werr := file.Write(data); werr != nil {
			err = errors.New("audit keyring: cannot write private file")
		}
	}
	if err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(temp)
		return "", err
	}
	return temp, nil
}

func syncDir(dataDir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(dataDir)
	if err != nil {
		return err
	}
	err = dir.Sync()
	return errors.Join(err, dir.Close())
}
