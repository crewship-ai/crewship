package config

import (
	"errors"
	"path/filepath"
	"runtime"
)

type DocumentsConfig struct {
	MaxDocumentsPerWorkspace int    `yaml:"max_documents_per_workspace"`
	MaxOperationsPerWindow   int    `yaml:"max_operations_per_window"`
	OperationWindowSeconds   int    `yaml:"operation_window_seconds"`
	Enabled                  bool   `yaml:"enabled"`
	AuthoritySocket          string `yaml:"authority_socket"`
	ServiceSocket            string `yaml:"service_socket"`
	ServiceTokenFile         string `yaml:"service_token_file"`
}

func (c DocumentsConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.MaxDocumentsPerWorkspace < 0 || c.MaxOperationsPerWindow < 0 || c.OperationWindowSeconds < 0 || c.OperationWindowSeconds > 86400 {
		return errors.New("Docs limits must be nonnegative; operation window cannot exceed 86400 seconds")
	}
	if runtime.GOOS == "windows" {
		return errors.New("Docs foundation requires Unix sockets")
	}
	for _, p := range []string{c.AuthoritySocket, c.ServiceSocket, c.ServiceTokenFile} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || len(p) > 100 {
			return errors.New("Docs paths require clean absolute paths <=100 bytes")
		}
	}
	if c.AuthoritySocket == c.ServiceSocket || c.ServiceTokenFile == c.AuthoritySocket || c.ServiceTokenFile == c.ServiceSocket {
		return errors.New("Docs service paths must be distinct")
	}
	return nil
}
