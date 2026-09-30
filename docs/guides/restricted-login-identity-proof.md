# Restricted OpenAI login identity proof

Restricted production provider-login inference is disabled. An identity proof
is not model entitlement, SIWC enrollment, or a subscription spending limit.
The native API-key profile remains the production adapter with a financial
reservation before every upstream request.

`providerlogin.CodexProofStore.Enroll` is a host-only primitive. Its caller must
authorize the operator and exact workspace. There is no public enrollment route
or worker-controlled issuer, account, key endpoint, or token verifier. It verifies
both tokens before a conditional write binds their exact encrypted generations.

The verifier accepts only RS256 tokens signed by the fixed OpenAI issuer and
JWKS endpoint. It requires the legacy Codex ID-token client audience and the
access audience `https://api.openai.com/v1`, matching signed subject, account,
user and plan claims, and bounded integer issuance/expiry times. Tokens whose
legacy backend audience differs fail closed; support for such a protocol has
not been established. A plan label confers no model or billing entitlement.

JWKS retrieval uses the SSRF-safe host transport, no proxy or redirects, a
three-second deadline, a bounded response and key count, a five-minute cache,
and a shared thirty-second minimum refresh interval. An expired key cache and
failed retrieval deny verification.

The existing provider-login refresher changes behavior only for explicitly
proof-enrolled credentials. It verifies a refresh result outside the database
writer transaction, then commits the proof with the rotating access, refresh,
and ID-token ciphertext in the existing transaction. Signed access expiry
wins over the endpoint's estimate. Concurrent replacement, enrollment, proof
deletion, or token substitution denies the commit. Identity verification failure
disables the old proof generation and revokes bound attempts and descendants;
explicit re-enrollment cannot revive those attempts. Unenrolled legacy logins
keep their existing refresh behavior.

Proof rows are private runtime state and must be excluded from backup bundle
forks. They cannot be restored as authorization for a different credential,
workspace, or token generation.

Remaining release gates include an actual supported login audience and account
model-entitlement protocol, a distinct subscription budget contract, a closed
host-owned inference transport, and positive synthetic tests of that adapter.
SIWC enrollment separately requires its issued application client, state, nonce,
PKCE, granted scopes and account model discovery. Existing `auth.json` imports
must never be relabeled as SIWC enrollment.

Primary protocol references:

- [OpenAI SIWC token reference](https://developers.openai.com/siwc/token-reference)
- [OpenAI SIWC models and inference](https://developers.openai.com/siwc/models-and-inference)
- [OpenAI SIWC preview limitations](https://developers.openai.com/siwc/preview-limitations)
- [Codex 0.159.0 authentication source](https://github.com/openai/codex/tree/rust-v0.159.0/codex-rs/login/src/auth)

Verification lives in `codex_identity_test.go`, `codex_proof_test.go`, and the
API `TestProviderLoginRefreshEnrolledIdentityGuard` test. These use synthetic
keys and tokens; they do not enroll an account or make paid inference calls.
