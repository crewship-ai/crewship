// Package internaltoken derives and validates workspace-bound
// X-Internal-Token values (PR-F24, token-to-workspace binding).
//
// The master internal token (cfg.Auth.InternalToken) is a single
// process-wide secret. Handing it to every sidecar meant any agent
// that captured it from inside its container (UID escalation, memory
// dump, shared file) could call /api/v1/internal/* on behalf of ANY
// workspace just by picking the ?workspace_id it wanted — the
// documented symmetric cross-tenant bypass (CHANGELOG Unreleased /
// docs/security/threat-model.mdx "Known exception"). Sidecars now
// receive a token derived per workspace instead:
//
//	wsv1.<workspace_id>.<hex(HMAC-SHA256(master, context || workspace_id))>
//
// Properties this buys:
//
//   - A captured sidecar token only authorizes the workspace baked
//     into it. The API middleware re-derives the MAC from the embedded
//     workspace_id and the in-memory master, so a tampered workspace
//     segment fails the constant-time compare.
//   - Derivation is deterministic and stateless — no issuance table,
//     no persistence. Tokens are minted at sidecar start and stay
//     valid for the lifetime of one server boot (the master is
//     regenerated when unset in config, so a restart naturally rolls
//     every derived token together with the master).
//   - The master token itself never enters a container. It remains
//     valid for host-side trusted callers (chatbridge resolver,
//     llmproxy cost monitor) that talk to the internal API over
//     loopback from inside the trust boundary.
//
// The derivation context string is versioned ("v1") so a future
// format change can coexist with old tokens during a migration
// window without ambiguity.
package internaltoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// Prefix marks a workspace-bound internal token on the wire. The
// middleware branches on it to pick the validation path; it is not a
// secret.
const Prefix = "wsv1"

// derivationContext domain-separates this HMAC use from any other
// future HMAC over the same master secret. The trailing NUL keeps the
// context unambiguously delimited from the workspace ID.
const derivationContext = "crewship internal-token workspace binding v1\x00"

// callerSigContext domain-separates the caller-identity HMAC (SignCaller
// / VerifyCaller) from the workspace-binding HMAC above so the two can
// never be confused even though both run over the same key material.
// The trailing NUL delimits the context from the signed payload.
const callerSigContext = "crewship internal-token caller-identity v1\x00"

// agentDerivationContext domain-separates the per-agent token HMAC
// (DeriveAgentToken) from the workspace-binding and caller-identity HMACs
// so a token minted for one purpose can never validate for another even
// though all three run over the same master key. The trailing NUL delimits
// the context from the (workspace_id, agent_id) tuple.
const agentDerivationContext = "crewship internal-token agent binding v1\x00"

// DeriveWorkspaceToken returns the workspace-bound internal token for
// workspaceID, derived from the master internal token. Returns ""
// when either input is empty — an empty master means internal auth is
// unconfigured (every call is rejected anyway), and a token bound to
// an empty workspace must never exist (it could otherwise act as a
// wildcard). Callers must treat "" as "do not issue".
func DeriveWorkspaceToken(master, workspaceID string) string {
	if master == "" || workspaceID == "" {
		return ""
	}
	return Prefix + "." + workspaceID + "." + mac(master, workspaceID)
}

// IsWorkspaceToken reports whether token is shaped like a
// workspace-bound token (prefix match only — call
// ValidateWorkspaceToken to verify it).
func IsWorkspaceToken(token string) bool {
	return strings.HasPrefix(token, Prefix+".")
}

// ValidateWorkspaceToken verifies token against the master secret and
// returns the workspace ID it is bound to. ok is false when the
// token is malformed, the MAC doesn't verify, the bound workspace is
// empty, or master is empty (fail closed — never authorize anything
// without a configured master).
//
// The MAC comparison is constant-time. The workspace ID segment is
// parsed with LastIndex so workspace IDs containing "." would still
// round-trip (the hex MAC can never contain one).
func ValidateWorkspaceToken(master, token string) (workspaceID string, ok bool) {
	if master == "" {
		return "", false
	}
	rest, found := strings.CutPrefix(token, Prefix+".")
	if !found {
		return "", false
	}
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 {
		// No separator, or empty workspace segment ("wsv1..<mac>").
		return "", false
	}
	wsID, sig := rest[:i], rest[i+1:]
	expected := mac(master, wsID)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return "", false
	}
	return wsID, true
}

