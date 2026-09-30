package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Environment restore outcomes.
const (
	// EnvRestored: the image was loaded (and, for a container Crewship
	// does not manage, the container recreated and started).
	EnvRestored = "restored"
	// EnvRebuilt: the environment could not come back as captured
	// (architecture, missing layers); the crew's data is restored and its
	// environment is built again from the crew image.
	EnvRebuilt = "rebuilt"
	// EnvSkipped: nothing was done for the environment (no Docker, or an
	// error); the reason says which.
	EnvSkipped = "skipped"
)

// BlobOpener opens an environment blob by digest.
type BlobOpener func(ctx context.Context, digest string) (io.ReadCloser, int64, error)

// BlobSources chains stores: the first that holds a blob serves it. A nil
// store is skipped.
func BlobSources(stores ...*EnvironmentStore) (BlobOpener, func(string) bool) {
	var live []*EnvironmentStore
	for _, s := range stores {
		if s != nil {
			live = append(live, s)
		}
	}
	has := func(d string) bool {
		for _, s := range live {
			if s.Has(d) {
				return true
			}
		}
		return false
	}
	open := func(ctx context.Context, d string) (io.ReadCloser, int64, error) {
		for _, s := range live {
			if s.Has(d) {
				return s.Open(ctx, d)
			}
		}
		return nil, 0, fmt.Errorf("%w: %s", ErrBlobMissing, d)
	}
	return open, has
}

// EnvironmentCheck is one crew's runtime check before a restore.
type EnvironmentCheck struct {
	Crew         string `json:"crew"`
	Action       string `json:"action"` // restore | rebuild | skip
	Platform     string `json:"platform"`
	HostPlatform string `json:"host_platform"`
	MissingBlobs int    `json:"missing_blobs"`
	NeedBytes    int64  `json:"need_bytes"`
	Detail       string `json:"detail"`
}

// Check actions.
const (
	EnvActionRestore = "restore"
	EnvActionRebuild = "rebuild"
	EnvActionSkip    = "skip"
)

// platformMatches compares os and architecture (variant ignored: the
// daemon reports none for amd64 and a restore of arm64/v8 onto arm64 is
// fine).
func platformMatches(envPlatform, host string) bool {
	if envPlatform == "" || host == "" {
		return true
	}
	e := strings.Split(envPlatform, "/")
	h := strings.Split(host, "/")
	if len(e) < 2 || len(h) < 2 {
		return envPlatform == host
	}
	return e[0] == h[0] && normArch(e[1]) == normArch(h[1])
}

func normArch(a string) string {
	switch a {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	}
	return a
}

