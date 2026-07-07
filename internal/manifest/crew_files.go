package manifest

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// crewFileMaxBytes caps one delivered crew file at 1 MiB — same budget as an
// inline code-step body and the script step's stdout cap. Bigger assets
// belong in object storage or the devcontainer image, not the manifest flow.
const crewFileMaxBytes = 1 << 20

// crewFileSharedPrefix is the only in-crew tree a manifest file may target.
// It maps to /crew/shared inside the container — the same root the script
// step's path fence anchors to (internal/pipeline/runner_script.go).
const crewFileSharedPrefix = "shared/"

// normalizeCrewFileDest resolves a CrewFile's destination to the canonical
// "shared/..." form: defaults to shared/<basename(src)>, accepts
// "/crew/shared/..." spellings, and rejects traversal or any path outside
// the shared tree (the crew's /output, /secrets, agent homes are off-limits
// to declarative delivery on purpose).
func normalizeCrewFileDest(src, dest string) (string, error) {
	d := strings.TrimSpace(dest)
	if d == "" {
		s := strings.TrimSpace(src)
		if s == "" {
			return "", fmt.Errorf("src is required")
		}
		d = crewFileSharedPrefix + path.Base(filepath.ToSlash(s))
	}
	d = strings.TrimPrefix(filepath.ToSlash(d), "/crew/")
	d = strings.TrimPrefix(d, "/")
	clean := path.Clean(d)
	if !strings.HasPrefix(clean, crewFileSharedPrefix) || clean == crewFileSharedPrefix {
		return "", fmt.Errorf("dest %q must be a file under %s (no traversal; e.g. shared/scripts/parse.py)", dest, crewFileSharedPrefix)
	}
	return clean, nil
}

// loadCrewFile reads a CrewFile's source from disk, resolving a relative Src
// against baseDir (the manifest file's directory), and enforces the size cap.
func loadCrewFile(baseDir, src string) ([]byte, error) {
	s := strings.TrimSpace(src)
	if s == "" {
		return nil, fmt.Errorf("src is required")
	}
	p := s
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("src %q: %w", src, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("src %q is a directory — list files individually", src)
	}
	if info.Size() > crewFileMaxBytes {
		return nil, fmt.Errorf("src %q is %d bytes, exceeds the %d-byte crew-file cap — ship big assets via the devcontainer image or object storage", src, info.Size(), crewFileMaxBytes)
	}
	return os.ReadFile(p)
}

// checkFiles validates a crew's declarative file list: src present, dest
// normalizable under shared/. Filesystem existence/size are checked at plan
// time (BuildPlan has the manifest's BaseDir; pure validation does not).
func (v *validator) checkFiles(scope string, files []CrewFile) {
	seen := map[string]bool{}
	for i := range files {
		f := &files[i]
		if strings.TrimSpace(f.Src) == "" {
			v.errf("%s: files[%d] missing src", scope, i)
			continue
		}
		dest, err := normalizeCrewFileDest(f.Src, f.Dest)
		if err != nil {
			v.errf("%s: files[%d]: %v", scope, i, err)
			continue
		}
		if seen[dest] {
			v.errf("%s: files[%d]: duplicate dest %q", scope, i, dest)
		}
		seen[dest] = true
	}
}

// planCrewFiles emits one plan item per declared crew file. The exec closure
// re-reads the source at apply time (so the uploaded bytes are current) and
// PUTs through the same /files/save endpoint `crewship crew files save` uses
// — one write path, one validation surface. When the parent crew is new,
// crewID is empty and the closure resolves it by slug at apply time, exactly
// like the other crew children.
func (pb *planBuilder) planCrewFiles(crewSlug, crewID string, files []CrewFile) error {
	for i := range files {
		f := files[i]
		dest, err := normalizeCrewFileDest(f.Src, f.Dest)
		if err != nil {
			return fmt.Errorf("crew %q files[%d]: %w", crewSlug, i, err)
		}
		// Plan-time existence + size check: a missing or oversized local
		// file fails the plan (and --dry-run) instead of mid-apply.
		if _, err := loadCrewFile(pb.opts.BaseDir, f.Src); err != nil {
			return fmt.Errorf("crew %q files[%d]: %w", crewSlug, i, err)
		}
		action := ActionUpdate
		if crewID == "" {
			action = ActionCreate
		}
		src := f.Src
		id := crewID
		pb.appendItem(action, "crew-file", crewSlug+"/"+dest,
			func(ctx context.Context, client *Client, opts Options) error {
				cid := id
				if cid == "" {
					crew, err := client.FindCrewBySlug(ctx, crewSlug)
					if err != nil || crew == nil {
						return fmt.Errorf("crew %q not found for file upload: %v", crewSlug, err)
					}
					cid = crew.ID
				}
				data, err := loadCrewFile(opts.BaseDir, src)
				if err != nil {
					return err
				}
				return client.SaveCrewFile(ctx, cid, dest, data)
			})
	}
	return nil
}
