package memport

import (
	"io/fs"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/safepath"
)

// SecureDirFS opens a memory tree for reading without following
// symlinks.
//
// os.DirFS is the obvious choice and the wrong one here. It follows
// links, and the tree being read is one the agent itself owns: a `.md`
// pointed at another crew's memory, or at any file the server process
// can read, comes back inside a response the operator believes is
// scoped to one agent. The memory package already reads its own tree
// this way (readRegularNoFollow, #1043); a new read door has to do the
// same or the guarantee is only as strong as the doors that remember.
//
// Symlinks are skipped, not fatal. An export of a tampered tree should
// still return the real files — refusing the whole read would turn a
// planted link into a denial of service against the operator.
func SecureDirFS(root string) fs.FS { return secureDir{root: root} }

type secureDir struct{ root string }

// resolve turns an fs-relative name into a host path built from
// individually validated components.
//
// safepath.JoinUnder (rather than JoinRel on the joined string) is
// deliberate: it runs ValidateComponent on every segment, so the value
// that reaches a syscall is one whose every part was checked, not one
// whose combined text passed a check. Same reasoning as
// EnsureDirNoFollow — the guarantee belongs on the value at the point
// of use.
func (d secureDir) resolve(name string) (string, error) {
	if name == "." {
		return d.root, nil
	}
	if !fs.ValidPath(name) {
		return "", &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return safepath.JoinUnder(d.root, strings.Split(name, "/")...)
}

// Open satisfies fs.FS. It refuses a symlinked final component; callers
// reaching a directory get an *os.File, which fs.WalkDir never uses
// directly because ReadDir below takes precedence.
func (d secureDir) Open(name string) (fs.File, error) {
	p, err := d.resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	// Directories still need a plain open — O_NOFOLLOW would refuse a
	// legitimately symlinked ROOT, and the walk only reaches a directory
	// through ReadDir, which drops links before descending.
	info, err := os.Lstat(p)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if info.IsDir() {
		f, err := os.Open(p)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return f, nil
	}
	// Files open with O_NOFOLLOW. Lstat-then-Open leaves a window in
	// which the entry is swapped for a link and Open follows it; the
	// refusal has to be part of the open syscall.
	f, err := memory.OpenNoFollow(p)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return f, nil
}

// ReadDir drops symlinked entries entirely, so fs.WalkDir never
// descends through one and never reports a linked file as a candidate.
func (d secureDir) ReadDir(name string) ([]fs.DirEntry, error) {
	p, err := d.resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	out := entries[:0]
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// ReadFile reads through the memory package's no-follow reader, so a
// link swapped in between the walk and the read is still refused.
func (d secureDir) ReadFile(name string) ([]byte, error) {
	p, err := d.resolve(name)
	if err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
	}
	b, err := memory.ReadFileNoFollow(p)
	if err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
	}
	return b, nil
}

var (
	_ fs.FS         = secureDir{}
	_ fs.ReadDirFS  = secureDir{}
	_ fs.ReadFileFS = secureDir{}
)
