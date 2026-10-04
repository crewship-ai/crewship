//go:build linux

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"golang.org/x/sys/unix"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// Host-owned intent is published before DELETE. It contains no secrets and
// survives a failed delete, failed env cleanup, or server restart. Only this
// private journal, never the env tree, is enumerated for reconciliation.
type stagedCleanupIntent struct {
	Installation, Crew, Container, Image, Handle string
}

func cleanupIntent(c container.InspectResponse) stagedCleanupIntent {
	return stagedCleanupIntent{c.Config.Labels[resourcelifecycle.InstanceLabel], c.Config.Labels[crewCrewIDLabel], c.ID, c.Image, c.Config.Labels[stagedEnvLabel]}
}

func (p *Provider) stagedCleanupDir(create bool) (*os.File, error) {
	path, err := filepath.Abs(filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName, "staged-env-cleanup"))
	if err != nil {
		return nil, errStagedDenied
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		if create && i == len(parts)-1 {
			if err = unix.Mkdirat(int(dir.Fd()), part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
				dir.Close()
				return nil, err
			}
			if err = dir.Sync(); err != nil {
				dir.Close()
				return nil, err
			}
		}
		next, e := unix.Openat(int(dir.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		dir.Close()
		if e != nil {
			return nil, e
		}
		dir = os.NewFile(uintptr(next), part)
		info, e := dir.Stat()
		if e != nil || !safeLauncherDirectory(info) || (i == len(parts)-1 && info.Mode().Perm() != 0700) {
			dir.Close()
			return nil, errStagedDenied
		}
	}
	return dir, nil
}

func (p *Provider) readStagedCleanup(dir *os.File, name string) (container.InspectResponse, *os.File, error) {
	var c container.InspectResponse
	if !strings.HasSuffix(name, ".json") || !stagedEnvHandle.MatchString(strings.TrimSuffix(name, ".json")) {
		return c, nil, errStagedDenied
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return c, nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !stagedOwned(info) || info.Size() > 4096 {
		f.Close()
		return c, nil, errStagedDenied
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	var saved stagedCleanupIntent
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &saved) != nil || saved.Container+".json" != name {
		f.Close()
		return c, nil, errStagedDenied
	}
	c.Config = &container.Config{Labels: map[string]string{resourcelifecycle.InstanceLabel: saved.Installation, crewCrewIDLabel: saved.Crew, stagedEnvLabel: saved.Handle}}
	c.ID, c.Image = saved.Container, saved.Image
	if err := p.validateStagedEnvScope(c); err != nil {
		f.Close()
		return c, nil, err
	}
	return c, f, nil
}

func (p *Provider) publishStagedCleanup(c container.InspectResponse) error {
	if p.validateStagedEnvScope(c) != nil || !stagedEnvHandle.MatchString(c.ID) {
		return errStagedDenied
	}
	dir, err := p.stagedCleanupDir(true)
	if err != nil {
		return err
	}
	defer dir.Close()
	name := c.ID + ".json"
	temp := ".pending-" + newStagedEnvHandle()
	fd, err := unix.Openat(int(dir.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), temp)
	defer f.Close()
	defer func() { _ = unix.Unlinkat(int(dir.Fd()), temp, 0) }()
	raw, err := json.Marshal(cleanupIntent(c))
	if err != nil || len(raw) > 4096 {
		return errStagedDenied
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = unix.Linkat(int(dir.Fd()), temp, int(dir.Fd()), name, 0); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
		existing, opened, e := p.readStagedCleanup(dir, name)
		if e != nil {
			return e
		}
		opened.Close()
		if cleanupIntent(existing) != cleanupIntent(c) {
			return errStagedDenied
		}
	}
	return dir.Sync()
}

func unlinkStagedCleanup(dir, opened *os.File) error {
	var before, current unix.Stat_t
	name := opened.Name()
	if unix.Fstat(int(opened.Fd()), &before) != nil || unix.Fstatat(int(dir.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || before.Dev != current.Dev || before.Ino != current.Ino {
		return errStagedDenied
	}
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
		return err
	}
	return dir.Sync()
}

func (p *Provider) finishStagedCleanup(c container.InspectResponse) error {
	dir, err := p.stagedCleanupDir(false)
	if err != nil {
		return err
	}
	defer dir.Close()
	saved, opened, err := p.readStagedCleanup(dir, c.ID+".json")
	if err != nil {
		return err
	}
	defer opened.Close()
	if cleanupIntent(saved) != cleanupIntent(c) {
		return errStagedDenied
	}
	if err := p.removeStagedEnv(saved); err != nil {
		return err
	}
	if err := unlinkStagedCleanup(dir, opened); err != nil {
		return err
	}
	p.stagedEnvScopes.Delete(c.ID)
	return nil
}

// Core invokes this at its existing staged startup/recovery boundary. Each
// call handles at most 64 journal entries in 10 seconds, without a background
// worker. Existing containers are retained, not force-deleted by reconciliation.
func (p *Provider) reconcileStagedEnvCleanup(ctx context.Context, limit int) error {
	if limit < 1 || limit > 64 {
		return errStagedDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dir, err := p.stagedCleanupDir(false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var failures []error
	if len(names) > limit {
		names = names[:limit]
		failures = append(failures, fmt.Errorf("staged cleanup batch limit reached"))
	}
	for _, name := range names {
		if ctx.Err() != nil {
			failures = append(failures, ctx.Err())
			break
		}
		c, opened, e := p.readStagedCleanup(dir, name)
		if e != nil {
			failures = append(failures, fmt.Errorf("invalid staged cleanup record: %w", e))
			continue
		}
		_, e = p.client.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if !errdefs.IsNotFound(e) {
			if e == nil {
				e = fmt.Errorf("staged cleanup runtime still exists")
			}
		} else {
			e = p.removeStagedEnv(c)
			if e == nil {
				e = unlinkStagedCleanup(dir, opened)
			}
			if e == nil {
				p.stagedEnvScopes.Delete(c.ID)
			}
		}
		opened.Close()
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
