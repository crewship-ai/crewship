//go:build linux

package managedlaunch

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	if !ValidRunID(d.RunID) {
		return errors.New("managed launch: valid run identity required")
	}
	// Establish identity before potentially expensive artifact validation so
	// stop and restart reconciliation can also locate the trusted launcher.
	if err := establishRunIdentity(d.RunID); err != nil {
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
	if !ok || (stat.Uid != 0 && stat.Uid != 1001) {
		return errors.New("managed launch: unsupported executable ownership")
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

// Keep the legacy direct-run PID/starttime contract without invoking image
// shell code. execve preserves PID and session/process group; group signals
// therefore reach the CLI and its children. An existing file (including a
// symlink) refuses this attempt instead of overwriting another run identity.
func establishRunIdentity(runID string) error {
	_, sessionErr := syscall.Setsid()
	if sessionErr != nil && !errors.Is(sessionErr, syscall.EPERM) {
		return errors.New("managed launch: cannot establish isolated run session")
	}
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return errors.New("managed launch: process identity unavailable")
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return errors.New("managed launch: invalid process identity")
	}
	fields := strings.Fields(string(raw[end+1:]))
	// Some container runtimes already create the exec process as a session
	// leader. EPERM is safe only when kernel evidence confirms this process
	// owns both the session and group; never borrow a caller-supplied identity.
	pid := strconv.Itoa(os.Getpid())
	if len(fields) < 20 || fields[2] != pid || fields[3] != pid {
		return errors.New("managed launch: invalid process identity")
	}
	stamp, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || stamp == 0 {
		return errors.New("managed launch: invalid process identity")
	}
	f, err := os.OpenFile(DirectRunPIDFile(runID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("managed launch: exclusive run identity unavailable")
	}
	_, writeErr := fmt.Fprintf(f, "%d %d\n", os.Getpid(), stamp)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("managed launch: cannot publish run identity")
	}
	return nil
}
