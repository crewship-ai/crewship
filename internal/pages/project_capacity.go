package pages

import (
	"context"
	"io/fs"
	"strings"
)

// StoragePressure leaves capacity for a maximum-size source and its Git trees.
// Called under a shared workspace lease; exact write admission remains separate.
func (s *ProjectStore) StoragePressure(ctx context.Context, ws string) (bool, error) {
	release, err := s.GitLease(ctx, ws, false)
	if err != nil {
		return false, err
	}
	defer release()
	mu := s.workspaceMutex(ws)
	mu.Lock()
	defer mu.Unlock()
	root, err := s.root(ws, true)
	if err != nil {
		return false, err
	}
	defer root.Close()
	var sourceBytes, gitBytes int64
	var sources int
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fs.ErrInvalid
		}
		if !strings.HasPrefix(name, "git/") && !strings.HasSuffix(name, ".yaml") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if strings.HasPrefix(name, "git/") {
			gitBytes += info.Size()
		} else {
			sources++
			sourceBytes += info.Size()
		}
		return nil
	})
	return sources >= 224 || sourceBytes >= 96<<20 || gitBytes >= 96<<20, err
}
