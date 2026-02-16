package server

import (
	"fmt"
	"os"
	"path/filepath"
)

func removeSocketFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove socket file: %w", err)
	}
	return nil
}
