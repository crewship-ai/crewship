package localfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// scopedRoot is an os.Root opened on one storage unit (a crew's directory, or
// one crew's or workspace's entry under crews/, workspaces/, attachments/),
// that takes base-relative paths. Every operation goes through the unit's
// root, so a link planted in the unit by an agent can never carry an
// operation into another crew's or area's directory: not for an existing
// target, not for one that does not exist yet (a dangling link followed on
// create), and not by swapping a link in between a check and the open.
type scopedRoot struct {
	root *os.Root
	unit string
}

func (s *scopedRoot) in(rel string) string {
	if s.unit == "." {
		return rel
	}
	r, err := filepath.Rel(s.unit, rel)
	if err != nil {
		return rel
	}
	return r
}

func (s *scopedRoot) Close() error                             { return s.root.Close() }
func (s *scopedRoot) Open(rel string) (*os.File, error)        { return s.root.Open(s.in(rel)) }
func (s *scopedRoot) Create(rel string) (*os.File, error)      { return s.root.Create(s.in(rel)) }
func (s *scopedRoot) Stat(rel string) (fs.FileInfo, error)     { return s.root.Stat(s.in(rel)) }
func (s *scopedRoot) Lstat(rel string) (fs.FileInfo, error)    { return s.root.Lstat(s.in(rel)) }
func (s *scopedRoot) Chmod(rel string, m fs.FileMode) error    { return s.root.Chmod(s.in(rel), m) }
func (s *scopedRoot) Remove(rel string) error                  { return s.root.Remove(s.in(rel)) }
func (s *scopedRoot) RemoveAll(rel string) error               { return s.root.RemoveAll(s.in(rel)) }
func (s *scopedRoot) MkdirAll(rel string, m fs.FileMode) error { return s.root.MkdirAll(s.in(rel), m) }

// FS serves base-relative names that lie inside the unit.
func (s *scopedRoot) FS() fs.FS { return scopedFS{s} }

type scopedFS struct{ s *scopedRoot }

func (f scopedFS) Open(name string) (fs.File, error) {
	if f.s.unit != "." && name != f.s.unit && !strings.HasPrefix(name, filepath.ToSlash(f.s.unit)+"/") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	inner := name
	if f.s.unit != "." {
		inner = strings.TrimPrefix(strings.TrimPrefix(name, filepath.ToSlash(f.s.unit)), "/")
		if inner == "" {
			inner = "."
		}
	}
	return f.s.root.FS().Open(inner)
}
