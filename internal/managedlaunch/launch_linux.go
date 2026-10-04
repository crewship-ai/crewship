//go:build linux

package managedlaunch

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Launch runs inside the statically linked, read-only host launcher. Its argv
// and selected environment never pass through a shell, tmux or writable file.
func Launch(encoded string, args []string) error {
	if len(encoded) > 64<<10 || len(args) == 0 {
		return errors.New("managed launch: invalid launcher arguments")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return errors.New("managed launch: invalid descriptor encoding")
	}
	var d Descriptor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("managed launch: invalid descriptor encoding")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(d.Path)
	if err != nil || canonical != d.Path {
		return errors.New("managed launch: executable path is not canonical")
	}
	f, err := os.Open(d.Path)
	if err != nil {
		return errors.New("managed launch: executable unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 || info.Size() > MaxArtifactBytes {
		return errors.New("managed launch: unsafe executable permissions")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("managed launch: executable is not root-owned")
	}
	content, err := io.ReadAll(io.LimitReader(f, MaxArtifactBytes+1))
	if err != nil {
		return errors.New("managed launch: executable read failed")
	}
	artifact, err := Capture(d.Path, content)
	if err != nil {
		return err
	}
	if artifact.SHA256 != d.SHA256 {
		return errors.New("managed launch: executable hash mismatch")
	}
	env, err := SelectedEnvironment(d, os.Environ())
	if err != nil {
		return err
	}
	argv := append([]string{d.Path}, args...)
	// The provider attests read-only root and no covering mounts while retaining
	// the exact runtime. Those facts close the hash-to-exec replacement window.
	return syscall.Exec(d.Path, argv, env)
}
