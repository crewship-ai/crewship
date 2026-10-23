package journal

import (
	"bytes"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// The app-owned keyring is sealed at rest with AES-256-GCM under a key derived
// (HKDF-SHA256, distinct purpose label) from the configured master key. The
// envelope names the master-key version, so a rotation that keeps the old
// ENCRYPTION_KEY_V<N> configured can still open it and re-wrap it at boot.
const (
	sealedKeyringFormat = "crewship-audit-keyring-sealed-v1"
	keyringWrapLabel    = "crewship audit keyring at rest v1"
)

type sealedKeyringDocument struct {
	Format   string `json:"format"`
	Envelope string `json:"envelope"`
}

// MasterKeyFunc returns the 32-byte master key of an envelope version.
type MasterKeyFunc func(version string) ([]byte, error)

// environmentMasterKey resolves master keys exactly as Decrypt does.
func environmentMasterKey(version string) ([]byte, error) {
	master, err := encryption.ResolveKey(version)
	if err != nil {
		return nil, err
	}
	return master.Key, nil
}

func keyringWrapKey(version string, master MasterKeyFunc) ([]byte, error) {
	key, err := master(version)
	if err != nil {
		return nil, fmt.Errorf("audit keyring: master key %s unavailable: %w", version, err)
	}
	return hkdf.Key(sha256.New, key, nil, keyringWrapLabel, 32)
}

// sealSigningKeys wraps a plaintext keyring document under the current
// master-key version. It fails closed when no usable current key exists.
func sealSigningKeys(plain []byte) ([]byte, error) {
	if err := encryption.VerifyCurrentKey(); err != nil {
		return nil, fmt.Errorf("audit keyring: cannot seal at rest: %w", err)
	}
	version, err := encryption.CurrentKeyVersion()
	if err != nil {
		return nil, err
	}
	key, err := keyringWrapKey(version, environmentMasterKey)
	if err != nil {
		return nil, err
	}
	envelope, err := encryption.EncryptWithKey(version, key, string(plain))
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealedKeyringDocument{Format: sealedKeyringFormat, Envelope: envelope})
}

// openSigningKeys returns the plaintext keyring document and the master-key
// version that sealed it ("" for a legacy plaintext file). Plaintext stays
// readable: operator-managed files and keyrings written before sealing.
func openSigningKeys(data []byte) ([]byte, string, error) {
	return openSigningKeysWith(data, environmentMasterKey)
}

func openSigningKeysWith(data []byte, master MasterKeyFunc) ([]byte, string, error) {
	var probe struct {
		Format *string `json:"format"`
	}
	if json.Unmarshal(data, &probe) != nil || probe.Format == nil {
		return data, "", nil
	}
	var document sealedKeyringDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.Format != sealedKeyringFormat {
		return nil, "", errors.New("audit keyring: unknown sealed format")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, "", errors.New("audit keyring: trailing data")
	}
	version, ok := encryption.ParseEnvelopeVersion(document.Envelope)
	if !ok {
		return nil, "", errors.New("audit keyring: invalid sealed envelope")
	}
	key, err := keyringWrapKey(version, master)
	if err != nil {
		return nil, "", err
	}
	plain, err := encryption.DecryptWithKeys(document.Envelope, map[string][]byte{version: key})
	if err != nil {
		return nil, "", fmt.Errorf("audit keyring: cannot open with master key %s (was ENCRYPTION_KEY changed or a retired key version removed?)", version)
	}
	return []byte(plain), version, nil
}

