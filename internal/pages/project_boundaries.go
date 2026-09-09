package pages

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

// CheckpointBoundaries lists Git shallow roots. A retained checkpoint keeps its
// exact commit hash and complete tree; ancestry outside the SQL retention window
// is not part of the supported archive. Backups carry these boundaries explicitly.
func (s *ProjectStore) CheckpointBoundaries(ctx context.Context, ws, page string) ([]string, error) {
	repo, err := s.gitPath(ctx, ws, page, false)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open("shallow")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const maximum = 4096 * 41
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximum {
		return nil, errors.New("checkpoint boundaries exceed size limit")
	}
	ids := strings.Fields(string(data))
	if len(ids) > 4096 {
		return nil, errors.New("too many checkpoint boundaries")
	}
	for _, id := range ids {
		if !ValidProjectCommit(id) {
			return nil, errors.New("invalid checkpoint boundary")
		}
	}
	return ids, nil
}

// SetCheckpointBoundaries requires exclusive workspace access (or an isolated
// restore staging namespace). It never rewrites a commit or SQL receipt.
func (s *ProjectStore) SetCheckpointBoundaries(ctx context.Context, ws, page string, commits []string) error {
	if len(commits) == 0 || len(commits) > 4096 {
		return errors.New("too many checkpoint boundaries")
	}
	unique := map[string]bool{}
	for _, id := range commits {
		if !ValidProjectCommit(id) {
			return errors.New("invalid checkpoint boundary")
		}
		unique[id] = true
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	repo, err := s.gitPath(ctx, ws, page, false)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return err
	}
	defer root.Close()
	// Exclusive namespace access proves no live shallow writer owns a leftover lock.
	if err := root.Remove("shallow.lock"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := root.OpenFile("shallow.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove("shallow.lock")
	_, err = f.WriteString(strings.Join(ids, "\n") + "\n")
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename("shallow.lock", "shallow"); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
