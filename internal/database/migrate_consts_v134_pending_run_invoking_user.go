package database

// migrationPendingRunInvokingUser (v134) threads the triggering user onto
// deferred runs. Immediate runs already carry invoking_user_id on
// pipeline_runs (so a notify step's `to: trigger` resolves to the caller);
// deferred/debounced triggers parked in pending_runs dropped it, so a
// delayed run's `to: trigger` fell back to a workspace-wide notice. This
// additive column persists the enqueuing user with the pending row; the
// dispatcher reads it back into RunInput.InvokingUserID when the row fires
// (issue #842 Phase 1). Nullable — service/token triggers and pre-migration
// rows carry no user and keep the workspace-notice fallback.
const migrationPendingRunInvokingUser = `
ALTER TABLE pending_runs ADD COLUMN invoking_user_id TEXT;
`
