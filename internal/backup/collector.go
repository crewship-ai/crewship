package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"
)

// CrewTarget describes a single crew to back up. The runner resolves
// target metadata once up front (via DB query) so the collector can
// run purely against Docker without re-hitting the DB for every step.
type CrewTarget struct {
	ID                 string
	Slug               string
	Name               string
	ContainerID        string
	DevcontainerConfig string
	MiseConfig         string
	RuntimeImage       string
	BaseImageDigest    string
	CachedImageDigest  string
	ConfigHash         string
	AgentCount         int
}

// WorkspaceTarget describes the workspace being backed up along with
// the crews it contains. An empty CrewTargets slice is valid (empty
// workspace) and produces a bundle with only DB rows.
type WorkspaceTarget struct {
	ID          string
	Slug        string
	Name        string
	CrewTargets []CrewTarget
}

// Paths inside the crew container that the collector harvests. These
// match the Docker provider's mount conventions; keeping them in one
// place means the restorer can mirror them without having to consult
// the provider implementation.
//
// /var/lib captures rootfs writable-layer state that isn't on a named
// volume — typically service data dirs (redis, postgresql, mysql,
// mongodb) that an agent installed and started inside the container.
// Without this, the bundle is "user data only" and an admin restoring
// after a wipe gets a healthy /workspace + /home/agent but every
// service the agent stood up is starting from zero. Pure rebuilds-
// from-image content (/var/lib/dpkg, /var/lib/apt, /var/lib/systemd)
// is filtered out at repack time so the bundle stays small.
const (
	ContainerWorkspacePath = "/workspace"
	ContainerHomePath      = "/home/agent"
	ContainerToolsPath     = "/opt/crew-tools"
	ContainerMemoryPath    = "/output"
	ContainerVarLibPath    = "/var/lib"
)

// CollectCrew pauses the crew container, streams its workspace bind,
// named volumes and memory directory into dst (prefixed by the crew's
// slug), and unpauses. Inside dst the layout looks like:
//
//	workspace/<slug>/…   (bind mount contents)
//	volumes/<slug>/home/…
//	volumes/<slug>/tools/…
//	memory/<slug>/…
//	system/<slug>/var-lib/…
//
// level selects which subset is included (Quick keeps just workspace +
// memory; Standard adds the named volumes; Full adds /var/lib).
func CollectCrew(ctx context.Context, ops DockerOps, dst *TarZstWriter, crew CrewTarget, level ScopeLevel) error {
	if crew.ContainerID == "" {
		// Container was never created or was removed. The crew's DB rows
		// still restore; callers see a bundle without per-crew volume
		// data which is the right fallback.
		return nil
	}
	if !level.Valid() {
		level = DefaultScopeLevel
	}
	return WithPaused(ctx, ops, crew.ContainerID, func() error {
		type pair struct {
			src, prefix string
			excludes    []string
		}
		// Quick: just the things that describe the agent's active
		// engagement. workspace = code under edit (vendored deps,
		// node_modules, build outputs all belong to the user — no
		// exclusions, otherwise an offline / committed-deps project
		// silently loses content), memory = the agent's persisted
		// thoughts (tiny, no exclusions to apply).
		pairs := []pair{
			{ContainerWorkspacePath, fmt.Sprintf("workspace/%s", crew.Slug), nil},
			{ContainerMemoryPath, fmt.Sprintf("memory/%s", crew.Slug), nil},
		}
		// Standard adds the named volumes (home dotfiles + installed
		// tools). volumeExclusions trims regenerable caches (mise,
		// pyenv, npm, .yarn/cache) so /home/agent's 1.6 GB does not
		// land in every bundle, while preserving credential paths
		// (~/.config/<tool>/, ~/.aws, ~/.ssh, ~/.docker, ~/.gitconfig).
		if level == ScopeLevelStandard || level == ScopeLevelFull {
			pairs = append(pairs,
				pair{ContainerHomePath, fmt.Sprintf("volumes/%s/home", crew.Slug), volumeExclusions},
				pair{ContainerToolsPath, fmt.Sprintf("volumes/%s/tools", crew.Slug), volumeExclusions},
			)
		}
		// Full adds /var/lib so any service the agent installed
		// (redis, postgresql, mysql, mongo) round-trips its data dir.
		if level == ScopeLevelFull {
			pairs = append(pairs,
				pair{ContainerVarLibPath, fmt.Sprintf("system/%s/var-lib", crew.Slug), varLibExclusions},
			)
		}
		for _, p := range pairs {
			if err := copyContainerPath(ctx, ops, dst, crew.ContainerID, p.src, p.prefix, p.excludes); err != nil {
				return fmt.Errorf("backup: collect %s:%s: %w", crew.Slug, p.src, err)
			}
		}
		return nil
	})
}

// copyContainerPath streams srcPath from the container as a tar and
// repacks it into dst under prefix. Non-existent paths are silently
// skipped — a crew that was never fully provisioned can still be
// backed up without erroring on a missing /opt/crew-tools. excludes
// is the section-specific exclusion list applied at repack time so
// regeneratable content (/home/agent caches, /var/lib/dpkg state)
// never lands in the bundle.
func copyContainerPath(ctx context.Context, ops DockerOps, dst *TarZstWriter, containerID, srcPath, prefix string, excludes []string) error {
	rc, err := ops.CopyFrom(ctx, containerID, srcPath)
	if err != nil {
		// Treat "No such container:path" as skippable; anything else is
		// a hard error.
		if isNotFoundErr(err) {
			return nil
		}
		return err
	}
	defer func() { _ = rc.Close() }()
	if _, err := RepackTarWithExcludes(rc, dst, prefix, excludes); err != nil {
		return err
	}
	return nil
}

