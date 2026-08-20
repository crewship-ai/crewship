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
func (pb *planBuilder) planCrewFiles(ctx context.Context, crewSlug, crewID string, files []CrewFile) error {
	pb.warnIfCrewStopped(ctx, crewSlug, crewID, len(files))
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

// warnIfCrewStopped adds a plan-time advisory when the manifest ships
// files into a crew whose container is not running.
//
// Files under the crew's shared tree are owned by the container user, so
// the server can only overwrite one by replaying the write inside the
// container — which has to be up. Against a stopped crew the save
// answers 409, and because apply is fail-fast that 409 lands in the
// middle of a run, after earlier resources are already committed. This
// is the same fact, twenty seconds earlier, in the dry-run someone reads
// before they commit to anything.
//
// Advisory and not an error, on purpose. The crew may be started between
// the plan and the apply — including by the operator reading this line —
// and a manifest that refused to plan against a stopped crew would be
// unusable in exactly the deploy where it matters most.
//
// Only for crews that already exist: a crew being created in this same
// apply has no container yet, its files are a first write to an
// unowned path, and warning about it would fire on every fresh install.
func (pb *planBuilder) warnIfCrewStopped(ctx context.Context, crewSlug, crewID string, fileCount int) {
	if crewID == "" || fileCount == 0 {
		return
	}
	statusFn := pb.containerStatusFn
	if statusFn == nil {
		statusFn = func(id string) (bool, bool) {
			return pb.client.CrewContainerStatus(ctx, id)
		}
	}
	running, known := statusFn(crewID)
	if !known || running {
		return
	}
	pb.plan.Warnings = append(pb.plan.Warnings, fmt.Sprintf(
		"crew %q is not running, and this manifest writes %d file(s) into it — files it already owns can only be overwritten while its container is up, so those saves will fail with 409. Start it first: `crewship crew start %s`",
		crewSlug, fileCount, crewSlug))
}
