package encryption

// The inventory of every database column that stores an envelope. It is the
// single source of truth for "where do envelopes live": master-key rotation
// (POST /api/v1/admin/reencrypt) walks it, the instance backup counts
// envelopes per key version from it for the recovery kit, and a backup drill
// decrypts every value in it. If you add a new Encrypt call site that persists
// to a NEW column, add it here in the same change.

// EnvelopeColumn names one envelope-bearing column. Table/Column/Where are
// compile-time constants assembled into SQL — never caller input.
type EnvelopeColumn struct {
	Table  string
	Column string
	// Prefix wraps the standard envelope for purpose-specific documents.
	Prefix string
	// Where is an extra predicate for columns that only SOMETIMES hold
	// envelopes. Currently unused (escalations.resolution left the inventory
	// in #2408) and kept for the next such column.
	Where string
	// FailOpen marks a column that is encrypted at rest only when a key is
	// configured (webhook secrets, #1072). A bare (non-enveloped) value there
	// is EXPECTED legacy/key-less state, not a rotation failure — so it counts
	// as Skipped rather than Failed, keeping the "failed=0 ⇒ retire old key"
	// signal honest.
	FailOpen bool
}

// EnvelopeColumns is the exhaustive envelope-column inventory (verified
// against every encryption.Encrypt call site):
//
//   - credentials.encrypted_value          — main secret / access token
//   - credentials.encrypted_refresh_token  — legacy refresh-token column (v01)
//   - credentials.oauth_client_secret_enc  — OAuth2 client secret
//   - credentials.oauth_refresh_token_enc  — OAuth2 refresh token (v26+)
//   - credential_rotations.old_value       — previous envelope, grace window
//   - notification_channels.secret_enc     — webhook HMAC signing secret
//   - composio_settings.encrypted_api_key  — Composio API key
//   - oauth_states.code_verifier           — PKCE verifier (ephemeral rows)
//   - agents.webhook_secret                — agent webhook signing secret (#1072/#1029)
//   - pipeline_webhooks.signing_secret     — pipeline webhook HMAC key (#1029)
//   - crews.services_json                 — private service documents (crewsvc wrapper)
//   - backup_offsite_destinations.secret_enc — off-site (S3) secret access key
//
// The two webhook columns are FAIL-OPEN at rest (encrypted only when a key is
// configured; #1072). A key-less deployment can't run reencrypt at all, and a
// key-ful one has these enveloped by migration v140 — so any bare row a
// rotation encounters is left untouched (the reencrypt handler's undecryptable path),
// never corrupted.
//
// escalations.resolution is deliberately NOT here (#2408). It held an envelope
// only for a CREDENTIAL escalation a human answered with a typed value; #2379
// ended that path (the value goes to the vault, the escalation resolves with
// NULL) and its migration rewrote every historical row to the plaintext
// marker "[credential submitted]". Expiry, cancel and auto-resolve write
// plaintext prose there too. A target on that column can only ever count
// Failed, which poisons the "failed=0 ⇒ retire old key" gate for good.
//
// Non-SQLite envelope storage (~/.crewship/backup-keyring.enc) is handled
// separately; see the runbook in docs/guides/credentials.mdx.
var EnvelopeColumns = []EnvelopeColumn{
	{Table: "credentials", Column: "encrypted_value"},
	{Table: "credentials", Column: "encrypted_refresh_token"},
	{Table: "credentials", Column: "oauth_client_secret_enc"},
	{Table: "credentials", Column: "oauth_refresh_token_enc"},
	{Table: "credential_rotations", Column: "old_value"},
	{Table: "notification_channels", Column: "secret_enc"},
	{Table: "composio_settings", Column: "encrypted_api_key"},
	{Table: "oauth_states", Column: "code_verifier"},
	{Table: "agents", Column: "webhook_secret", FailOpen: true},
	{Table: "pipeline_webhooks", Column: "signing_secret", FailOpen: true},
	{Table: "crews", Column: "services_json", Where: "services_json LIKE 'crewsvc:%'", Prefix: "crewsvc:"},
	{Table: "backup_offsite_destinations", Column: "secret_enc"},
}
