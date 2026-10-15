package documents

import (
	"errors"
	"os"
	"strings"
)

func ReadServiceSecret(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 512 {
		return "", errors.New("Docs service secret must be a regular mode-0600 file <=512 bytes")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(raw))
	if len(secret) < 32 || len(secret) > 256 {
		return "", errors.New("Docs service secret must contain 32..256 characters")
	}
	return secret, nil
}
