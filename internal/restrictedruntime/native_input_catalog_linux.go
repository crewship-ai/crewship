//go:build linux

package restrictedruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// NativeInputSource is trusted host code which rechecks current immutable
// attempt/source authority and returns exactly the manifest's captured bytes.
type NativeInputSource func(context.Context, Plan) ([]NativeInputData, error)

// FrozenNativeCatalog owns per-instance snapshot objects and private durable
// cleanup records. Neither directory nor Docker names come from request JSON.
type FrozenNativeCatalog struct {
	mu     sync.Mutex
	dir    string
	owner  string
	docker Docker
	source NativeInputSource
}

type nativeSnapshotRecord struct {
	Attempt, Resource, Workspace, Scope, Revision, Provenance string
	Fingerprint, Volume, Populator, Consumer, State           string
}

func NewFrozenNativeCatalog(dir string, docker Docker, source NativeInputSource) (*FrozenNativeCatalog, error) {
	if source == nil || !filepath.IsAbs(dir) {
		return nil, ErrDenied
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if !privateNativeCatalogPath(dir, true) {
		return nil, ErrDenied
	}
	ownerFile := filepath.Join(dir, "owner")
	b, err := readPrivateNativeCatalogFile(ownerFile)
	if os.IsNotExist(err) {
		seed := make([]byte, 16)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		b = []byte(hex.EncodeToString(seed))
		f, err := os.OpenFile(ownerFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := f.Write(b)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if !privateNativeCatalogPath(ownerFile, false) || len(b) != 32 || !identifier.MatchString(string(b)) {
		return nil, ErrDenied
	}
	return &FrozenNativeCatalog{dir: dir, owner: string(b), docker: docker, source: source}, nil
}

func privateNativeCatalogPath(path string, directory bool) bool {
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || st.IsDir() != directory {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != uint32(os.Getuid()) {
		return false
	}
	if directory {
		return st.Mode().Perm() == 0700
	}
	return st.Mode().IsRegular() && st.Mode().Perm() == 0600 && sys.Nlink == 1
}

func (c *FrozenNativeCatalog) load(attempt string) (nativeSnapshotRecord, error) {
	var r nativeSnapshotRecord
	if !identifier.MatchString(attempt) {
		return r, ErrDenied
	}
	name := filepath.Join(c.dir, attempt+".json")
	if !privateNativeCatalogPath(name, false) {
		if _, err := os.Lstat(name); os.IsNotExist(err) {
			return r, os.ErrNotExist
		}
		return r, ErrDenied
	}
	b, err := readPrivateNativeCatalogFile(name)
	if err != nil {
		return r, err
	}
	if len(b) > 8192 || json.Unmarshal(b, &r) != nil || r.Attempt != attempt || r.Volume != "crewship-rtest-input-"+c.owner+"-"+nativeInputObjectSuffix(attempt) || r.Populator != "crewship-rtest-input-writer-"+c.owner+"-"+nativeInputObjectSuffix(attempt) {
		return nativeSnapshotRecord{}, ErrDenied
	}
	return r, nil
}

func nativeInputObjectSuffix(attempt string) string {
	h := sha256.Sum256([]byte(attempt))
	return hex.EncodeToString(h[:12])
}

func readPrivateNativeCatalogFile(name string) ([]byte, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || sys.Uid != uint32(os.Getuid()) || sys.Nlink != 1 {
		return nil, ErrDenied
	}
	b, err := io.ReadAll(io.LimitReader(f, 8193))
	if len(b) > 8192 {
		return nil, ErrDenied
	}
	return b, err
}

func (c *FrozenNativeCatalog) save(r nativeSnapshotRecord) error {
	if !identifier.MatchString(r.Attempt) || !privateNativeCatalogPath(c.dir, true) {
		return ErrDenied
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.dir, ".snapshot-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(c.dir, r.Attempt+".json")); err != nil {
		return err
	}
	d, err := os.Open(c.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
