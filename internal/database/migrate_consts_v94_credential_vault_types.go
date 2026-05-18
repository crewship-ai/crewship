package database

// migrationAddCredentialVaultTypes (v94) extends the credentials and
// agent_credentials tables to support four new credential types beyond
// the existing API_KEY / OAUTH2 / SECRET set:
//
//	USERPASS         — username (cleartext) + password (encrypted).
//	                   Inject as <name>_USERNAME + <name>_PASSWORD env vars.
//	SSH_KEY          — PEM private key (encrypted). Mount as file
//	                   ~/.ssh/keys/<basename> mode 0600.
//	CERTIFICATE      — PEM cert chain (encrypted). Mount as file
//	                   ~/.crewship/certs/<basename>.pem mode 0600.
//	GENERIC_SECRET   — raw opaque secret (encrypted). Inject as env var.
//
// Positioning: this is what turns Crewship's credentials into a
// "runtime vault for AI agents" — HashiCorp Vault's free-tier
// injection use case with a saner UI. Not a general-purpose
// password manager (no browser autofill, no sharing UI).
//
// Schema impact: additive only.
//
//	credentials:
//	  + username TEXT NULL
//	      Cleartext on purpose — usernames are identifiers, not
//	      secrets, and Bitwarden's vault encrypts only the password
//	      half of a login record for the same reason. Keeping it
//	      cleartext lets us index/search by it without a decrypt
//	      round-trip and shrinks the AEAD surface area.
//
//	agent_credentials:
//	  + mount_type TEXT NOT NULL DEFAULT 'env'
//	      'env'  → inject as env var(s) (current behaviour)
//	      'file' → write to a file inside the container fs
//	  The existing env_var_name column is reinterpreted per mount_type:
//	    env  → the actual env var name (unchanged)
//	    file → the basename (e.g. "github" → ~/.ssh/keys/github).
//	           A helper env var <NAME>_PATH is auto-injected so the
//	           agent can locate the file without hardcoding paths.
//
// The encrypted PEM body for SSH_KEY and CERTIFICATE reuses the
// existing encrypted_value column — same AES-256-GCM path that
// already handles API keys, no second encryption surface to audit.
//
// Backfill: existing rows get NULL username and mount_type='env',
// preserving every current credential's behaviour. No data migration
// needed.
const migrationAddCredentialVaultTypes = `
ALTER TABLE credentials
    ADD COLUMN username TEXT;

ALTER TABLE agent_credentials
    ADD COLUMN mount_type TEXT NOT NULL DEFAULT 'env';
`
