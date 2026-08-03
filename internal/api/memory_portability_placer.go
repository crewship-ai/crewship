package api

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/memport"
)

// crewContainerPlacer puts imported memory where the container user can
// own it.
//
// The memory tree is 1001:1002 mode 2775 (the docker provider's
// buildChownInitCmd): write belongs to group 1002 and crewshipd is in
// neither, so a host-side write is refused however correct the bytes
// are (#1741). Every other writer into agent memory already runs on the
// container side; this is how an operator-initiated import joins them.
//
// The transport is backup.DockerOps.CopyToPath — `tar -x` over an exec
// session as uid 1001 — which #1714 built for the identical ownership
// problem on the restore path. Docker's native CopyToContainer is not
// usable here: its CopyUIDGID flag means "chown to the container's
// configured user", which resolves to the daemon's remapped root.
type crewContainerPlacer struct {
	ops         backup.DockerOps
	containerID string
	// dest is the container-absolute .memory directory:
	// /crew/shared/.memory, or /crew/agents/<slug>/.memory.
	dest string
}

// Place tars the staged documents and extracts them inside the crew
// container.
//
// Directory entries are emitted for every parent the documents need.
// Modes are preserved because the memory tree's setgid bits are the
// contract that keeps crew-shared memory readable, and mtimes because a
// memory note's timestamp is content — the same two decisions the
// restore path records for this tree.
func (p crewContainerPlacer) Place(ctx context.Context, stagingDir string, rels []string) error {
	if p.ops == nil || p.containerID == "" {
		return fmt.Errorf("no crew container available to write into")
	}
	archive, err := tarStagedDocs(stagingDir, rels)
	if err != nil {
		return err
	}
	return p.ops.CopyToPath(ctx, p.containerID, backup.ExtractSpec{
		Dest:          p.dest,
		User:          "1001:1001",
		PreserveModes: true,
		PreserveTimes: true,
	}, bytes.NewReader(archive))
}

// tarStagedDocs builds the archive in memory. The documents are bounded
// by the import's own per-file caps and by the request body limit, so
// the whole set is small by construction — streaming would buy nothing
// and would make the "all or nothing" contract harder to keep.
func tarStagedDocs(stagingDir string, rels []string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Parent directories first, deduplicated and in ascending depth, so
	// tar never extracts a file into a directory it has not made yet.
	dirs := map[string]bool{}
	for _, rel := range rels {
		for d := path.Dir(rel); d != "." && d != "/"; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		ordered = append(ordered, d)
	}
	sort.Strings(ordered)
	for _, d := range ordered {
		if err := tw.WriteHeader(&tar.Header{
			Name:     d + "/",
			Typeflag: tar.TypeDir,
			// Group-writable: the tree's setgid parent puts new entries
			// in group 1002, and the crew's agents share that group.
			Mode: 0o2775,
		}); err != nil {
			return nil, fmt.Errorf("tar header %s: %w", d, err)
		}
	}

	for _, rel := range rels {
		body, err := os.ReadFile(filepath.Join(stagingDir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("read staged %s: %w", rel, err)
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:     rel,
			Typeflag: tar.TypeReg,
			Mode:     0o664,
			Size:     int64(len(body)),
		}); err != nil {
			return nil, fmt.Errorf("tar header %s: %w", rel, err)
		}
		if _, err := tw.Write(body); err != nil {
			return nil, fmt.Errorf("tar body %s: %w", rel, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("tar close: %w", err)
	}
	return buf.Bytes(), nil
}

// hostPlacer writes straight into the target directory. It is correct
// wherever the host process owns the memory tree — a single-process
// dev instance, a deployment that puts crewshipd in group 1002 — and it
// is what the tests use. It is NOT the default for a container
// deployment; see crewContainerPlacer.
type hostPlacer struct{ root string }

func (p hostPlacer) Place(_ context.Context, stagingDir string, rels []string) error {
	for _, rel := range rels {
		src := filepath.Join(stagingDir, filepath.FromSlash(rel))
		dst := filepath.Join(p.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		body, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, body, 0o664); err != nil {
			return err
		}
	}
	return nil
}

var (
	_ memport.Placer = crewContainerPlacer{}
	_ memport.Placer = hostPlacer{}
)

// containerMemoryDest returns the container-absolute .memory directory
// for a scope. The container sees the crew's host tree at /crew.
func containerMemoryDest(agentSlug string) string {
	if agentSlug == "" {
		return "/crew/shared/.memory"
	}
	return path.Join("/crew", "agents", agentSlug, ".memory")
}
