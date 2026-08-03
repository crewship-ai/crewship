package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/memory"
	"io"
	"path"
	"strconv"
	"strings"
)

// symlinkSectionIsStrict reports whether a payload entry's section
// rejects `..` and absolute symlink targets. workspace/ and volumes/
// hold user code and dotfiles — both routinely contain parent-relative
// symlinks (node_modules dedup, mise / pnpm / pyenv) that the
// container's filesystem containment already bounds. memory/ and
// system/ stay strict because nothing legitimate writes parent-ref
// symlinks there.
//
// Names without a known section prefix default to STRICT so an
// unrecognised future section cannot be used to smuggle escaping
// links by mistake — which is what keeps the crew/ section (the memory
// tree, added for #1713) strict without needing a case of its own.
func symlinkSectionIsStrict(name string) bool {
	switch {
	case strings.HasPrefix(name, "workspace/"),
		strings.HasPrefix(name, "volumes/"):
		return false
	default:
		return true
	}
}

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
	// crewPathBySlug holds the /crew section — the real agent and
	// crew-shared memory tree (#1713). memoryPathBySlug, despite the
	// name, is the /output section; the names are kept as-is because
	// they are the on-disk bundle section names and renaming them would
	// silently orphan the sections in every existing bundle.
	crewPathBySlug   map[string]string
	memoryPathBySlug map[string]string
	// systemPathsBySlug holds /var/lib (and any future rootfs sections
	// added under "system/") keyed by sub-section name. Separate from
	// volumePathsBySlug so a future migration that drops a real named
	// volume can't accidentally collide with a system section name.
	systemPathsBySlug map[string]map[string]string // crew → kind ("var-lib") → path

	// memoryBlobsPath is the temp tar holding the memory-blobs/
	// section (content-addressed memory_versions blobs). Unlike the
	// per-crew sections above this one is workspace-scoped, not keyed
	// by slug — memory_versions has no crew_id. Empty when the bundle
	// carried no such section (older bundles, or BlobRoot unset at
	// backup time).
	memoryBlobsPath string
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
	p.crewPathBySlug = nil
	p.memoryPathBySlug = nil
	p.systemPathsBySlug = nil
	p.memoryBlobsPath = ""
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

// HasCrew reports whether the bundle carried the /crew section — the
// agent and crew-shared memory tree — for the given slug. False for
// every bundle produced before #1713 was fixed, which is the signal
// callers use to tell the operator that a restore from it will not
// bring memory back.
func (p *ExtractedPayload) HasCrew(slug string) bool {
	_, ok := p.crewPathBySlug[slug]
	return ok
}

