package backup

import (
	"fmt"
	"io"

	"filippo.io/age"
)

// EncryptionAlgorithm is the string recorded in Manifest.Encryption.Algorithm
// when native encryption is applied. Other values are reserved for future
// schemes; decryptors must refuse anything they do not recognise.
const EncryptionAlgorithm = "age-v1"

// EncryptStream wraps out with an AGE encryptor. The returned WriteCloser
// MUST be closed by the caller to flush the final AGE chunk; failing to
// do so produces a truncated bundle that the decryptor will reject.
//
// At least one recipient must be supplied. Passphrase mode is exposed
// via EncryptStreamPassphrase below.
func EncryptStream(out io.Writer, recipients ...age.Recipient) (io.WriteCloser, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("backup: EncryptStream requires at least one recipient")
	}
	w, err := age.Encrypt(out, recipients...)
	if err != nil {
		return nil, fmt.Errorf("backup: init age encryptor: %w", err)
	}
	return w, nil
}

// EncryptStreamPassphrase is a convenience wrapper for the passphrase
// path. Internally it constructs a ScryptRecipient from passphrase and
// calls EncryptStream. The returned WriteCloser MUST be closed.
//
// Passphrase strength is the caller's responsibility — AGE enforces no
// minimum. The CLI layer prompts twice for confirmation and rejects
// blank passphrases before reaching this function.
func EncryptStreamPassphrase(out io.Writer, passphrase string) (io.WriteCloser, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("backup: passphrase must not be empty")
	}
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("backup: init scrypt recipient: %w", err)
	}
	return EncryptStream(out, r)
}

// DecryptStream returns a Reader that streams plaintext from in. At
// least one identity must be supplied and match one of the AGE
// recipients the bundle was encrypted to. Wrong/missing identity
// surfaces ErrDecryption.
func DecryptStream(in io.Reader, identities ...age.Identity) (io.Reader, error) {
	if len(identities) == 0 {
		return nil, fmt.Errorf("backup: DecryptStream requires at least one identity")
	}
	r, err := age.Decrypt(in, identities...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecryption, err)
	}
	return r, nil
}

// DecryptStreamPassphrase is the passphrase counterpart to
// EncryptStreamPassphrase.
func DecryptStreamPassphrase(in io.Reader, passphrase string) (io.Reader, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("backup: passphrase must not be empty")
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fmt.Errorf("backup: init scrypt identity: %w", err)
	}
	return DecryptStream(in, id)
}
