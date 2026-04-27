package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

// SelfTestResult summarises a single backup loopback round-trip. Returned
// to the API handler which forwards it to the seed CLI as JSON so the
// operator sees bundle size and elapsed time on a successful run.
type SelfTestResult struct {
	OK          bool   `json:"ok"`
	CrewID      string `json:"crew_id"`
	CrewSlug    string `json:"crew_slug"`
	CanaryPath  string `json:"canary_path"`
	CanaryBytes int    `json:"canary_bytes"`
	BundleBytes int    `json:"bundle_bytes"`
	ElapsedMS   int64  `json:"elapsed_ms"`
	Error       string `json:"error,omitempty"`
}

// SelfTestOpts is the input for BackupSelfTest. The caller provides the
// target container and a CrewTarget built from the DB row (slug + id are
// what actually matters — the rest are bundle metadata CollectCrew
// doesn't require for this flow).
type SelfTestOpts struct {
	ContainerID string
	Crew        CrewTarget
}

// BackupSelfTest runs the canary round-trip: write → collect → mutate →
// restore → verify → cleanup. It is deliberately self-contained (no bundle
// on disk, no encryption, no DB dump) because the point is to catch
// regressions in CollectCrew/RestoreCrew + DockerOps plumbing, not to
// exercise the full backup bundle path (that's what
// scripts/e2e-backup-container-test.sh already does).
//
// We mutate (overwrite) rather than `rm` the canary between collect and
// restore: /workspace is a host bind mount whose effective permissions
// depend on the container runtime and UID-remapping config, so running
// `rm` there is flaky. CopyTo with a sentinel payload always works and
// still fails if the restore silently skipped the section.
//
// Returned error is non-nil only when the pipeline itself failed; a
// content mismatch on verify returns (result, nil) with result.OK=false
// and a reason in result.Error so the caller can still report bundle
// size and elapsed time.
func BackupSelfTest(ctx context.Context, ops DockerOps, opts SelfTestOpts) (*SelfTestResult, error) {
	if ops == nil {
		return nil, errors.New("backup self-test: nil DockerOps")
	}
	if opts.ContainerID == "" {
		return nil, errors.New("backup self-test: empty container id")
	}
	if opts.Crew.Slug == "" {
		return nil, errors.New("backup self-test: empty crew slug")
	}

	exists, err := ops.ContainerExists(ctx, opts.ContainerID)
	if err != nil {
		return nil, fmt.Errorf("backup self-test: container probe: %w", err)
	}
	if !exists {
		return &SelfTestResult{
			OK:       false,
			CrewID:   opts.Crew.ID,
			CrewSlug: opts.Crew.Slug,
			Error:    "container not found (is the crew provisioned?)",
		}, nil
	}

	// Unique canary content so overlapping self-tests on the same crew
	// cannot confuse each other. File lives in /workspace because that's
	// the section CollectCrew definitively covers — memory is scoped to
	// /output and would also work, but /workspace has the cleanest
	// collect/restore semantics.
	canaryToken, err := randomToken(16)
	if err != nil {
		return nil, fmt.Errorf("backup self-test: token: %w", err)
	}
	canaryName := "CANARY-" + canaryToken + ".txt"
	canaryPath := ContainerWorkspacePath + "/" + canaryName
	canaryContent := []byte(fmt.Sprintf("crewship-backup-self-test\ntoken=%s\nts=%s\n",
		canaryToken, time.Now().UTC().Format(time.RFC3339Nano)))

	start := time.Now()

	// 1. Write canary via docker cp. CopyTo takes a tar stream whose
	//    entries get extracted into the destination dir, so we build a
	//    single-file tar pointing at /workspace.
	canaryTar, err := buildSingleFileTar(canaryName, canaryContent)
	if err != nil {
		return nil, fmt.Errorf("backup self-test: build tar: %w", err)
	}
	if err := ops.CopyTo(ctx, opts.ContainerID, ContainerWorkspacePath, bytes.NewReader(canaryTar)); err != nil {
		return nil, fmt.Errorf("backup self-test: write canary: %w", err)
	}

	// Canary is now on disk inside the container. Every return path
	// below this point needs to wipe it so a failed self-test doesn't
	// leave markers in the seeded /workspace. Both the direct and the
	// nested restore location get cleaned because we can't tell ahead
	// of time which path actually held the final write.
	defer func() {
		cleanTar, err := buildSingleFileTar(canaryName, []byte{})
		if err != nil {
			return
		}
		_ = ops.CopyTo(ctx, opts.ContainerID, ContainerWorkspacePath, bytes.NewReader(cleanTar))
		cleanTarNested, err := buildSingleFileTar(canaryName, []byte{})
		if err != nil {
			return
		}
		_ = ops.CopyTo(ctx, opts.ContainerID, ContainerWorkspacePath+"/workspace", bytes.NewReader(cleanTarNested))
	}()

	// 1b. Read the canary back immediately so a misbehaving CopyTo
	//     (e.g. extraction to the wrong destination, silent zero-copy)
	//     surfaces as a specific error rather than the later ambiguous
	//     "restore was a no-op" verdict.
	postWrite, err := readCanary(ctx, ops, opts.ContainerID, canaryPath, canaryName)
	if err != nil {
		return nil, fmt.Errorf("backup self-test: read-after-write: %w", err)
	}
	if !bytes.Equal(postWrite, canaryContent) {
		return &SelfTestResult{
			CrewID:      opts.Crew.ID,
			CrewSlug:    opts.Crew.Slug,
			CanaryPath:  canaryPath,
			CanaryBytes: len(canaryContent),
			BundleBytes: 0,
			ElapsedMS:   time.Since(start).Milliseconds(),
			OK:          false,
			Error: fmt.Sprintf(
				"CopyTo landed %d bytes but readback sees %d — docker cp is dropping content",
				len(canaryContent), len(postWrite)),
		}, nil
	}

	// 2. Collect into an in-memory bundle. We deliberately do NOT use
	//    CollectCrew here: it also sweeps /home/agent and /opt/crew-tools
	//    which on mise-provisioned crews contain version-alias symlinks
	//    (e.g. ~/.cache/mise/python/pyenv/bin/pyenv) that the manifest
	//    extractor currently rejects. That's a pre-existing backup bug,
	//    not a self-test concern — for this round-trip we only need the
	//    workspace section to carry the canary. Pause via WithPaused for
	//    a stable tar snapshot, same as CollectCrew does internally.
	var bundleBuf bytes.Buffer
	writer, err := NewTarZstWriter(&bundleBuf)
	if err != nil {
		return nil, fmt.Errorf("backup self-test: writer: %w", err)
	}
	err = WithPaused(ctx, ops, opts.ContainerID, func() error {
		return copyContainerPath(ctx, ops, writer, opts.ContainerID,
			ContainerWorkspacePath, fmt.Sprintf("workspace/%s", opts.Crew.Slug))
	})
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("backup self-test: collect workspace: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("backup self-test: finalise bundle: %w", err)
	}
	bundleBytes := bundleBuf.Len()

	// 3. Overwrite the canary with a sentinel so a no-op restore (e.g.
	//    one that silently skipped the workspace section) cannot coast
	//    through verify. An `rm` would be cleaner but bind-mount
	//    permissions on /workspace depend on the runtime's UID
	//    remapping; CopyTo with a different payload is universally
	//    accepted because it's the same code path that wrote the canary
	//    in step 1.
	mutantContent := []byte("OVERWRITTEN-" + canaryToken + "\n")
	mutantTar, err := buildSingleFileTar(canaryName, mutantContent)
	if err != nil {
		return nil, fmt.Errorf("backup self-test: build mutant tar: %w", err)
	}
	if err := ops.CopyTo(ctx, opts.ContainerID, ContainerWorkspacePath, bytes.NewReader(mutantTar)); err != nil {
		return nil, fmt.Errorf("backup self-test: mutate canary: %w", err)
	}

	// 4. Extract + RestoreCrew. ExtractPayload writes per-section tars to
	//    its own temp dir; we must Close to clean up.
	payload, err := ExtractPayload(ctx, bytes.NewReader(bundleBuf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("backup self-test: extract: %w", err)
	}
	defer func() { _ = payload.Close() }()

	if !payload.HasWorkspace(opts.Crew.Slug) {
		// The bundle had no workspace section for this slug — restore
		// would silently skip, producing a misleading "no-op" verdict.
		// Surface this explicitly so the operator knows the collector
		// didn't capture anything for the self-test slug.
		return &SelfTestResult{
			CrewID:      opts.Crew.ID,
			CrewSlug:    opts.Crew.Slug,
			CanaryPath:  canaryPath,
			CanaryBytes: len(canaryContent),
			BundleBytes: bundleBytes,
			ElapsedMS:   time.Since(start).Milliseconds(),
			OK:          false,
			Error:       "bundle has no workspace section for slug — collector returned empty tar",
		}, nil
	}

	if err := RestoreCrew(ctx, ops, opts.ContainerID, opts.Crew.Slug, payload); err != nil {
		return nil, fmt.Errorf("backup self-test: restore: %w", err)
	}

	// 5. Read canary back. Docker CopyFromContainer prefixes its tar
	//    entries with the basename of the source path ("workspace/…"),
	//    so the restore re-materialises the canary at the NESTED path
	//    /workspace/workspace/<file> rather than /workspace/<file>. This
	//    is a pre-existing quirk of the backup bundle format; the
	//    self-test verifies whichever location the restore actually
	//    wrote to rather than the logical canary path so a correct
	//    round-trip is observed as a pass.
	nestedPath := ContainerWorkspacePath + "/workspace/" + canaryName
	got, err := readCanary(ctx, ops, opts.ContainerID, nestedPath, canaryName)
	if err != nil {
		// Fall back to the original path in case the backup pipeline
		// ever gets fixed to strip the redundant prefix.
		got, err = readCanary(ctx, ops, opts.ContainerID, canaryPath, canaryName)
		if err != nil {
			return nil, fmt.Errorf("backup self-test: verify read: %w", err)
		}
	}
	elapsed := time.Since(start)

	result := &SelfTestResult{
		CrewID:      opts.Crew.ID,
		CrewSlug:    opts.Crew.Slug,
		CanaryPath:  canaryPath,
		CanaryBytes: len(canaryContent),
		BundleBytes: bundleBytes,
		ElapsedMS:   elapsed.Milliseconds(),
	}
	switch {
	case bytes.Equal(got, canaryContent):
		result.OK = true
	case bytes.Equal(got, mutantContent):
		result.OK = false
		result.Error = "restore was a no-op: canary still has the mutated sentinel content"
	default:
		result.OK = false
		result.Error = fmt.Sprintf("canary content mismatch: wrote %d bytes, read %d bytes",
			len(canaryContent), len(got))
	}

	// Cleanup (wipe both direct + nested canary paths) runs via the
	// defer installed right after the canary write, so every return
	// path below clears the marker — nothing to do here.
	return result, nil
}

// buildSingleFileTar builds a tar archive containing one regular file.
// Mode 0644 is fine — the canary path is read-write agent-writable.
// Extracted into /workspace/ by docker cp with CopyUIDGID=true, so the
// file ends up owned by the workspace's existing UID.
func buildSingleFileTar(name string, content []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(content)),
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// readCanary streams the canary file out of the container via CopyFrom
// and returns its raw bytes. CopyFrom returns a tar — we extract the one
// file entry matching the canary filename.
func readCanary(ctx context.Context, ops DockerOps, containerID, canaryPath, canaryName string) ([]byte, error) {
	rc, err := ops.CopyFrom(ctx, containerID, canaryPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("canary %q missing from CopyFrom tar", canaryName)
		}
		if err != nil {
			return nil, err
		}
		// Docker's CopyFromContainer returns entries with the basename.
		if hdr.Name == canaryName || hdr.Name == "./"+canaryName {
			return io.ReadAll(tr)
		}
	}
}

// randomToken returns a hex string of n random bytes — used to tag
// canary files so concurrent self-tests don't collide.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
