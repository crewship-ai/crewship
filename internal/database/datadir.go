package database

import (
	"fmt"
	"os"
	"path/filepath"
)

const defaultDirName = ".crewship"

type DataDir struct {
	Root string
}

func DefaultDataDir() (*DataDir, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	return NewDataDir(filepath.Join(home, defaultDirName))
}

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

func (d *DataDir) DatabasePath() string {
	return filepath.Join(d.Root, "crewship.db")
}

func (d *DataDir) DatabaseURL() string {
	return "file:" + d.DatabasePath()
}

func (d *DataDir) OutputDir() string {
	return filepath.Join(d.Root, "output")
}

func (d *DataDir) ChatsDir() string {
	return filepath.Join(d.Root, "chats")
}

func (d *DataDir) LogsDir() string {
	return filepath.Join(d.Root, "logs")
}

func (d *DataDir) SkillsDir() string {
	return filepath.Join(d.Root, "skills")
}
