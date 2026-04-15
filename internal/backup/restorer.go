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
// payload tar. All fields may be nil if the section is absent.
//
// Values are eagerly materialised into byte slices for MVP simplicity.
// V2 may switch to on-the-fly restoration, but the current one-pass
// read keeps the error model tractable.
type ExtractedPayload struct {
	// DBDump is the parsed contents of db/dump.json. nil when the
	// bundle had no DB section.
	DBDump *DBDump

	// DevcontainerBySlug maps crew slug to the devcontainer.json bytes
	// recorded in the bundle. Missing slugs indicate the crew had no
	// devcontainer config.
	DevcontainerBySlug map[string][]byte

	// MiseBySlug is the mise.toml counterpart to DevcontainerBySlug.
	MiseBySlug map[string][]byte

	// WorkspaceTarsBySlug holds a tar archive per crew, containing the
	// workspace bind mount contents. The runner hands these to
	// DockerOps.CopyTo after the container is recreated.
	WorkspaceTarsBySlug map[string][]byte

	// VolumeTarsBySlug["home"] and ["tools"] follow the same pattern.
	VolumeTarsBySlug map[string]map[string][]byte

	// MemoryTarsBySlug covers the /output directory contents.
	MemoryTarsBySlug map[string][]byte
}

// ExtractPayload walks the payload tar produced by the collector and
// splits it into the ExtractedPayload buckets. It does NOT touch the
// database or docker — that's the runner's job.
func ExtractPayload(payload io.Reader) (*ExtractedPayload, error) {
	out := &ExtractedPayload{
		DevcontainerBySlug:  map[string][]byte{},
		MiseBySlug:          map[string][]byte{},
		WorkspaceTarsBySlug: map[string][]byte{},
		VolumeTarsBySlug:    map[string]map[string][]byte{},
		MemoryTarsBySlug:    map[string][]byte{},
	}
	tr, err := NewTarZstReader(payload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tr.Close() }()

	// Build nested tars section-by-section. We re-tar inside buckets so
	// CopyTo receives a single coherent archive per destination.
	//
	// Keyed by destination label (e.g. "workspace/my-crew") -> in-memory
	// tar writer + backing buffer.
	sinks := map[string]*sink{}
	sinkFor := func(key string) *sink {
		if s, ok := sinks[key]; ok {
			return s
		}
		buf := &bytes.Buffer{}
		s := &sink{buf: buf, tw: tar.NewWriter(buf)}
		sinks[key] = s
		return s
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

	// Close all inner tars and distribute into the typed buckets.
	for key, s := range sinks {
		if err := s.tw.Close(); err != nil {
			return nil, fmt.Errorf("backup: close inner tar %s: %w", key, err)
		}
		parts := strings.SplitN(key, "/", 3)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "workspace":
			out.WorkspaceTarsBySlug[parts[1]] = s.buf.Bytes()
		case "memory":
			out.MemoryTarsBySlug[parts[1]] = s.buf.Bytes()
		case "volumes":
			if len(parts) < 3 {
				continue
			}
			slug, vol := parts[1], parts[2]
			bySlug, ok := out.VolumeTarsBySlug[slug]
			if !ok {
				bySlug = map[string][]byte{}
				out.VolumeTarsBySlug[slug] = bySlug
			}
			bySlug[vol] = s.buf.Bytes()
		}
	}
	return out, nil
}

// repackIntoSink streams the current tar entry (hdr + tr body) into
// the appropriate in-memory sink, keyed by the top-level prefix.
// Sink keys are:
//
//	workspace/<slug>                  → one sink per crew
//	volumes/<slug>/<volumeName>       → one sink per crew+volume
//	memory/<slug>                     → one sink per crew
//
// Entry names inside the sink are stripped of their outermost path
// segments so CopyTo places them directly at the container destination
// (e.g. /workspace/, /home/agent/, /output/).
func repackIntoSink(tr *TarZstReader, hdr *tar.Header, name, topPrefix string, sinkFor func(string) *sink) error {
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
	s := sinkFor(key)
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

// sink is a package-local struct for repackIntoSink.
type sink struct {
	buf *bytes.Buffer
	tw  *tar.Writer
}

// splitFirst splits s on the first "/" and returns the two halves plus
// true if the separator was found, or "", "", false otherwise. Kept
// local so we don't take another dependency on path/filepath split
// semantics (which normalise unexpectedly on Windows).
func splitFirst(s string) (head, tail string, ok bool) {
	if i := strings.Index(s, "/"); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

// RestoreCrew writes the per-crew sections of an ExtractedPayload into
// a freshly-provisioned container. The container MUST already exist
// (created, optionally started) and have the canonical mount paths
// available; the caller is responsible for invoking the existing
// devcontainer provisioner before calling this.
func RestoreCrew(ctx context.Context, ops DockerOps, containerID string, crewSlug string, payload *ExtractedPayload) error {
	if payload == nil {
		return fmt.Errorf("backup: RestoreCrew: nil payload")
	}
	// Workspace bind.
	if body, ok := payload.WorkspaceTarsBySlug[crewSlug]; ok && len(body) > 0 {
		if err := ops.CopyTo(ctx, containerID, ContainerWorkspacePath, bytes.NewReader(body)); err != nil {
			return fmt.Errorf("backup: restore workspace %s: %w", crewSlug, err)
		}
	}
	// Named volumes.
	if byVol, ok := payload.VolumeTarsBySlug[crewSlug]; ok {
		if body := byVol["home"]; len(body) > 0 {
			if err := ops.CopyTo(ctx, containerID, ContainerHomePath, bytes.NewReader(body)); err != nil {
				return fmt.Errorf("backup: restore home %s: %w", crewSlug, err)
			}
		}
		if body := byVol["tools"]; len(body) > 0 {
			if err := ops.CopyTo(ctx, containerID, ContainerToolsPath, bytes.NewReader(body)); err != nil {
				return fmt.Errorf("backup: restore tools %s: %w", crewSlug, err)
			}
		}
	}
	// Memory / output.
	if body, ok := payload.MemoryTarsBySlug[crewSlug]; ok && len(body) > 0 {
		if err := ops.CopyTo(ctx, containerID, ContainerMemoryPath, bytes.NewReader(body)); err != nil {
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
