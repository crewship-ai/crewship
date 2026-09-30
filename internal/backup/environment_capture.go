package backup

import (
	"archive/tar"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// ErrEnvironmentUnsupported: the Docker ops given cannot commit or save
// images, so no complete environment can be captured with them.
var ErrEnvironmentUnsupported = errors.New("backup: complete environments need a Docker client that can commit and save images")

// Bundle layout of environments.
const (
	// environmentsPrefix holds environments/<crew>.json and
	// environments/<crew>/mounts/<n>/… .
	environmentsPrefix = "environments/"
	// environmentBlobsPrefix holds inline blobs:
	// environment-blobs/sha256/<hex>.
	environmentBlobsPrefix = "environment-blobs/"
)

// FlushHookLabel is the container label a crew uses to declare its own
// pre-pause flush command (run with `sh -c` as the container's user), e.g.
// "redis-cli save". Without it the collector runs `sync`.
const FlushHookLabel = "crewship.backup.flush"

// DefaultFlushTimeout bounds the flush hook.
const DefaultFlushTimeout = 30 * time.Second

// EnvironmentOptions configure one capture.
type EnvironmentOptions struct {
	// Store receives the image blobs (required), encrypted under Key
	// (required).
	Store *EnvironmentStore
	Key   *StoreKey
	// DB records the blobs and PendingRef in the reference counts; nil
	// records nothing (tests, and captures nobody will rotate).
	DB         *sql.DB
	PendingRef string
	// Payload receives environments/<crew>.json and the content of mounts
	// no crew section covers. Nil writes only to the store.
	Payload *TarZstWriter
	// Inline also writes every blob into Payload (environment-blobs/), so
	// the bundle restores without the store — what an off-site copy needs.
	Inline bool
	// Covered names the crew sections (SectionCrew*) the collector writes
	// into the same payload; a mount whose section is covered is not
	// copied twice.
	Covered map[string]bool
	// FlushTimeout bounds the flush hook (default DefaultFlushTimeout).
	FlushTimeout time.Duration
	// NoFlush skips the flush hook.
	NoFlush bool
	Now     func() time.Time
}

func (o *EnvironmentOptions) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

// environmentCapture is one capture in progress. CollectEnvironment runs
// begin → underPause (inside WithPaused) → finish; CreateBackup runs
// underPause inside the same pause as the crew's files.
type environmentCapture struct {
	ops       EnvironmentOps
	crew      CrewTarget
	opts      EnvironmentOptions
	snap      ContainerSnapshot
	env       *Environment
	committed bool
}

// CollectEnvironment captures one crew container's complete environment:
// flush hook, pause, commit and the content of every mount no crew section
// covers, unpause, then save the committed image into the store (each blob
// once) and remove the temporary image. The record is written to the
// store's index and, with a payload, to environments/<crew>.json.
func CollectEnvironment(ctx context.Context, ops DockerOps, crew CrewTarget, store *EnvironmentStore, opts EnvironmentOptions) (*Environment, error) {
	opts.Store = store
	c, err := beginEnvironment(ctx, ops, crew, opts)
	if err != nil {
		return nil, err
	}
	if err := WithPaused(ctx, ops, crew.ContainerID, func() error { return c.underPause(ctx) }); err != nil {
		c.abort()
		return nil, err
	}
	return c.finish(ctx)
}

func beginEnvironment(ctx context.Context, ops DockerOps, crew CrewTarget, opts EnvironmentOptions) (*environmentCapture, error) {
	eo, ok := ops.(EnvironmentOps)
	if !ok {
		return nil, ErrEnvironmentUnsupported
	}
	if opts.Store == nil {
		return nil, errors.New("backup: environment capture needs a store")
	}
	if opts.Key == nil {
		return nil, ErrStoreKeyMissing
	}
	if crew.ContainerID == "" {
		return nil, fmt.Errorf("backup: crew %s has no container", crew.Slug)
	}
	snap, err := eo.InspectContainer(ctx, crew.ContainerID)
	if err != nil {
		return nil, err
	}
	now := opts.now()
	id := newEnvironmentID(now)
	cfg, unsafe, mounts := SanitizeContainer(snap)
	env := &Environment{
		Format: EnvironmentFormat, ID: id, Crew: crew.Slug, CrewID: crew.ID, Container: snap.Name,
		CreatedAt: now, Managed: isManagedContainer(snap),
		SourceImage: snap.Image, SourceImageID: snap.ImageID, ImageRef: environmentImageRef(crew.Slug, id), StoreKey: opts.Key.Gen,
		Config: cfg, Unsafe: unsafe, Mounts: mounts,
		Runtime: RuntimeRequirements{Runtime: snap.Runtime},
		Notes: []string{
			"processes start fresh on restore: what was only in memory is not captured",
		},
	}
	if env.Runtime.Runtime == "" {
		env.Runtime.Runtime = "runc"
	}
	if len(cfg.DroppedEnv) > 0 {
		env.Notes = append(env.Notes, "environment variables that look like secrets were left out: "+strings.Join(cfg.DroppedEnv, ", "))
	}
	for i := range env.Mounts {
		m := &env.Mounts[i]
		if sec, ok := standardMountSections[m.Destination]; ok && opts.Covered[sec] && m.Restore == MountRestoreVolume {
			m.Data = "section:" + sec
		}
	}
	c := &environmentCapture{ops: eo, crew: crew, opts: opts, snap: snap, env: env}
	if !opts.NoFlush {
		c.flush(ctx)
	}
	return c, nil
}

// flush runs the pre-pause hook: the crew's declared command, or `sync`.
// It is best-effort by design — a failing hook is recorded, never fatal:
// the pause that follows still gives a crash-consistent copy.
func (c *environmentCapture) flush(ctx context.Context) {
	if !c.snap.Running || c.snap.Paused {
		c.env.Flush = FlushResult{Detail: "container not running; nothing to flush"}
		return
	}
	cmd, user := []string{"sync"}, "0:0"
	if hook := strings.TrimSpace(c.snap.Labels[FlushHookLabel]); hook != "" {
		cmd, user = []string{"sh", "-c", hook}, c.snap.User
		if user == "" {
			user = "0:0"
		}
	}
	timeout := c.opts.FlushTimeout
	if timeout <= 0 {
		timeout = DefaultFlushTimeout
	}
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	code, out, err := c.ops.ExecAs(fctx, c.crew.ContainerID, user, cmd)
	c.env.Flush = FlushResult{Ran: true, Command: cmd}
	switch {
	case err != nil:
		c.env.Flush.Detail = "flush hook failed: " + err.Error()
	case code != 0:
		c.env.Flush.Detail = fmt.Sprintf("flush hook exited %d: %s", code, strings.TrimSpace(truncate(string(out), 300)))
	default:
		c.env.Flush.OK = true
	}
	if !c.env.Flush.OK {
		slog.Warn("backup: environment flush hook did not succeed; continuing with the pause", "crew", c.crew.Slug, "detail", c.env.Flush.Detail)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// underPause commits the container and copies every mount no crew section
// covers. Called with the container paused (or stopped).
func (c *environmentCapture) underPause(ctx context.Context) error {
	var env []string
	if len(c.env.Config.DroppedEnv) > 0 {
		env = append(env, c.env.Config.Env...)
		for _, n := range c.env.Config.DroppedEnv {
			env = append(env, n+"=")
		}
	}
	if _, err := c.ops.CommitContainer(ctx, c.crew.ContainerID, c.env.ImageRef, env); err != nil {
		return err
	}
	c.committed = true
	if c.opts.Payload == nil {
		return nil
	}
	for i := range c.env.Mounts {
		m := &c.env.Mounts[i]
		if m.Restore != MountRestoreVolume || m.Data != "" {
			continue
		}
		prefix := fmt.Sprintf("%s%s/mounts/%d", environmentsPrefix, c.crew.Slug, i)
		res, err := copyContainerPath(ctx, c.ops, c.opts.Payload, c.crew.ContainerID, m.Destination, prefix, nil)
		if err != nil {
			return fmt.Errorf("backup: environment %s: copy mount %s: %w", c.crew.Slug, m.Destination, err)
		}
		m.Data, m.Files = prefix, res.Files
	}
	return nil
}

// abort removes the temporary image of a capture that will not finish.
func (c *environmentCapture) abort() {
	if !c.committed {
		return
	}
	rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.ops.RemoveImage(rctx, c.env.ImageRef); err != nil {
		slog.Warn("backup: could not remove a temporary environment image", "image", c.env.ImageRef, "error", err)
	}
	c.committed = false
}

// finish saves the committed image into the store, records the refs and
// writes the record. The temporary image is removed whatever happens.
func (c *environmentCapture) finish(ctx context.Context) (*Environment, error) {
	defer c.abort()
	env := c.env
	p, err := c.ops.ImagePlatform(ctx, env.ImageRef)
	if err != nil {
		return nil, err
	}
	env.Platform = p
	if ri, err := c.ops.RuntimeInfo(ctx); err == nil {
		env.Runtime.DockerVersion, env.Runtime.APIVersion, env.Runtime.HostPlatform = ri.DockerVersion, ri.APIVersion, ri.Platform()
	}
	if err := c.ingest(ctx); err != nil {
		return nil, err
	}
	if err := c.opts.Store.WriteIndex(env); err != nil {
		return nil, fmt.Errorf("backup: write environment index: %w", err)
	}
	if c.opts.Payload != nil {
		// The key rides the (encrypted) payload, so the bundle alone —
		// opened with its own identity — decrypts the layers.
		if err := writeStoreKey(c.opts.Payload, c.opts.Key, env.CreatedAt); err != nil {
			return nil, err
		}
		b, err := json.MarshalIndent(env, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := c.opts.Payload.WriteFile(environmentsPrefix+c.crew.Slug+".json", 0o600, env.CreatedAt, b); err != nil {
			return nil, err
		}
	}
	return env, nil
}

// ingest streams `docker save` into the store. Every regular file of the
// archive becomes a blob; directories and symlinks are recorded as entries
// so the archive can be reassembled exactly.
func (c *environmentCapture) ingest(ctx context.Context) error {
	rc, err := c.ops.SaveImage(ctx, c.env.ImageRef)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	unlock := c.opts.Store.lock()
	defer unlock()
	sizes := map[string]int64{}
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("backup: read saved image: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if strings.Contains(name, "..") {
			return fmt.Errorf("backup: saved image entry %q climbs out of the archive", hdr.Name)
		}
		e := ArchiveEntry{Path: name, Mode: hdr.Mode}
		switch hdr.Typeflag {
		case tar.TypeDir:
			e.Type = "dir"
		case tar.TypeSymlink:
			e.Type, e.Link = "symlink", hdr.Linkname
		case tar.TypeReg:
			e.Type = "file"
			// A blob named by digest must hash to that digest.
			want := ""
			if h, ok := strings.CutPrefix(name, "blobs/sha256/"); ok && len(h) == 64 {
				want = "sha256:" + h
			}
			d, obj, n, err := c.opts.Store.PutSealed(tr, want, c.opts.Key)
			if err != nil {
				return err
			}
			e.Digest, e.Object, e.Size = d, obj, n
			sizes[obj] = n
			c.env.Image.Bytes += n
		default:
			continue
		}
		c.env.Image.Entries = append(c.env.Image.Entries, e)
	}
	if err := recordEnvironmentRefs(ctx, c.opts.DB, c.opts.PendingRef, sizes); err != nil {
		return err
	}
	if c.opts.Inline && c.opts.Payload != nil {
		for _, d := range c.env.Blobs() {
			if err := writeInlineBlob(ctx, c.opts.Store, c.opts.Payload, d, c.env.CreatedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeInlineBlob copies one blob from the store into a payload.
func writeInlineBlob(ctx context.Context, store *EnvironmentStore, dst *TarZstWriter, digest string, now time.Time) error {
	h, err := digestHex(digest)
	if err != nil {
		return err
	}
	rc, n, err := store.Open(ctx, digest)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	return dst.WriteStream(environmentBlobsPrefix+"sha256/"+h, 0o400, now, n, rc)
}

// crewSectionsFor lists the crew sections collectCrewSections writes for a
// level and filter — the "covered" set a capture in the same payload uses.
func crewSectionsFor(level ScopeLevel, want func(string) bool) map[string]bool {
	out := map[string]bool{}
	secs := []string{SectionCrewWorkspace, SectionCrewMemory, SectionCrewOutput}
	if level == ScopeLevelStandard || level == ScopeLevelFull {
		secs = append(secs, SectionCrewHome, SectionCrewTools)
	}
	for _, s := range secs {
		if want == nil || want(s) {
			out[s] = true
		}
	}
	return out
}
