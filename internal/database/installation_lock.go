package database

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/writerlease"
)

// AcquireInstallationOperation fences server startup against offline reset.
// The anchor is retained permanently; deleting it would allow two holders.
func AcquireInstallationOperation(root string) (*writerlease.Lease, error) {
	run := filepath.Join(root, "run")
	checkRun := func() error {
		info, err := os.Lstat(run)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("installation run path must be a real directory: %s", run)
		}
		return nil
	}
	if err := checkRun(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	if err := os.MkdirAll(run, 0700); err != nil {
		return nil, err
	}
	if err := checkRun(); err != nil {
		return nil, err
	}
	anchor := filepath.Join(run, "installation.lock")
	if info, err := os.Lstat(anchor); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("installation lock must be a regular non-symlink file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	lease, err := writerlease.Acquire(anchor)
	if err != nil {
		return nil, fmt.Errorf("installation is running or another offline operation owns it: %w", err)
	}
	return lease, nil
}
