package api

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// StartAuditVerification verifies the full instance and workspace histories at
// boot and hourly. It blocks until server shutdown; the caller owns the
// goroutine. Each sweep has a deadline and uses read snapshots, never a writer
// transaction. Appends continue to authenticate only the tail.
func (r *Router) StartAuditVerification(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		sweep, cancel := context.WithTimeout(ctx, 5*time.Minute)
		if err := r.verifyAuditHistories(sweep); err != nil && ctx.Err() == nil {
			r.logger.Error("periodic audit verification failed", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (r *Router) verifyAuditHistories(ctx context.Context) error {
	provider, ok := r.journal.(interface {
		SigningKeySnapshot() (*journal.SigningKeys, error)
	})
	if !ok {
		return fmt.Errorf("audit signing key snapshot unavailable")
	}
	keys, err := provider.SigningKeySnapshot()
	if err != nil {
		return err
	}
	ctx = journal.WithSigningKeys(ctx, keys)
	// Close the enumeration before verification/alert writes, also on pools
	// with a single connection. Include deleted workspaces' retained history.
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT workspace_id FROM journal_entries ORDER BY workspace_id`)
	if err != nil {
		return err
	}
	var workspaces []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		workspaces = append(workspaces, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	verify := func(workspace string) error {
		tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		var result *journal.VerifyResult
		if workspace == "" {
			result, err = journal.VerifyInstanceAuditWithKeys(ctx, tx, keys)
		} else {
			result, err = journal.VerifyChainWithKeys(ctx, tx, workspace, keys)
		}
		if err != nil {
			return err
		}
		// Release the read snapshot before dispatching the independently written alert.
		if err := tx.Rollback(); err != nil {
			return err
		}
		scope := "workspace"
		if workspace == "" {
			scope = "instance"
		}
		if result.BrokenSeq > 0 || result.BreakCount > 0 || result.HistoricalStatus == "post_upgrade_invalid" {
			r.auditAlerts.raise(ctx, auditChainIncident{Kind: "chain_break", Scope: scope, WorkspaceID: workspace, Seq: result.BrokenSeq, Reason: auditIncidentReason(result)})
		}
		if result.UnchainedFallback > 0 {
			// Seq zero is a separate, stable fallback incident. Counting the alert's
			// own fallback row must not create a fresh incident on every sweep.
			r.auditAlerts.raise(ctx, auditChainIncident{Kind: "chain_break", Scope: scope, WorkspaceID: workspace, Reason: fmt.Sprintf("unchained_fallback: %d audit records outside the chain", result.UnchainedFallback)})
		}
		return nil
	}
	var first error
	if err := verify(""); err != nil {
		first = err
	}
	for _, workspace := range workspaces {
		if err := verify(workspace); err != nil {
			r.logger.Error("periodic workspace audit verification failed", "workspace_id", workspace, "err", err)
			if first == nil {
				first = err
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err := r.retryAuditNotices(ctx); err != nil && first == nil {
		first = err
	}
	return first
}
