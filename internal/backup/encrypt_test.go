package backup

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"filippo.io/age"
)

const testPayload = "the quick brown fox jumps over the lazy dog\n" +
	"—with some extra bytes so the AGE HMAC has something to chew on"

func TestEncryptDecrypt_Passphrase_Roundtrip(t *testing.T) {
	const pass = "correct horse battery staple"

	var encrypted bytes.Buffer
	w, err := EncryptStreamPassphrase(&encrypted, pass)
	if err != nil {
		t.Fatalf("encrypt init: %v", err)
	}
	if _, err := io.WriteString(w, testPayload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	r, err := DecryptStreamPassphrase(&encrypted, pass)
	if err != nil {
		t.Fatalf("decrypt init: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != testPayload {
		t.Errorf("round-trip mismatch; got %q want %q", out, testPayload)
	}
}

func TestEncryptDecrypt_X25519_Roundtrip(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("gen identity: %v", err)
	}

	var encrypted bytes.Buffer
	w, err := EncryptStream(&encrypted, id.Recipient())
	if err != nil {
		t.Fatalf("encrypt init: %v", err)
	}
	if _, err := io.WriteString(w, testPayload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	r, err := DecryptStream(&encrypted, id)
	if err != nil {
		t.Fatalf("decrypt init: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != testPayload {
		t.Errorf("round-trip mismatch")
	}
}

func TestDecrypt_WrongPassphrase(t *testing.T) {
	var encrypted bytes.Buffer
	w, err := EncryptStreamPassphrase(&encrypted, "right")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, _ = io.WriteString(w, testPayload)
	_ = w.Close()

	_, err = DecryptStreamPassphrase(&encrypted, "wrong")
	if !errors.Is(err, ErrDecryption) {
		t.Errorf("expected ErrDecryption, got %v", err)
	}
}

func TestDecrypt_CorruptedBundle(t *testing.T) {
	var encrypted bytes.Buffer
	w, _ := EncryptStreamPassphrase(&encrypted, "pw")
	_, _ = io.WriteString(w, testPayload)
	_ = w.Close()

	// Flip a byte in the middle of the ciphertext.
	b := encrypted.Bytes()
	if len(b) < 50 {
		t.Fatal("encrypted output too short")
	}
	b[len(b)/2] ^= 0xFF

	r, err := DecryptStreamPassphrase(bytes.NewReader(b), "pw")
	if err != nil {
		// age 1.x may detect the tamper at init time.
		if !errors.Is(err, ErrDecryption) {
			t.Errorf("expected ErrDecryption at init, got %v", err)
		}
		return
	}
	// Or it may stream-detect on read.
	if _, err := io.ReadAll(r); err == nil {
		t.Error("expected error reading corrupted ciphertext")
	}
}

func TestEncryptStream_NoRecipients(t *testing.T) {
	_, err := EncryptStream(&bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "recipient") {
		t.Errorf("expected 'recipient' error, got %v", err)
	}
}

func TestEncryptStreamPassphrase_EmptyPassword(t *testing.T) {
	_, err := EncryptStreamPassphrase(&bytes.Buffer{}, "")
	if err == nil {
		t.Error("expected error for empty passphrase")
	}
}

func TestEncryptStream_RejectsMixedScryptAndX25519(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("gen identity: %v", err)
	}
	scrypt, err := age.NewScryptRecipient("test-passphrase")
	if err != nil {
		t.Fatalf("new scrypt: %v", err)
	}
	// Scrypt + X25519 in the same bundle is a library-level error that
	// surfaces deep inside Wrap; EncryptStream must catch it up front.
	_, err = EncryptStream(&bytes.Buffer{}, scrypt, id.Recipient())
	if err == nil || !strings.Contains(err.Error(), "only recipient") {
		t.Errorf("expected 'only recipient' error, got %v", err)
	}
}
