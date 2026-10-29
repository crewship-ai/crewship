package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	atRestKeyV1 = "1111111111111111111111111111111111111111111111111111111111111111"
	atRestKeyV2 = "2222222222222222222222222222222222222222222222222222222222222222"
)

func exportedKeys(t *testing.T, keys *SigningKeys) []byte {
	t.Helper()
	raw, err := ExportSigningKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sealedVersion(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("key_b64")) || bytes.Contains(raw, []byte("active_key_id")) {
		return ""
	}
	var document sealedKeyringDocument
	if err := json.Unmarshal(raw, &document); err != nil || document.Format != sealedKeyringFormat {
		t.Fatalf("keyring is neither plaintext nor sealed: %s", raw)
	}
	version, _, _ := strings.Cut(document.Envelope, ":")
	return version
}

// The app-owned keyring is sealed at rest under a key derived from the master
// key, existing plaintext files migrate at boot without changing a key, and a
// master-key rotation that keeps the old version configured re-wraps it.
func TestAuditKeyringSealedAtRest(t *testing.T) {
	for _, tc := range []string{"new", "plaintext_migrates", "master_rotation_rewraps", "wrong_master_refused", "external_plaintext_read_only"} {
		t.Run(tc, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", atRestKeyV1)
			t.Setenv("ENCRYPTION_KEY_V2", "")
			t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
			dir := t.TempDir()
			path := filepath.Join(dir, SigningKeysFile)
			switch tc {
			case "plaintext_migrates":
				plain, err := ExportSigningKeys(BootstrapSigningKeys(ChainKeyFromEnv()))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, plain, 0o600); err != nil {
					t.Fatal(err)
				}
			case "external_plaintext_read_only":
				plain, err := ExportSigningKeys(BootstrapSigningKeys(ChainKeyFromEnv()))
				if err != nil {
					t.Fatal(err)
				}
				external := filepath.Join(t.TempDir(), "operator-keyring.json")
				if err := os.WriteFile(external, plain, 0o600); err != nil {
					t.Fatal(err)
				}
				keys, got, err := EnsureSigningKeys(dir, external)
				if err != nil || got != external || !bytes.Equal(exportedKeys(t, keys), plain) {
					t.Fatalf("external keyring: %v", err)
				}
				if after, _ := os.ReadFile(external); !bytes.Equal(after, plain) {
					t.Error("boot rewrote an operator-managed keyring")
				}
				return
			}
			var before []byte
			if raw, err := os.ReadFile(path); err == nil {
				before = raw
			}
			keys, got, err := EnsureSigningKeys(dir, "")
			if err != nil || got != path {
				t.Fatalf("boot: %v", err)
			}
			if before != nil {
				was, err := ParseSigningKeys(before)
				if err != nil || !bytes.Equal(exportedKeys(t, was), exportedKeys(t, keys)) {
					t.Fatal("migration changed the key set")
				}
			}
			if v := sealedVersion(t, path); v != "v1" {
				t.Fatalf("keyring sealed under %q, want v1", v)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("sealed keyring mode: %v", err)
			}
			switch tc {
			case "master_rotation_rewraps":
				t.Setenv("ENCRYPTION_KEY_V2", atRestKeyV2)
				t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v2")
				rotated, _, err := EnsureSigningKeys(dir, "")
				if err != nil || !bytes.Equal(exportedKeys(t, rotated), exportedKeys(t, keys)) {
					t.Fatalf("boot after master rotation: %v", err)
				}
				if v := sealedVersion(t, path); v != "v2" {
					t.Fatalf("rotation left keyring under %q", v)
				}
				// The legacy v1 root must still be kept for audit MACs, but the
				// keyring itself now opens with the v2 key alone.
				t.Setenv("ENCRYPTION_KEY", strings.Repeat("3", 64))
				if _, err := LoadSigningKeys(path); err != nil {
					t.Fatalf("re-wrapped keyring does not open with v2: %v", err)
				}
			case "wrong_master_refused":
				sealed, _ := os.ReadFile(path)
				t.Setenv("ENCRYPTION_KEY", strings.Repeat("4", 64))
				if _, _, err := EnsureSigningKeys(dir, ""); err == nil || !strings.Contains(err.Error(), "cannot open") {
					t.Fatalf("keyring opened under a different master key: %v", err)
				}
				if after, _ := os.ReadFile(path); !bytes.Equal(after, sealed) {
					t.Error("refused boot modified the keyring")
				}
			}
		})
	}
}

