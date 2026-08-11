package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultDirName = ".crewship"

// DataDir manages the crewship data directory structure, ensuring that
// required subdirectories (output, chats, logs, skills) exist.
type DataDir struct {
	Root string
}

// DefaultDataDir returns a DataDir rooted at $CREWSHIP_DATA_DIR (if set)
// or ~/.crewship otherwise, creating the directory structure if it does
// not already exist. The env-var override is the single supported way to
// move state off the home dir without passing --data-dir to every
// command; admin / backup / doctor / start all flow through this helper.
func DefaultDataDir() (*DataDir, error) {
	root, err := defaultDataDirRoot()
	if err != nil {
		return nil, err
	}
	return NewDataDir(root)
}

// ResolveDefaultDataDir returns the same DataDir as DefaultDataDir but creates
// nothing: no root, none of the four subdirectories. It is the route for
// read-only and diagnostic callers.
//
// The distinction is not cosmetic. DefaultDataDir provisions the tree as a side
// effect of being asked where the tree is, so a probe built on it reports on a
// directory it has just brought into existence: `crewship doctor` on a box that
// had never run crewshipd printed "database file does not exist (crewshipd has
// never run)" and left ~/.crewship/{output,chats,logs,skills} behind (#1922,
// B-02). A command that only inspects state must not change it.
//
// What it does NOT drop is DefaultDataDir's error surface. Callers that today
// get an error out of DefaultDataDir get one here too — an unusable location is
// something `doctor` and `telemetry status` must report, and going quiet about
// it would trade one wrong answer for another. checkDataDirUsable below tests
// the conditions MkdirAll would have failed on, without the MkdirAll.
func ResolveDefaultDataDir() (*DataDir, error) {
	root, err := defaultDataDirRoot()
	if err != nil {
		return nil, err
	}
	if err := checkDataDirUsable(root); err != nil {
		return nil, err
	}
	return &DataDir{Root: root}, nil
}

// checkDataDirUsable answers "would NewDataDir's MkdirAll have failed here?"
// without creating anything.
//
// It walks up to the first path component that exists. A component that exists
// but is not a directory is the ENOTDIR MkdirAll would have hit. Otherwise, if
// the root itself is missing, the nearest existing ancestor has to be writable
// or the root can never come into being — a mistyped CREWSHIP_DATA_DIR under a
// read-only mount, say, which the operator needs told plainly rather than as
// "database not found".
//
// A missing root over a writable ancestor is NOT an error: that is a box that
// has simply never run crewshipd, the exact state doctor exists to report.
//
// The writability test is the owner-write bit, which is an approximation — a
// 0755 directory owned by root is "writable" here and MkdirAll would still get
// EACCES for a non-root process. The residual case degrades to the pre-existing
// behaviour of the caller (doctor's own "data dir writable" probe, or the mkdir
// error from `--fix` / `crewship start`), so the approximation costs a worse
// message, never a wrong one.
func checkDataDirUsable(root string) error {
	for p := root; ; {
		info, err := os.Stat(p)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("data dir %s: %s is not a directory", root, p)
			}
			if p != root && info.Mode().Perm()&0o200 == 0 {
				return fmt.Errorf("data dir %s cannot be created: %s is not writable", root, p)
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", p, err)
		}
		parent := filepath.Dir(p)
		if parent == p {
			// Reached the volume root without finding anything; there is
			// nothing left to check.
			return nil
		}
		p = parent
	}
}

// defaultDataDirRoot resolves the root path both constructors above share:
// $CREWSHIP_DATA_DIR if set, ~/.crewship otherwise.
func defaultDataDirRoot() (string, error) {
	if override := strings.TrimSpace(os.Getenv("CREWSHIP_DATA_DIR")); override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve CREWSHIP_DATA_DIR: %w", err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, defaultDirName), nil
}

// NewDataDir creates a DataDir at the given root path, ensuring all
// required subdirectories exist.
func NewDataDir(root string) (*DataDir, error) {
	dirs := []string{
		root,
		filepath.Join(root, "output"),
		filepath.Join(root, "chats"),
		filepath.Join(root, "logs"),
		filepath.Join(root, "skills"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("create %s: %w", d, err)
		}
	}
	return &DataDir{Root: root}, nil
}

// DatabasePath returns the absolute path to the SQLite database file.
func (d *DataDir) DatabasePath() string {
	return filepath.Join(d.Root, "crewship.db")
}

// DatabaseURL returns the database path as a "file:" URI suitable for sql.Open.
func (d *DataDir) DatabaseURL() string {
	return "file:" + d.DatabasePath()
}

// OutputDir returns the path to the directory used for agent output files.
func (d *DataDir) OutputDir() string {
	return filepath.Join(d.Root, "output")
}

// ChatsDir returns the path to the directory used for chat conversation files.
func (d *DataDir) ChatsDir() string {
	return filepath.Join(d.Root, "chats")
}

// LogsDir returns the path to the directory used for agent log files.
func (d *DataDir) LogsDir() string {
	return filepath.Join(d.Root, "logs")
}

// SkillsDir returns the path to the directory used for bundled and custom skill definitions.
func (d *DataDir) SkillsDir() string {
	return filepath.Join(d.Root, "skills")
}

// WorkspaceMemoryDir returns the path to workspace-level memory for a given workspace.
// Reserved for the v0.2 workspace-tier memory roadmap.
func (d *DataDir) WorkspaceMemoryDir(workspaceID string) string {
	return filepath.Join(d.Root, "memory", workspaceID)
}
