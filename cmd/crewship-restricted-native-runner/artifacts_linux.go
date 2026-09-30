//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

type artifact struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content []byte `json:"content"`
}

// collectArtifacts confines every open to the scratch root even if a worker
// races directory replacement. Hidden state and symlinks are never exported.
func collectArtifacts(rootPath string) ([]artifact, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var files []artifact
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("artifact symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if len(name) > 240 || len(files) >= 32 {
			return errors.New("artifact limit")
		}
		file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return errors.New("artifact type or size")
		}
		content, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(content) > 1<<20 {
			return errors.New("artifact read limit")
		}
		total += len(content)
		if total > 4<<20 {
			return errors.New("artifact total limit")
		}
		files = append(files, artifact{Type: "artifact", Name: name, Content: content})
		return nil
	})
	return files, err
}

func emitArtifacts(output io.Writer, root string) error {
	files, err := collectArtifacts(root)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	for _, file := range files {
		if err := encoder.Encode(file); err != nil {
			return err
		}
	}
	return nil
}
