package server

import (
	"os"
	"path/filepath"
)

func removeSocketFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.Remove(path)
}
