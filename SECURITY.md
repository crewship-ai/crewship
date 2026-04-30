# Security Policy

## Supported versions

Crewship is pre-1.0; we patch security issues on the most recent tagged
release and on `main`. Older tags are not backported. If you are
self-hosting and not tracking `main`, plan to upgrade to the latest tag
when an advisory lands.

| Version           | Security fixes |
|-------------------|----------------|
| `main` (HEAD)     | Yes — patched directly |
| Latest tag        | Yes — patched + new tag cut |
| Older tags        | No — please upgrade |
| `release` branch  | Yes — fast-forwarded from main after fix lands |

We use [GitHub Security Advisories](https://github.com/crewship-ai/crewship/security/advisories)
to coordinate disclosure. Subscribe to the repository's "Security alerts"
notification setting to be told when one is published.

## Reporting a vulnerability

Please do **not** open a public GitHub issue for security problems.

Preferred channel: open a private vulnerability report via
[GitHub Security Advisories](https://github.com/crewship-ai/crewship/security/advisories/new).
This gives both sides a private discussion thread tied to the repo.

Alternative: email **security@unify.cz**. For sensitive reports you may
encrypt the message with our PGP key:

```
PGP fingerprint: TBD — placeholder until the key is published.
```

(If the fingerprint above still reads `TBD` when you find this, please
email us first and we will provide the current key out-of-band.)

Either way, include:

- A description of the issue and its impact.
- Steps to reproduce — a minimal proof-of-concept is ideal.
- The affected version / commit SHA.
- Any suggested fix or mitigation.

You will get an acknowledgement within **3 business days**. We aim to
provide an initial assessment within **7 days** and a fix or coordinated
disclosure plan within **30 days** for confirmed issues. If we cannot
hit those windows we will say so explicitly and propose a revised
timeline rather than going silent.

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
