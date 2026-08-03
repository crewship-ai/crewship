package memport

import (
	"io/fs"
	"os"
	"path/filepath"
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
// The root itself MAY be a symlink — an operator pointing --from at
// ~/memory-latest is normal, and refusing it would be refusing their own
// directory. It is resolved once here; everything inside is then checked
// against the resolved form, which is what the guarantee is actually
// about. A relative root ("." from inside the bundle) is made absolute
// for the same reason: safepath needs a base it can compare against.
func SecureDirFS(root string) fs.FS {
	resolved := root
	if abs, err := filepath.Abs(root); err == nil {
		resolved = abs
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	}
	return secureDir{root: resolved}
}

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
	info, err := os.Lstat(p)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if info.IsDir() {
		// A directory is opened plainly: the root was already resolved
		// in SecureDirFS, and the walk reaches subdirectories only
		// through ReadDir, which drops links before descending.
		f, err := os.Open(p)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return f, nil
	}
	// Files open with O_NOFOLLOW: Lstat-then-Open leaves a window in
	// which the entry is swapped for a link and Open follows it, so the
	// refusal has to be part of the open syscall.
	f, err := memory.OpenNoFollow(p)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	// And then re-check what was actually opened. O_NONBLOCK keeps the
	// open from parking on a FIFO, but only this check keeps the READ
	// from doing so — a pipe named AGENT.md would otherwise hang the
	// import with no output and no timeout. Same pairing the memory
	// package's own reader uses.
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
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
			// A symlinked DIRECTORY is dropped so the walk never
			// descends through it. A symlinked FILE stays in the
			// listing on purpose: Open refuses it, the reader records
			// the refusal as a Skip, and the operator is told the file
			// was left behind. Dropping it here instead would make it
			// vanish from the plan with nothing said — the one outcome
			// this feature's contract rules out.
			if target, terr := os.Stat(filepath.Join(p, e.Name())); terr == nil && target.IsDir() {
				continue
			}
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
