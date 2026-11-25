package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// openPlainDir opens a memory directory for server-side writes and deletes.
//
// The memory directories live in a mount an agent container can write
// (<base>/crews/<id>/agents/<slug>/.memory, <base>/crews/<id>/shared/.memory),
// so the agent can replace any directory on that chain with a link to a host
// path. Resolving the name afterwards would follow it. When dir lies under a
// crew directory the walk therefore runs inside an os.Root on that directory,
// which refuses any link that leaves it; links that stay inside the same crew
// are still allowed. The crew directory itself is server-created, and may be
// a link an administrator placed (another disk).
//
// A directory outside that layout (tests, tools) must itself not be a link.
func openPlainDir(dir string) (*os.Root, error) {
	if crew, rel, ok := splitCrewDir(dir); ok {
		root, err := os.OpenRoot(crew)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		sub, err := root.OpenRoot(rel)
		if err != nil {
			return nil, fmt.Errorf("memory: %s is not inside its crew directory: %w", dir, err)
		}
		return sub, nil
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("memory: %s is not a plain directory", dir)
	}
	return os.OpenRoot(dir)
}

// splitCrewDir splits <base>/crews/<id>/(agents|shared)/... into the crew
// directory and the rest. The FIRST match wins: everything after .memory is
// named by the agent, so a directory crews/x/agents inside it must not move
// the boundary into the agent's own tree. The cost is a storage base that
// itself contains crews/<id>/(agents|shared): the boundary is then that outer
// directory, which still stops any link from leaving it.
func splitCrewDir(dir string) (crew, rel string, ok bool) {
	clean := filepath.Clean(dir)
	parts := strings.Split(clean, string(filepath.Separator))
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "crews" && parts[i+1] != "" && (parts[i+2] == "agents" || parts[i+2] == "shared") {
			crew = strings.Join(parts[:i+2], string(filepath.Separator))
			if crew == "" {
				crew = string(filepath.Separator)
			}
			return crew, filepath.Join(parts[i+2:]...), true
		}
	}
	return "", "", false
}

// crewRootFor opens an os.Root on the crew directory when path lies below
// <base>/crews/<id>/(agents|shared), and returns path relative to it. Every
// operation on such a path then goes through the root: the agent can swap a
// link into the chain at any moment, and the root refuses one that leaves the
// crew directory at the time of each step, so there is no check to race.
// ok is false for any other layout, which keeps the path-based behaviour.
func crewRootFor(path string) (root *os.Root, rel string, ok bool, err error) {
	crew, rel, ok := splitCrewDir(path)
	if !ok {
		return nil, "", false, nil
	}
	root, err = os.OpenRoot(crew)
	if err != nil {
		return nil, "", true, err
	}
	return root, rel, true, nil
}

// mkdirAllSharedRoot is mkdirAllShared on a path below root, one level at a
// time, applying the shared-tree mode through a descriptor.
func mkdirAllSharedRoot(root *os.Root, rel string, perm os.FileMode) error {
	cur := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		parent := cur
		if parent == "" {
			parent = "."
		}
		cur = filepath.Join(cur, part)
		if fi, err := root.Stat(cur); err == nil {
			if !fi.IsDir() {
				return &fs.PathError{Op: "mkdir", Path: cur, Err: syscall.ENOTDIR}
			}
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := root.Mkdir(cur, perm); err != nil {
			// Another writer won the race; it applies the contract itself.
			if fi, statErr := root.Stat(cur); statErr == nil && fi.IsDir() {
				continue
			}
			return err
		}
		if pfi, err := root.Stat(parent); err == nil && isSharedDir(pfi) {
			d, err := root.Open(cur)
			if err != nil {
				return fmt.Errorf("open shared dir %s: %w", cur, err)
			}
			err = shareDirFile(d)
			_ = d.Close()
			if err != nil {
				return fmt.Errorf("chmod shared dir %s: %w", cur, err)
			}
		}
	}
	return nil
}
