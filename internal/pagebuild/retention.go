package pagebuild

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
)

// Prune requires the source store's exclusive workspace maintenance lease.
func (s *Store) Prune(ctx context.Context, ws string, retained map[string]bool) error {
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
		return errors.New("unexpected Page artifact directory size")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		digest := strings.TrimSuffix(name, ".json")
		known := digestPattern.MatchString(digest) && name == digest+".json"
		if entry.Type().IsRegular() && ((known && !retained[digest]) || strings.HasPrefix(name, ".staging-")) {
			if err := root.Remove(name); err != nil {
				return err
			}
		}
	}
	return dir.Sync()
}
