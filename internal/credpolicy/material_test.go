package credpolicy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestFileValueLegacyKeyEncodings(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	canonical := string(pem.EncodeToMemory(block))
	for _, value := range []string{canonical, strings.TrimSuffix(canonical, "\n"), strings.ReplaceAll(canonical, "\n", "\r\n")} {
		if got := FileValue("SSH_KEY", value); got != canonical {
			t.Fatal("key normalization changed material")
		}
		if err := ValidateFileValue("SSH_KEY", value); err != nil {
			t.Fatal(err)
		}
	}
	for _, typ := range []string{"SECRET", "GENERIC_SECRET", "CLI_TOKEN", "USERPASS", "UNKNOWN"} {
		value := "\x00\xffopaque\r\n\n"
		if FileValue(typ, value) != value {
			t.Fatalf("opaque %s changed", typ)
		}
	}
	if ValidateFileValue("SSH_KEY", "not a private key") == nil {
		t.Fatal("invalid key accepted")
	}
	if ValidateFileValue("CERTIFICATE", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")}))) == nil {
		t.Fatal("invalid certificate accepted")
	}
}
