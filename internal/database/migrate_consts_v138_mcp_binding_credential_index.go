package database

// migrationMCPBindingCredentialIndex (v138) adds an index on
// agent_mcp_bindings(credential_id) (issue #1042).
//
// Two hot credential paths scan by credential_id with no supporting index:
//   - loadMCPUsedBatch (credentials_loaders.go), on every GET /credentials:
//     SELECT DISTINCT credential_id FROM agent_mcp_bindings WHERE credential_id IN (…)
//   - CredentialHandler.Delete: UPDATE … WHERE credential_id = ?
//
// The table only carried indexes on agent_id and (mcp_server_id,
// mcp_server_scope) (v26–v32), so both did a full table scan that worsens
// linearly with binding count. This adds the missing index.
const migrationMCPBindingCredentialIndex = `
CREATE INDEX IF NOT EXISTS idx_agent_mcp_bindings_credential_id
    ON agent_mcp_bindings(credential_id);
`