// CheckEnvironment decides what a restore does with one environment.
func CheckEnvironment(s EnvironmentSummary, host string, dockerErr error, has func(string) bool) EnvironmentCheck {
	c := EnvironmentCheck{Crew: s.Crew, Platform: s.Platform, HostPlatform: host}
	for _, d := range s.Blobs {
		if has == nil || !has(d) {
			c.MissingBlobs++
		}
	}
	c.NeedBytes = s.Bytes
	switch {
	case dockerErr != nil:
		c.Action = EnvActionSkip
		c.Detail = "Docker is not available: the crew's data comes back, its environment waits until Docker is (" + dockerErr.Error() + ")"
	case !platformMatches(s.Platform, host):
		c.Action = EnvActionRebuild
		c.Detail = fmt.Sprintf("built for %s and this server is %s: its data comes back, its environment is rebuilt from the crew image", s.Platform, host)
	case c.MissingBlobs > 0:
		c.Action = EnvActionRebuild
		c.Detail = fmt.Sprintf("%d of %d image layer file(s) are not available here: its data comes back, its environment is rebuilt from the crew image", c.MissingBlobs, len(s.Blobs))
	default:
		c.Action = EnvActionRestore
		c.Detail = fmt.Sprintf("%s, %s of image layers", s.Platform, humanBytes(s.Bytes))
	}
	return c
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// EnvironmentRestoreOptions configure RestoreEnvironment.
type EnvironmentRestoreOptions struct {
	Open BlobOpener
	Has  func(string) bool
	// Host is the daemon's platform; zero asks the daemon.
	Host RuntimeInfo
	// Recreate creates and starts a container from the environment when
	// Crewship does not manage it. A managed crew's container is always
	// left to Crewship, which recreates it from the restored image with the
	// runtime settings it owns.
	Recreate bool
	// Name is the recreated container's name.
	Name string
	// VolumePrefix names the recreated container's volumes
	// <prefix>-<n>; empty makes anonymous volumes.
	VolumePrefix string
	// MountData opens a mount's captured content (entries relative to the
	// mount point).
	MountData func(ctx context.Context, m EnvironmentMount) (io.ReadCloser, bool, error)
	// DryRun checks only.
	DryRun bool
}

// EnvironmentOutcome reports one crew's environment restore.
type EnvironmentOutcome struct {
	Crew      string   `json:"crew"`
	Result    string   `json:"result"`
	Reason    string   `json:"reason,omitempty"`
	Image     string   `json:"image,omitempty"`
	Container string   `json:"container,omitempty"`
	Volumes   []string `json:"volumes,omitempty"`
	// Unsafe lists what was NOT carried over, as sentences.
	Unsafe []string `json:"unsafe"`
	Notes  []string `json:"notes,omitempty"`
}

// UnsafeSentences renders an environment's unsafe list.
func (e *Environment) UnsafeSentences() []string {
	out := []string{}
	for _, u := range e.Unsafe {
		out = append(out, u.Sentence(e.Crew))
	}
	return out
}

// RestoreEnvironment brings one environment back: check, load the image
// from its blobs, and — for a container Crewship does not manage, when
// asked — create the container with the sanitized configuration, land its
// mounts' content and start it. It never returns an error: every failure
// is an outcome with a reason, so one crew cannot fail another's restore.
func RestoreEnvironment(ctx context.Context, ops DockerOps, env *Environment, opts EnvironmentRestoreOptions) EnvironmentOutcome {
	out := EnvironmentOutcome{Crew: env.Crew, Unsafe: env.UnsafeSentences(), Notes: []string{"processes start fresh: what was only in memory is not restored"}}
	eo, ok := ops.(EnvironmentOps)
	if !ok {
		out.Result, out.Reason = EnvSkipped, ErrEnvironmentUnsupported.Error()
		return out
	}
	host := opts.Host
	var dockerErr error
	if host.Platform() == "" {
		host, dockerErr = eo.RuntimeInfo(ctx)
	}
	chk := CheckEnvironment(env.Summary(false), host.Platform(), dockerErr, opts.Has)
	switch chk.Action {
	case EnvActionSkip:
		out.Result, out.Reason = EnvSkipped, chk.Detail
		return out
	case EnvActionRebuild:
		out.Result, out.Reason = EnvRebuilt, chk.Detail
		return out
	}
	if opts.DryRun {
		out.Result, out.Reason = EnvRestored, "dry run: "+chk.Detail
		return out
	}
	if err := loadEnvironmentImage(ctx, eo, env, opts.Open); err != nil {
		out.Result, out.Reason = EnvSkipped, err.Error()
		return out
	}
	out.Image = env.ImageRef
	if env.Managed && env.SourceImage != "" && env.SourceImage != env.ImageRef {
		// Crewship addresses a crew's image by its cache tag; give the
		// restored image that tag when the host has none, so the crew runs
		// from the captured environment instead of a rebuild.
		exists, err := eo.ImageExists(ctx, env.SourceImage)
		switch {
		case err != nil:
			out.Notes = append(out.Notes, "could not check for "+env.SourceImage+": "+err.Error())
		case !exists:
			if err := eo.TagImage(ctx, env.ImageRef, env.SourceImage); err != nil {
				out.Notes = append(out.Notes, "could not tag the restored image as "+env.SourceImage+": "+err.Error())
			} else {
				out.Notes = append(out.Notes, "tagged as "+env.SourceImage+", the image the crew runs from")
			}
		default:
			out.Notes = append(out.Notes, env.SourceImage+" is already on this server and was left as it is; the captured image is "+env.ImageRef)
		}
	}
	if env.Managed || !opts.Recreate {
		out.Result = EnvRestored
		if env.Managed {
			out.Notes = append(out.Notes, "Crewship recreates the crew container from this image with the runtime settings it manages; the crew's files land through the restore")
		}
		return out
	}
	id, vols, notes, err := recreateContainer(ctx, eo, env, opts)
	out.Notes = append(out.Notes, notes...)
	out.Volumes = vols
	if err != nil {
		out.Result, out.Reason = EnvSkipped, err.Error()
		return out
	}
	out.Result, out.Container = EnvRestored, id
	return out
}

// restoreBundleEnvironments brings back every environment a workspace or
// crew bundle carries. Layers come from the bundle's inline blobs, the
// store beside the bundle file, then opts.EnvironmentStore. A managed crew
// gets its image loaded and tagged; its container is Crewship's to create.
func restoreBundleEnvironments(ctx context.Context, opts RestoreOptions, ex *ExtractedPayload, dryRun bool) []EnvironmentOutcome {
	if ex == nil || len(ex.EnvironmentBySlug) == 0 {
		return nil
	}
	var besides *EnvironmentStore
	if opts.Path != "" {
		besides = EnvironmentStoreFor(filepath.Dir(opts.Path))
	}
	open, has := BlobSources(ex.InlineEnvironmentBlobs(), besides, opts.EnvironmentStore)
	var out []EnvironmentOutcome
	for _, slug := range ex.Environments() {
		env := ex.EnvironmentBySlug[slug]
		var o EnvironmentOutcome
		if opts.DockerOps == nil {
			o = EnvironmentOutcome{Crew: env.Crew, Result: EnvSkipped, Reason: "no Docker on this server: the crew's data comes back, its environment waits until Docker is", Unsafe: env.UnsafeSentences()}
		} else {
			o = RestoreEnvironment(ctx, opts.DockerOps, env, EnvironmentRestoreOptions{Open: open, Has: has, DryRun: dryRun})
		}
		if opts.Logger != nil {
			line := fmt.Sprintf("environment %s: %s", o.Crew, o.Result)
			if o.Reason != "" {
				line += " — " + o.Reason
			}
			if len(o.Unsafe) > 0 {
				line += "; not carried over for safety: " + strings.Join(o.Unsafe, ", ")
			}
			opts.Logger(line)
		}
		out = append(out, o)
	}
	return out
}

// loadEnvironmentImage reassembles the saved archive and docker-loads it.
func loadEnvironmentImage(ctx context.Context, eo EnvironmentOps, env *Environment, open BlobOpener) error {
	if open == nil {
		return errors.New("backup: no blob source for the environment")
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(AssembleImageArchive(ctx, env, pw, open))
	}()
	_, err := eo.LoadImage(ctx, pr)
	_ = pr.CloseWithError(err)
	if err != nil {
		return fmt.Errorf("backup: load environment image %s: %w", env.ImageRef, err)
	}
	ok, err := eo.ImageExists(ctx, env.ImageRef)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("backup: docker load finished but %s is not on this server", env.ImageRef)
	}
	return nil
}

