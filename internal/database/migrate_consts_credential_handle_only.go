package database

// credential_handle_only — the answer to a credential ask is a grant, not a
// value (#2376).
//
// Two things, one migration, because they are two halves of the same
// decision: the vault becomes the ONLY store of a secret a human supplies to
// an agent, and whether an agent may READ a secret becomes a property of the
// credential row rather than of the path it entered through.
//
//   - credentials.handle_only — "the agent may use this value, never read
//     it". Enforced at loadDeliveredCredentials, the one chokepoint every
//     boot/delegation resolver shares, independent of Keeper state: a
//     handle_only credential is reachable only through /keeper/execute (the
//     value is injected into one command inside the container and scrubbed
//     from the output) or the sidecar proxy. Default 0 so every existing row
//     keeps exactly the delivery it had; every credential created from an
//     agent's ask is written with 1.
//
//   - escalations.resolution for a CREDENTIAL escalation used to hold the
//     human-typed secret, encrypted — a second secret store with no tier, no
//     lease, no rotation, no revoke and no audit timeline, whose only reader
//     handed the plaintext back to the agent. Nothing reads it any more; the
//     rows that were ever written are replaced by the marker the list view
//     already showed for them, so "[credential submitted]" is what a
//     historical row says on every surface, and the ciphertext is gone from
//     the database rather than merely unread.
//
// Timestamp version, per the scheme the sequential block closed at v169 on.
const migrationCredentialHandleOnly = `
ALTER TABLE credentials ADD COLUMN handle_only INTEGER NOT NULL DEFAULT 0;

UPDATE escalations
   SET resolution = '[credential submitted]'
 WHERE type = 'CREDENTIAL'
   AND status = 'RESOLVED'
   AND resolution IS NOT NULL
   AND resolution <> '';
`
