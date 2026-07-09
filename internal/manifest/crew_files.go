package manifest

import (
	"context"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/crewfile"
)

// crewFileMaxBytes / crewFileSharedPrefix are retained as local aliases; the
// canonical values (and the normalize/load logic) live in the shared crewfile
// leaf package so the SPEC-2 kinds path enforces the identical fence + cap.
const (
	crewFileMaxBytes     = crewfile.MaxBytes
	crewFileSharedPrefix = crewfile.SharedPrefix
)

// normalizeCrewFileDest delegates to the shared leaf so both apply paths
// normalize destinations identically.
func normalizeCrewFileDest(src, dest string) (string, error) {
	return crewfile.NormalizeDest(src, dest)
}

// loadCrewFile delegates to the shared leaf so both apply paths enforce the
// same source-existence + size checks.
func loadCrewFile(baseDir, src string) ([]byte, error) {
	return crewfile.Load(baseDir, src)
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
