package backupplan

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

// RunSpec is what one run backs up.
type RunSpec struct {
	RunID  string
	PlanID string
	Preset string
	// Scope is instance or workspaces; WorkspaceID is set for workspaces.
	Scope       string
	WorkspaceID string
	// Categories is the resolved list (dependencies included) for a custom
	// run; empty for a full one.
	Categories []string
	// Environments: this run also takes complete container environments
	// (backup.EnvModeComplete).
	Environments bool
	// Exactly one of Recipients or Passphrase encrypts the bundle. A
	// passphrase comes only from a manual run's request and is held in
	// memory until the run uses it; it is never stored.
	Recipients []age.Recipient
	Passphrase string
	Actor      backup.Actor
	// HoldCap is the quiet window's hard cap (the plan's hold_cap_minutes).
	HoldCap time.Duration
	// EncoderConcurrency caps the zstd encoder (settings: cpu_cores).
	EncoderConcurrency int
	// DiskThrottle is the instance-wide disk_mbps limiter, shared by every
	// run; nil when there is no cap.
	DiskThrottle backup.Throttle
}

// RunResult is a bundle an executor wrote.
type RunResult struct {
	Path     string
	Size     int64
	SHA256   string
	Manifest *backup.Manifest
	// HoldMS is how long writes were held for the consistent copy, when
	// the executor measured it itself; nil lets the service measure it.
	HoldMS *int64
}

// Executor writes one bundle. progress is told the create phases
// ("copy", "pack", "encrypt", "write"); an executor that cannot report
// them may stay silent.
type Executor interface {
	Run(ctx context.Context, spec RunSpec, progress func(phase string)) (*RunResult, error)
}

// WorkspaceExecutor writes a workspace bundle with backup.CreateBackup,
// wired like the per-workspace backup endpoint.
type WorkspaceExecutor struct {
	DB                *sql.DB
	DockerOps         backup.DockerOps
	CrewContainerName func(id, slug string) string
	BlobRoot          string
	AttachmentRoot    string
	PageProjectsPath  string
	CrewshipVersion   string
	// OutputDir overrides the default backups directory (tests).
	OutputDir string
	// EnvInline embeds environment layers in the bundle instead of leaving
	// them in the shared store (backup.EnvironmentInlineDefault).
	EnvInline bool
}

// environmentsPhase reads the environments phase's outcome from the written
// bundle's manifest: captured environments, and the crews whose environment
// failed (recorded as incomplete, their files still in the bundle).
func environmentsPhase(m *backup.Manifest) (status, detail string) {
	if m == nil {
		return "skipped", "the bundle has no manifest to read"
	}
	var bytes int64
	inline := false
	for _, e := range m.Contents.Environments {
		bytes += e.Bytes
		inline = inline || e.Inline
	}
	failed := 0
	for _, it := range m.Contents.Incomplete {
		if it.Kind == backup.IncompleteEnvironmentFailed {
			failed += it.Count
		}
	}
	n := len(m.Contents.Environments)
	where := "in the shared environment store"
	if inline {
		where = "inside the bundle"
	}
	switch {
	case n == 0 && failed == 0:
		return "skipped", "no crew container to capture"
	case failed > 0:
		return "failed", fmt.Sprintf("%d environment(s) captured, %d not captured (their files are in the bundle)", n, failed)
	}
	return "done", fmt.Sprintf("%d environment(s) captured, %s of image layers %s", n, humanSize(bytes), where)
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// EnvMode is the backup.CreateOptions.EnvMode a run asks for.
func (s RunSpec) EnvMode() string {
	if s.Environments {
		return backup.EnvModeComplete
	}
	return backup.EnvModeFiles
}

func (e *WorkspaceExecutor) Run(ctx context.Context, spec RunSpec, progress func(string)) (*RunResult, error) {
	level := backup.ScopeLevelStandard
	if spec.Preset == backup.PresetComplete {
		level = backup.ScopeLevelFull
	}
	res, err := backup.CreateBackup(ctx, e.DB, backup.CreateOptions{
		Scope:              backup.ScopeWorkspace,
		WorkspaceID:        spec.WorkspaceID,
		Level:              level,
		OutputDir:          e.OutputDir,
		CrewshipVersion:    e.CrewshipVersion,
		Actor:              spec.Actor,
		Recipients:         spec.Recipients,
		Passphrase:         spec.Passphrase,
		CrewContainerName:  e.CrewContainerName,
		DockerOps:          e.DockerOps,
		BlobRoot:           e.BlobRoot,
		PageProjectsPath:   e.PageProjectsPath,
		AttachmentRoot:     e.AttachmentRoot,
		Categories:         spec.Categories,
		Progress:           progress,
		EncoderConcurrency: spec.EncoderConcurrency,
		DiskThrottle:       spec.DiskThrottle,
		EnvMode:            spec.EnvMode(),
		EnvInline:          e.EnvInline,
	})
	if err != nil {
		return nil, err
	}
	return &RunResult{Path: res.Path, Size: res.Size, SHA256: res.SHA256, Manifest: res.Manifest}, nil
}
