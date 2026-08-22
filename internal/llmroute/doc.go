// Package llmroute is the dependency-free leaf describing how one LLM provider
// is routed through the sidecar: the path an agent's CLI dials on
// 127.0.0.1:9119, the upstream that path forwards to, and where the credential
// is written on the way out.
//
// Why it is its own package, and a leaf. Three callers need the same table —
// internal/sidecar (routes the request and injects the key), internal/api and
// internal/orchestrator (decide which credential to deliver and which env var
// to withhold). The sidecar must not grow an import edge to internal/llm to
// get it: that package describes how *crewshipd* constructs an outbound
// Provider for keeper-aux calls (a different axis, lowercase ids, no path
// prefix or upstream host at all) and importing it would drag paymaster,
// telemetry, journal, lookout and modelcatalog onto the sidecar side of a
// future cycle. So this package carries NO crewship imports — std-lib only —
// exactly like internal/egressallow, which internal/sidecar/allowlist.go
// documents as the precedent for this move.
//
// The table replaces four parallel encodings of the same three-way choice that
// had to be edited in lockstep to add a provider: handleLocal's hardcoded path
// prefixes, injectCredential's three-arm header switch, providerForHost's host
// switch, and /health's three hardcoded credential counts. A fourth provider
// could not exist until all four read one table.
//
// Nothing here performs I/O, and nothing here holds a secret. ApplyAuth takes
// the token as an argument and writes it onto a request; the token's storage,
// lifetime and redaction remain the sidecar's business.
package llmroute
