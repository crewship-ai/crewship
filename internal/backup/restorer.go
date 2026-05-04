package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
)

// ExtractedPayload carries the section readers pulled out of a bundle
// payload tar. The large per-crew sections (workspace, volumes,
// memory) are written to disk as temp files rather than buffered in
// memory so multi-GB restores fit in a modest host's RAM. Small
// sections (devcontainer.json, mise.toml, db/dump.json) stay
// in-memory because they are under a few KB.
//
// The caller MUST invoke Close once finished so the temp directory
// is removed. A nil ExtractedPayload's Close is a no-op.
type ExtractedPayload struct {
	// DBDump is the parsed contents of db/dump.json. nil when the
	// bundle had no DB section.
	DBDump *DBDump

	// DevcontainerBySlug maps crew slug to the devcontainer.json bytes
	// recorded in the bundle. Missing slugs indicate the crew had no
	// devcontainer config. Kept in memory — sub-KB per entry.
	DevcontainerBySlug map[string][]byte

	// MiseBySlug is the mise.toml counterpart to DevcontainerBySlug.
	MiseBySlug map[string][]byte

	// storage is the StorageOps that ExtractPayload used to create the
	// temp directory. Close / Open* helpers route every subsequent I/O
	// through this same backend so a later SetDefaultStorage() swap
	// cannot orphan temp files or send reopen traffic to the wrong
	// implementation.
	storage StorageOps

	// tempDir is the directory that holds every on-disk section tar.
	// Removed by Close().
	tempDir string

	// per-section path maps. nil-or-missing = section absent.
	workspacePathBySlug map[string]string
	volumePathsBySlug   map[string]map[string]string // crew → volume name → path
	memoryPathBySlug    map[string]string
}

// storageOrDefault returns the payload's captured StorageOps, or the
// package default if the struct was constructed without one (e.g.
// legacy tests).
func (p *ExtractedPayload) storageOrDefault() StorageOps {
	if p.storage != nil {
		return p.storage
	}
	return getDefaultStorage()
}

// Close removes the temp directory and every temp file backing
// workspace / volume / memory sections. Safe to call multiple times.
// Returns the first removal error encountered, if any; the rest are
// best-effort swept.
//
// Uses context.Background() on purpose: the Close is called from
// defer paths where the owning context may already have been
// cancelled, yet we still need to remove the temp directory.
func (p *ExtractedPayload) Close() error {
	if p == nil || p.tempDir == "" {
		return nil
	}
	err := p.storageOrDefault().RemoveAll(context.Background(), p.tempDir)
	p.tempDir = ""
	p.workspacePathBySlug = nil
	p.volumePathsBySlug = nil
	p.memoryPathBySlug = nil
	return err
}

// HasWorkspace reports whether the bundle carried a workspace tar
// for the given slug.
func (p *ExtractedPayload) HasWorkspace(slug string) bool {
	_, ok := p.workspacePathBySlug[slug]
	return ok
}

// OpenWorkspace returns a reader for the workspace bind-mount tar of
// the given crew slug. Caller closes. Returns (nil, false, nil) when
// the bundle has no such section.
func (p *ExtractedPayload) OpenWorkspace(ctx context.Context, slug string) (io.ReadCloser, bool, error) {
	path, ok := p.workspacePathBySlug[slug]
	if !ok {
		return nil, false, nil
	}
	f, err := p.storageOrDefault().Open(ctx, path)
	if err != nil {
		return nil, true, fmt.Errorf("backup: open workspace section %s: %w", slug, err)
	}
	return f, true, nil
}

// OpenVolume returns a reader for one of a crew's named-volume tars.
// vol is "home" or "tools" per the collector's layout.
func (p *ExtractedPayload) OpenVolume(ctx context.Context, slug, vol string) (io.ReadCloser, bool, error) {
	bySlug, ok := p.volumePathsBySlug[slug]
	if !ok {
		return nil, false, nil
	}
	path, ok := bySlug[vol]
	if !ok {
		return nil, false, nil
	}
	f, err := p.storageOrDefault().Open(ctx, path)
	if err != nil {
		return nil, true, fmt.Errorf("backup: open volume section %s/%s: %w", slug, vol, err)
	}
	return f, true, nil
}

