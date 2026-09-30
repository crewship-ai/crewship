package resourcelifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const InstanceLabel = "crewship.instance-id"

// Installation identity deliberately lives outside SQLite and workspace backups.
// A new data directory receives a new identity. Never copy this file to create
// a second installation sharing a daemon.
func LoadIdentity(root string) (string, error) {
	path := filepath.Join(root, "instance-id")
	read := func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		id := strings.TrimSpace(string(b))
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 32 {
			return "", fmt.Errorf("invalid installation identity")
		}
		return id, nil
	}
	if id, err := read(); err == nil {
		return id, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(root, ".instance-id-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(hex.EncodeToString(raw) + "\n"); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	// Link publishes a complete file without overwriting a competing startup.
	if err = os.Link(f.Name(), path); err != nil && !os.IsExist(err) {
		return "", err
	}
	return read()
}

func WithInstanceLabel(labels map[string]string, instance string) map[string]string {
	// Explicit presence masks any label inherited from a cache/custom image,
	// even when identity is unavailable and automatic cleanup must be disabled.
	labels[InstanceLabel] = instance
	return labels
}
