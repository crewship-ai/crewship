package provider

import (
	"context"
	"io"
	"time"
)

// FileInfo describes a file or directory in a storage provider.
type FileInfo struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

// FileEvent represents a filesystem change event (create, write, remove, rename)
// observed by a storage provider's Watch method.
type FileEvent struct {
	Op        string    `json:"op"` // "create", "write", "remove", "rename"
	Path      string    `json:"path"`
	Agent     string    `json:"agent"`
	Size      int64     `json:"size"`
	Timestamp time.Time `json:"timestamp"`
}

// StorageProvider defines the interface for reading, writing, listing, and
// watching files in agent workspaces. The localfs implementation is the default.
type StorageProvider interface {
	Read(ctx context.Context, path string) (io.ReadCloser, error)
	Write(ctx context.Context, path string, r io.Reader) error
	List(ctx context.Context, dir string) ([]FileInfo, error)
	ListRecursive(ctx context.Context, dir string) ([]FileInfo, error)
	Delete(ctx context.Context, path string) error
	Exists(ctx context.Context, path string) (bool, error)
	EnsureDir(ctx context.Context, path string) error
	Watch(ctx context.Context, dir string, events chan<- FileEvent) error
}