// isNotFoundErr returns true if err comes from docker complaining
// about a missing PATH inside an EXISTING container — the signal we
// want to swallow so a never-provisioned crew still produces a bundle
// without its volumes.
//
// Deliberately narrow: a bare "not found" can also mean the container
// itself disappeared (stale CrewTarget), and masking that would
// produce a silent, empty backup. Only moby's two known "path missing"
// phrasings qualify. "No such container" — which contains "not found"
// depending on daemon version — is NOT matched here and therefore
// propagates up as a hard error.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Could not find the file") ||
		strings.Contains(msg, "No such container:path")
}

// LoadWorkspaceTarget resolves a workspace slug-or-id into a full
// WorkspaceTarget, including the list of crews and the docker container
// IDs. It only touches the DB; docker interactions happen later.
//
// crewContainerName is the Docker provider's naming function (typically
// `crewship-team-<slug>`). We pass it as a callback so this package does
// not depend on internal/provider/docker.
func LoadWorkspaceTarget(ctx context.Context, db *sql.DB, workspaceID string, crewContainerName func(slug string) string) (*WorkspaceTarget, error) {
	var wt WorkspaceTarget
	if err := db.QueryRowContext(ctx,
		`SELECT id, slug, name FROM workspaces WHERE id = ?`,
		workspaceID,
	).Scan(&wt.ID, &wt.Slug, &wt.Name); err != nil {
		return nil, fmt.Errorf("backup: load workspace %s: %w", workspaceID, err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, slug, name,
		       COALESCE(devcontainer_config, ''),
		       COALESCE(mise_config, ''),
		       COALESCE(runtime_image, ''),
		       COALESCE(cached_image, ''),
		       COALESCE(config_hash, '')
		  FROM crews
		 WHERE workspace_id = ?
		   AND (deleted_at IS NULL OR deleted_at = '')
		 ORDER BY created_at`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("backup: list crews: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c CrewTarget
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.DevcontainerConfig, &c.MiseConfig, &c.RuntimeImage, &c.CachedImageDigest, &c.ConfigHash); err != nil {
			return nil, err
		}
		if crewContainerName != nil {
			c.ContainerID = crewContainerName(c.Slug)
		}
		// Best-effort agent count; a missing table is not fatal.
		_ = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM agents WHERE crew_id = ?`, c.ID,
		).Scan(&c.AgentCount)
		wt.CrewTargets = append(wt.CrewTargets, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &wt, nil
}

// LoadCrewTarget resolves a single crew for --scope=crew backup.
func LoadCrewTarget(ctx context.Context, db *sql.DB, crewID string, crewContainerName func(slug string) string) (*WorkspaceTarget, error) {
	var crew CrewTarget
	var workspaceID string
	if err := db.QueryRowContext(ctx, `
		SELECT c.id, c.slug, c.name,
		       COALESCE(c.devcontainer_config, ''),
		       COALESCE(c.mise_config, ''),
		       COALESCE(c.runtime_image, ''),
		       COALESCE(c.cached_image, ''),
		       COALESCE(c.config_hash, ''),
		       c.workspace_id
		  FROM crews c
		 WHERE c.id = ? AND (c.deleted_at IS NULL OR c.deleted_at = '')`,
		crewID,
	).Scan(&crew.ID, &crew.Slug, &crew.Name, &crew.DevcontainerConfig, &crew.MiseConfig,
		&crew.RuntimeImage, &crew.CachedImageDigest, &crew.ConfigHash, &workspaceID); err != nil {
		return nil, fmt.Errorf("backup: load crew %s: %w", crewID, err)
	}
	if crewContainerName != nil {
		crew.ContainerID = crewContainerName(crew.Slug)
	}
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE crew_id = ?`, crew.ID,
	).Scan(&crew.AgentCount)

	var wt WorkspaceTarget
	wt.CrewTargets = []CrewTarget{crew}
	if err := db.QueryRowContext(ctx,
		`SELECT id, slug, name FROM workspaces WHERE id = ?`,
		workspaceID,
	).Scan(&wt.ID, &wt.Slug, &wt.Name); err != nil {
		return nil, fmt.Errorf("backup: load workspace %s: %w", workspaceID, err)
	}
	return &wt, nil
}

// WriteDevcontainerSection serializes each crew's devcontainer.json
// and mise.toml into the bundle at devcontainer/<slug>/ so a restore
// can reprovision identical images.
func WriteDevcontainerSection(dst *TarZstWriter, crews []CrewTarget, now time.Time) error {
	for _, c := range crews {
		if c.DevcontainerConfig != "" {
			name := fmt.Sprintf("devcontainer/%s/devcontainer.json", c.Slug)
			if err := dst.WriteFile(name, 0o644, now, []byte(c.DevcontainerConfig)); err != nil {
				return err
			}
		}
		if c.MiseConfig != "" {
			name := fmt.Sprintf("devcontainer/%s/mise.toml", c.Slug)
			if err := dst.WriteFile(name, 0o644, now, []byte(c.MiseConfig)); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteDBSection serializes dump into db/dump.json inside the bundle.
func WriteDBSection(dst *TarZstWriter, dump *DBDump, now time.Time) error {
	data, err := MarshalDump(dump)
	if err != nil {
		return err
	}
	return dst.WriteFile("db/dump.json", 0o644, now, data)
}

// EnsureSectionReader is a small helper: if a bundle payload lacks a
// section (e.g. a crew had no memory), we want to return a no-op reader
// rather than nil so downstream code can always call Read/Close.
// Included here to keep runner.go lean.
func EnsureSectionReader(r io.Reader) io.Reader {
	if r == nil {
		return noopReader{}
	}
	return r
}

type noopReader struct{}

func (noopReader) Read(p []byte) (int, error) { return 0, io.EOF }