func mac(master, workspaceID string) string {
	m := hmac.New(sha256.New, []byte(master))
	m.Write([]byte(derivationContext))
	m.Write([]byte(workspaceID))
	return hex.EncodeToString(m.Sum(nil))
}

// AgentPrefix marks a per-agent derived token on the wire (#812). It is not
// a secret; it lets a reader distinguish a per-agent token from a
// workspace-bound one at a glance in logs/config.
const AgentPrefix = "agtv1"

// DeriveAgentToken returns the per-agent bearer token for (workspaceID,
// agentID), derived from the master internal token (#812). The shared
// per-crew sidecar can't trust a caller-supplied `from`/slug — any crew
// member could claim a sibling's identity. A per-agent token can't be
// forged from inside the container (the master never enters it) and maps to
// exactly one agent, so the sidecar can attribute a call to the ACTING agent
// instead of the boot agent or an advisory body field.
//
// Format: agtv1.<workspace_id>.<agent_id>.<hex(HMAC-SHA256(master, ctx ||
// workspace_id || NUL || agent_id))>. The workspace_id and agent_id segments
// are informational (the sidecar matches the whole token by constant-time
// equality against the roster it was minted with); the MAC binds them.
//
// Returns "" when any input is empty — an empty master means internal auth
// is unconfigured, and a token bound to an empty workspace or agent must
// never exist. Callers must treat "" as "do not issue".
func DeriveAgentToken(master, workspaceID, agentID string) string {
	if master == "" || workspaceID == "" || agentID == "" {
		return ""
	}
	return AgentPrefix + "." + workspaceID + "." + agentID + "." + agentMAC(master, workspaceID, agentID)
}

func agentMAC(master, workspaceID, agentID string) string {
	m := hmac.New(sha256.New, []byte(master))
	m.Write([]byte(agentDerivationContext))
	m.Write([]byte(workspaceID))
	m.Write([]byte{0})
	m.Write([]byte(agentID))
	return hex.EncodeToString(m.Sum(nil))
}

// AgentRunPrefix marks a per-RUN derived token on the wire (E0). Like
// AgentPrefix it is a public format marker, not a secret.
//
// It exists because agtv1 could not answer the question E0 asks. A v1 token is
// a pure function of (master, workspace, agent), so two concurrent runs of one
// agent present BYTE-IDENTICAL credentials: the sidecar cannot tell them
// apart, cannot attribute a memory write or a policy decision to the run that
// made it, and cannot stop a finished run's token from still working. v2 binds
// the run.
const AgentRunPrefix = "agtv2"

// agentRunDerivationContext domain-separates v2 from v1 and from every other
// derived token. A v1 token and a v2 token for the same (workspace, agent) must
// not share a MAC — otherwise truncating a v2 token back to v1 would forge one.
const agentRunDerivationContext = "crewship internal-token agent-run binding v2\x00"

const agentRunKeyContext = "crewship agent-run key v2\x00"

// DeriveAgentRunKey returns the purpose-limited key a crew's sidecar uses to
// VALIDATE per-run agent tokens, derived from the master for one
// workspace/crew.
//
// The sidecar cannot be handed the master. It already is not: its IPC bearer
// (sidecarIPCToken) is crew-derived precisely so a compromised sidecar cannot
// mint identities outside its own crew, and a validator that needed the master
// would undo that. This mirrors DeriveLLMRouteKey exactly, for the same reason
// and with the same blast radius: holding it grants the authority to mint and
// verify run tokens for ONE crew, and nothing else.
//
// That is what makes cryptographic validation affordable here. The sidecar's
// roster is frozen at boot, so a token for a run that started later can never
// be recognised by lookup — and restarting the sidecar to admit one would end
// every other run sharing the container. With this key it does not have to
// recognise anything; it verifies.
//
// A crew-less run is scoped to the workspace. Empty master/workspace fails
// closed.
func DeriveAgentRunKey(master, workspaceID, crewID string) string {
	if master == "" || workspaceID == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(master))
	m.Write([]byte(agentRunKeyContext))
	m.Write([]byte(workspaceID))
	m.Write([]byte{0})
	m.Write([]byte(crewID))
	return hex.EncodeToString(m.Sum(nil))
}