// A rotation is staged beside the keyring and installed only after its audit
// entry committed. At the next start a staged file is installed only when that
// entry exists, is the newest rotation, names the staged active key and is not
// yet applied; anything else is kept aside without failing the start and
// without overwriting an earlier set-aside copy. Earlier keys always survive.
func TestStagedSigningKeyRotation(t *testing.T) {
	for _, tc := range []string{"finish", "stale_expected", "crash_committed", "crash_uncommitted", "not_newest", "wrong_active_key", "replayed_after_apply", "lookup_error", "discarded_name_taken"} {
		t.Run(tc, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", atRestKeyV1)
			t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
			dir := t.TempDir()
			keys, path, err := EnsureSigningKeys(dir, "")
			if err != nil {
				t.Fatal(err)
			}
			live, _ := os.ReadFile(path)
			expected := keys.ActiveID()
			if tc == "stale_expected" {
				expected = "key-not-current"
			}
			previous, active, pending, err := StageSigningKeyRotation(dir, expected, "audit-row-1")
			if tc == "stale_expected" {
				if !errors.Is(err, ErrSigningKeyRotationConflict) {
					t.Fatalf("stale expected key: %v", err)
				}
				if staged, _ := filepath.Glob(path + ".pending-*"); len(staged) != 0 {
					t.Error("refused rotation staged a keyring")
				}
				return
			}
			if err != nil || previous != keys.ActiveID() || active == previous || sealedVersion(t, pending) != "v1" {
				t.Fatalf("stage: %q -> %q %v", previous, active, err)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, live) {
				t.Fatal("staging changed the live keyring")
			}
			if _, _, _, err := StageSigningKeyRotation(dir, "", "audit-row-2"); !errors.Is(err, ErrSigningKeyRotationConflict) {
				t.Fatalf("second rotation while one is pending: %v", err)
			}
			stagedBytes, _ := os.ReadFile(pending)
			record := PendingRotation{Found: true, ActiveKeyID: active, Newest: true}
			var lookupErr error
			switch tc {
			case "crash_uncommitted":
				record = PendingRotation{}
			case "not_newest":
				record.Newest = false
			case "wrong_active_key":
				record.ActiveKeyID = "key-someone-else"
			case "replayed_after_apply":
				// The rotation was installed; an old copy of its staged file
				// shows up again (restored from a backup, say).
				if err := FinishSigningKeyRotation(dir, pending); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(pending, stagedBytes, 0o600); err != nil {
					t.Fatal(err)
				}
			case "lookup_error":
				lookupErr = errors.New("database unavailable")
			case "discarded_name_taken":
				record = PendingRotation{}
				if err := os.WriteFile(path+".discarded-audit-row-1", []byte("earlier copy"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "finish" {
				err = FinishSigningKeyRotation(dir, pending)
			} else {
				var changed bool
				var notes []string
				changed, notes, err = ResolvePendingSigningKeyRotations(dir, func(id string) (PendingRotation, error) {
					if id != "audit-row-1" {
						t.Errorf("lookup for %q", id)
					}
					return record, lookupErr
				})
				installs := tc == "crash_committed"
				if err == nil && (changed != installs || (len(notes) == 0) == !installs) {
					t.Errorf("changed=%v notes=%v", changed, notes)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			now, err := LoadSigningKeys(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc == "lookup_error" {
				if _, err := os.Stat(pending); err != nil || now.ActiveID() != keys.ActiveID() {
					t.Fatalf("lookup failure did not leave the staged file for the next start: %v", err)
				}
				return
			}
			if staged, _ := filepath.Glob(path + ".pending-*"); len(staged) != 0 {
				t.Errorf("staged rotation left behind: %v", staged)
			}
			switch tc {
			case "finish", "crash_committed", "replayed_after_apply":
				if now.ActiveID() != active {
					t.Fatalf("active key %s, want %s", now.ActiveID(), active)
				}
				for id, key := range keys.keys {
					if !bytes.Equal(now.keys[id], key) {
						t.Errorf("rotation dropped verification key %s", id)
					}
				}
			default:
				if now.ActiveID() != keys.ActiveID() {
					t.Error("a rotation its audit entry does not vouch for was installed")
				}
			}
			if tc == "finish" || tc == "crash_committed" {
				return
			}
			kept := path + ".discarded-audit-row-1"
			if tc == "discarded_name_taken" {
				if earlier, _ := os.ReadFile(kept); string(earlier) != "earlier copy" {
					t.Error("setting a rotation aside overwrote an earlier copy")
				}
				kept += ".1"
			}
			if raw, err := os.ReadFile(kept); err != nil || !bytes.Equal(raw, stagedBytes) {
				t.Errorf("set-aside rotation not kept at %s: %v", kept, err)
			}
		})
	}
}