// AssembleImageArchive writes the saved image archive back out from its
// entries, reading each file from open.
func AssembleImageArchive(ctx context.Context, env *Environment, w io.Writer, open BlobOpener) error {
	tw := tar.NewWriter(w)
	epoch := time.Unix(0, 0)
	for _, e := range env.Image.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.Contains(e.Path, "..") || strings.HasPrefix(e.Path, "/") {
			return fmt.Errorf("backup: environment archive entry %q is not relative", e.Path)
		}
		hdr := &tar.Header{Name: e.Path, Mode: e.Mode, ModTime: epoch}
		switch e.Type {
		case "dir":
			hdr.Typeflag = tar.TypeDir
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
		case "symlink":
			hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, e.Link
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
		case "file":
			rc, n, err := open(ctx, e.Digest)
			if err != nil {
				return err
			}
			hdr.Typeflag, hdr.Size = tar.TypeReg, n
			if err := tw.WriteHeader(hdr); err != nil {
				_ = rc.Close()
				return err
			}
			_, err = io.CopyN(tw, rc, n)
			_ = rc.Close()
			if err != nil {
				return fmt.Errorf("backup: environment blob %s: %w", e.Digest, err)
			}
		}
	}
	return tw.Close()
}

// recreateContainer creates the container from the restored image with the
// sanitized configuration, lands every mount's content, and starts it.
func recreateContainer(ctx context.Context, eo EnvironmentOps, env *Environment, opts EnvironmentRestoreOptions) (string, []string, []string, error) {
	var notes, vols []string
	cfg := env.Config
	cfg.Labels = map[string]string{}
	for k, v := range env.Config.Labels {
		cfg.Labels[k] = v
	}
	cfg.Labels["crewship.restored-environment"] = env.ID
	spec := ContainerCreateSpec{Name: opts.Name, Image: env.ImageRef, Volumes: map[string]string{}, ReadOnly: map[string]bool{}}
	for i, m := range env.Mounts {
		switch m.Restore {
		case MountRestoreVolume:
			name := ""
			if opts.VolumePrefix != "" {
				name = fmt.Sprintf("%s-%d", opts.VolumePrefix, i)
				vols = append(vols, name)
			}
			spec.Volumes[m.Destination] = name
			if !m.RW {
				spec.ReadOnly[m.Destination] = true
			}
		case MountRestoreTmpfs:
			if cfg.Tmpfs == nil {
				cfg.Tmpfs = map[string]string{}
			}
			if _, ok := cfg.Tmpfs[m.Destination]; !ok {
				cfg.Tmpfs[m.Destination] = ""
			}
		case MountRestoreManaged:
			notes = append(notes, "Crewship's own bind at "+m.Destination+" is not recreated outside Crewship")
		}
	}
	spec.Config = cfg
	id, err := eo.CreateContainer(ctx, spec)
	if err != nil && cfg.NetworkMode != "" && !isBuiltinNetwork(cfg.NetworkMode) && strings.Contains(strings.ToLower(err.Error()), "network") {
		notes = append(notes, "network "+cfg.NetworkMode+" is not on this server; the container is on the default bridge")
		spec.Config.NetworkMode = "bridge"
		id, err = eo.CreateContainer(ctx, spec)
	}
	if err != nil && cfg.Runtime != "" && cfg.Runtime != "runc" && strings.Contains(strings.ToLower(err.Error()), "runtime") {
		notes = append(notes, "runtime "+cfg.Runtime+" is not on this server; the container uses the default runtime")
		spec.Config.Runtime = ""
		id, err = eo.CreateContainer(ctx, spec)
	}
	if err != nil {
		return "", vols, notes, err
	}
	fail := func(err error) (string, []string, []string, error) {
		rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = eo.RemoveContainer(rctx, id)
		return "", vols, notes, err
	}
	for _, m := range env.Mounts {
		if m.Restore != MountRestoreVolume || m.Data == "" || opts.MountData == nil {
			continue
		}
		rc, ok, err := opts.MountData(ctx, m)
		if err != nil {
			return fail(fmt.Errorf("backup: open content of %s: %w", m.Destination, err))
		}
		if !ok {
			notes = append(notes, "no content for "+m.Destination+" in the bundle; it starts empty")
			continue
		}
		err = eo.CopyTo(ctx, id, m.Destination, dropRootEntry(rc))
		_ = rc.Close()
		if err != nil {
			return fail(fmt.Errorf("backup: restore content of %s: %w", m.Destination, err))
		}
	}
	if err := eo.StartContainer(ctx, id); err != nil {
		return fail(err)
	}
	return id, vols, notes, nil
}

func isBuiltinNetwork(mode string) bool {
	switch mode {
	case "bridge", "default", "none", "host":
		return true
	}
	return false
}

// dropRootEntry filters a section tar's "." entry: extracting it onto a
// mount point would try to change the mount point itself.
func dropRootEntry(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		tr := tar.NewReader(r)
		tw := tar.NewWriter(pw)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				pw.CloseWithError(tw.Close())
				return
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			name := strings.TrimPrefix(hdr.Name, "./")
			if name == "" || name == "." {
				continue
			}
			hdr.Name = name
			if err := tw.WriteHeader(hdr); err != nil {
				pw.CloseWithError(err)
				return
			}
			if hdr.Typeflag == tar.TypeReg {
				if _, err := io.Copy(tw, tr); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
		}
	}()
	return pr
}
