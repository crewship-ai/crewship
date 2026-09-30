package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// environmentRun captures the environments of every crew one bundle
// carries, next to their files. A nil *environmentRun (env_mode=files)
// collects the files alone.
type environmentRun struct {
	store      *EnvironmentStore
	db         *sql.DB
	pending    string
	inline     bool
	envs       []*Environment
	deferred   []deferredEnvironment
	incomplete []IncompleteItem
}

// newEnvironmentRun returns nil unless envMode asks for complete
// environments.
func newEnvironmentRun(db *sql.DB, backupsDir, envMode string, inline bool, now time.Time) *environmentRun {
	if envMode != EnvModeComplete {
		return nil
	}
	return &environmentRun{store: EnvironmentStoreFor(backupsDir), db: db, pending: newPendingRef(now), inline: inline}
}

// level raises Quick to Standard: a complete environment carries every
// volume, and the named volumes are Standard's sections.
func (r *environmentRun) level(l ScopeLevel) ScopeLevel {
	if r != nil && l == ScopeLevelQuick {
		return ScopeLevelStandard
	}
	return l
}

// collect writes one crew's sections and, when environments are on, its
// environment under the same pause. A crew whose environment cannot be
// captured still gets its files; the failure is recorded as incomplete.
func (r *environmentRun) collect(ctx context.Context, ops DockerOps, dst *TarZstWriter, crew CrewTarget, level ScopeLevel, want func(string) bool) (CrewCapture, error) {
	if r == nil {
		return collectCrewSections(ctx, ops, dst, crew, level, want, nil)
	}
	c, err := beginEnvironment(ctx, ops, crew, EnvironmentOptions{
		Store: r.store, DB: r.db, PendingRef: r.pending, Payload: dst, Inline: r.inline,
		Covered: crewSectionsFor(level, want),
	})
	if err != nil {
		r.fail(crew, err)
		return collectCrewSections(ctx, ops, dst, crew, level, want, nil)
	}
	var commitErr error
	capture, err := collectCrewSections(ctx, ops, dst, crew, level, want, func() error {
		if err := c.underPause(ctx); err != nil {
			if !c.committed {
				// Nothing reached the payload yet: the files still
				// count, the environment does not.
				commitErr = err
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		c.abort()
		return capture, err
	}
	if commitErr != nil {
		r.fail(crew, commitErr)
		return capture, nil
	}
	env, err := c.finish(ctx)
	if err != nil {
		r.fail(crew, err)
		return capture, nil
	}
	r.envs = append(r.envs, env)
	return capture, nil
}

// deferredEnvironment is a capture whose commit is done and whose save
// waits until the instance's quiet window is released.
type deferredEnvironment struct {
	workspace string
	crew      CrewTarget
	c         *environmentCapture
}

// collectDeferred is collect for the instance backup: the commit and the
// extra mounts happen under the crew's pause inside the quiet window, the
// `docker save` into the store — the slow part, and one that touches no
// Crewship data — happens after the window is released (finishDeferred).
func (r *environmentRun) collectDeferred(ctx context.Context, ops DockerOps, dst *TarZstWriter, crew CrewTarget, level ScopeLevel, workspace string) (CrewCapture, error) {
	if r == nil {
		return collectCrewSections(ctx, ops, dst, crew, level, nil, nil)
	}
	c, err := beginEnvironment(ctx, ops, crew, EnvironmentOptions{
		Store: r.store, DB: r.db, PendingRef: r.pending, Payload: dst, Inline: r.inline,
		Covered: crewSectionsFor(level, nil),
	})
	if err != nil {
		r.fail(crew, err)
		return collectCrewSections(ctx, ops, dst, crew, level, nil, nil)
	}
	var commitErr error
	capture, err := collectCrewSections(ctx, ops, dst, crew, level, nil, func() error {
		if err := c.underPause(ctx); err != nil {
			if !c.committed {
				commitErr = err
				return nil
			}
			return err
		}
		return nil
	})
	if err != nil {
		c.abort()
		return capture, err
	}
	if commitErr != nil {
		r.fail(crew, commitErr)
		return capture, nil
	}
	r.deferred = append(r.deferred, deferredEnvironment{workspace: workspace, crew: crew, c: c})
	return capture, nil
}

// finishDeferred saves every deferred capture. Each workspace's records
// (and inline blobs) go to <dir>/<workspace>.tar.zst; the returned map
// names those archives.
// create opens each archive file (the instance backup passes its encrypted
// staging writer).
func (r *environmentRun) finishDeferred(ctx context.Context, dir string, create func(path string) (io.WriteCloser, error)) (map[string]string, error) {
	out := map[string]string{}
	if r == nil || len(r.deferred) == 0 {
		return out, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	type archive struct {
		f  io.WriteCloser
		tw *TarZstWriter
	}
	archives := map[string]*archive{}
	closeAll := func() {
		for _, a := range archives {
			_ = a.tw.Close()
			_ = a.f.Close()
		}
	}
	for i, d := range r.deferred {
		a := archives[d.workspace]
		if a == nil {
			p := filepath.Join(dir, d.workspace+instanceCrewsSuffix)
			f, err := create(p)
			if err != nil {
				closeAll()
				r.abortDeferred(i)
				return nil, err
			}
			tw, err := NewTarZstWriter(f)
			if err != nil {
				_ = f.Close()
				closeAll()
				r.abortDeferred(i)
				return nil, err
			}
			a = &archive{f: f, tw: tw}
			archives[d.workspace] = a
			out[d.workspace] = p
		}
		d.c.opts.Payload = a.tw
		env, err := d.c.finish(ctx)
		if err != nil {
			r.fail(d.crew, err)
			continue
		}
		r.envs = append(r.envs, env)
	}
	r.deferred = nil
	for ws, a := range archives {
		if err := a.tw.Close(); err != nil {
			_ = a.f.Close()
			return nil, fmt.Errorf("backup: close environments archive %s: %w", ws, err)
		}
		if err := a.f.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// abortDeferred removes the temporary images of captures from i on.
func (r *environmentRun) abortDeferred(from int) {
	for _, d := range r.deferred[from:] {
		d.c.abort()
	}
	r.deferred = nil
}

// abortAll removes the temporary images of every unfinished capture.
func (r *environmentRun) abortAll() {
	if r != nil {
		r.abortDeferred(0)
	}
}

func (r *environmentRun) fail(crew CrewTarget, err error) {
	slog.Warn("backup: complete environment not captured; the crew's files are in the bundle", "crew", crew.Slug, "error", err)
	r.incomplete = append(r.incomplete, IncompleteItem{
		Kind: IncompleteEnvironmentFailed, Count: 1,
		Detail: "the complete environment of crew " + crew.Slug + " was not captured (" + err.Error() + "); its files are in the bundle and a restore rebuilds its environment from the crew image",
	})
}

// summaries is the manifest's environments list.
func (r *environmentRun) summaries() []EnvironmentSummary {
	if r == nil {
		return nil
	}
	out := []EnvironmentSummary{}
	for _, e := range r.envs {
		out = append(out, e.Summary(r.inline))
	}
	return out
}

// incompleteItems lists crews whose environment failed.
func (r *environmentRun) incompleteItems() []IncompleteItem {
	if r == nil {
		return nil
	}
	return r.incomplete
}

// commit moves the run's refs onto the finished bundle.
func (r *environmentRun) commit(ctx context.Context, bundlePath string) error {
	if r == nil {
		return nil
	}
	return commitEnvironmentRefs(ctx, r.db, r.pending, bundlePath)
}

// release drops the refs of a run whose bundle was not written.
func (r *environmentRun) release() {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ReleaseEnvironmentRefs(ctx, r.db, r.pending); err != nil {
		slog.Warn("backup: could not release a failed run's environment refs; the collector drops them after a day", "error", err)
	}
}
