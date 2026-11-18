package credpolicy

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"
)

// FileValue normalizes only textual key/certificate encodings. This also
// repairs legacy imports at delivery without changing the stored credential.
// Opaque values, including binary strings, are returned byte for byte.
func FileValue(typ, value string) string {
	if typ != "SSH_KEY" && typ != "CERTIFICATE" {
		return value
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	if value != "" && !strings.HasSuffix(value, "\n") {
		value += "\n"
	}
	return value
}

// ValidateFileValue parses material with the same normalization as delivery.
// Errors deliberately exclude parser input and credential contents.
func ValidateFileValue(typ, value string) error {
	value = FileValue(typ, value)
	switch typ {
	case "SSH_KEY":
		if _, err := ssh.ParseRawPrivateKey([]byte(value)); err != nil {
			return errors.New("ssh key must be a valid, unencrypted PEM-encoded private key")
		}
	case "CERTIFICATE":
		remaining := []byte(value)
		count := 0
		for len(strings.TrimSpace(string(remaining))) > 0 {
			// pem.Decode can skip leading garbage; never accept such a paste.
			if !strings.HasPrefix(strings.TrimSpace(string(remaining)), "-----BEGIN CERTIFICATE-----") {
				return errors.New("certificate must be PEM-encoded and contain a valid X.509 certificate chain")
			}
			block, rest := pem.Decode(remaining)
			if block == nil || block.Type != "CERTIFICATE" {
				return errors.New("certificate must be PEM-encoded and contain a valid X.509 certificate chain")
			}
			if _, err := x509.ParseCertificate(block.Bytes); err != nil {
				return errors.New("certificate must be PEM-encoded and contain a valid X.509 certificate chain")
			}
			count++
			remaining = rest
		}
		if count == 0 {
			return errors.New("certificate must be PEM-encoded and contain a valid X.509 certificate chain")
		}
	}
	return nil
}
