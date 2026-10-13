-- Legacy unaccepted proofs were historically included in invitation listings.
-- Retire only those pending proofs. An authorized issuer can create a new
-- invitation for the same email or explicitly reissue this row. Preserve
-- accepted history and the new versioned hash-only proofs unchanged.
UPDATE workspace_invitations
SET token = 'revoked:legacy:' || id,
    expires_at = '1970-01-01T00:00:00Z'
WHERE accepted_at IS NULL
  AND token NOT LIKE 'sha256:%'
  AND token NOT LIKE 'revoked:%';
