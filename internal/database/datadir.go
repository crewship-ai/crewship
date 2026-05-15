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
	if override := strings.TrimSpace(os.Getenv("CREWSHIP_DATA_DIR")); override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return nil, fmt.Errorf("resolve CREWSHIP_DATA_DIR: %w", err)
		}
		return NewDataDir(abs)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	return NewDataDir(filepath.Join(home, defaultDirName))
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
