package pages

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Prune must run under an exclusive workspace lease, after committing the SQL
// retention transaction. Failure leaves extra immutable files, not broken roots.
func (s *ProjectStore) Prune(ctx context.Context, ws string, sources map[string]bool, checkpoints map[string][]string) error {
	root, err := s.root(ws, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(1024)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) >= 1024 {
		return errors.New("unexpected Page source directory size")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		digest := strings.TrimSuffix(name, ".yaml")
		known := len(digest) == 64 && strings.Trim(digest, "0123456789abcdef") == "" && name == digest+".yaml"
		if entry.Type().IsRegular() && ((known && !sources[digest]) || strings.HasPrefix(name, ".staging-")) {
			if err := root.Remove(name); err != nil {
				return err
			}
		}
	}
	gitRoot, err := root.OpenRoot("git")
	if errors.Is(err, os.ErrNotExist) {
		return dir.Sync()
	}
	if err != nil {
		return err
	}
	defer gitRoot.Close()
	gitDir, err := gitRoot.Open(".")
	if err != nil {
		return err
	}
	defer gitDir.Close()
	repos, err := gitDir.ReadDir(4096)
	if err != nil && err != io.EOF {
		return err
	}
	if len(repos) >= 4096 {
		return errors.New("too many Page repositories")
	}
	retained := map[string]string{}
	for page, commits := range checkpoints {
		if len(commits) == 0 {
			continue
		}
		retained[fmt.Sprintf("%x", sha256.Sum256([]byte(page)))] = page
	}
	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := repo.Name()
		if !repo.IsDir() || len(name) != 64 || strings.Trim(name, "0123456789abcdef") != "" {
			return errors.New("unexpected Page repository entry")
		}
		page, keep := retained[name]
		if !keep {
			if err := gitRoot.RemoveAll(name); err != nil {
				return err
			}
			continue
		}
		path, err := s.gitPath(ctx, ws, page, false)
		if err != nil {
			return err
		}
		pins := map[string]bool{}
		for _, commit := range checkpoints[page] {
			if !ValidProjectCommit(commit) {
				return errors.New("invalid retained checkpoint")
			}
			pins["refs/checkpoints/"+commit] = true
		}
		if err := s.SetCheckpointBoundaries(ctx, ws, page, checkpoints[page]); err != nil {
			return err
		}
		output, err := runProjectGit(ctx, path, nil, "for-each-ref", "--format=%(refname)", "refs/checkpoints/")
		if err != nil {
			return err
		}
		for _, ref := range strings.Fields(string(output)) {
			if !pins[ref] {
				if _, err := runProjectGit(ctx, path, nil, "update-ref", "-d", ref); err != nil {
					return err
				}
			}
		}
	}
	if err := gitDir.Sync(); err != nil {
		return err
	}
	return dir.Sync()
}
