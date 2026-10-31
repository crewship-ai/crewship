package pagebuild

// The trust anchor for the Pages compiler image (#3108, security review of
// ADR-PAGES-IMAGE-BY-LABEL). The image label alone is not one: Crewship itself
// pulls crew runtime images chosen by workspace managers onto the same Docker
// daemon, and an image built FROM a labelled base inherits the label. So
// `crewship page enable` records the exact image ID it built in a file in the
// operator's page project storage, and the server uses an image only when
//
//   - its ID equals the recorded ID for this release's build context
//     fingerprint,
//   - it carries the exact local tag `crewship-pages-build:<fingerprint[:12]>`,
//   - and its label equals that fingerprint.
//
// Anything else — no record, a record for another release, an image that is
// gone, retagged or relabelled — means Pages Apps are not enabled.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

// BuildImageRecordFile is the record's name inside page_projects_path. Page
// project storage holds only per-workspace hash directories otherwise, and
// backups copy referenced Page files, not this directory, so the record never
// travels to another host.
const BuildImageRecordFile = "pages-build-image.json"

// ImageRepository is the local repository `crewship page enable` tags its
// image into. Crew pulls and builds produce registry-qualified or
// crewship-cache:/crewship-feat: names, never this one.
const ImageRepository = "crewship-pages-build"

const maxRecordBytes = 64 << 10

// ErrNotRecorded marks "nothing recorded for this release" (no record file,
// or no entry for the fingerprint), as opposed to a record that does not
// verify. Both also wrap ErrNotEnabled.
var ErrNotRecorded = errors.New("no compiler image is recorded for this release")

// ImageTag is the exact local tag of the compiler image for a fingerprint: one
// tag per release, so images of different versions sit side by side.
func ImageTag(fingerprint string) string {
	short := fingerprint
	if len(short) > 12 {
		short = short[:12]
	}
	return ImageRepository + ":" + short
}

// RecordedImage is one compiler image `crewship page enable` built.
type RecordedImage struct {
	ID         string    `json:"id"`
	Tag        string    `json:"tag"`
	RecordedAt time.Time `json:"recorded_at,omitzero"`
}

// BuildImageRecord maps a build context fingerprint to the image recorded for
// it. Several releases may be recorded at once (rollback).
type BuildImageRecord struct {
	Version int                      `json:"version"`
	Images  map[string]RecordedImage `json:"images"`
}

// BuildImageRecordPath is where the record lives for a page_projects_path.
func BuildImageRecordPath(projectsDir string) string {
	return filepath.Join(projectsDir, BuildImageRecordFile)
}

// ReadBuildImageRecord reads the record. A missing record is reported as an
// error wrapping os.ErrNotExist. The record is refused when it is not a
// regular file or, on Unix, when anyone but its owner may write it: only the
// operator's `crewship page enable` writes it.
func ReadBuildImageRecord(projectsDir string) (*BuildImageRecord, error) {
	if projectsDir == "" {
		return nil, errors.New("page project storage (CREWSHIP_PAGE_PROJECTS_PATH) is not configured")
	}
	path := BuildImageRecordPath(projectsDir)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s is writable by group or others (mode %v); only the operator may write it", path, info.Mode().Perm())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRecordBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxRecordBytes)
	}
	var rec BuildImageRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return nil, fmt.Errorf("%s is not a valid record: %w", path, err)
	}
	if rec.Version != 1 {
		return nil, fmt.Errorf("%s has unsupported version %d", path, rec.Version)
	}
	if rec.Images == nil {
		rec.Images = map[string]RecordedImage{}
	}
	return &rec, nil
}

// WriteBuildImageRecord replaces the record atomically with mode 0600,
// creating page_projects_path (0700) when needed.
func WriteBuildImageRecord(projectsDir string, rec *BuildImageRecord) error {
	if projectsDir == "" {
		return errors.New("page project storage (CREWSHIP_PAGE_PROJECTS_PATH) is not configured")
	}
	if !filepath.IsAbs(projectsDir) {
		return fmt.Errorf("page project storage %q must be an absolute path", projectsDir)
	}
	if err := os.MkdirAll(projectsDir, 0o700); err != nil {
		return err
	}
	rec.Version = 1
	if rec.Images == nil {
		rec.Images = map[string]RecordedImage{}
	}
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > maxRecordBytes {
		return fmt.Errorf("the record would be %d bytes, over the %d-byte limit the server reads; remove old versions with `crewship page disable --all` first", len(body), maxRecordBytes)
	}
	tmp, err := os.CreateTemp(projectsDir, ".pages-build-image-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := matchDirOwner(name, projectsDir); err != nil {
		return err
	}
	return os.Rename(name, BuildImageRecordPath(projectsDir))
}

// ImageInfo is what `docker image inspect` reports about one image.
type ImageInfo struct {
	ID       string
	RepoTags []string
	Labels   map[string]string
}

// InspectImage asks the docker CLI about one image by ID or tag. A variable
// so tests can replace it.
var InspectImage = inspectImageCLI