// DeriveAgentRunToken returns the per-RUN bearer token for (workspaceID,
// agentID, runID), derived from a crew's run key (DeriveAgentRunKey) — NOT
// from the master, which never reaches the sidecar that has to verify this.
//
// Format: agtv2.<b64(workspace)>.<b64(agent)>.<b64(run)>.<hex(HMAC-SHA256(
// master, ctx || workspace || NUL || agent || NUL || run))>.
//
// The three id segments are base64url (raw, unpadded) rather than literal, so
// an id containing the "." separator cannot split a token into a different
// shape than the one that was signed. agtv1 embeds them literally, which is
// survivable only because every id generator in the tree happens to avoid
// dots; this does not rely on that.
//
// Unlike v1, the segments are not merely informational: ValidateAgentRunToken
// recomputes the MAC over them, so a v2 token VERIFIES rather than needing to
// be recognised. That is the property that lets a sidecar accept a run which
// started after it booted — its roster is frozen at boot (internal/sidecar/
// server.go), so a lookup-based scheme structurally cannot admit a later run
// without a restart, and restarting the sidecar to admit a run would end every
// other run sharing the container.
//
// Returns "" when any input is empty. Callers must treat "" as "do not issue".
func DeriveAgentRunToken(runKey, workspaceID, agentID, runID string) string {
	if runKey == "" || workspaceID == "" || agentID == "" || runID == "" {
		return ""
	}
	enc := base64.RawURLEncoding.EncodeToString
	return AgentRunPrefix + "." +
		enc([]byte(workspaceID)) + "." +
		enc([]byte(agentID)) + "." +
		enc([]byte(runID)) + "." +
		agentRunMAC(runKey, workspaceID, agentID, runID)
}

// ValidateAgentRunToken verifies a v2 token against the crew's run key and
// returns the identity it is bound to. It fails closed on an empty key, a
// wrong prefix, a malformed shape, or a MAC mismatch.
//
// This is the verifier agtv1 never had — nothing in the tree ever recomputed a
// v1 MAC, so "is this token valid?" was only ever answered by equality against
// a roster.
func ValidateAgentRunToken(runKey, token string) (workspaceID, agentID, runID string, ok bool) {
	if runKey == "" {
		return "", "", "", false
	}
	rest, found := strings.CutPrefix(token, AgentRunPrefix+".")
	if !found {
		return "", "", "", false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 4 {
		return "", "", "", false
	}
	dec := func(s string) (string, bool) {
		if s == "" {
			return "", false
		}
		raw, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil || len(raw) == 0 {
			return "", false
		}
		return string(raw), true
	}
	ws, wsOK := dec(parts[0])
	ag, agOK := dec(parts[1])
	run, runOK := dec(parts[2])
	sig := parts[3]
	if !wsOK || !agOK || !runOK || sig == "" {
		return "", "", "", false
	}
	expected := agentRunMAC(runKey, ws, ag, run)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return "", "", "", false
	}
	return ws, ag, run, true
}

func agentRunMAC(runKey, workspaceID, agentID, runID string) string {
	m := hmac.New(sha256.New, []byte(runKey))
	m.Write([]byte(agentRunDerivationContext))
	m.Write([]byte(workspaceID))
	m.Write([]byte{0})
	m.Write([]byte(agentID))
	m.Write([]byte{0})
	m.Write([]byte(runID))
	return hex.EncodeToString(m.Sum(nil))
}

// CrewPrefix marks a crew-bound internal token on the wire (#1159). Like
// Prefix/AgentPrefix it is a public format marker, not a secret; the
// middleware branches on it to pick the crew-binding validation path.
const CrewPrefix = "crwv1"

// RouteFingerprintDelimiter separates the per-agent token embedded in an LLM
// proxy dummy key from the keyed fingerprint of the credential set that run
// expects. It is public framing, not secret material.
const RouteFingerprintDelimiter = "~cfp~"

// LLMRoutePrefix marks a provider-route token that a sidecar can validate
// without holding the process-wide master or a complete crew roster. The key
// is independently derived for this purpose; the agent id is encoded rather
// than trusted, and the HMAC binds it.
const LLMRoutePrefix = "llmrv1"

const llmRouteKeyContext = "crewship llm route key v1\x00"
const llmRouteContext = "crewship llm route identity v1\x00"

