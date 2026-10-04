//go:build linux

package docker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/moby/moby/api/types/container"
	"golang.org/x/sys/unix"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

const stagedEnvLabel = "crewship.staged-env-ref"

var stagedEnvHandle = regexp.MustCompile(`^[a-f0-9]{64}$`)

type stagedEnvironment struct {
	Installation, Crew, Container, Image string
	Env                                  []string
}

func newStagedEnvHandle() string {
	var token [32]byte
	_, _ = rand.Read(token[:])
	return hex.EncodeToString(token[:])
}

// Only an opaque random handle is discoverable on Docker. Confidential values
// stay in the existing host-owned runtime material tree, never mounted or logged.
func (p *Provider) stagedEnvPath(c container.InspectResponse) (string, error) {
	scope, handle, e := p.stagedEnvScope(c)
	if e != nil {
		return "", e
	}
	parent := filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName)
	info, e := os.Lstat(parent)
	if e != nil || !safeLauncherDirectory(info) {
		return "", errStagedDenied
	}
	// The authoritative scope selects a private directory BEFORE reading material.
	// Copying a reference onto another runtime cannot select the original file.
	dir := parent
	for _, part := range []string{"staged-env", scope} {
		dir = filepath.Join(dir, part)
		if e = os.Mkdir(dir, 0700); e != nil && !os.IsExist(e) {
			return "", errStagedDenied
		}
		info, e = os.Lstat(dir)
		if e != nil || !info.IsDir() || !stagedOwned(info) || info.Mode().Perm()&0077 != 0 {
			return "", errStagedDenied
		}
	}
	return filepath.Join(dir, handle+".json"), nil
}

func (p *Provider) saveStagedEnv(c container.InspectResponse, env []string) error {
	if c.Config == nil || p.cfg.InstanceID == "" || c.Config.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || c.ID == "" || c.Image == "" || c.Config.Labels[crewCrewIDLabel] == "" {
		return errStagedDenied
	}
	target, e := p.stagedEnvPath(c)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(stagedEnvironment{Installation: p.cfg.InstanceID, Crew: c.Config.Labels[crewCrewIDLabel], Container: c.ID, Image: c.Image, Env: env})
	if e != nil || len(raw) > 1<<20 {
		return errStagedDenied
	}
	f, e := os.CreateTemp(filepath.Dir(target), ".env-")
	if e != nil {
		return errStagedDenied
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(raw); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil || closeErr != nil {
		return errStagedDenied
	}
	// Publication never overwrites material referenced by another runtime.
	if e = os.Link(f.Name(), target); e != nil {
		return errStagedDenied
	}
	return nil
}

func (p *Provider) loadStagedEnv(c container.InspectResponse) ([]string, error) {
	if p.cfg.InstanceID == "" || c.Config == nil || c.Config.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || c.Config.Labels[crewCrewIDLabel] == "" {
		return nil, errStagedDenied
	}
	target, e := p.stagedEnvPath(c)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(target, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, errStagedDenied
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !stagedOwned(info) || info.Size() > 1<<20 {
		return nil, errStagedDenied
	}
	raw, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return nil, errStagedDenied
	}
	var saved stagedEnvironment
	if json.Unmarshal(raw, &saved) != nil || saved.Installation != p.cfg.InstanceID || saved.Crew != c.Config.Labels[crewCrewIDLabel] || saved.Container != c.ID || saved.Image != c.Image {
		return nil, errStagedDenied
	}
	return saved.Env, nil
}

func (p *Provider) stagedEnvScope(c container.InspectResponse) (scope, handle string, err error) {
	if os.Geteuid() == 1001 || os.Geteuid() == 1002 || p.cfg.InstanceID == "" || c.Config == nil || c.Config.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || c.Config.Labels[crewCrewIDLabel] == "" || c.ID == "" || c.Image == "" {
		return "", "", errStagedDenied
	}
	handle = c.Config.Labels[stagedEnvLabel]
	if !stagedEnvHandle.MatchString(handle) {
		return "", "", errStagedDenied
	}
	digest := sha256.Sum256([]byte(p.cfg.InstanceID + "\x00" + c.Config.Labels[crewCrewIDLabel] + "\x00" + c.ID + "\x00" + c.Image))
	return hex.EncodeToString(digest[:]), handle, nil
}

// No global sweep: an orphan lacking authentic runtime metadata is retained
// for explicit operator reconciliation. Cleanup neither creates directories
// nor follows symlinks, including intermediate components. Directory FDs pin
// the validated scope throughout the read/validate/unlink sequence.
func (p *Provider) removeStagedEnv(c container.InspectResponse) error {
	scope, handle, err := p.stagedEnvScope(c)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName, "staged-env", scope))
	if err != nil {
		return errStagedDenied
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return errStagedDenied
	}
	defer func() { _ = unix.Close(fd) }()
	parts := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	for i, part := range parts {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) {
			return nil
		}
		if e != nil {
			return errStagedDenied
		}
		f := os.NewFile(uintptr(next), part)
		info, e := f.Stat()
		if e != nil || !safeLauncherDirectory(info) || (i >= len(parts)-2 && info.Mode().Perm()&0077 != 0) {
			_ = f.Close()
			return errStagedDenied
		}
		// Transfer the directory descriptor without an os.File finalizer owning it.
		owned, e := unix.Dup(next)
		_ = f.Close()
		if e != nil {
			return errStagedDenied
		}
		unix.CloseOnExec(owned)
		_ = unix.Close(fd)
		fd = owned
	}
	name := handle + ".json"
	fileFD, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return errStagedDenied
	}
	f := os.NewFile(uintptr(fileFD), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !stagedOwned(info) || info.Size() > 1<<20 {
		return errStagedDenied
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	var saved stagedEnvironment
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &saved) != nil || saved.Installation != p.cfg.InstanceID || saved.Crew != c.Config.Labels[crewCrewIDLabel] || saved.Container != c.ID || saved.Image != c.Image {
		return errStagedDenied
	}
	var before, current unix.Stat_t
	if unix.Fstat(fileFD, &before) != nil || unix.Fstatat(fd, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || before.Dev != current.Dev || before.Ino != current.Ino {
		return errStagedDenied
	}
	if err := unix.Unlinkat(fd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}