// sealSigningKeysAtRest re-wraps the app-owned keyring in place when it is
// still plaintext or sealed under an older master-key version. The key set is
// unchanged; the file is replaced atomically and read back before returning.
func sealSigningKeysAtRest(dataDir, path string) error {
	raw, err := readSigningKeysFile(path)
	if err != nil {
		return err
	}
	plain, version, err := openSigningKeys(raw)
	if err != nil {
		return err
	}
	current, err := encryption.CurrentKeyVersion()
	if err != nil {
		return err
	}
	if version == current {
		return nil
	}
	before, err := ParseSigningKeys(plain)
	if err != nil {
		return err
	}
	canonical, err := ExportSigningKeys(before)
	if err != nil {
		return err
	}
	if err := writeSigningKeysFile(dataDir, path, canonical); err != nil {
		return err
	}
	after, err := LoadSigningKeys(path)
	if err != nil {
		return err
	}
	if raw, err := ExportSigningKeys(after); err != nil || !bytes.Equal(raw, canonical) {
		return errors.New("audit keyring: re-wrapped keyring differs")
	}
	return nil
}

// writeSigningKeysFile seals a canonical document and renames it over path.
func writeSigningKeysFile(dataDir, path string, canonical []byte) error {
	sealed, err := sealSigningKeys(canonical)
	if err != nil {
		return err
	}
	temp, err := writePrivateTemp(dataDir, sealed)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if err := os.Rename(temp, path); err != nil {
		return errors.New("audit keyring: cannot replace keyring")
	}
	return syncDir(dataDir)
}

// ErrSigningKeyRotationConflict: the keyring's active key is not the one the
// caller reviewed, or another rotation is still pending.
var ErrSigningKeyRotationConflict = errors.New("audit keyring: active key changed")

const pendingRotationSuffix = ".pending-"

// SigningKeyRotatedAction is the instance audit action that decides a staged
// rotation.
const SigningKeyRotatedAction = "audit.signing_key_rotated"

var rotationAuditID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// StageSigningKeyRotation writes the app-owned keyring with a fresh random
// active key, every earlier key kept for verification only, as the sealed
// file <keyring>.pending-<auditID>. The live keyring is not touched: the
// caller commits the audit entry auditID, then FinishSigningKeyRotation
// renames the staged file in place. A crash in between is settled at the
// next start by ResolvePendingSigningKeyRotations.
func StageSigningKeyRotation(dataDir, expectedActive, auditID string) (previous, active, pending string, err error) {
	if !filepath.IsAbs(dataDir) || !rotationAuditID.MatchString(auditID) {
		return "", "", "", errors.New("audit keyring: invalid rotation target")
	}
	path := filepath.Join(dataDir, SigningKeysFile)
	if staged, _ := filepath.Glob(path + pendingRotationSuffix + "*"); len(staged) > 0 {
		return "", "", "", ErrSigningKeyRotationConflict
	}
	current, err := LoadSigningKeys(path)
	if err != nil {
		return "", "", "", err
	}
	if expectedActive != "" && current.activeID != expectedActive {
		return "", "", "", ErrSigningKeyRotationConflict
	}
	if len(current.keys) >= 128 {
		return "", "", "", errors.New("audit keyring: key limit reached")
	}
	key, identity := make([]byte, 32), make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return "", "", "", err
	}
	if _, err := rand.Read(identity); err != nil {
		return "", "", "", err
	}
	next := &SigningKeys{activeID: "key-" + hex.EncodeToString(identity), keys: map[string][]byte{}}
	for id, k := range current.keys {
		next.keys[id] = k
	}
	next.keys[next.activeID] = key
	canonical, err := ExportSigningKeys(next)
	if err != nil {
		return "", "", "", err
	}
	sealed, err := sealSigningKeys(canonical)
	if err != nil {
		return "", "", "", err
	}
	pending = path + pendingRotationSuffix + auditID
	if err := publishSigningKeysExclusive(dataDir, pending, sealed); err != nil {
		return "", "", "", err
	}
	return current.activeID, next.activeID, pending, nil
}

