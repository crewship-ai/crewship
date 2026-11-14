// Package artifactrun records the trusted identity of a run without a human
// (#5317 step 5, #5339): a routine, webhook or scheduled run that may reach
// artifacts only through its agent's own standing access.
//
// The run token a crew container holds (internaltoken agtv2) cannot carry
// that identity. It is derived from the per-crew run key, which the crew's
// sidecar holds, so anything with that key can mint a valid token for any
// agent and any run id of the crew. For a chat run that is harmless because
// the artifact tool also requires a principal that only trusted human
// admission writes. A run without a human has no such principal, so this
// package gives it one:
//
//   - the HOST records the run (agent, crew, trigger, source) before the
//     agent starts; no HTTP endpoint and no internal token can write it;
//   - the server mints a random capability for that one run and stores only
//     its SHA-256. It is not a function of any key a container holds, so it
//     cannot be derived for another agent or a fresh run id;
//   - the capability ends with the run (End, an expiry, and the run's
//     terminal journal entry, which the API checks on every use).
//
// The capability travels to the crew sidecar (never the agent environment)
// and is attached by it to the artifact tool calls of that run only.
package artifactrun

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Header carries the capability from the crew sidecar to the API.
const Header = "X-Crewship-Run-Capability"

// Prefix marks a capability on the wire. It is a format marker, not a secret.
const Prefix = "urcv1."

// FlagKey is the workspace flag the unattended mode sits behind (the same
// flag as every other agent access mode, artifact_agent_access_modes).
const FlagKey = "artifact_agent_access_modes"

// MaxTTL bounds a recorded run even when its end is never reported.
const MaxTTL = 2 * time.Hour

// Triggers a run without a human may be recorded for.
const (
	TriggerRoutine   = "ROUTINE"
	TriggerWebhook   = "WEBHOOK"
	TriggerScheduled = "SCHEDULED"
)

// Run is what the host knows about the run before the agent starts.
type Run struct {
	WorkspaceID, CrewID, AgentID, RunID string
	Trigger                             string
	// Source names what started the run (pipeline step, webhook, schedule
	// occurrence). Informational; it is copied into the audit.
	Source string
}

// ErrInvalid refuses a run that cannot be recorded.
var ErrInvalid = errors.New("artifactrun: invalid unattended run")

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	featureflags.RowQuerier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Record records run and returns its capability. It returns "" and no error
// when the workspace flag is off, so nothing is minted and the run reaches
// nothing through the artifact path, exactly as before. The capability is
// returned once and never stored in clear.
func Record(ctx context.Context, db Execer, run Run, ttl time.Duration) (string, error) {
	if run.WorkspaceID == "" || run.CrewID == "" || run.AgentID == "" || run.RunID == "" || len(run.Source) > 500 {
		return "", ErrInvalid
	}
	switch run.Trigger {
	case TriggerRoutine, TriggerWebhook, TriggerScheduled:
	default:
		return "", ErrInvalid
	}
	on, err := featureflags.IsEnabledWith(ctx, db, run.WorkspaceID, FlagKey)
	if err != nil {
		return "", err
	}
	if !on {
		return "", nil
	}
	if ttl <= 0 || ttl > MaxTTL {
		ttl = MaxTTL
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return "", err
	}
	capability := Prefix + hex.EncodeToString(secret)
	now := time.Now().UTC()
	// A run id is recorded once. A second Record for the same id (a retry or
	// a replay) is refused rather than handed a second live capability.
	res, err := db.ExecContext(ctx, `INSERT INTO artifact_unattended_runs(run_id,workspace_id,crew_id,agent_id,trigger_type,source_ref,capability_sha256,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO NOTHING`,
		run.RunID, run.WorkspaceID, run.CrewID, run.AgentID, run.Trigger, run.Source, Digest(capability), tsformat.Format(now), tsformat.Format(now.Add(ttl)))
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return "", ErrInvalid
	}
	return capability, nil
}

// End ends the run's capability. Idempotent; a run never recorded is a no-op.
func End(ctx context.Context, db Execer, runID string) error {
	if runID == "" {
		return nil
	}
	_, err := db.ExecContext(ctx, `UPDATE artifact_unattended_runs SET ended_at=? WHERE run_id=? AND ended_at IS NULL`, tsformat.Format(time.Now().UTC()), runID)
	return err
}

// Digest is the stored form of a capability.
func Digest(capability string) string {
	sum := sha256.Sum256([]byte(capability))
	return hex.EncodeToString(sum[:])
}

// Matches reports, in constant time, whether capability is the one whose
// digest was stored. A value without the prefix never matches.
func Matches(capability, storedDigest string) bool {
	if !strings.HasPrefix(capability, Prefix) || len(capability) != len(Prefix)+64 || len(storedDigest) != 64 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(Digest(capability)), []byte(storedDigest)) == 1
}
