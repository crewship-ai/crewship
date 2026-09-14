package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/spf13/cobra"
)

// Packing is read-only: no installs, lifecycle scripts or dependency resolution.
// A private agent working directory must not be modified concurrently while packed.
func packPageProject(directory string) (*pages.SourceProject, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	project := &pages.SourceProject{Format: pages.SourceProjectFormat, Runtime: pages.SourceProjectRuntime}
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("only regular source files can be packed: %s", name)
		}
		if len(project.Files) >= pages.MaxProjectFiles {
			return fmt.Errorf("too many project files")
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, pages.MaxProjectFileBytes+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(data) > pages.MaxProjectFileBytes {
			return fmt.Errorf("source file too large: %s", name)
		}
		total += len(data)
		if total > pages.MaxProjectSourceBytes {
			return fmt.Errorf("source project exceeds limit")
		}
		file := pages.ProjectFile{Path: name, Encoding: "utf8", Content: string(data)}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			file.Encoding = "base64"
			file.Content = base64.StdEncoding.EncodeToString(data)
		}
		project.Files = append(project.Files, file)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := project.Validate(); err != nil {
		return nil, err
	}
	return project, nil
}

// Extract only into a new directory: existing user files are never overwritten.
func unpackPageProject(project *pages.SourceProject, directory string) (err error) {
	if err := project.Validate(); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(directory)
		}
	}()
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, file := range project.Files {
		if err := root.MkdirAll(filepath.Dir(file.Path), 0700); err != nil {
			return err
		}
		data, err := file.Bytes()
		if err != nil {
			return err
		}
		if err := memory.WriteFileDurableRoot(root, file.Path, data, 0600); err != nil {
			return err
		}
	}
	complete = true
	return nil
}
func newPageProjectPackCommand() *cobra.Command {
	return &cobra.Command{Use: "pack <directory>", Args: cobra.ExactArgs(1), Short: "Print a working directory as a portable source YAML; skip .git, node_modules and dist", RunE: func(cmd *cobra.Command, args []string) error {
		project, err := packPageProject(args[0])
		if err != nil {
			return err
		}
		data, err := project.MarshalYAMLSource()
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(data)
		return err
	}}
}
func newPageProjectUnpackCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "unpack <directory>", Args: cobra.ExactArgs(1), Short: "Extract source YAML/JSON into a new working directory", RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		if strings.TrimSpace(file) == "" {
			return fmt.Errorf("--file is required")
		}
		var reader io.Reader = cmd.InOrStdin()
		if file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		project, err := pages.ParseSourceProject(reader)
		if err != nil {
			return err
		}
		return unpackPageProject(project, args[0])
	}}
	cmd.Flags().String("file", "", "Source project YAML/JSON; - reads stdin")
	return cmd
}