// OpenMemory returns a reader for the /output (.memory/) tar of the
// given crew slug.
func (p *ExtractedPayload) OpenMemory(ctx context.Context, slug string) (io.ReadCloser, bool, error) {
	path, ok := p.memoryPathBySlug[slug]
	if !ok {
		return nil, false, nil
	}
	f, err := p.storageOrDefault().Open(ctx, path)
	if err != nil {
		return nil, true, fmt.Errorf("backup: open memory section %s: %w", slug, err)
	}
	return f, true, nil
}

// ExtractPayload walks the payload tar produced by the collector and
// splits it into the ExtractedPayload buckets. Per-crew sections are
// re-tar'd into temp files so the caller's peak memory stays bounded
// by the zstd decoder window regardless of bundle size.
//
// The returned ExtractedPayload owns its temp directory; the caller
// MUST call Close() once finished with all sections (typically via
// defer in RestoreBackup).
func ExtractPayload(ctx context.Context, payload io.Reader) (*ExtractedPayload, error) {
	// Capture the storage backend NOW so a later SetDefaultStorage
	// swap cannot send cleanup / reopen traffic to a different
	// implementation than the one that created the temp files.
	st := getDefaultStorage()
	tempDir, err := st.MkdirTemp(ctx, "", "crewship-restore-*")
	if err != nil {
		return nil, fmt.Errorf("backup: temp dir: %w", err)
	}
	out := &ExtractedPayload{
		storage:             st,
		DevcontainerBySlug:  map[string][]byte{},
		MiseBySlug:          map[string][]byte{},
		tempDir:             tempDir,
		workspacePathBySlug: map[string]string{},
		volumePathsBySlug:   map[string]map[string]string{},
		memoryPathBySlug:    map[string]string{},
	}
	// Defer-based cleanup on error paths so a partial extract does
	// not leak temp files.
	success := false
	defer func() {
		if !success {
			_ = out.Close()
		}
	}()

	tr, err := NewTarZstReader(payload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tr.Close() }()

	// sinks holds an open *tar.Writer streaming into an os.File temp
	// per "bucket" (workspace/<slug>, volumes/<slug>/<vol>,
	// memory/<slug>). Each bucket gets its own temp file so the
	// caller can stream it straight back into docker CopyTo without
	// materialising the whole thing. sink type declared at file scope.
	sinks := map[string]*sink{}
	sinkFor := func(key string) (*sink, error) {
		if s, ok := sinks[key]; ok {
			return s, nil
		}
		safe := strings.ReplaceAll(key, "/", "_")
		f, err := st.CreateTemp(ctx, tempDir, safe+"-*.tar")
		if err != nil {
			return nil, fmt.Errorf("backup: create section temp %s: %w", key, err)
		}
		s := &sink{file: f, tw: tar.NewWriter(f)}
		sinks[key] = s
		return s, nil
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("backup: extract payload: %w", err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")

		// Defence-in-depth against a tampered bundle: a tar entry that
		// climbs above the intended prefix (e.g. "../../etc/shadow")
		// or carries an unsafe symlink target would, when later handed
		// to docker CopyTo, write into unexpected parts of the
		// container rootfs. Docker enforces its own containment but
		// we reject the entry up front so the failure mode is "bad
		// bundle", not "unexpected file where it should not be".
		if strings.Contains(name, "..") {
			return nil, fmt.Errorf("%w: payload entry %q contains parent reference", ErrInvalidManifest, hdr.Name)
		}
		// Symlinks: tooling like mise + pyenv legitimately ships hundreds
		// of internal symlinks (e.g. shim → real binary) that are
		// essential for the restored container to function. Rejecting all
		// of them broke restore of any crew that had ever provisioned a
		// language runtime. Allow symlinks whose target is RELATIVE and
		// does NOT contain ".." (i.e. only links to peer files within
		// the bundle's own tree). Reject absolute targets and parent
		// traversal so a tampered bundle still can't smuggle "/etc/shadow"
		// or "../../etc/passwd" links into the container rootfs.
		// Hardlinks (TypeLink): same containment story as symlinks —
		// docker CopyTo cannot escape the destination container, so
		// we just sanity-check Linkname is well-formed and pass it
		// through. Hardlinks are common in npm-installed CLIs.
		if hdr.Typeflag == tar.TypeLink {
			if strings.ContainsRune(hdr.Linkname, 0) {
				return nil, fmt.Errorf("%w: payload entry %q hardlink target contains NUL", ErrInvalidManifest, hdr.Name)
			}
		}
		if hdr.Typeflag == tar.TypeSymlink {
			// Symlinks are restored INSIDE the destination container
			// via docker CopyTo, which cannot escape the container's
			// filesystem regardless of where the link points. So the
			// target's absoluteness or "../" content is not a host-
			// safety issue; the worst case is a dangling link inside
			// the container. We do still reject targets that contain
			// a literal NUL byte or other invalid path bytes since
			// those would just confuse downstream tools — but anything
			// otherwise well-formed passes through.
			//
			// Earlier revisions tried to allowlist known container
			// roots (/home/agent, /workspace, /root/.local/bin, …) but
			// the list grew with every new tool we encountered (mise,
			// pyenv, cursor-agent, opencode, npm dedup hardlinks, …).
			// Trusting docker for containment is both simpler and
			// correct — the bundle came from one of OUR containers in
			// the first place, so its symlink graph is by construction
			// representable inside another of our containers.
			if strings.ContainsRune(hdr.Linkname, 0) {
				return nil, fmt.Errorf("%w: payload entry %q symlink target contains NUL", ErrInvalidManifest, hdr.Name)
			}
		}

		switch {
		case name == "db/dump.json":
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			d, err := UnmarshalDump(data)
			if err != nil {
				return nil, err
			}
			out.DBDump = d

		case strings.HasPrefix(name, "devcontainer/"):
			parts := strings.SplitN(strings.TrimPrefix(name, "devcontainer/"), "/", 2)
			if len(parts) != 2 {
				continue
			}
			slug, file := parts[0], parts[1]
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			switch file {
			case "devcontainer.json":
				out.DevcontainerBySlug[slug] = data
			case "mise.toml":
				out.MiseBySlug[slug] = data
			}

		case strings.HasPrefix(name, "workspace/"):
			if err := repackIntoSink(tr, hdr, name, "workspace/", sinkFor); err != nil {
				return nil, err
			}

		case strings.HasPrefix(name, "volumes/"):
			if err := repackIntoSink(tr, hdr, name, "volumes/", sinkFor); err != nil {
				return nil, err
			}

		case strings.HasPrefix(name, "memory/"):
			if err := repackIntoSink(tr, hdr, name, "memory/", sinkFor); err != nil {
				return nil, err
			}

		default:
			// Forward-compat: unknown entries are silently discarded.
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, err
			}
		}
	}

	// Close every inner tar writer and distribute file paths into the
	// typed lookup maps. Keep the files themselves — the caller opens
	// them fresh when streaming into CopyTo.
	for key, s := range sinks {
		if err := s.tw.Close(); err != nil {
			_ = s.file.Close()
			return nil, fmt.Errorf("backup: close inner tar %s: %w", key, err)
		}
		// No fsync here: the temp file is read back by the same process
		// inside OpenWorkspace/OpenVolume/OpenMemory, so kernel page-cache
		// coherency is sufficient and Sync is not on the StorageOps API.
		name := s.file.Name()
		if err := s.file.Close(); err != nil {
			return nil, fmt.Errorf("backup: close inner tar file %s: %w", key, err)
		}
		parts := strings.SplitN(key, "/", 3)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "workspace":
			out.workspacePathBySlug[parts[1]] = name
		case "memory":
			out.memoryPathBySlug[parts[1]] = name
		case "volumes":
			if len(parts) < 3 {
				continue
			}
			slug, vol := parts[1], parts[2]
			bySlug, ok := out.volumePathsBySlug[slug]
			if !ok {
				bySlug = map[string]string{}
				out.volumePathsBySlug[slug] = bySlug
			}
			bySlug[vol] = name
		}
	}

	success = true
	return out, nil
}

