package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
)

const pageProjectsPrefix = "page-projects/"
const maxPageArchiveBytes int64 = 768 << 20

type pageCheckpointRef struct {
	Spec   string
	Digest string
}

type pageFileRefs struct {
	sources   map[string]bool
	artifacts map[string]bool
	commits   map[string]map[string]pageCheckpointRef // Page ID -> commit -> exact SQL source and spec
}

func collectPageFileRefs(dump *DBDump) (pageFileRefs, error) {
	refs := pageFileRefs{sources: map[string]bool{}, artifacts: map[string]bool{}, commits: map[string]map[string]pageCheckpointRef{}}
	if dump == nil {
		return refs, nil
	}
	for _, table := range []string{"page_project_drafts", "page_project_revisions", "page_project_builds", "page_project_publications"} {
		for _, row := range dump.Tables[table] {
			if digest, _ := row["source_digest"].(string); digest != "" {
				if !validPageDigest(digest) {
					return refs, errors.New("invalid Page source pointer")
				}
				refs.sources[digest] = true
			}
			if digest, _ := row["artifact_digest"].(string); digest != "" {
				if !validPageDigest(digest) {
					return refs, errors.New("invalid Page artifact pointer")
				}
				refs.artifacts[digest] = true
			}
			if commit, _ := row["git_commit"].(string); commit != "" {
				id, _ := row["page_id"].(string)
				spec, _ := row["spec_json"].(string)
				sourceDigest, _ := row["source_digest"].(string)
				if id == "" || !pages.ValidProjectCommit(commit) || spec == "" || !validPageDigest(sourceDigest) {
					return refs, errors.New("invalid Page checkpoint pointer")
				}
				if refs.commits[id] == nil {
					refs.commits[id] = map[string]pageCheckpointRef{}
				}
				if old, ok := refs.commits[id][commit]; ok && (old.Spec != spec || old.Digest != sourceDigest) {
					return refs, errors.New("conflicting Page checkpoint definitions")
				}
				refs.commits[id][commit] = pageCheckpointRef{Spec: spec, Digest: sourceDigest}
			}
		}
	}
	if len(refs.sources) > 256 || len(refs.artifacts) > 256 {
		return refs, errors.New("Page archive exceeds workspace object quota")
	}
	return refs, nil
}
func validPageDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}
func (r pageFileRefs) empty() bool {
	return len(r.sources) == 0 && len(r.artifacts) == 0 && len(r.commits) == 0
}
func pageArchiveKey(id string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(id))) }
func originalPageIDs(dump *DBDump) []string {
	var ids []string
	if dump != nil {
		for _, row := range dump.Tables["pages"] {
			id, _ := row["id"].(string)
			ids = append(ids, id)
		}
	}
	return ids
}

// The caller holds a shared project lease from before taking this DB snapshot.
func WritePageProjectsSection(ctx context.Context, dst *TarZstWriter, directory string, dump *DBDump, now time.Time) error {
	refs, err := collectPageFileRefs(dump)
	if err != nil {
		return err
	}
	if refs.empty() {
		return nil
	}
	if directory == "" {
		return errors.New("backup: Page project files exist in the database but page_projects_path is not configured")
	}
	store := &pages.ProjectStore{Directory: directory}
	artifacts := &pagebuild.Store{Directory: filepath.Join(directory, "artifacts")}
	used := int64(0)
	write := func(name string, data []byte) error {
		used += int64(len(data))
		if used > maxPageArchiveBytes {
			return errors.New("Page archive exceeds workspace byte limit")
		}
		return dst.WriteFile(pageProjectsPrefix+name, 0600, now, data)
	}
	keys := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for key := range set {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	}
	for _, digest := range keys(refs.sources) {
		source, err := store.Get(ctx, dump.WorkspaceID, digest)
		if err != nil {
			return fmt.Errorf("backup: missing/corrupt Page source %s: %w", digest, err)
		}
		data, err := source.MarshalYAMLSource()
		if err != nil {
			return err
		}
		if err := write("sources/"+digest+".yaml", data); err != nil {
			return err
		}
	}
	for _, digest := range keys(refs.artifacts) {
		artifact, err := artifacts.Get(ctx, dump.WorkspaceID, digest)
		if err != nil {
			return fmt.Errorf("backup: missing/corrupt Page artifact %s: %w", digest, err)
		}
		data, _, err := artifact.Encode()
		if err != nil {
			return err
		}
		if err := write("artifacts/"+digest+".json", data); err != nil {
			return err
		}
	}
	for page, checkpoints := range refs.commits {
		commits := make([]string, 0, len(checkpoints))
		for commit, spec := range checkpoints {
			source, archived, err := store.ReadCheckpoint(ctx, dump.WorkspaceID, page, commit)
			if err != nil {
				return err
			}
			digest, digestErr := source.Digest()
			if archived != spec.Spec || digestErr != nil || digest != spec.Digest {
				return errors.New("backup: Page Git definition/source differs from database")
			}
			commits = append(commits, commit)
		}
		sort.Strings(commits)
		boundaries, err := store.CheckpointBoundaries(ctx, dump.WorkspaceID, page)
		if err != nil {
			return err
		}
		if len(boundaries) > 0 {
			for _, id := range boundaries {
				if _, ok := checkpoints[id]; !ok {
					return errors.New("backup: checkpoint boundary is not a retained SQL root")
				}
			}
			if err := write("git/"+pageArchiveKey(page)+"/shallow", []byte(strings.Join(boundaries, "\n")+"\n")); err != nil {
				return err
			}
		}
		if err := store.VisitGitObjects(ctx, dump.WorkspaceID, page, commits, func(id, kind string, data []byte) error {
			return write("git/"+pageArchiveKey(page)+"/"+kind+"/"+id, data)
		}); err != nil {
			return err
		}
	}
	return nil
}

