# Security Policy

## Reporting a vulnerability

Please do **not** open a public GitHub issue for security problems.

Email **security@unify.cz** with:

- A description of the issue and its impact.
- Steps to reproduce (a minimal proof-of-concept is ideal).
- The affected version / commit SHA.
- Any suggested fix or mitigation.

You will get an acknowledgement within 3 business days. We aim to
provide an initial assessment within 7 days and a fix or coordinated
disclosure plan within 30 days for confirmed issues.

## Scope

In scope:

- The `crewshipd` server (`cmd/crewship/`)
- The crew-side sidecar (`cmd/crewship-sidecar/`)
- The web UI shipped from this repo (`app/`, `components/`)
- The credential vault and Keeper gatekeeper (`internal/keeper/`,
  encryption code in `internal/encryption/`)
- The IPC layer (`/tmp/crewship.sock`, X-Internal-Token auth)

Out of scope:

- Vulnerabilities in third-party LLM providers we connect to.
- Issues that require physical access or root on the host running
  `crewshipd`.
- Findings in user-supplied skills / tool definitions — those are the
  responsibility of the skill author. (Sandboxing concerns at the
  Crewship layer are in scope.)

## What you can expect from us

- Acknowledgement of your report.
- A point of contact for the duration of triage.
- Credit in release notes (if you want it) when the fix lands.
- A clear timeline if the fix takes longer than 30 days.

We don't currently run a paid bug bounty.
