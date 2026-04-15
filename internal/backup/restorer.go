package backup

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
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

	// tempDir is the directory that holds every on-disk section tar.
	// Removed by Close().
	tempDir string

	// per-section path maps. nil-or-missing = section absent.
	workspacePathBySlug map[string]string
	volumePathsBySlug   map[string]map[string]string // crew → volume name → path
	memoryPathBySlug    map[string]string
}

// Close removes the temp directory and every temp file backing
// workspace / volume / memory sections. Safe to call multiple times.
// Returns the first removal error encountered, if any; the rest are
// best-effort swept.
func (p *ExtractedPayload) Close() error {
	if p == nil || p.tempDir == "" {
		return nil
	}
	err := os.RemoveAll(p.tempDir)
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
func (p *ExtractedPayload) OpenWorkspace(slug string) (io.ReadCloser, bool, error) {
	path, ok := p.workspacePathBySlug[slug]
	if !ok {
		return nil, false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, fmt.Errorf("backup: open workspace section %s: %w", slug, err)
	}
	return f, true, nil
}

// OpenVolume returns a reader for one of a crew's named-volume tars.
// vol is "home" or "tools" per the collector's layout.
func (p *ExtractedPayload) OpenVolume(slug, vol string) (io.ReadCloser, bool, error) {
	bySlug, ok := p.volumePathsBySlug[slug]
	if !ok {
		return nil, false, nil
	}
	path, ok := bySlug[vol]
	if !ok {
		return nil, false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, fmt.Errorf("backup: open volume section %s/%s: %w", slug, vol, err)
	}
	return f, true, nil
}

// OpenMemory returns a reader for the /output (.memory/) tar of the
// given crew slug.
func (p *ExtractedPayload) OpenMemory(slug string) (io.ReadCloser, bool, error) {
	path, ok := p.memoryPathBySlug[slug]
	if !ok {
		return nil, false, nil
	}
	f, err := os.Open(path)
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
func ExtractPayload(payload io.Reader) (*ExtractedPayload, error) {
	tempDir, err := os.MkdirTemp("", "crewship-restore-*")
	if err != nil {
		return nil, fmt.Errorf("backup: temp dir: %w", err)
	}
	out := &ExtractedPayload{
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
		f, err := os.CreateTemp(tempDir, safe+"-*.tar")
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
		// or carries a symlink target would, when later handed to
		// docker CopyTo, write into unexpected parts of the container
		// rootfs. Docker enforces its own containment but we reject
		// the entry up front so the failure mode is "bad bundle", not
		// "unexpected file where it should not be".
		if strings.Contains(name, "..") {
			return nil, fmt.Errorf("%w: payload entry %q contains parent reference", ErrInvalidManifest, hdr.Name)
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			return nil, fmt.Errorf("%w: payload entry %q is a symlink", ErrInvalidManifest, hdr.Name)
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
		if err := s.file.Sync(); err != nil {
			_ = s.file.Close()
			return nil, fmt.Errorf("backup: sync inner tar %s: %w", key, err)
		}
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
// *os.File and its wrapping *tar.Writer.
type sink struct {
	file *os.File
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
	// Workspace bind.
	if r, ok, err := payload.OpenWorkspace(crewSlug); err != nil {
		return err
	} else if ok {
		err := ops.CopyTo(ctx, containerID, ContainerWorkspacePath, r)
		_ = r.Close()
		if err != nil {
			return fmt.Errorf("backup: restore workspace %s: %w", crewSlug, err)
		}
	}
	// Named volumes.
	for _, pair := range []struct{ vol, dest string }{
		{"home", ContainerHomePath},
		{"tools", ContainerToolsPath},
	} {
		r, ok, err := payload.OpenVolume(crewSlug, pair.vol)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		err = ops.CopyTo(ctx, containerID, pair.dest, r)
		_ = r.Close()
		if err != nil {
			return fmt.Errorf("backup: restore %s %s: %w", pair.vol, crewSlug, err)
		}
	}
	// Memory / output.
	if r, ok, err := payload.OpenMemory(crewSlug); err != nil {
		return err
	} else if ok {
		err := ops.CopyTo(ctx, containerID, ContainerMemoryPath, r)
		_ = r.Close()
		if err != nil {
			return fmt.Errorf("backup: restore memory %s: %w", crewSlug, err)
		}
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
