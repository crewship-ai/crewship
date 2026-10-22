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
// The app-owned default is sealed at rest: a plaintext file, or one sealed
// under an older master-key version, is re-wrapped in place (same keys).
func EnsureSigningKeys(dataDir, explicitPath string) (*SigningKeys, string, error) {
	path := ""
	if filepath.IsAbs(dataDir) {
		path = filepath.Join(dataDir, SigningKeysFile)
	}
	if explicitPath != "" && filepath.Clean(explicitPath) != path {
		keys, err := LoadSigningKeys(explicitPath)
		return keys, explicitPath, err
	}
	if path == "" {
		return nil, "", errors.New("audit keyring: absolute data directory required")
	}
	if _, err := os.Lstat(path); err == nil {
		keys, err := LoadSigningKeys(path)
		if err != nil {
			return nil, "", err
		}
		if err := sealSigningKeysAtRest(dataDir, path); err != nil {
			return nil, "", err
		}
		return keys, path, nil
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
	if data, err = sealSigningKeys(data); err != nil {
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
// set as the app-owned keyring of dataDir, sealed under the master key in the
// environment (recovery runs it under the kit's keys). An identical keyring is
// kept as is. A different one (a target that already booted mints its own) is
// never deleted: it is kept as <file>.replaced-<UTC time>, re-sealed under the
// same master key when previousMaster (the keys it was sealed with) opens it,
// byte for byte otherwise. It returns the keyring path, the backup path ("" when
// nothing was replaced) and, when the previous keyring could not be opened,
// why ("" otherwise). Nothing is written in plaintext.
func ReplaceSigningKeys(dataDir string, data []byte, now time.Time, previousMaster MasterKeyFunc) (string, string, string, error) {
	if !filepath.IsAbs(dataDir) {
		return "", "", "", errors.New("audit keyring: absolute data directory required")
	}
	keys, err := ParseSigningKeys(data)
	if err != nil {
		return "", "", "", err
	}
	canonical, err := ExportSigningKeys(keys)
	if err != nil {
		return "", "", "", err
	}
	if err := CheckSigningKeysReplaceable(dataDir); err != nil {
		return "", "", "", err
	}
	path, backup, unreadable := filepath.Join(dataDir, SigningKeysFile), "", ""
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		sealed, err := sealSigningKeys(canonical)
		if err != nil {
			return "", "", "", err
		}
		if err := publishSigningKeys(dataDir, path, sealed); err != nil {
			return "", "", "", err
		}
	} else if same, _ := signingKeysFileEquals(path, canonical); !same {
		raw, err := readSigningKeysFile(path)
		if err != nil {
			return "", "", "", err
		}
		kept := raw
		if previous, openErr := openPreviousSigningKeys(raw, previousMaster); openErr != nil {
			unreadable = openErr.Error()
		} else if kept, err = sealSigningKeys(previous); err != nil {
			return "", "", "", err
		}
		backup = path + ".replaced-" + now.UTC().Format("20060102T150405.000000000Z")
		if err := publishSigningKeysExclusive(dataDir, backup, kept); err != nil {
			return "", "", "", errors.New("audit keyring: cannot keep a backup of the existing keyring")
		}
		if err := writeSigningKeysFile(dataDir, path, canonical); err != nil {
			return "", "", "", err
		}
	}
	if same, err := signingKeysFileEquals(path, canonical); err != nil || !same {
		return "", "", "", errors.Join(err, errors.New("audit keyring: installed keyring differs from recovery kit"))
	}
	return path, backup, unreadable, nil
}

// openPreviousSigningKeys opens a keyring being replaced: plaintext, sealed
// under the master key now in the environment, or under previousMaster.
func openPreviousSigningKeys(raw []byte, previousMaster MasterKeyFunc) ([]byte, error) {
	plain, _, err := openSigningKeys(raw)
	if err != nil && previousMaster != nil {
		plain, _, err = openSigningKeysWith(raw, previousMaster)
	}
	if err != nil {
		return nil, err
	}
	if _, err := ParseSigningKeys(plain); err != nil {
		return nil, err
	}
	return plain, nil
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
	err := publishSigningKeysExclusive(dataDir, path, data)
	if errors.Is(err, os.ErrExist) {
		return syncDir(dataDir)
	}
	return err
}

// publishSigningKeysExclusive links a complete private file at path, failing
// with os.ErrExist rather than replacing anything already there.
func publishSigningKeysExclusive(dataDir, path string, data []byte) error {
	temp, err := writePrivateTemp(dataDir, data)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if err := os.Link(temp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return err
		}
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
