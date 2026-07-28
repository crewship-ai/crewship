-- Credential reveal — the two pieces of persistent state the reveal gate reads
-- (PRD-CREDENTIALS-V2-2026 §2.6, phase P7). Everything else the gate needs
-- already exists: the 5-tier role, the v109 capability JSON on
-- workspace_members, credential_crews for crew scope, and the journal chain.
--
-- 1. credentials.sensitivity (L0) — the classification that decides whether a
--    reveal is possible AT ALL. SEALED has no escape hatch by design: no role,
--    not even OWNER, can reveal it. Break-glass for a SEALED credential is
--    rotation (a new value, the old one draining through the v70 grace
--    window), not disclosure. Default STANDARD so every pre-existing row keeps
--    the weakest classification — which is safe here only because the
--    workspace switch below defaults to OFF, so "STANDARD" alone reveals
--    nothing.
--
--    The CHECK is the enforcement, not a convention: an unrecognised value
--    must be impossible to store, because the reveal gate compares against a
--    closed set and an unknown string would fall through the SEALED test.
--    Go's classification helper (credentials_reveal.go) rejects the same three
--    values, so a drift between the two is a failing test, not a silent hole.
--
-- 2. workspaces.credential_reveal_enabled (L1) — workspace-level default deny.
--    Reveal is OFF for every workspace, including freshly created ones, until
--    an OWNER turns it on. A default of 1 would mean "the security posture of
--    a new tenant depends on someone remembering to lock it", which is the
--    failure mode this layer exists to remove. Mirrors the shape of v145's
--    allow_privileged_credentials: additive, NOT NULL, safe default.
--
-- Both are ALTER TABLE ADD COLUMN, so no table rebuild and no FK dance — the
-- statements run inside the wrapper transaction.

ALTER TABLE credentials ADD COLUMN sensitivity TEXT NOT NULL DEFAULT 'STANDARD'
    CHECK (sensitivity IN ('STANDARD', 'RESTRICTED', 'SEALED'));

ALTER TABLE workspaces ADD COLUMN credential_reveal_enabled INTEGER NOT NULL DEFAULT 0;
