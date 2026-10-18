//go:build !windows

package documents

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ServePrivateUnix refuses to replace any preexisting path and owns only the
// listener it created. No network listener exposes the persistence authority.
func ServePrivateUnix(path string, handler http.Handler, initialize ...func() error) (func(context.Context) error, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 100 {
		return nil, errors.New("private Unix socket requires a clean absolute path <=100 bytes")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, errors.New("private Unix socket path already exists or is inaccessible")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// The default UnixListener close unlinks by pathname, which could remove
	// another process's replacement. Retain inode custody and unlink explicitly.
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	owned, err := os.Lstat(path)
	if err != nil {
		listener.Close()
		return nil, err
	}
	removeOwned := func() error {
		current, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !os.SameFile(owned, current) {
			return nil
		}
		return os.Remove(path)
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		_ = removeOwned()
		return nil, err
	}
	// Never fence an existing process before proving ownership of its socket.
	for _, init := range initialize {
		if err = init(); err != nil {
			listener.Close()
			_ = removeOwned()
			return nil, err
		}
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	var stopOnce sync.Once
	var stopErr error
	return func(ctx context.Context) error {
		stopOnce.Do(func() {
			stopErr = server.Shutdown(ctx)
			if stopErr != nil {
				_ = server.Close()
			}
			<-done
			stopErr = errors.Join(stopErr, removeOwned())
		})
		return stopErr
	}, nil
}
