// Package keeper defines the contract between agents and the credential
// gatekeeping subsystem.
//
// Keeper is Crewship's authorisation layer for credential access. When an
// agent requests a secret, the request is sent through its in-container
// sidecar to crewshipd, where the Keeper subsystem evaluates it and
// returns one of four Decisions:
//
//   - ALLOW    — the credential is delivered (or, for /keeper/execute,
//     the command is run with the credential injected and the
//     output scrubbed before being returned to the agent).
//   - DENY     — the request is refused. No credential value ever crosses
//     into the agent process.
//   - ESCALATE — human review is required before the request can resolve.
//   - PENDING  — interim state while an asynchronous decision is in flight.
//
// Each credential has a SecurityLevel (L1–L4) describing the blast
// radius of misuse. L1 covers low-impact tokens (npm, read-only APIs);
// L2 medium (DB read, GitHub write); L3 high (SSH, DB admin, AWS); L4
// is reserved for production-administrative access and currently always
// requires human approval.
//
// The package is intentionally a thin types-only surface. The actual
// decision logic lives in:
//
//   - internal/keeper/gatekeeper — the LLM-backed Evaluator that scores
//     a Request against the agent's task context and conversation
//     history before producing a GatekeeperResponse.
//   - internal/keeper/secrets    — the in-memory DecryptedCredential
//     store that holds credential plaintext inside crewshipd
//     (never inside agent containers or environment variables).
//
// Treat the types defined here as the wire contract: changing field
// names, JSON tags, or the Decision constant set is a breaking change
// for every sidecar build that talks to crewshipd.
package keeper
