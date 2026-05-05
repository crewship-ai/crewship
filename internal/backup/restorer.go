package backup

import (
	"archive/tar"
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
	// systemPathsBySlug holds /var/lib (and any future rootfs sections
	// added under "system/") keyed by sub-section name. Separate from
	// volumePathsBySlug so a future migration that drops a real named
	// volume can't accidentally collide with a system section name.
	systemPathsBySlug map[string]map[string]string // crew → kind ("var-lib") → path
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
	p.systemPathsBySlug = nil
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

// OpenSystem returns a reader for one of a crew's system-rootfs tars
// (currently only "var-lib"). Bundles produced by older collectors
// have no system/* section so the (false, nil) signal lets RestoreCrew
// silently skip without erroring.
func (p *ExtractedPayload) OpenSystem(ctx context.Context, slug, kind string) (io.ReadCloser, bool, error) {
	bySlug, ok := p.systemPathsBySlug[slug]
	if !ok {
		return nil, false, nil
	}
	path, ok := bySlug[kind]
	if !ok {
		return nil, false, nil
	}
	f, err := p.storageOrDefault().Open(ctx, path)
	if err != nil {
		return nil, true, fmt.Errorf("backup: open system section %s/%s: %w", slug, kind, err)
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
		systemPathsBySlug:   map[string]map[string]string{},
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
		// Hardlinks (TypeLink) and symlinks (TypeSymlink) get the same
		// validation: NUL-free target, not absolute, no ".." escape.
		// Docker's CopyTo bounds extraction to the dst container, so a
		// rogue link cannot reach the host — but a tampered bundle
		// could still smuggle a `/etc/shadow` or `../../etc/passwd`
		// link INTO the restored container's rootfs (especially the
		// uid-0 /var/lib path). Defence-in-depth: reject up front so
		// the failure mode is "bad bundle", not "unexpected file
		// inside the container".
		//
		// Legitimate `..`-bearing targets that crews actually ship
		// (mise / pyenv / npm dedup) live under paths the collector
		// already excludes (.local/share/mise/, .local/share/pnpm/,
		// node_modules/, etc.), so this check does not regress real
		// restores. If a future tool under a non-excluded path needs
		// a parent-relative link, the collector exclusion list — not
		// this safety check — is the right knob.
		if hdr.Typeflag == tar.TypeLink || hdr.Typeflag == tar.TypeSymlink {
			if strings.ContainsRune(hdr.Linkname, 0) {
				return nil, fmt.Errorf("%w: payload entry %q link target contains NUL", ErrInvalidManifest, hdr.Name)
			}
			clean := path.Clean(hdr.Linkname)
			if path.IsAbs(clean) {
				return nil, fmt.Errorf("%w: payload entry %q link target is absolute (%q)", ErrInvalidManifest, hdr.Name, clean)
			}
			if clean == ".." || strings.HasPrefix(clean, "../") {
				return nil, fmt.Errorf("%w: payload entry %q link target escapes via parent reference (%q)", ErrInvalidManifest, hdr.Name, clean)
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

		case strings.HasPrefix(name, "system/"):
			if err := repackIntoSink(tr, hdr, name, "system/", sinkFor); err != nil {
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
		case "system":
			if len(parts) < 3 {
				continue
			}
			slug, kind := parts[1], parts[2]
			bySlug, ok := out.systemPathsBySlug[slug]
			if !ok {
				bySlug = map[string]string{}
				out.systemPathsBySlug[slug] = bySlug
			}
			bySlug[kind] = name
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
	case "system/":
		slug, more, ok := splitFirst(rest)
		if !ok {
			return nil
		}
		kind, _, ok := splitFirst(more)
		if !ok {
			return nil
		}
		key = "system/" + slug + "/" + kind
		strip = slug + "/" + kind + "/"
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
	// Two restore strategies live in the inner switch below. Workspace
	// + memory go through the SDK's CopyTo with parent==/. The named
	// volumes and the system /var/lib path go through CopyToVolume /
	// CopyToSystem (exec + tar -x) because Docker's archive API
	// rejects writes whose dst is a named-volume mountpoint inside a
	// read-only rootfs path.
	type section struct {
		open   func() (io.ReadCloser, bool, error)
		dest   string // container absolute path of the section root
		name   string // human label for error messages
		asRoot bool   // exec the tar as uid 0 instead of the agent user
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
		{
			// /var/lib carries service data dirs (redis, postgresql, ...)
			// the agent populated at runtime. Bundles produced before the
			// system section was added simply have no entry under
			// system/<slug>/var-lib so OpenSystem returns (false, nil) and
			// this is a silent skip — full backwards compatibility.
			//
			// Must extract as uid 0: every parent dir under /var/lib is
			// root-owned, the agent user (1001) has no write bit, and
			// inner files (mysql/ibdata1, postgres data) are root-owned
			// reads from the bundle perspective. CopyToSystem handles the
			// uid switch via a separate exec session.
			open:   func() (io.ReadCloser, bool, error) { return payload.OpenSystem(ctx, crewSlug, "var-lib") },
			dest:   ContainerVarLibPath,
			name:   "var-lib",
			asRoot: true,
		},
	}
	// Per-section errors are collected so a hiccup on one section
	// (e.g. a leftover root-owned file blocking unlink in /home/agent)
	// doesn't prevent the others from being restored. The aggregated
	// error is returned at the end so the operator sees ALL the
	// failures, not just the first one.
	var sectionErrs []string
	for _, s := range sections {
		r, ok, err := s.open()
		if err != nil {
			// Aggregate Open* failures into the same partial-restore
			// path as CopyTo failures below — a corrupt temp section
			// for the home volume should not block restoring the
			// workspace + memory the operator probably cares about
			// more.
			sectionErrs = append(sectionErrs, fmt.Sprintf("%s: %v", s.name, err))
			continue
		}
		if !ok {
			continue
		}
		parent := path.Dir(s.dest)
		// Two restore strategies, picked by where the section lands:
		//
		//   parent == "/" (e.g. /workspace, /output): Docker's archive
		//     API works fine because the dst itself is a bind-mounted
		//     writable target, not a read-only rootfs path. Use the
		//     SDK's CopyTo directly — it's faster and doesn't require
		//     `tar` to be installed in the container.
		//
		//   parent != "/" (e.g. /home/agent, /opt/crew-tools): these
		//     are typically NAMED VOLUMES whose mountpoint sits inside
		//     a read-only rootfs path (/home, /opt). Docker's archive
		//     API rejects ANY CopyTo into them with "rootfs is marked
		//     read-only" (when dst=parent) or "Could not find the file"
		//     (when dst=mountpoint, because the API checks the rootfs
		//     view rather than the live mount table). Pipe the tar
		//     into `tar -x -C <dst>` over an exec session instead —
		//     that runs INSIDE the container, sees the live mounts,
		//     and lands files on the volume properly.
		if parent == "/" {
			err := ops.CopyTo(ctx, containerID, s.dest, r)
			_ = r.Close()
			if err != nil {
				sectionErrs = append(sectionErrs, fmt.Sprintf("%s: %v", s.name, err))
			}
			continue
		}
		if s.asRoot {
			err = ops.CopyToSystem(ctx, containerID, s.dest, r)
		} else {
			err = ops.CopyToVolume(ctx, containerID, s.dest, r)
		}
		_ = r.Close()
		if err != nil {
			sectionErrs = append(sectionErrs, fmt.Sprintf("%s: %v", s.name, err))
		}
	}
	if len(sectionErrs) > 0 {
		return fmt.Errorf("backup: restore crew %s — partial: %s", crewSlug, strings.Join(sectionErrs, "; "))
	}
	return nil
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
