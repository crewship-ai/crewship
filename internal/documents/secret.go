package documents

import (
	"errors"
	"io"
	"strings"
)

func ReadServiceSecret(path string) (string, error) {
	file, err := openServiceSecret(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 512 {
		return "", errors.New("Docs service secret must be a regular mode-0600 file <=512 bytes")
	}
	return readServiceSecretContents(file)
}

func readServiceSecretContents(reader io.Reader) (string, error) {
	// The inode may grow after fstat. Never allocate or read beyond the limit.
	raw, err := io.ReadAll(io.LimitReader(reader, 513))
	if err != nil {
		return "", err
	}
	if len(raw) > 512 {
		return "", errors.New("Docs service secret exceeds 512 bytes")
	}
	secret := strings.TrimSpace(string(raw))
	if len(secret) < 32 || len(secret) > 256 {
		return "", errors.New("Docs service secret must contain 32..256 characters")
	}
	return secret, nil
}
