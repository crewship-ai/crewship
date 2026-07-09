package sidecar

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// readRegularNoFollow reads path safely for the memory-read surface, mirroring
// internal/memory.readRegularNoFollow (index.go):
//   - O_NOFOLLOW makes the open fail (ELOOP) if path's final component is a
//     symlink — closing the TOCTOU gap between the path check in the handler
//     and this read, so an agent can't swap a whitelisted .md for a symlink
//     pointing at an arbitrary file.
//   - O_NONBLOCK keeps the open from hanging if the target is swapped for a
//     FIFO with no writer (which would otherwise block the request goroutine
//     forever).
//   - After open we re-Stat via the fd and reject anything that isn't a
//     regular file (sockets, devices, FIFOs that survived O_NONBLOCK, dirs).
func readRegularNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	return io.ReadAll(f)
}