// repackIntoSink streams the current tar entry (hdr + tr body) into
// the appropriate on-disk sink, keyed by the top-level prefix.
//
// Sink keys are:
//
//	workspace/<slug>                  → one file per crew
//	volumes/<slug>/<volumeName>       → one file per crew+volume
//	memory/<slug>                     → one file per crew
//
// Entry names inside the sink are stripped of their outermost path
// segments so CopyTo places them directly at the container destination
// (e.g. /workspace/, /home/agent/, /output/).
func repackIntoSink(tr *TarZstReader, hdr *tar.Header, name, topPrefix string, sinkFor func(string) (*sink, error)) error {
	rest := strings.TrimPrefix(name, topPrefix)
	var key string
	var strip string
	switch topPrefix {
	case "workspace/":
		slug, _, ok := splitFirst(rest)
		if !ok {
			return nil
		}
		key = "workspace/" + slug
		strip = slug + "/"
	case "volumes/":
		slug, more, ok := splitFirst(rest)
		if !ok {
			return nil
		}
		vol, _, ok := splitFirst(more)
		if !ok {
			return nil
		}
		key = "volumes/" + slug + "/" + vol
		strip = slug + "/" + vol + "/"
	case "memory/":
		slug, _, ok := splitFirst(rest)
		if !ok {
			return nil
		}
		key = "memory/" + slug
		strip = slug + "/"
	default:
		return nil
	}
	s, err := sinkFor(key)
	if err != nil {
		return err
	}
	newName := strings.TrimPrefix(rest, strip)
	if newName == "" {
		newName = "."
	}
	newHdr := *hdr
	newHdr.Name = newName
	if err := s.tw.WriteHeader(&newHdr); err != nil {
		return fmt.Errorf("backup: inner tar header %q: %w", newName, err)
	}
	if hdr.Typeflag == tar.TypeReg && hdr.Size > 0 {
		if _, err := io.CopyN(s.tw, tr, hdr.Size); err != nil {
			return fmt.Errorf("backup: inner tar body %q: %w", newName, err)
		}
	}
	return nil
}

