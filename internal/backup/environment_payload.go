package backup

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// maxEnvironmentRecordBytes bounds environments/<crew>.json (a record
// lists every archive entry; a large image has a few hundred).
const maxEnvironmentRecordBytes = 16 << 20

// environments is the part of ExtractedPayload that complete environments
// fill in.
type environments struct {
	// EnvironmentBySlug holds each crew's environment record.
	EnvironmentBySlug map[string]*Environment
	// envMountPaths: "<slug>/<n>" → temp tar of that mount's content,
	// entries relative to the mount point.
	envMountPaths map[string]string
	// envBlobs holds inline blobs (environment-blobs/), verified by digest
	// as they are extracted. Nil when the bundle carried none.
	envBlobs *EnvironmentStore
}

const envMountSinkPrefix = "envmount/"

// extractEnvironmentEntry handles one payload entry under environments/ or
// environment-blobs/.
func (p *ExtractedPayload) extractEnvironmentEntry(tr *TarZstReader, hdr *tar.Header, name string, sinkFor func(string) (*sink, error)) error {
	if rest, ok := strings.CutPrefix(name, environmentBlobsPrefix); ok {
		if hdr.Typeflag != tar.TypeReg {
			return nil
		}
		h, ok := strings.CutPrefix(rest, "sha256/")
		if !ok || strings.Contains(h, "/") {
			return nil
		}
		if p.envBlobs == nil {
			p.envBlobs = &EnvironmentStore{Dir: filepath.Join(p.tempDir, "environment-store")}
		}
		if _, _, err := p.envBlobs.Put(tr, "sha256:"+h); err != nil {
			return fmt.Errorf("backup: inline environment blob %s: %w", h, err)
		}
		return nil
	}
	rest := strings.TrimPrefix(name, environmentsPrefix)
	if slug, ok := strings.CutSuffix(rest, ".json"); ok && !strings.Contains(slug, "/") {
		data, err := io.ReadAll(io.LimitReader(tr, maxEnvironmentRecordBytes))
		if err != nil {
			return err
		}
		var env Environment
		if err := json.Unmarshal(data, &env); err != nil {
			return fmt.Errorf("%w: environment record %s: %v", ErrInvalidManifest, name, err)
		}
		if p.EnvironmentBySlug == nil {
			p.EnvironmentBySlug = map[string]*Environment{}
		}
		p.EnvironmentBySlug[slug] = &env
		return nil
	}
	// environments/<slug>/mounts/<n>/<path…>
	slug, more, ok := splitFirst(rest)
	if !ok {
		return nil
	}
	tail, ok := strings.CutPrefix(more, "mounts/")
	if !ok {
		return nil
	}
	n, inner, _ := splitFirst(tail)
	if _, err := strconv.Atoi(n); err != nil {
		return nil
	}
	s, err := sinkFor(envMountSinkPrefix + slug + "/" + n)
	if err != nil {
		return err
	}
	if inner == "" {
		inner = "."
	}
	h := *hdr
	h.Name = inner
	if err := s.tw.WriteHeader(&h); err != nil {
		return fmt.Errorf("backup: inner tar header %q: %w", inner, err)
	}
	if hdr.Typeflag == tar.TypeReg && hdr.Size > 0 {
		if _, err := io.CopyN(s.tw, tr, hdr.Size); err != nil {
			return fmt.Errorf("backup: inner tar body %q: %w", inner, err)
		}
	}
	return nil
}

// placeEnvironmentSink files a closed sink; false when the key is not one.
func (p *ExtractedPayload) placeEnvironmentSink(key, path string) bool {
	rest, ok := strings.CutPrefix(key, envMountSinkPrefix)
	if !ok {
		return false
	}
	if p.envMountPaths == nil {
		p.envMountPaths = map[string]string{}
	}
	p.envMountPaths[rest] = path
	return true
}

// Environments lists the crews whose environment the bundle carries.
func (p *ExtractedPayload) Environments() []string {
	var out []string
	for s := range p.EnvironmentBySlug {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// InlineEnvironmentBlobs is the store holding the bundle's inline blobs, or
// nil.
func (p *ExtractedPayload) InlineEnvironmentBlobs() *EnvironmentStore { return p.envBlobs }

// OpenEnvironmentMount opens the content of one mount of a crew's
// environment, entries relative to the mount point. data is the mount's
// Data field.
func (p *ExtractedPayload) OpenEnvironmentMount(ctx context.Context, slug string, m EnvironmentMount) (io.ReadCloser, bool, error) {
	switch {
	case strings.HasPrefix(m.Data, "section:"):
		switch strings.TrimPrefix(m.Data, "section:") {
		case SectionCrewWorkspace:
			return p.OpenWorkspace(ctx, slug)
		case SectionCrewMemory:
			return p.OpenCrew(ctx, slug)
		case SectionCrewOutput:
			return p.OpenMemory(ctx, slug)
		case SectionCrewHome:
			return p.OpenVolume(ctx, slug, "home")
		case SectionCrewTools:
			return p.OpenVolume(ctx, slug, "tools")
		}
		return nil, false, nil
	case strings.HasPrefix(m.Data, environmentsPrefix):
		n := m.Data[strings.LastIndex(m.Data, "/")+1:]
		path, ok := p.envMountPaths[slug+"/"+n]
		if !ok {
			return nil, false, nil
		}
		f, err := p.storageOrDefault().Open(ctx, path)
		if err != nil {
			return nil, true, err
		}
		return f, true, nil
	}
	return nil, false, nil
}