// DeriveLLMRouteKey returns a purpose-limited key for validating disposable
// provider-route tokens in one workspace/crew sidecar. It is deliberately not
// an internal-API bearer token: handing it to the sidecar grants no authority
// outside LLM route-token validation. A crew-less run is scoped to the
// workspace. Empty master/workspace input fails closed.
func DeriveLLMRouteKey(master, workspaceID, crewID string) string {
	if master == "" || workspaceID == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(master))
	m.Write([]byte(llmRouteKeyContext))
	m.Write([]byte(workspaceID))
	m.Write([]byte{0})
	m.Write([]byte(crewID))
	return hex.EncodeToString(m.Sum(nil))
}

// DeriveLLMRouteToken creates an agent identity for disposable provider keys.
// routeKey stays in crewshipd + the UID-1002 sidecar; only the derived token is
// placed in the UID-1001 agent environment.
func DeriveLLMRouteToken(routeKey, agentID string) string {
	if routeKey == "" || agentID == "" {
		return ""
	}
	encodedID := base64.RawURLEncoding.EncodeToString([]byte(agentID))
	return LLMRoutePrefix + "." + encodedID + "." + llmRouteMAC(routeKey, agentID)
}

// ValidateLLMRouteToken verifies a derived route token and returns its bound
// agent id. It fails closed on malformed input or an empty route key.
func ValidateLLMRouteToken(routeKey, routeValue string) (string, bool) {
	if routeKey == "" {
		return "", false
	}
	rest, ok := strings.CutPrefix(routeValue, LLMRoutePrefix+".")
	if !ok {
		return "", false
	}
	encodedID, sig, ok := strings.Cut(rest, ".")
	if !ok || encodedID == "" || sig == "" || strings.Contains(sig, ".") {
		return "", false
	}
	rawID, err := base64.RawURLEncoding.DecodeString(encodedID)
	if err != nil || len(rawID) == 0 {
		return "", false
	}
	agentID := string(rawID)
	expected := llmRouteMAC(routeKey, agentID)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return "", false
	}
	return agentID, true
}

func llmRouteMAC(routeKey, agentID string) string {
	m := hmac.New(sha256.New, []byte(routeKey))
	m.Write([]byte(llmRouteContext))
	m.Write([]byte(agentID))
	return hex.EncodeToString(m.Sum(nil))
}

// crewDerivationContext domain-separates the crew-binding HMAC
// (DeriveCrewToken) from the workspace-binding, caller-identity, and
// per-agent HMACs so a token minted for one purpose can never validate for
// another even though all run over the same master key. The trailing NUL
// delimits the context from the (workspace_id, crew_id) tuple.
const crewDerivationContext = "crewship internal-token crew binding v1\x00"

// DeriveCrewToken returns the crew-bound internal token for (workspaceID,
// crewID), derived from the master internal token (#1159). It extends the
// workspace binding down to a single crew: a per-crew sidecar receives this
// as its X-Internal-Token, so the API middleware can inject BOTH the
// workspace and the crew scope server-side instead of trusting a
// caller-supplied ?crew_id (which any workspace-bound-token holder could
// forge to enumerate every crew's credential metadata — the #1031/#1159
// leak).
//
// Format: crwv1.<workspace_id>.<crew_id>.<hex(HMAC-SHA256(master, ctx ||
// workspace_id || NUL || crew_id))>. The workspace_id and crew_id segments
// are informational (the MAC binds them); parsing recovers them.
//
// Returns "" when any input is empty — an empty master means internal auth is
// unconfigured, and a token bound to an empty workspace or crew must never
// exist (either could otherwise act as a wildcard). Callers must treat "" as
// "do not issue" (fail closed).
func DeriveCrewToken(master, workspaceID, crewID string) string {
	if master == "" || workspaceID == "" || crewID == "" {
		return ""
	}
	return CrewPrefix + "." + workspaceID + "." + crewID + "." + crewMAC(master, workspaceID, crewID)
}

// IsCrewToken reports whether token is shaped like a crew-bound token
// (prefix match only — call ValidateCrewToken to verify it). The distinct
// prefix ("crwv1." vs "wsv1.") means a crew token never collides with the
// workspace-token branch in the middleware.
func IsCrewToken(token string) bool {
	return strings.HasPrefix(token, CrewPrefix+".")
}

