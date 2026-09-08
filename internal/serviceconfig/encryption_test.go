package serviceconfig

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func TestServiceEncryption(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENCRYPTION_KEY", hex.EncodeToString(key))
	t.Setenv(encryption.KeyVersionEnvVar, "v1")
	const raw = `[{"name":"db","image":"postgres:16","env":{"POSTGRES_PASSWORD":"inert-private-canary"}}]`
	sealed, err := Seal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "inert-private-canary") || Public(sealed) != Redacted {
		t.Fatal("stored or public configuration leaked")
	}
	if plain, err := Open(sealed); err != nil || plain != raw {
		t.Fatal("runtime round trip failed")
	}
	if again, err := Seal(sealed); err != nil || again != sealed {
		t.Fatal("encryption must be idempotent")
	}
	wrongPurpose, err := encryption.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, broken := range []string{sealed[:len(sealed)-5], envelopePrefix + wrongPurpose, "crewsvc:v1:broken"} {
		if plain, err := Open(broken); err == nil || plain != "" {
			t.Fatal("tampered or wrong-purpose ciphertext accepted")
		}
	}
	t.Setenv("ENCRYPTION_KEY", "")
	t.Setenv(encryption.AllowPlaintextSecretsEnvVar, "true")
	if _, err := Seal(raw); err == nil {
		t.Fatal("plaintext opt-out must not bypass service encryption")
	}
	if plain, err := Open(sealed); err == nil || plain != "" {
		t.Fatal("missing key must fail closed")
	}
	if safe, err := Seal(`[]`); err != nil || safe != `[]` {
		t.Fatal("empty topology should not require key")
	}
}
