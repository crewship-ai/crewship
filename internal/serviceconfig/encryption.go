package serviceconfig

import (
	"errors"
	"strings"

	"github.com/crewship-ai/crewship/internal/encryption"
)

const envelopePrefix = "crewsvc:"
const purpose = "crewship/service-config/v1\n"

// Seal protects private runtime settings without changing their exact bytes.
// Unlike the legacy opt-out helper, this never allows plaintext secret writes.
func Seal(raw string) (string, error) {
	if strings.HasPrefix(raw, envelopePrefix) {
		if _, err := Open(raw); err != nil {
			return "", err
		}
		return raw, nil
	}
	if Public(raw) != Redacted {
		return raw, nil
	}
	encrypted, err := encryption.Encrypt(purpose + raw)
	if err != nil {
		return "", errors.New("cannot encrypt private service configuration: configure a valid encryption key")
	}
	return envelopePrefix + encrypted, nil
}

// Open is only for trusted server runtime readers. Never fall back to treating
// an invalid envelope as plaintext, even when a deployment opts out elsewhere.
// The encrypted purpose marker prevents ciphertext from another vault field
// from being interpreted as a service document.
func Open(raw string) (string, error) {
	if !strings.HasPrefix(raw, envelopePrefix) {
		return raw, nil
	}
	plain, err := encryption.Decrypt(strings.TrimPrefix(raw, envelopePrefix))
	if err != nil || !strings.HasPrefix(plain, purpose) {
		return "", errors.New("private service configuration cannot be decrypted")
	}
	return strings.TrimPrefix(plain, purpose), nil
}
