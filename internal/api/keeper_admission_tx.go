package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// The callback performs only SQL on tx. Both authority checks share its write
// transaction with the authorization/lease mutation; notification happens only
// after commit. This does not make a later Docker operation atomic with the DB.
func withKeeperRunAdmission(ctx context.Context, db *sql.DB, tr keeperTransition, apply func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("keeper admission: begin: %w", err)
	}
	defer tx.Rollback()
	run, _, err := keeperStoredRun(ctx, tx, tr.RequestID)
	if err != nil {
		return fmt.Errorf("keeper admission: read attribution: %w", err)
	}
	if err := validateKeeperRunTx(ctx, tx, tr.WorkspaceID, tr.AgentID, tr.CrewID, run); err != nil {
		return err
	}
	if err := apply(tx); err != nil {
		return err
	}
	if err := validateKeeperRunTx(ctx, tx, tr.WorkspaceID, tr.AgentID, tr.CrewID, run); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("keeper admission: commit: %w", err)
	}
	return nil
}

type keeperLeaseEffect struct {
	input  leaseIssueInput
	expiry string
	issued bool
}

// Only the caller may publish this effect, after its admission Tx commits.
// Unlike legacy best-effort lease callers, authorization cannot outlive an audit
// failure here: both the grant and its Vault event roll back together.
func issueKeeperLeaseTx(ctx context.Context, tx *sql.Tx, in leaseIssueInput) (keeperLeaseEffect, error) {
	if !in.leaseEligible() {
		return keeperLeaseEffect{}, nil
	}
	now := time.Now().UTC()
	expiry := now.Add(time.Duration(in.TTLSeconds) * time.Second).Format(time.RFC3339)
	result, err := tx.ExecContext(ctx, `UPDATE agent_credentials SET expires_at=?,lease_source=?,lease_issued_at=?,lease_request_id=? WHERE agent_id=? AND credential_id=? AND (expires_at IS NULL OR expires_at<?)`, expiry, in.Source, now.Format(time.RFC3339), nullIfEmpty(in.RequestID), in.AgentID, in.CredentialID, expiry)
	if err != nil {
		return keeperLeaseEffect{}, fmt.Errorf("keeper lease: update: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return keeperLeaseEffect{}, fmt.Errorf("keeper lease: result: %w", err)
	}
	if count == 0 {
		return keeperLeaseEffect{}, nil
	}
	if err := RecordCredentialEventTx(ctx, tx, in.CredentialID, AuditEventLeased, in.AgentID, "", map[string]any{"lease_source": in.Source, "lease_expires_at": expiry, "ttl_seconds": in.TTLSeconds, "security_level": in.SecurityLevel, "request_id": in.RequestID}); err != nil {
		return keeperLeaseEffect{}, fmt.Errorf("keeper lease: audit: %w", err)
	}
	return keeperLeaseEffect{input: in, expiry: expiry, issued: true}, nil
}

func (e keeperLeaseEffect) emit(ctx context.Context, j journal.Emitter, logger *slog.Logger) {
	if !e.issued || j == nil {
		return
	}
	in := e.input
	if _, err := j.Emit(ctx, journal.Entry{WorkspaceID: in.WorkspaceID, CrewID: in.CrewID, AgentID: in.AgentID, Type: journal.EntryCredentialLeaseIssued, Severity: journal.SeverityNotice, ActorType: journal.ActorKeeper, ActorID: "keeper", Summary: fmt.Sprintf("credential %s leased to %s until %s", in.CredentialName, in.AgentName, e.expiry), Payload: map[string]any{"credential_id": in.CredentialID, "credential_name": in.CredentialName, "lease_source": in.Source, "lease_expires_at": e.expiry, "ttl_seconds": in.TTLSeconds, "security_level": in.SecurityLevel, "request_id": in.RequestID}, Refs: map[string]any{"credential_id": in.CredentialID, "keeper_request_id": in.RequestID}}); err != nil && logger != nil {
		logger.Warn("keeper lease: journal emit failed", "error", err, "credential_id", in.CredentialID)
	}
}