// preparePageProjectsRestore verifies the whole section in a temporary namespace
// BEFORE the target DB transaction. The returned apply closure runs under an
// exclusive target lease and before DB commit. Failed applies can leave only
// content-addressed orphans, never a live pointer to unverified code.
func preparePageProjectsRestore(ctx context.Context, payload *ExtractedPayload, directory string, dump *DBDump, originalIDs []string) (apply func(context.Context) error, cleanup func(), err error) {
	noop := func(context.Context) error { return nil }
	refs, err := collectPageFileRefs(dump)
	if err != nil {
		return nil, nil, err
	}
	if refs.empty() {
		if payload.pageProjectsPath != "" {
			return nil, nil, errors.New("restore: unreferenced Page project section")
		}
		return noop, func() {}, nil
	}
	if directory == "" {
		return nil, nil, errors.New("restore: Page project files require page_projects_path")
	}
	if payload.pageProjectsPath == "" {
		return nil, nil, errors.New("restore: Page metadata has no source/Git/artifact section; this is an incomplete backup")
	}
	if len(originalIDs) != len(dump.Tables["pages"]) {
		return nil, nil, errors.New("restore: Page identity mapping changed")
	}
	targetWS := firstWorkspaceID(dump)
	if targetWS == "" {
		return nil, nil, errors.New("restore: missing target workspace identity")
	}
	mapping := map[string]string{}
	archiveKeys := map[string]string{}
	for i, row := range dump.Tables["pages"] {
		id, _ := row["id"].(string)
		if id == "" || originalIDs[i] == "" {
			return nil, nil, errors.New("restore: missing Page identity")
		}
		mapping[pageArchiveKey(originalIDs[i])] = id
		archiveKeys[id] = pageArchiveKey(originalIDs[i])
	}
	staging, err := os.MkdirTemp("", "crewship-pages-restore-")
	if err != nil {
		return nil, nil, err
	}
	cleanup = func() { _ = os.RemoveAll(staging) }
	copyTo := func(ctx context.Context, target string) error {
		stream, err := payload.storageOrDefault().Open(ctx, payload.pageProjectsPath)
		if err != nil {
			return err
		}
		defer stream.Close()
		store := &pages.ProjectStore{Directory: target}
		artifacts := &pagebuild.Store{Directory: filepath.Join(target, "artifacts")}
		tr := tar.NewReader(stream)
		seen := map[string]bool{}
		used := int64(0)
		count := 0
		gitEntries := 0
		boundaries := map[string][]string{}
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			count++
			used += hdr.Size
			if count > 66048 || hdr.Typeflag != tar.TypeReg || hdr.Size < 0 || hdr.Size > pagebuild.MaxArtifactDocumentBytes || used > maxPageArchiveBytes {
				return errors.New("restore: invalid or oversized Page archive entry")
			}
			if seen[hdr.Name] {
				return errors.New("restore: duplicate Page archive entry")
			}
			seen[hdr.Name] = true
			parts := strings.Split(hdr.Name, "/")
			data, err := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
			if err != nil || int64(len(data)) != hdr.Size {
				return errors.New("restore: truncated Page archive entry")
			}
			switch {
			case len(parts) == 2 && parts[0] == "sources":
				digest := strings.TrimSuffix(parts[1], ".yaml")
				if !refs.sources[digest] || parts[1] != digest+".yaml" {
					return errors.New("restore: unreferenced Page source")
				}
				source, err := pages.ParseSourceProject(bytes.NewReader(data))
				if err != nil {
					return err
				}
				actual, err := source.Digest()
				if err != nil || actual != digest {
					return errors.New("restore: Page source checksum mismatch")
				}
				if _, err := store.Put(ctx, targetWS, source); err != nil {
					return err
				}
			case len(parts) == 2 && parts[0] == "artifacts":
				digest := strings.TrimSuffix(parts[1], ".json")
				if !refs.artifacts[digest] || parts[1] != digest+".json" {
					return errors.New("restore: unreferenced Page artifact")
				}
				var artifact pagebuild.Artifact
				if err := json.Unmarshal(data, &artifact); err != nil {
					return err
				}
				_, actual, err := artifact.Encode()
				if err != nil || actual != digest {
					return errors.New("restore: Page artifact checksum mismatch")
				}
				if _, err := artifacts.Put(ctx, targetWS, &artifact); err != nil {
					return err
				}
			case len(parts) == 3 && parts[0] == "git" && parts[2] == "shallow":
				page := mapping[parts[1]]
				if page == "" || len(refs.commits[page]) == 0 {
					return errors.New("restore: boundaries for an unknown Page")
				}
				ids := strings.Fields(string(data))
				if len(ids) == 0 || len(ids) > 4096 {
					return errors.New("restore: invalid checkpoint boundaries")
				}
				for _, id := range ids {
					if _, ok := refs.commits[page][id]; !ok {
						return errors.New("restore: boundary is not a retained SQL checkpoint")
					}
				}
				boundaries[page] = ids
			case len(parts) == 4 && parts[0] == "git":
				gitEntries++
				page := mapping[parts[1]]
				if page == "" || len(refs.commits[page]) == 0 {
					return errors.New("restore: Git object for an unknown Page")
				}
				if err := store.ImportGitObject(ctx, targetWS, page, parts[3], parts[2], data); err != nil {
					return err
				}
			default:
				return errors.New("restore: invalid Page archive path")
			}
		}
		for digest := range refs.sources {
			if !seen["sources/"+digest+".yaml"] {
				return errors.New("restore: missing Page source")
			}
		}
		for digest := range refs.artifacts {
			if !seen["artifacts/"+digest+".json"] {
				return errors.New("restore: missing Page artifact")
			}
		}
		for page, ids := range boundaries {
			if err := store.SetCheckpointBoundaries(ctx, targetWS, page, ids); err != nil {
				return err
			}
		}
		visited := 0
		for page, commits := range refs.commits {
			roots := make([]string, 0, len(commits))
			for commit := range commits {
				roots = append(roots, commit)
			}
			if err := store.VisitGitObjects(ctx, targetWS, page, roots, func(id, kind string, _ []byte) error {
				if !seen["git/"+archiveKeys[page]+"/"+kind+"/"+id] {
					return errors.New("restore: missing reachable Git object")
				}
				visited++
				return nil
			}); err != nil {
				return err
			}
			for commit, spec := range commits {
				source, archived, err := store.ReadCheckpoint(ctx, targetWS, page, commit)
				if err != nil {
					return err
				}
				if archived != spec.Spec {
					return errors.New("restore: Git definition integrity failure")
				}
				digest, err := source.Digest()
				if err != nil || digest != spec.Digest {
					return errors.New("restore: Git source integrity failure")
				}
				if err := store.PinCheckpoint(ctx, targetWS, page, commit); err != nil {
					return err
				}
			}
		}
		if visited != gitEntries {
			return errors.New("restore: unreferenced Git objects")
		}
		if err := store.CheckGitQuota(ctx, targetWS); err != nil {
			return err
		}
		return nil
	}
	if err := copyTo(ctx, staging); err != nil {
		cleanup()
		return nil, nil, err
	}
	return func(ctx context.Context) error {
		target := &pages.ProjectStore{Directory: directory}
		if err := target.CheckGitRestoreQuota(ctx, targetWS, &pages.ProjectStore{Directory: staging}); err != nil {
			return err
		}
		return copyTo(ctx, directory)
	}, cleanup, nil
}