// FinishSigningKeyRotation installs a staged keyring after its audit entry
// committed. It refuses a staged file that dropped any current key.
func FinishSigningKeyRotation(dataDir, pending string) error {
	path := filepath.Join(dataDir, SigningKeysFile)
	if filepath.Dir(pending) != filepath.Clean(dataDir) || !strings.HasPrefix(filepath.Base(pending), SigningKeysFile+pendingRotationSuffix) {
		return errors.New("audit keyring: invalid staged rotation")
	}
	current, err := LoadSigningKeys(path)
	if err != nil {
		return err
	}
	staged, err := LoadSigningKeys(pending)
	if err != nil {
		return err
	}
	for id, key := range current.keys {
		if !bytes.Equal(staged.keys[id], key) {
			return errors.New("audit keyring: staged rotation does not retain every key")
		}
	}
	if err := os.Rename(pending, path); err != nil {
		return errors.New("audit keyring: cannot install the rotated keyring")
	}
	return syncDir(dataDir)
}

// PendingRotation is what the instance audit log says about one staged
// rotation: whether its entry exists, the active key it names, and whether
// it is the newest rotation entry.
type PendingRotation struct {
	Found       bool
	ActiveKeyID string
	Newest      bool
}

// ResolvePendingSigningKeyRotations settles rotations a crash interrupted.
// A staged keyring is installed only when its audit entry exists, is the
// newest rotation, names exactly the staged active key, and that key is not
// already in the live keyring (a replayed or stale file is not a rotation).
// Anything else is set aside as .discarded-<auditID> (never installed, never
// overwritten) without failing the start; a lookup error leaves the file in
// place for the next start. It reports whether the live keyring changed and
// one note per file it did not install.
func ResolvePendingSigningKeyRotations(dataDir string, lookup func(auditID string) (PendingRotation, error)) (bool, []string, error) {
	path := filepath.Join(dataDir, SigningKeysFile)
	staged, err := filepath.Glob(path + pendingRotationSuffix + "*")
	if err != nil {
		return false, nil, err
	}
	changed := false
	var notes []string
	for _, pending := range staged {
		auditID := strings.TrimPrefix(filepath.Base(pending), SigningKeysFile+pendingRotationSuffix)
		reason := ""
		if !rotationAuditID.MatchString(auditID) {
			reason = "invalid audit entry id"
		} else if record, err := lookup(auditID); err != nil {
			notes = append(notes, filepath.Base(pending)+": left for the next start: "+err.Error())
			continue
		} else {
			reason = pendingRotationMismatch(path, pending, record)
		}
		if reason == "" {
			if err := FinishSigningKeyRotation(dataDir, pending); err != nil {
				reason = err.Error()
			} else {
				changed = true
				continue
			}
		}
		kept, err := setAsideRotation(dataDir, pending, path+".discarded-"+auditID)
		if err != nil {
			return changed, notes, err
		}
		notes = append(notes, filepath.Base(pending)+" not installed ("+reason+"); kept as "+filepath.Base(kept))
	}
	return changed, notes, nil
}

func pendingRotationMismatch(path, pending string, record PendingRotation) string {
	switch {
	case !record.Found:
		return "its audit entry never committed"
	case !record.Newest:
		return "a newer rotation was recorded"
	}
	staged, err := LoadSigningKeys(pending)
	if err != nil {
		return err.Error()
	}
	if staged.ActiveID() != record.ActiveKeyID {
		return "its active key is not the one the audit entry names"
	}
	current, err := LoadSigningKeys(path)
	if err != nil {
		return err.Error()
	}
	if _, applied := current.keys[record.ActiveKeyID]; applied {
		return "that rotation is already applied"
	}
	return ""
}

// setAsideRotation links pending to a name that does not exist yet (target,
// then target.1, target.2, …) and removes pending only after that link.
func setAsideRotation(dataDir, pending, target string) (string, error) {
	for i := 0; i < 100; i++ {
		name := target
		if i > 0 {
			name = fmt.Sprintf("%s.%d", target, i)
		}
		err := os.Link(pending, name)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", errors.New("audit keyring: cannot set aside a staged rotation")
		}
		if err := os.Remove(pending); err != nil {
			return "", err
		}
		return name, syncDir(dataDir)
	}
	return "", errors.New("audit keyring: too many set-aside rotations")
}
