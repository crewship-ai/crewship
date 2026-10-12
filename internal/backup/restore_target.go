package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/slug"
)

// SlugState is what the server holds under one workspace slug.
type SlugState int

const (
	SlugFree SlugState = iota
	SlugLive
	SlugDeleted
)

// SlugLookup reports the state of a workspace slug on this server.
type SlugLookup func(ctx context.Context, slug string) (SlugState, error)

// DBSlugLookup answers SlugLookup from the workspaces table.
func DBSlugLookup(db queryRower) SlugLookup {
	return func(ctx context.Context, slug string) (SlugState, error) {
		var deleted sql.NullString
		err := db.QueryRowContext(ctx, `SELECT deleted_at FROM workspaces WHERE slug = ?`, slug).Scan(&deleted)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return SlugFree, nil
		case err != nil:
			return SlugFree, err
		case deleted.Valid:
			return SlugDeleted, nil
		}
		return SlugLive, nil
	}
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// RestoreTargetSpec names where a restore lands: one of the Target*
// constants, or "" for an in-place restore under the bundle's own identity
// (the CLI's plain `backup restore`). NewName is the slug a new_workspace or
// crew target lands under.
type RestoreTargetSpec struct {
	Target  string
	NewName string
}

// RestoreTargetError is a refused target. Detail is the sentence the Recovery
// page shows; Err is the sentinel the HTTP status follows.
type RestoreTargetError struct {
	Err    error
	Detail string
}

func (e *RestoreTargetError) Error() string { return e.Err.Error() + ": " + e.Detail }
func (e *RestoreTargetError) Unwrap() error { return e.Err }

func refuse(err error, format string, args ...any) error {
	return &RestoreTargetError{Err: err, Detail: fmt.Sprintf(format, args...)}
}

// TargetFromOptions maps the restore flags onto a target, refusing the
// combinations that mean nothing: both --as flags, or --replace (keep the
// bundle's identity) with an --as flag (take a new one).
func TargetFromOptions(o RestoreOptions) (RestoreTargetSpec, error) {
	switch {
	case o.AsWorkspace != "" && o.AsCrew != "":
		return RestoreTargetSpec{}, refuse(ErrInvalidScope, "supply only one of --as-workspace or --as-crew")
	case o.Replace && (o.AsWorkspace != "" || o.AsCrew != ""):
		return RestoreTargetSpec{}, refuse(ErrInvalidScope, "--replace is incompatible with --as-workspace / --as-crew: replace keeps the backup's identity, --as gives it a new one")
	case o.Replace:
		return RestoreTargetSpec{Target: TargetReplace}, nil
	case o.AsWorkspace != "":
		return RestoreTargetSpec{Target: TargetNewWorkspace, NewName: o.AsWorkspace}, nil
	case o.AsCrew != "":
		return RestoreTargetSpec{Target: TargetCrew, NewName: o.AsCrew}, nil
	}
	return RestoreTargetSpec{}, nil
}

