package database

// migrationCredentialApproval (v119) wires the "agent proposes a credential,
// human approves" loop.
//
//   - escalations.credential_id: when a CREDENTIAL escalation carries a proposed
//     secret, the server creates a credential row up front in PENDING_APPROVAL
//     state and links it here. Resolving the escalation then activates (approve)
//     or soft-deletes (reject) that exact row. NULL for every legacy / plain
//     escalation.
//   - credentials.approved_by_user_id / approved_at: who flipped an agent-proposed
//     credential from PENDING_APPROVAL to ACTIVE, and when. This is the named-human
//     attribution the audit model requires at the moment the credential becomes
//     usable; the agent that proposed it stays recorded in created_by_actor_* (v98).
//
// credentials.status stays free-form TEXT (no CHECK), so the new PENDING_APPROVAL
// value needs no column change. All additive and backward-compatible.
const migrationCredentialApproval = `
ALTER TABLE escalations ADD COLUMN credential_id TEXT;
ALTER TABLE credentials ADD COLUMN approved_by_user_id TEXT;
ALTER TABLE credentials ADD COLUMN approved_at TEXT;
`
