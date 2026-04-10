package database

import (
	"fmt"
	"os"
	"path/filepath"
)

const defaultDirName = ".crewship"

// DataDir manages the crewship data directory structure, ensuring that
// required subdirectories (output, chats, logs, skills) exist.
type DataDir struct {
	Root string
}

// DefaultDataDir returns a DataDir rooted at ~/.crewship, creating the
// directory structure if it does not already exist.
func DefaultDataDir() (*DataDir, error) {
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
