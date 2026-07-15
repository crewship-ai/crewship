package database

// migrationWorkspaceAllowPrivilegedCredentials (v145) adds the workspace-level
// opt-in gating credential injection into a --privileged crew's sidecar
// (#1032). A privileged container drops no-new-privileges + CapDrop:ALL,
// collapsing the UID 1001 (agent) / 1002 (sidecar) boundary that normally
// keeps a compromised agent from reading the sidecar's process memory — and
// with it the crew-bound IPC token plus any credentials loaded into its
// CredStore. Default 0 (off): the agent-config resolver fails closed and
// omits credentials entirely for a privileged crew unless the workspace
// operator has explicitly accepted that trade-off. Additive, non-nullable
// with a safe default so pre-migration workspaces keep the strict default.
const migrationWorkspaceAllowPrivilegedCredentials = `
ALTER TABLE workspaces ADD COLUMN allow_privileged_credentials INTEGER NOT NULL DEFAULT 0;
`