func inspectImageCLI(ctx context.Context, ref string) (ImageInfo, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", ref)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return ImageInfo{}, fmt.Errorf("inspect %s: %s", ref, msg)
		}
		return ImageInfo{}, fmt.Errorf("inspect %s: %w", ref, err)
	}
	var images []struct {
		ID       string   `json:"Id"`
		RepoTags []string `json:"RepoTags"`
		Config   struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(out, &images); err != nil {
		return ImageInfo{}, fmt.Errorf("inspect %s: %w", ref, err)
	}
	if len(images) != 1 {
		return ImageInfo{}, fmt.Errorf("inspect %s: %d images", ref, len(images))
	}
	return ImageInfo{ID: images[0].ID, RepoTags: images[0].RepoTags, Labels: images[0].Config.Labels}, nil
}

// hasExactTag reports whether tags contain tag as a local name. Docker reports
// a library-namespace tag either short or fully qualified, depending on the
// image store.
func hasExactTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag || t == "docker.io/library/"+tag {
			return true
		}
	}
	return false
}

// VerifyCompilerImage checks that info is the compiler image recorded as id
// for fingerprint: same ID, the exact local tag and the label.
func VerifyCompilerImage(info ImageInfo, id, fingerprint string) error {
	switch {
	case ValidateImage(id) != nil || !strings.HasPrefix(id, "sha256:"):
		return fmt.Errorf("recorded image %q is not an immutable image ID", id)
	case info.ID != id:
		return fmt.Errorf("image %s is not the recorded image %s", info.ID, id)
	case info.Labels[pageprofile.ImageLabel] != fingerprint:
		return fmt.Errorf("image %s does not carry the %s label for this release", shortID(id), pageprofile.ImageLabel)
	case !hasExactTag(info.RepoTags, ImageTag(fingerprint)):
		return fmt.Errorf("image %s is not tagged %s", shortID(id), ImageTag(fingerprint))
	}
	return nil
}

// RecordedCompilerImage returns the compiler image recorded for fingerprint
// in projectsDir after verifying it against the daemon. Every failure wraps
// ErrNotEnabled: without a verified record there is no compiler.
func RecordedCompilerImage(ctx context.Context, projectsDir, fingerprint string) (string, error) {
	return RecordedCompilerImageWith(ctx, projectsDir, fingerprint, InspectImage)
}

// RecordedCompilerImageWith is RecordedCompilerImage with the inspect step
// supplied by the caller (the CLI's test hook).
func RecordedCompilerImageWith(ctx context.Context, projectsDir, fingerprint string, inspect func(context.Context, string) (ImageInfo, error)) (string, error) {
	rec, err := ReadBuildImageRecord(projectsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w (%w in %s)", ErrNotEnabled, ErrNotRecorded, BuildImageRecordPath(projectsDir))
		}
		return "", fmt.Errorf("%w (%v)", ErrNotEnabled, err)
	}
	entry, ok := rec.Images[fingerprint]
	if !ok {
		return "", fmt.Errorf("%w (%w)", ErrNotEnabled, ErrNotRecorded)
	}
	if entry.Tag != ImageTag(fingerprint) {
		return "", fmt.Errorf("%w (recorded tag %q is not %s)", ErrNotEnabled, entry.Tag, ImageTag(fingerprint))
	}
	if err := ValidateImage(entry.ID); err != nil || !strings.HasPrefix(entry.ID, "sha256:") {
		return "", fmt.Errorf("%w (recorded image %q is not an immutable image ID)", ErrNotEnabled, entry.ID)
	}
	info, err := inspect(ctx, entry.ID)
	if err != nil {
		return "", fmt.Errorf("%w (recorded image %s: %v)", ErrNotEnabled, shortID(entry.ID), err)
	}
	if err := VerifyCompilerImage(info, entry.ID, fingerprint); err != nil {
		return "", fmt.Errorf("%w (%v)", ErrNotEnabled, err)
	}
	return entry.ID, nil
}

func shortID(id string) string {
	trimmed := strings.TrimPrefix(id, "sha256:")
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	return trimmed
}

// BuildImageRecordLockFile is the advisory lock file next to the record that
// serialises read/modify/write cycles of `crewship page enable|disable`.
const BuildImageRecordLockFile = ".pages-build-image.lock"

// matchLockOwner is matchDirOwner, replaceable by tests.
var matchLockOwner = matchDirOwner

// LockBuildImageRecord takes an exclusive cross-process lock for a
// read/modify/write of the record and returns its release function. Atomic
// rename keeps the file whole, but without this lock two operators' cycles
// interleave and the later write silently drops the earlier one's change.
func LockBuildImageRecord(projectsDir string) (func(), error) {
	if projectsDir == "" || !filepath.IsAbs(projectsDir) {
		return nil, fmt.Errorf("page project storage %q must be an absolute path", projectsDir)
	}
	if err := os.MkdirAll(projectsDir, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(projectsDir, BuildImageRecordLockFile)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	// Like the record: a lock file created by root (sudo) belongs to the
	// directory's owner, or that account's later commands are denied.
	if err := matchLockOwner(lockPath, projectsDir); err != nil {
		f.Close()
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil // closing the descriptor releases the flock
}
