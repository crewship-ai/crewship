//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"syscall"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func verifyProjectInputs(input io.Reader) error {
	var manifest restrictedruntime.NativeInputManifest
	d := json.NewDecoder(io.LimitReader(input, 32768))
	d.DisallowUnknownFields()
	if d.Decode(&manifest) != nil || d.Decode(new(any)) != io.EOF {
		return restrictedruntime.ErrDenied
	}
	return verifyProjectInputsAt(restrictedruntime.NativeInputTarget, manifest, 1001)
}

func verifyProjectInputsAt(base string, m restrictedruntime.NativeInputManifest, uid uint32) error {
	canonical, err := restrictedruntime.NewNativeInputManifest(m.Files)
	if err != nil || canonical.Hash != m.Hash {
		return restrictedruntime.ErrDenied
	}
	files := map[string]restrictedruntime.NativeInputFile{}
	dirs := map[string]bool{}
	for _, file := range m.Files {
		name := file.VersionID + "/" + file.Name
		files[name] = file
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	seen := 0
	err = filepath.WalkDir(base, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == base {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return restrictedruntime.ErrDenied
			}
			return nil
		}
		rel, err := filepath.Rel(base, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uid || st.Gid != uid {
			return restrictedruntime.ErrDenied
		}
		if entry.IsDir() {
			if !dirs[rel] || info.Mode().Perm() != 0500 {
				return restrictedruntime.ErrDenied
			}
			return nil
		}
		want, exists := files[rel]
		if !exists || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || st.Nlink != 1 || info.Size() != want.Size {
			return restrictedruntime.ErrDenied
		}
		f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		opened, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return err
		}
		openedStat, ok := opened.Sys().(*syscall.Stat_t)
		if !ok || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0400 || openedStat.Uid != uid || openedStat.Gid != uid || openedStat.Nlink != 1 || opened.Size() != want.Size {
			_ = f.Close()
			return restrictedruntime.ErrDenied
		}
		h := sha256.New()
		n, readErr := io.Copy(h, io.LimitReader(f, want.Size+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
			return restrictedruntime.ErrDenied
		}
		seen++
		return nil
	})
	if err != nil || seen != len(files) {
		return restrictedruntime.ErrDenied
	}
	return nil
}
