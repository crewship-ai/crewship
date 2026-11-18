package orchestrator

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/credname"
	"github.com/crewship-ai/crewship/internal/credpolicy"
)

// NativeCredentialDeliveryRecorder records a completed file write, independently
// of USE or authentication. The server adapter supplies persistence; no material
// crosses this interface.
type NativeCredentialDeliveryRecorder interface {
	RecordNativeCredentialDelivery(context.Context, string, string, string) error
}

func (o *Orchestrator) recordNativeCredentialDeliveries(ctx context.Context, req AgentRunRequest, keeperEnabled bool) error {
	recorder, ok := o.getAuditLog().(NativeCredentialDeliveryRecorder)
	if !ok {
		return nil
	}
	for _, c := range req.Credentials {
		policy := credpolicy.For(c.Type)
		if !policy.FileMounted() || (keeperEnabled && policy.KeeperGated) || c.ID == "" || c.PlainValue == "" || !credname.Valid(c.EnvVarName) {
			continue
		}
		if err := recorder.RecordNativeCredentialDelivery(ctx, c.ID, req.AgentID, req.RunID); err != nil {
			return fmt.Errorf("record native credential delivery: %w", err)
		}
	}
	return nil
}
