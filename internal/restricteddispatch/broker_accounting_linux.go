//go:build linux

package restricteddispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/paymaster"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

// BrokerReserve resolves all payer/scope fields from durable server authority.
// No scope, principal, key, or model from an agent body can select a budget.
func (a Authority) BrokerReserve(ctx context.Context, handle, credential, model string, maxInput, maxOutput int64) (string, error) {
	attempt, err := a.Store.Resolve(ctx, handle)
	if err != nil {
		return "", err
	}
	binding, err := loadProvider(ctx, a.Store.DB, attempt.ID)
	if err != nil || binding.Credential != credential || binding.Model != model || maxOutput < 1 || maxOutput > binding.MaxOutputTokens {
		return "", access.ErrDenied
	}
	if _, err = a.checkedProvider(ctx, attempt, binding); err != nil {
		return "", err
	}
	var crew string
	if err = a.Store.DB.QueryRowContext(ctx, `SELECT crew_id FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, attempt.Agent, attempt.Workspace).Scan(&crew); err != nil {
		return "", err
	}
	mission, err := a.accountingMission(ctx, attempt)
	if err != nil {
		return "", err
	}
	reserved, err := paymaster.Reserve(ctx, a.Store.DB, paymaster.ReservationRequest{Scope: paymaster.Scope{WorkspaceID: attempt.Workspace, AgentID: attempt.Agent, CrewID: crew, MissionID: mission}, PrincipalID: attempt.Principal, AttemptID: attempt.ID, CredentialID: binding.Credential, Provider: "openai", Model: binding.Model, MaxInputTokens: maxInput, MaxOutputTokens: maxOutput})
	return reserved.ID, err
}

// Settlement is accounting for work already incurred and therefore does not
// require a still-valid grant. The opaque handle must own the reservation.
func (a Authority) BrokerSettle(ctx context.Context, handle, reservation string, usage restrictedruntime.BrokerUsage) error {
	// Resolve may fail after revocation; matching handle digest preserves payer
	// ownership without reauthorizing model execution or revealing resource data.
	digest := sha256.Sum256([]byte(handle))
	var attemptID string
	if err := a.Store.DB.QueryRowContext(ctx, `SELECT id FROM access_attempts WHERE handle_hash=?`, hex.EncodeToString(digest[:])).Scan(&attemptID); err != nil {
		return err
	}
	var owner string
	if err := a.Store.DB.QueryRowContext(ctx, `SELECT attempt_id FROM restricted_cost_reservations WHERE id=?`, reservation).Scan(&owner); err != nil || owner != attemptID {
		return access.ErrDenied
	}
	return paymaster.Settle(ctx, a.Store.DB, reservation, paymaster.ReservationUsage{Known: usage.Known, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CachedInputTokens: usage.CachedInputTokens})
}

var _ restrictedruntime.BrokerAccountingAuthority = Authority{}
