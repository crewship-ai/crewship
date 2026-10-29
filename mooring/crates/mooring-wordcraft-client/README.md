# Experimental WordCraft WASM model facade

This default-off crate exposes the qualified WordCraft model through an actual
WASM ABI. It imports complete native JSON with host-issued stable IDs, restores
opaque application checkpoints, exports full native data/IDs, and projects
bounded metadata and paragraph pages. Native input preflight rejects duplicate
keys without rounding the original native integers through binary64. Restore
requires the caller to authenticate host scope, revision and checkpoint digest.

`bind_agent_change` explicitly builds normalized immutable identity for a new
agent proposal. `preview_agent` requires those exact fingerprints, original
paragraph read observations and a complete preparation context; it returns an
`UnconfirmedWordCraftCandidate` while leaving the original model unchanged. A
browser-supplied context is never host authority. The server independently binds
participants/scope/context, validates the whole request and commits outcomes.
This facade has no authoritative install, network, Saved receipt, human raw Yrs
update admission, speculative causal recovery or native UI binding.

The application compatibility tuple and upstream source qualification identity
come from `mooring-wordcraft::profile`, independent of the facade package version.
Upstream Cargo 0.10.0 is reused 64 commits after its release tag. The qualified
upstream CLI/WASM hashes identify the qualification tools; the built facade and
future service/SDK release assets require separate final hashes and acceptance.
The facade does not embed the upstream WordCraft engine or assert that semver
alone proves binary identity.

Native JSON/identity/proposal/context input uses the existing 262144-byte public
preflight profile. Full opaque checkpoints use a separate 1572864-byte local cap.
Callers should check byte lengths before copying buffers across the WASM ABI.
Header/page limits use canonical decimal strings, avoiding JavaScript uint32
wraparound; stable after-IDs alone do not authenticate remote cursors. The host
binds pages to one immutable confirmed checkpoint/revision and rechecks current
permission before content delivery. Unsupported documents remain fully readable
and conservatively read-only.