// ValidateRestoreTarget is the one rule for where a backup may land. The
// restore checks (EvaluateRestoreChecks) and the restore itself
// (RestoreBackup) both call it, so the Recovery page never approves a target
// the restore then refuses. On success it returns what will happen, in a
// sentence; on refusal a *RestoreTargetError.
//
//   - An instance backup lands only offline: onto an empty server (crewship
//     recover) or as an isolated drill (crewship backup drill) — and those two
//     take nothing else.
//   - Replace needs a workspace backup that holds everything: a custom
//     (partial) backup would delete what it does not carry.
//   - A new workspace needs a workspace backup; a crew target a crew backup.
//   - A new name must be a valid slug and free on this server, deleted
//     workspaces included: workspaces.slug stays UNIQUE after a soft delete,
//     and a crew target lands in a new workspace under the same name.
func ValidateRestoreTarget(ctx context.Context, m *Manifest, t RestoreTargetSpec, slugs SlugLookup) (string, error) {
	if m == nil {
		return "", refuse(ErrInvalidManifest, "the backup has no manifest")
	}
	ws := ""
	if m.Contents.Workspace != nil {
		ws = m.Contents.Workspace.Slug
	}
	offline := t.Target == TargetEmptyServer || t.Target == TargetIsolated
	switch {
	case m.Scope == ScopeInstance && t.Target == "":
		return "", refuse(ErrInvalidScope, "instance scope restore is not supported yet (V1.5): an instance backup restores offline with `crewship recover`")
	case m.Scope == ScopeInstance && !offline:
		return "", refuse(ErrInvalidScope, "an instance backup restores only onto an empty server (crewship recover) or as an isolated drill (crewship backup drill)")
	case m.Scope != ScopeInstance && offline:
		return "", refuse(ErrInvalidScope, "only an instance backup restores onto an empty server or as an isolated drill; this is a %s backup", m.Scope)
	}
	switch t.Target {
	case TargetEmptyServer:
		return "runs offline with `crewship recover` into an empty data directory; nothing on this server changes", nil
	case TargetIsolated:
		return "runs offline with `crewship backup drill` in a throwaway directory; nothing on this server changes", nil
	case "":
		return "restores under the backup's own names", nil
	case TargetReplace:
		if m.Scope != ScopeWorkspace {
			return "", refuse(ErrInvalidScope, "--replace is only supported for workspace-scope bundles; this is a %s backup", m.Scope)
		}
		if m.Kind == KindCustom {
			return "", refuse(ErrInvalidScope, "--replace is refused for a custom bundle (it carries only %s): replacing would delete everything else in the workspace; restore it under a new name", strings.Join(m.Categories, ", "))
		}
		st, err := lookup(ctx, slugs, ws)
		if err != nil {
			return "", err
		}
		switch st {
		case SlugLive:
			return "replaces workspace " + ws + " on this server; its current data is overwritten", nil
		case SlugDeleted:
			return "workspace " + ws + " was deleted on this server; the restore brings it back", nil
		}
		return "workspace " + ws + " is not on this server; it is created", nil
	case TargetNewWorkspace:
		if m.Scope != ScopeWorkspace {
			return "", refuse(ErrInvalidScope, "a new workspace needs a workspace backup (--as-workspace is only valid for workspace-scope bundles; this bundle is %s)", m.Scope)
		}
		if err := checkNewName(ctx, slugs, t.NewName); err != nil {
			return "", err
		}
		return "lands as a new workspace " + t.NewName + "; nothing on this server changes", nil
	case TargetCrew:
		if m.Scope != ScopeCrew {
			return "", refuse(ErrInvalidScope, "a crew target needs a crew backup (--as-crew is only valid for crew-scope bundles; this bundle is %s)", m.Scope)
		}
		if err := checkNewName(ctx, slugs, t.NewName); err != nil {
			return "", err
		}
		return "lands as crew " + t.NewName + " in a new workspace of the same name; no other crew changes", nil
	}
	return "", refuse(ErrInvalidScope, "unknown target %q", t.Target)
}

func checkNewName(ctx context.Context, slugs SlugLookup, name string) error {
	switch {
	case name == "":
		return refuse(ErrInvalidScope, "give the new name it lands under")
	case !slug.Valid(name):
		return refuse(ErrInvalidScope, "%q is not a valid name: use lowercase letters, digits, - and _", name)
	}
	st, err := lookup(ctx, slugs, name)
	if err != nil {
		return err
	}
	switch st {
	case SlugLive:
		return refuse(ErrRestoreNameTaken, "a workspace named %s already exists on this server; pick another name", name)
	case SlugDeleted:
		return refuse(ErrRestoreNameTaken, "%s belonged to a workspace deleted on this server, and the name stays reserved; pick another name", name)
	}
	return nil
}

func lookup(ctx context.Context, slugs SlugLookup, s string) (SlugState, error) {
	if slugs == nil || s == "" {
		return SlugFree, nil
	}
	st, err := slugs(ctx, s)
	if err != nil {
		return SlugFree, fmt.Errorf("backup: look up workspace %q: %w", s, err)
	}
	return st, nil
}