// ValidateCrewToken verifies token against the master secret and returns the
// (workspaceID, crewID) it is bound to. ok is false when the token is
// malformed, either segment is empty, the MAC doesn't verify, or master is
// empty (fail closed — never authorize anything without a configured
// master). The MAC comparison is constant-time.
//
// Parsing mirrors ValidateWorkspaceToken's LastIndex approach so an ID
// containing "." still round-trips: the hex MAC is taken after the LAST
// dot, then the crew_id after the last dot of the remainder, leaving the
// workspace_id as the head.
func ValidateCrewToken(master, token string) (workspaceID, crewID string, ok bool) {
	if master == "" {
		return "", "", false
	}
	rest, found := strings.CutPrefix(token, CrewPrefix+".")
	if !found {
		return "", "", false
	}
	// rest = <workspace_id>.<crew_id>.<mac>
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 {
		return "", "", false
	}
	head, sig := rest[:i], rest[i+1:]
	j := strings.LastIndexByte(head, '.')
	if j <= 0 {
		// No crew separator, or empty workspace segment.
		return "", "", false
	}
	wsID, crID := head[:j], head[j+1:]
	if wsID == "" || crID == "" || sig == "" {
		return "", "", false
	}
	expected := crewMAC(master, wsID, crID)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return "", "", false
	}
	return wsID, crID, true
}

func crewMAC(master, workspaceID, crewID string) string {
	m := hmac.New(sha256.New, []byte(master))
	m.Write([]byte(crewDerivationContext))
	m.Write([]byte(workspaceID))
	m.Write([]byte{0})
	m.Write([]byte(crewID))
	return hex.EncodeToString(m.Sum(nil))
}

// SignCaller returns a hex HMAC that binds an acting user id to a
// workspace, keyed by the internal token the caller authenticates with
// (the workspace-bound token a sidecar holds, or the master token for
// host-side callers). It is the signature the sidecar attaches as
// X-Caller-Signature alongside the forwarded X-Caller-User-Id so the
// backend can prove the identity was vouched for by a holder of the
// token — and not forged by the agent process inside the container,
// which never sees the token (ID1, PRD §11).
//
// Returns "" when any input is empty (an unsigned identity, which
// VerifyCaller will reject). The workspaceID and callerUserID are
// length-prefix-free but unambiguously delimited by a NUL byte; the
// MAC over the domain-separated context makes cross-use forgery
// infeasible.
func SignCaller(token, workspaceID, callerUserID string) string {
	if token == "" || workspaceID == "" || callerUserID == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(callerSigContext))
	m.Write([]byte(workspaceID))
	m.Write([]byte{0})
	m.Write([]byte(callerUserID))
	return hex.EncodeToString(m.Sum(nil))
}

// VerifyCaller reports whether signature is a valid SignCaller output
// for (token, workspaceID, callerUserID). The comparison is
// constant-time. It fails closed when any input — including the
// signature itself — is empty, so a missing X-Caller-Signature never
// authorizes a caller id.
func VerifyCaller(token, workspaceID, callerUserID, signature string) bool {
	expected := SignCaller(token, workspaceID, callerUserID)
	if expected == "" || signature == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) == 1
}

// fingerprintContext domain-separates the token-fingerprint HMAC from every
// other derivation over the same secret. The fingerprint is a short, one-way
// tag a sidecar can safely advertise on its /health endpoint so the server
// can tell whether the container still holds the CURRENTLY-valid crew-bound
// token (#1385) — without the sidecar ever echoing the token itself. The
// trailing NUL keeps the label a fixed-length prefix.
const fingerprintContext = "crewship internal-token fingerprint v1\x00"

// Fingerprint returns a short, non-reversible tag for an internal token
// (#1385). It is HMAC-SHA256(token, context) truncated to 12 hex chars — the
// same shape as the sidecar's other /health digests (build hash, domains
// hash). Two processes derive the SAME fingerprint for the same token, so the
// server can compare a sidecar-reported fingerprint against the fingerprint of
// the token it WOULD mint today: a mismatch means the container is holding a
// token from a rotated master (an "orphan" after a restart that changed the
// master). Returns "" for an empty token so an unconfigured sidecar reports
// nothing to compare (the server then never false-classifies it as orphaned).
//
// The token keys the HMAC, so the fingerprint is one-way: an agent that reads
// /health cannot recover the token from it, and matching a 48-bit tag grants
// no advantage against the 256-bit crew MAC it would still have to forge.
func Fingerprint(token string) string {
	if token == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(fingerprintContext))
	return hex.EncodeToString(m.Sum(nil))[:12]
}
