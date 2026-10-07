package database

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const resetMarkerName = "reset-in-progress"

// CheckResetPending prevents starting a partially reset installation.
func CheckResetPending(root string) error {
	_, err := os.Lstat(filepath.Join(root, "run", resetMarkerName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect interrupted reset: %w", err)
	}
	return fmt.Errorf("installation has an interrupted reset; keep it stopped and rerun crewship reset --data with the same installation configuration")
}

// BeginOfflineReset persists only an operation marker, never resource IDs.
// Existing markers must belong to the same verified installation identity.
func BeginOfflineReset(root, instance string) error {
	run := filepath.Join(root, "run")
	path := filepath.Join(run, resetMarkerName)
	content := []byte("crewship offline reset pending\n" + instance + "\n")
	check := func() error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("reset marker is not a regular file")
		}
		previous, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(previous) != string(content) {
			return fmt.Errorf("interrupted reset marker has different or invalid ownership")
		}
		return nil
	}
	if err := check(); err == nil {
		// A prior attempt may have published the marker but failed to sync
		// its directory. Retries must make it durable before deleting data.
		return syncResetDirectory(run)
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(run, ".reset-marker-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Publish only complete bytes. A crash while writing precedes any
	// destructive action and cannot leave an unreadable recovery marker.
	if err := os.Link(file.Name(), path); err != nil && !os.IsExist(err) {
		return err
	}
	if err := check(); err != nil {
		return err
	}

	return syncResetDirectory(run)
}

func FinishOfflineReset(root string) error {
	run := filepath.Join(root, "run")
	if err := os.Remove(filepath.Join(run, resetMarkerName)); err != nil {
		return err
	}
	return syncResetDirectory(run)
}
func syncResetDirectory(path string) error {
	// Windows does not expose directory fsync through os.File.Sync.
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