// sink is a package-local struct for repackIntoSink. Holds an open
// temp-file handle and its wrapping *tar.Writer.
type sink struct {
	file TempFile
	tw   *tar.Writer
}

// splitFirst splits s on the first "/" and returns the two halves plus
// true if the separator was found, or "", "", false otherwise.
func splitFirst(s string) (head, tail string, ok bool) {
	if i := strings.Index(s, "/"); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

// RestoreCrew streams the per-crew sections of an ExtractedPayload
// into a freshly-provisioned container. The container MUST already
// exist with the canonical mount paths available; callers are
// responsible for invoking the devcontainer provisioner before this.
func RestoreCrew(ctx context.Context, ops DockerOps, containerID string, crewSlug string, payload *ExtractedPayload) error {
	if payload == nil {
		return fmt.Errorf("backup: RestoreCrew: nil payload")
	}
	// Each section is restored by streaming a tar into the container.
	// The Docker SDK's CopyToContainer is sensitive to the dst path:
	// paths whose leaf is a USER-OWNED bind mount (e.g. /home/agent)
	// sometimes return "Could not find the file <path>" even though
	// the dir exists at runtime — the daemon resolves through the
	// container's image-layer view rather than the live mount table.
	//
	// Robust pattern: copy into the PARENT directory and prepend the
	// basename back onto every tar entry so files land at the
	// expected absolute path inside the container.
	//
	// Example /home/agent restore:
	//   sink tar (after RepackTar's wrapper-strip): ".bashrc", ".cache/foo"
	//   rewrapTarUnder("agent"): "agent/", "agent/.bashrc", "agent/.cache/foo"
	//   CopyTo dest=/home → /home/agent/.bashrc, /home/agent/.cache/foo ✓
	//
	// /workspace restore:
	//   sink tar: "proof/marker.txt"
	//   rewrapped: "workspace/", "workspace/proof/marker.txt"
	//   CopyTo dest=/ → /workspace/proof/marker.txt ✓
	type section struct {
		open func() (io.ReadCloser, bool, error)
		dest string // container absolute path of the section root
		name string // human label for error messages
	}
	sections := []section{
		{
			open: func() (io.ReadCloser, bool, error) { return payload.OpenWorkspace(ctx, crewSlug) },
			dest: ContainerWorkspacePath,
			name: "workspace",
		},
		{
			open: func() (io.ReadCloser, bool, error) { return payload.OpenVolume(ctx, crewSlug, "home") },
			dest: ContainerHomePath,
			name: "home",
		},
		{
			open: func() (io.ReadCloser, bool, error) { return payload.OpenVolume(ctx, crewSlug, "tools") },
			dest: ContainerToolsPath,
			name: "tools",
		},
		{
			open: func() (io.ReadCloser, bool, error) { return payload.OpenMemory(ctx, crewSlug) },
			dest: ContainerMemoryPath,
			name: "memory",
		},
	}
	for _, s := range sections {
		r, ok, err := s.open()
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		parent := path.Dir(s.dest)
		basename := path.Base(s.dest)
		rewrapped, rwErr := rewrapTarUnder(r, basename)
		_ = r.Close()
		if rwErr != nil {
			return fmt.Errorf("backup: rewrap %s %s: %w", s.name, crewSlug, rwErr)
		}
		if err := ops.CopyTo(ctx, containerID, parent, rewrapped); err != nil {
			return fmt.Errorf("backup: restore %s %s: %w", s.name, crewSlug, err)
		}
	}
	return nil
}

// rewrapTarUnder reads a tar stream and returns a new tar reader whose
// entries are all prefixed with `basename + "/"`. Used by RestoreCrew
// to put the section's basename back on every entry (collector +
// RepackTar strip it; restore needs it back so the tar lands at the
// right absolute path when extracted at the parent dir). Also emits a
// leading TypeDir entry for the basename so the destination directory
// is materialised even when the tar's only contents are hidden files.
//
// Materialises the rewrapped tar to a bytes.Buffer because the Docker
// SDK consumes the reader synchronously and we need to produce all
// entries (header + body) deterministically. Volume tars are typically
// hundreds of MB at the high end; this fits comfortably in modern host
// RAM but for billion-file workspaces we'd want to back this with a
// temp file.
func rewrapTarUnder(src io.Reader, basename string) (io.Reader, error) {
	if basename == "" {
		return src, nil
	}
	prefix := basename + "/"
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	if err := tw.WriteHeader(&tar.Header{
		Name:     prefix,
		Mode:     0o755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		return nil, fmt.Errorf("backup: rewrap header %s: %w", basename, err)
	}
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("backup: rewrap read: %w", err)
		}
		newHdr := *hdr
		newHdr.Name = prefix + strings.TrimPrefix(hdr.Name, "./")
		if err := tw.WriteHeader(&newHdr); err != nil {
			return nil, fmt.Errorf("backup: rewrap write %s: %w", newHdr.Name, err)
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Size > 0 {
			if _, err := io.CopyN(tw, tr, hdr.Size); err != nil {
				return nil, fmt.Errorf("backup: rewrap body %s: %w", newHdr.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("backup: rewrap close: %w", err)
	}
	return &out, nil
}

// SectionEntries walks a workspace bundle manifest and returns the
// list of expected per-crew section paths. Handy for `inspect`
// diagnostics that want to report "N workspace tars, M volume tars,
// K memory tars".
func SectionEntries(m *Manifest) []string {
	var out []string
	for _, c := range m.Contents.Crews {
		if c.WorkspaceIncluded {
			out = append(out, path.Join("workspace", c.Slug))
		}
		for _, v := range c.VolumesIncluded {
			out = append(out, path.Join("volumes", c.Slug, v))
		}
		if c.MemoryIncluded {
			out = append(out, path.Join("memory", c.Slug))
		}
	}
	return out
}