// OpenCrew returns a reader for the /crew tar of the given crew slug.
// Returns (nil, false, nil) for bundles that predate the section.
func (p *ExtractedPayload) OpenCrew(ctx context.Context, slug string) (io.ReadCloser, bool, error) {
	path, ok := p.crewPathBySlug[slug]
	if !ok {
		return nil, false, nil
	}
	f, err := p.storageOrDefault().Open(ctx, path)
	if err != nil {
		return nil, true, fmt.Errorf("backup: open crew section %s: %w", slug, err)
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

// HasMemoryBlobs reports whether the bundle carried a memory-blobs
// section (content-addressed memory_versions blobs).
func (p *ExtractedPayload) HasMemoryBlobs() bool {
	return p.memoryBlobsPath != ""
}

// OpenMemoryBlobs returns a reader over the inner tar of memory-version
// blobs collected by WriteMemoryBlobsSection, named "<sha[:2]>/<sha>"
// (the memory-blobs/ prefix is stripped, matching how OpenWorkspace /
// OpenMemory strip their own crew-slug prefix). Caller closes. Returns
// (nil, false, nil) when the bundle has no such section — older
// bundles, or BlobRoot unset at backup time.
func (p *ExtractedPayload) OpenMemoryBlobs(ctx context.Context) (io.ReadCloser, bool, error) {
	if p.memoryBlobsPath == "" {
		return nil, false, nil
	}
	f, err := p.storageOrDefault().Open(ctx, p.memoryBlobsPath)
	if err != nil {
		return nil, true, fmt.Errorf("backup: open memory-blobs section: %w", err)
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
		crewPathBySlug:      map[string]string{},
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
		// Hardlinks (TypeLink) and symlinks (TypeSymlink) get a NUL-free
		// check unconditionally, then section-aware policy on the
		// target path.
		//
		// Docker's CopyTo bounds extraction to the dst container, so a
		// rogue link cannot reach the host — but a tampered bundle
		// could still smuggle a `/etc/shadow` or `../../etc/passwd`
		// link INTO the restored container's rootfs (especially the
		// uid-0 /var/lib path).
		//
		// Section policy:
		//
		//   workspace/  — user code section. Legitimate node_modules,
		//                 mise, pnpm, pyenv all use `..` parent
		//                 references for symlink dedup. Allow `..`
		//                 and absolute targets; Docker's CopyTo
		//                 containment is the security boundary.
		//   volumes/    — user dotfiles + tooling. Same as workspace.
		//   memory/     — agent-written markdown. Strict (memory should
		//                 never legitimately contain `..` symlinks).
		//   system/     — /var/lib service data. Strict — anything
		//                 escaping a service's data dir is suspicious.
		//
		// Pre-fix this check was unconditional and broke restores for
		// any workspace containing a Node.js/Python repo (bug #1).
		if hdr.Typeflag == tar.TypeLink || hdr.Typeflag == tar.TypeSymlink {
			if strings.ContainsRune(hdr.Linkname, 0) {
				return nil, fmt.Errorf("%w: payload entry %q link target contains NUL", ErrInvalidManifest, hdr.Name)
			}
			clean := path.Clean(hdr.Linkname)
			if symlinkSectionIsStrict(name) {
				if path.IsAbs(clean) {
					return nil, fmt.Errorf("%w: payload entry %q link target is absolute (%q)", ErrInvalidManifest, hdr.Name, clean)
				}
				if clean == ".." || strings.HasPrefix(clean, "../") {
					return nil, fmt.Errorf("%w: payload entry %q link target escapes via parent reference (%q)", ErrInvalidManifest, hdr.Name, clean)
				}
			}
		}

		switch {
		case name == "db/dump.json":
			// PR #493 follow-up: bound at maxBackupDBDumpBytes so an
			// attacker-claimed hdr.Size of 10 GB can't OOM the restorer
			// before UnmarshalDump even runs.
			data, err := io.ReadAll(io.LimitReader(tr, maxBackupDBDumpBytes))
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
			// PR #493 follow-up: devcontainer + mise blobs are < few KB;
			// bound the read at 5 MB to deny tar-bomb allocations.
			data, err := io.ReadAll(io.LimitReader(tr, maxBackupDevcontainerEntryBytes))
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

		case strings.HasPrefix(name, "crew/"):
			if err := repackIntoSink(tr, hdr, name, "crew/", sinkFor); err != nil {
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

		case strings.HasPrefix(name, memoryBlobsSectionPrefix):
			if err := repackIntoSink(tr, hdr, name, memoryBlobsSectionPrefix, sinkFor); err != nil {
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
		if key == memoryBlobsSinkKey {
			out.memoryBlobsPath = name
			continue
		}
		parts := strings.SplitN(key, "/", 3)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "workspace":
			out.workspacePathBySlug[parts[1]] = name
		case "crew":
			out.crewPathBySlug[parts[1]] = name
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
//	system/<slug>/<kind>              → one file per crew+kind
//	memory-blobs                      → single bundle-wide sink (no
//	                                     crew slug — memory_versions is
//	                                     workspace-scoped)
//
// Entry names inside the per-crew sinks are stripped of their outermost
// path segments so CopyTo places them directly at the container
// destination (e.g. /workspace/, /home/agent/, /output/). The
// memory-blobs sink keeps its entries as "<sha[:2]>/<sha>" — there is
// no crew-slug segment to strip, and RestoreMemoryBlobs wants that
// shape to reconstruct the destination path.
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
	case "crew/":
		slug, _, ok := splitFirst(rest)
		if !ok {
			return nil
		}
		key = "crew/" + slug
		strip = slug + "/"
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
	case memoryBlobsSectionPrefix:
		// No crew slug — memory_versions is workspace-scoped. rest is
		// already "<sha[:2]>/<sha>", so it rides straight through as
		// both the sink key and the inner tar entry name.
		key = memoryBlobsSinkKey
		strip = ""
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

// agentUser / sidecarWriterUser / rootUser are the exec identities the
// restore runs under.
//
// sidecarWriterUser is uid 1001 with PRIMARY group 1002 — the same
// identity the orchestrator's prepMemoryDirs uses, and the only one that
// can re-establish the memory tree's group ownership. Crew containers run
// CapDrop: ALL, so nothing inside them has CAP_CHOWN; what POSIX still
// permits is an owner changing a file's group to a group it is itself a
// member of, which is exactly this.
const (
	agentUser         = "1001:1001"
	sidecarWriterUser = "1001:1002"
	rootUser          = "0:0"
)

// restoreSection is one destination inside the container, its source in
// the bundle, and the policy for landing it.
type restoreSection struct {
	open func() (io.ReadCloser, bool, error)
	spec ExtractSpec
	name string
}

// crewRestoreSections is the section table, shared by the preflight and
// the apply pass so the two can never drift — a preflight that checks a
// different set of destinations than the writer writes is worse than no
// preflight.
func crewRestoreSections(ctx context.Context, crewSlug string, payload *ExtractedPayload) []restoreSection {
	return []restoreSection{
		{
			open: func() (io.ReadCloser, bool, error) { return payload.OpenWorkspace(ctx, crewSlug) },
			name: "workspace",
			spec: ExtractSpec{Dest: ContainerWorkspacePath, User: agentUser, PreserveTimes: true},
		},
		{
			// The crew memory tree. PreserveModes is not cosmetic here:
			// the .memory directories are setgid 2775 so that everything
			// the agent creates later inherits group 1002 and stays
			// readable by the memory sidecar. A restore that drops the
			// bit leaves memory that works today and silently stops
			// being shared tomorrow.
			open: func() (io.ReadCloser, bool, error) { return payload.OpenCrew(ctx, crewSlug) },
			name: "crew-memory",
			spec: ExtractSpec{Dest: ContainerCrewPath, User: agentUser, PreserveModes: true, PreserveTimes: true},
		},
		{
			open: func() (io.ReadCloser, bool, error) { return payload.OpenMemory(ctx, crewSlug) },
			name: "output",
			spec: ExtractSpec{Dest: ContainerOutputPath, User: agentUser, PreserveTimes: true},
		},
		{
			open: func() (io.ReadCloser, bool, error) { return payload.OpenVolume(ctx, crewSlug, "home") },
			name: "home",
			spec: ExtractSpec{Dest: ContainerHomePath, User: agentUser},
		},
		{
			open: func() (io.ReadCloser, bool, error) { return payload.OpenVolume(ctx, crewSlug, "tools") },
			name: "tools",
			spec: ExtractSpec{Dest: ContainerToolsPath, User: agentUser},
		},
		{
			// /var/lib carries service data dirs (redis, postgresql, ...)
			// the agent populated at runtime. Bundles produced before the
			// system section was added simply have no entry under
			// system/<slug>/var-lib so OpenSystem returns (false, nil) and
			// this is a silent skip — full backwards compatibility.
			//
			// Must extract as uid 0: every parent dir under /var/lib is
			// root-owned and the agent user has no write bit there. Root
			// needs no capability for this — it owns the directories.
			open: func() (io.ReadCloser, bool, error) { return payload.OpenSystem(ctx, crewSlug, "var-lib") },
			name: "var-lib",
			spec: ExtractSpec{Dest: ContainerVarLibPath, User: rootUser},
		},
	}
}

// ErrRestorePreflight is returned when a restore is refused before any
// bytes are written. Callers can distinguish it from a mid-flight
// failure: after ErrRestorePreflight the target is untouched.
var ErrRestorePreflight = errors.New("backup: restore preflight failed")

// ErrMemoryPermsDegraded reports that a crew's data restored correctly
// but the memory tree's group/setgid contract could not be fully
// re-applied. Callers must NOT treat it as a failed restore: every
// section landed, and re-running changes nothing because the entry that
// blocked the chgrp will block it again. It exists so the loss is loud
// without being fatal — crew-shared memory that silently stops being
// shared is the failure this whole change is about, and so is a restore
// that reports failure over data it successfully wrote.
var ErrMemoryPermsDegraded = errors.New("backup: crew memory permissions degraded")

// agentGID / sidecarGID mirror the runtime's group identities. Only the
// sidecar group is referenced here; the agent's own group comes from the
// exec identity.
const sidecarGID = 1002

// RestoreCrew streams the per-crew sections of an ExtractedPayload
// into a freshly-provisioned container. The container MUST already
// exist AND BE RUNNING with the canonical mount paths available;
// callers are responsible for invoking the devcontainer provisioner
// before this.
//
// # Atomicity
//
// #1715's complaint was not the HTTP 500 it produced — it was that the
// 500 arrived after the workspace and memory sections were already on
// disk, so a failed restore left a crew half-overwritten with no way to
// tell which half. The section loop below is now preceded by a preflight
// that probes EVERY destination this call will write, as the identity
// that will write it, and refuses the whole restore if any one of them
// is not writable. The common failure — a target whose named volumes are
// still root-owned — is therefore caught with nothing written.
//
// This is preflight-atomic, not transactional: a failure after the
// preflight passes (device full, daemon restart mid-stream) can still
// leave a partially written crew. Restore is idempotent, so the recovery
// for that case is to re-run it. Rolling back instead would mean staging
// a full copy of every section inside the container, which for a
// multi-GB workspace costs more disk than the crew has and would itself
// fail in the case it exists to protect.
func RestoreCrew(ctx context.Context, ops DockerOps, containerID string, crewSlug string, payload *ExtractedPayload) error {
	if payload == nil {
		return fmt.Errorf("backup: RestoreCrew: nil payload")
	}
	sections := crewRestoreSections(ctx, crewSlug, payload)

	// Preflight. Every section that the bundle actually carries gets its
	// destination probed for existence and writability under the exact
	// exec identity the apply pass will use. Nothing is written yet.
	var preflightErrs []string
	// Hand the memory tree back to the agent before anything looks at
	// whether it is writable.
	//
	// The sidecar writes an agent's memory as uid 1002 — deliberately,
	// so the agent process cannot read its /proc/<pid>/mem — at mode
	// 0644. Extraction runs as the agent, 1001, which then cannot
	// replace those files, and the preflight below correctly refused
	// the whole restore. The effect was that a crew became
	// unrestorable the moment its agents used the feature memory
	// exists for (#1746).
	//
	// The sweep is the tree's own contract (internal/memory), the same
	// one the docker provider applies at container start: owner 1001,
	// group 1002, group-writable. Afterwards the agent can extract and
	// the sidecar keeps write through the group.
	//
	// Failure is deliberately not fatal. If the reclaim could not run
	// and it mattered, the preflight refuses with the message it
	// already has; if it did not matter, a restore should not be
	// blocked by a sweep it never needed.
	reclaimCrewMemoryOwnership(ctx, ops, containerID)

	present := map[string]bool{}
	for _, s := range sections {
		r, ok, err := s.open()
		if err != nil {
			preflightErrs = append(preflightErrs, fmt.Sprintf("%s: %v", s.name, err))
			continue
		}
		if !ok {
			continue
		}
		// Read the section's directory set while it is open — the
		// preflight's sweep needs to know where this section writes so
		// it can tell an unwritable directory that matters from one that
		// does not.
		writesInto, writesPaths, derr := sectionWriteDirs(r)
		_ = r.Close()
		if derr != nil {
			preflightErrs = append(preflightErrs, fmt.Sprintf("%s: reading section layout: %v", s.name, derr))
			continue
		}
		present[s.name] = true
		if err := probeWritable(ctx, ops, containerID, crewSlug, s.spec, writesInto, writesPaths); err != nil {
			preflightErrs = append(preflightErrs, fmt.Sprintf("%s: %v", s.name, err))
		}
	}
	if len(preflightErrs) > 0 {
		return fmt.Errorf("%w for crew %s (nothing was written): %s",
			ErrRestorePreflight, crewSlug, strings.Join(preflightErrs, "; "))
	}

	// Apply. Per-section errors are still aggregated so the operator sees
	// every failure rather than only the first, but reaching here means
	// each destination was writable moments ago.
	var sectionErrs []string
	for _, s := range sections {
		if !present[s.name] {
			continue
		}
		r, ok, err := s.open()
		if err != nil {
			sectionErrs = append(sectionErrs, fmt.Sprintf("%s: %v", s.name, err))
			continue
		}
		if !ok {
			continue
		}
		err = ops.CopyToPath(ctx, containerID, s.spec, r)
		_ = r.Close()
		if err != nil {
			sectionErrs = append(sectionErrs, fmt.Sprintf("%s: %v", s.name, err))
		}
	}
	if len(sectionErrs) > 0 {
		return fmt.Errorf("backup: restore crew %s — partial: %s", crewSlug, strings.Join(sectionErrs, "; "))
	}

	if present["crew-memory"] {
		if err := reapplyMemoryPerms(ctx, ops, containerID); err != nil {
			// Wrapped with %w either way, so ErrMemoryPermsDegraded
			// travels to the caller, which is where the decision belongs:
			// every section above has landed by now, and only the caller
			// knows whether it is about to roll a DB transaction back
			// over data that is already on disk. RestoreBackup treats
			// the sentinel as a loud warning and keeps the restore.
			//
			// Deliberately NOT branching here to vary the message: a
			// branch that only changes a prefix is a branch no test can
			// hold, and it survived a mutation saying so.
			return fmt.Errorf("backup: restore crew %s: %w", crewSlug, err)
		}
	}
	return nil
}

// probeWritable checks that spec.Dest exists and is writable by
// spec.User, without writing anything the restore would not have
// written anyway. The probe file is created and removed inside the
// destination, which is the only way to answer the question the kernel
// will actually be asked: `test -w` reports the DAC bits, and a
// root-owned 0755 directory is reported writable to a root probe while
// the agent tar that follows gets EACCES on its first entry.
//
// It also doubles as the "is the container running" check — a stopped
// container cannot exec, and every restore path now needs exec.
// It probes in two steps, because the destination ROOT being writable is
// not the condition that matters. #1715's shape is an agent-owned volume
// that contains root-owned entries left by a root postCreate step:
// `touch` in the root succeeds, every section passes, and the apply loop
// writes workspace, then crew-memory, then output, and only fails on
// home — fourth — leaving exactly the half-overwritten crew the atomicity
// guarantee promises cannot happen.
//
// Step 2 therefore sweeps for a DIRECTORY under the destination that the
// exec identity cannot write. With UnlinkFirst on the agent sections, a
// root-owned FILE is no longer blocking (unlinking it needs write on its
// parent, which the agent has), so a non-writable directory is precisely
// what remains able to fail mid-apply.
//
// The sweep is EXACT, not broad, and that distinction is load-bearing.
// A real /home/agent legitimately contains root-owned directories left
// by feature installs; refusing over every one of them would turn a
// guarantee about not half-writing into a restore that never runs. So
// the container reports which directories are unwritable and Go
// intersects that list with the directories this section actually writes
// into — set logic where it is cheap to test, rather than in shell.
//
// `-writable` is a GNU find extension; where it is missing the sweep
// reports UNSUPPORTED and the preflight falls back to the root probe
// alone rather than failing a restore over a missing predicate.
//
// Sections that preserve modes or mtimes need more than writability:
// utime() and chmod() are OWNER rights, so tar hits "Cannot utime:
// Operation not permitted" and exits 2 on a directory the exec identity
// can write but does not own. That was found live against a .memory
// directory owned by the memory sidecar — mid-apply, after the workspace
// section had already landed. ownershipSweep adds that condition for
// exactly those sections.
// reclaimCrewMemoryOwnership applies the memory tree's ownership
// contract as root. Best-effort by design — see the call site.
func reclaimCrewMemoryOwnership(ctx context.Context, ops DockerOps, containerID string) {
	cmd := memory.MemoryReclaimOwnershipCmd(strconv.Quote(ContainerCrewPath))
	if _, _, err := ops.Exec(ctx, containerID, []string{"sh", "-c", cmd}); err != nil {
		_ = err // preflight decides; see the call site
	}
}

func probeWritable(ctx context.Context, ops DockerOps, containerID, crewSlug string, spec ExtractSpec, writesInto, writesPaths map[string]bool) error {
	probe := path.Join(spec.Dest, ".crewship-restore-probe")
	script := fmt.Sprintf(
		`[ -d %q ] || { echo "destination does not exist"; exit 3; }
touch %q 2>&1 || exit 4
rm -f %q
find %q -maxdepth 0 -writable >/dev/null 2>&1 || { echo UNSUPPORTED; exit 0; }
find %q ! -writable -print 2>/dev/null | head -n 500
%s`,
		spec.Dest, probe, probe, spec.Dest, spec.Dest, ownershipSweep(spec))

	code, out, err := ops.ExecAs(ctx, containerID, spec.User, []string{"sh", "-c", script})
	if err != nil {
		// Almost always a stopped container: exec needs a running one,
		// and every restore section now goes through exec-tar. Name a
		// command that EXISTS — `crewship crew start` does not, and
		// `crew provision` is what reconciles a stopped container back
		// to running (it logs "restarted stopped container" for exactly
		// this case).
		return fmt.Errorf("cannot reach %s to restore into it — the crew container must be RUNNING (exec is how every section is written now). Start it with `crewship crew provision %s`: %w",
			spec.Dest, crewSlug, err)
	}
	text := strings.TrimSpace(string(out))
	if code != 0 {
		return fmt.Errorf("%s is not writable by %s: %s", spec.Dest, spec.User, text)
	}
	if text == "" || text == "UNSUPPORTED" {
		return nil
	}
	for _, line := range strings.Split(text, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, spec.Dest), "/")
		if rel == "" {
			continue
		}
		// Two ways an unwritable path blocks this section, and it has to
		// be both: a DIRECTORY the section puts entries into, and a FILE
		// the section replaces. Anything else is unwritable and
		// irrelevant — a real /home/agent is full of root-owned paths
		// from feature installs, and refusing over one the bundle never
		// touches would trade a half-written restore for a restore that
		// never runs.
		if !writesInto[rel] && !writesPaths[rel] {
			continue
		}
		return fmt.Errorf("%s cannot be written by %s and this bundle has content for it; the restore would fail partway through the %s section, so it is refused before anything is written. Hand it to the agent (`chown -R 1001:1001 %s`) and re-run",
			p, spec.User, spec.Dest, p)
	}
	return nil
}

// ownershipSweep returns the extra find(1) line for sections whose
// extraction sets metadata, or "" for sections that do not.
//
// tar only calls utime/chmod when asked to preserve times or modes, and
// both are owner-only operations. A directory the exec identity can
// write but does not own therefore passes a writability probe and then
// fails the extraction — which is the mid-apply failure the preflight
// exists to prevent, so it belongs in the preflight.
func ownershipSweep(spec ExtractSpec) string {
	if !spec.PreserveTimes && !spec.PreserveModes {
		return ""
	}
	uid, _, _ := strings.Cut(spec.User, ":")
	if uid == "" {
		return ""
	}
	return fmt.Sprintf("find %q ! -user %s -print 2>/dev/null | head -n 500", spec.Dest, uid)
}

// sectionWriteDirs returns two sets, relative to the section root: the
// directories extracting this section will create entries in (every
// entry's parent, plus every directory entry), and the exact paths it
// will open for writing (every non-directory entry).
//
// Used to keep the preflight's writability sweep exact: an unwritable
// path only blocks a restore if the bundle actually writes there.
func sectionWriteDirs(r io.Reader) (dirs, paths map[string]bool, err error) {
	dirs, paths = map[string]bool{}, map[string]bool{}
	tr := tar.NewReader(r)
	for {
		hdr, nerr := tr.Next()
		if nerr == io.EOF {
			return dirs, paths, nil
		}
		if nerr != nil {
			return dirs, paths, nerr
		}
		name := strings.TrimSuffix(strings.TrimPrefix(hdr.Name, "./"), "/")
		if name == "" || name == "." {
			continue
		}
		if hdr.Typeflag == tar.TypeDir {
			dirs[name] = true
		} else {
			// Non-directory entries are the ones tar opens for writing,
			// so an existing unwritable file at this exact path is what
			// fails the extraction.
			paths[name] = true
		}
		// Every ancestor is written into as well: tar creates
		// intermediate directories, and creating an entry needs write on
		// its immediate parent.
		for d := path.Dir(name); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			dirs[d] = true
		}
	}
}

// reapplyMemoryPerms re-establishes the crew memory tree's group and
// setgid contract after a restore, using the same mechanism and the same
// identity the orchestrator uses when it prepares those directories.
//
// Why the tar cannot carry this on its own: the archive records group
// 1002, but extraction runs --no-same-owner because a CapDrop: ALL
// container has no CAP_CHOWN, so every restored entry lands in the exec
// identity's group. Running the extraction itself as 1001:1002 would put
// the WHOLE of /crew in group 1002, not just the .memory subtrees. So
// the tar lands the content under the agent's own group, and this pass
// re-flips exactly the subtrees that need it — the same two-step the
// docker provider's init container performs at crew creation.
//
// Idempotent, and scoped to .memory subtrees by find, so a bundle with
// no memory directories is a no-op rather than a broad chgrp.
//
// EVERY pass is best-effort, and the outcome is then VERIFIED. That
// split matters, and getting it wrong the other way was a bug:
//
// The passes cannot be strict. This runs AFTER every section has been
// written, and a `.memory` directory that already existed in the target
// can be owned by the memory sidecar (uid 1002) rather than by the
// agent — tar does not chown a directory it merely extracts into. chgrp
// by uid 1001 on a directory it does not own is EPERM, GNU
// `find -exec … +` exits non-zero if any exec fails, and under `set -e`
// that aborted the script. The result was RestoreCrew reporting failure
// on a crew that had in fact been fully and correctly overwritten —
// the same "which half landed?" ambiguity the preflight exists to
// prevent, moved from before the first write to after the last one. And
// re-running did not help, because the same entry EPERMs again.
// prepMemoryDirs tolerates exactly this case, for exactly this reason,
// and says so in its own comment.
//
// But tolerating silently would hide a real loss: without group 1002
// and setgid on those directories, crew-shared memory keeps working
// until the sidecar next writes and then quietly stops. So the passes
// are followed by a verification sweep, and anything still wrong is
// reported as ErrMemoryPermsDegraded — which RestoreCrew surfaces
// loudly WITHOUT failing a restore whose data did land. The operator
// gets told what to fix; they do not get told their restore failed
// when it did not.
//
// find -exec … + rather than `for p in $(find …)`: a path containing a
// space or a glob character would otherwise be split into pieces and the
// chgrp would silently target the wrong thing, or nothing.
func reapplyMemoryPerms(ctx context.Context, ops DockerOps, containerID string) error {
	root := ContainerCrewPath
	// Each line does exactly one thing, and `-path '*/.memory/*'` selects
	// strictly INSIDE a .memory directory, so the two halves do not
	// overlap. That matters beyond tidiness: an earlier version applied a
	// strict pass and then a tolerant `chmod -R` over the same paths, and
	// the tolerant pass silently repaired whatever the strict one got
	// wrong — which made a mutation of the strict pass survive. A
	// permission fix nothing can detect the absence of is how the
	// original bug got here.
	//
	// The resulting modes (2775 on directories, 2664 on files) are what
	// prepMemoryDirs' `chmod -R u+rwX,g+rwXs` produces on a live crew,
	// setgid on files included. A restored tree that differs from a live
	// one differs in a way nobody would notice until the sidecar stopped
	// being able to write.
	// The application passes send their own stderr to /dev/null: an
	// EPERM here is EXPECTED and benign (see above), and letting it into
	// the operator-facing message buries the verification result in
	// noise it cannot act on. The verification below is authoritative —
	// it reports the state that remains, not the attempts that failed.
	script := `
find ` + root + ` -type d -name .memory -exec chgrp 1002 {} + 2>/dev/null || true
find ` + root + ` -type d -name .memory -exec chmod 2775 {} + 2>/dev/null || true
find ` + root + ` -path '*/.memory/*' -exec chgrp 1002 {} + 2>/dev/null || true
find ` + root + ` -path '*/.memory/*' -type d -exec chmod 2775 {} + 2>/dev/null || true
find ` + root + ` -path '*/.memory/*' -type f -exec chmod ug+rw,g+s {} + 2>/dev/null || true
# Verify rather than trust. Every pass above swallows its own failures,
# so this sweep is the only thing standing between a silent permission
# loss and the operator. A .memory directory that is not group 1002, or
# not setgid, is one the sidecar will stop being able to write into.
# Deduplicated: a directory can fail both conditions and naming it twice
# reads like two problems.
{ find ` + root + ` -type d -name .memory ! -group 1002 -print 2>/dev/null
  find ` + root + ` -type d -name .memory ! -perm -2000 -print 2>/dev/null
} | sort -u`
	code, out, err := ops.ExecAs(ctx, containerID, sidecarWriterUser, []string{"sh", "-c", script})
	if err != nil {
		return fmt.Errorf("re-apply memory permissions: %w", err)
	}
	residual := strings.TrimSpace(string(out))
	if code != 0 {
		return fmt.Errorf("re-apply memory permissions exited %d: %s", code, residual)
	}
	if residual != "" {
		// The data is on disk and correct. Only the sharing contract is
		// degraded, so this must not read as a failed restore — see the
		// doc comment.
		return fmt.Errorf("%w: these .memory directories are not group %d + setgid, so the memory sidecar will not be able to write them: %s",
			ErrMemoryPermsDegraded, sidecarGID, strings.Join(strings.Fields(residual), " "))
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
