package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMalformedLicenseReloadPreservesVerifiedClaims(t *testing.T) {
	pub, priv := generateTestKeypair(t)
	key := base64.StdEncoding.EncodeToString(pub)
	previousKey := publicKey
	t.Cleanup(func() { setPublicKey(previousKey) })
	claims := Claims{LicenseID: "verified", Edition: EditionTeam, MaxCrews: 20, Features: []string{"audit_export"}, IssuedAt: time.Now().Unix()}
	valid := signClaims(t, priv, claims)
	invalidPayload := "{broken claims"
	signedInvalid, err := json.Marshal(signedLicense{Payload: invalidPayload, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(invalidPayload)))})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key, message string
		data               []byte
	}{
		{"malformed envelope", key, "parse license", []byte("{")},
		{"invalid key encoding", "not base64!", "decode public key", valid},
		{"wrong key length", base64.StdEncoding.EncodeToString([]byte("short")), "invalid public key size", valid},
		{"invalid signature encoding", key, "decode signature", []byte(`{"payload":"{}","signature":"not base64!"}`)},
		{"truncated signature", key, "invalid license signature", []byte(`{"payload":"{}","signature":""}`)},
		{"signed malformed claims", key, "parse license claims", signedInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setPublicKey(key)
			l := New()
			if err := l.LoadFromBytes(valid); err != nil {
				t.Fatal(err)
			}
			setPublicKey(tc.key)
			if err := l.LoadFromBytes(tc.data); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("invalid reload accepted: %v", err)
			}
			if !reflect.DeepEqual(l.Claims(), claims) {
				t.Fatalf("invalid reload changed verified license: %#v", l.Claims())
			}
		})
	}
}

func TestLicenseFileReadFailuresAndCorruptionLeaveDefaults(t *testing.T) {
	l := New()
	dir := t.TempDir()
	if err := l.LoadFromFile(dir); err == nil || !strings.Contains(err.Error(), "read license file") {
		t.Fatalf("directory accepted as license: %v", err)
	}
	path := filepath.Join(dir, "license.json")
	if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := l.LoadFromFile(path); err == nil || !strings.Contains(err.Error(), "parse license") {
		t.Fatalf("corrupt file accepted: %v", err)
	}
	if !reflect.DeepEqual(l.Claims(), CommunityDefaults()) || l.HasFeature("audit_export") {
		t.Fatalf("failed file granted capabilities: %#v", l.Claims())
	}
}
