# Privacy Policy

Crewship is self-hosted. Your workspaces, prompts, credentials, and
agent output never leave your machine. The one optional outbound
surface is anonymous crash reporting, whose default depends on the
build flavour (on for prereleases, off for stable releases — details
below). This page documents what that means in practice.

**Controller:** Unify Technology s.r.o. (Czech Republic).
**Contact:** privacy@unify.cz
**Supervisory authority:** Úřad pro ochranu osobních údajů (ÚOOÚ), Czech Republic.

## What the default install does

Out of the box, after `brew install crewship-ai/tap/crewship` or the
equivalent on Linux/Windows:

- Crewship runs entirely on your machine.
- It opens a server on `localhost:8080` for the web UI.
- It writes a SQLite database to `~/.crewship/crewship.db` (or your
  configured data dir).
- It talks to your container runtime (Docker, Podman, OrbStack, …)
  to spawn crew containers.
- It talks to whichever LLM providers you configure (Anthropic,
  OpenAI, …) using the credentials you give it — over HTTPS,
  directly to those providers.

**Crewship sends no usage data to Unify Technology s.r.o.** No usage
analytics, no phone-home on agent runs, no metrics. The one exception
is **crash reporting** (Sentry), which is consent-gated and
build-flavour-dependent — see "Crash reporting" below.

## What we check from the public internet

- **Version check (optional).** `crewship version --check` hits the
  GitHub Releases API once per invocation to see whether a newer
  release exists. GitHub sees your IP and a `User-Agent: crewship/<version>`
  header. We don't see anything — GitHub does. This check is
  off by default; you opt in by running it.
- **Update notifier (background, opt-out).** If enabled in
  `~/.crewship/config.yaml`, the running daemon does the same
  GitHub Releases lookup once per day and surfaces an upgrade banner
  in the web UI. Same data shape — GitHub sees a request, we don't.
  Disable with `CREWSHIP_UPDATE_CHECK=off`.

That's the entire outbound footprint of the default install.

## Crash reporting

Release binaries include anonymous crash reporting (Sentry). The
default follows the build flavour:

- **Prerelease builds** (`-beta.*`, `-rc.*`, dev): **enabled by
  default**; disable any time with `crewship telemetry off`. The
  onboarding wizard also surfaces the consent checkbox.
- **Stable releases** (`v1.0.0` and later): **disabled** until you
  explicitly opt in (`crewship telemetry on`).

What a crash report contains (and what the defense-in-depth scrubbers
strip — prompts, credentials, paths) is documented in the
[Telemetry guide](docs/guides/telemetry.mdx). Consent state lives in
your local database; the browser UI inherits the same consent via
`GET /api/v1/system/telemetry` and fails closed.

## What we don't collect

- ❌ No usage analytics or product metrics.
- ❌ No third-party cookies in the embedded web UI.
- ❌ No phone-home on container starts, agent runs, or LLM calls.
- ❌ No fingerprinting, no advertising IDs, no platform analytics.
- ❌ No prompt, credential, or workspace content in crash reports
  (scrubbed before send; see the Telemetry guide).

## What your data does

Everything Crewship records — workspaces, agents, missions, journal
entries, credentials, conversation history — lives in your local
SQLite database on disk. Encryption at rest for credentials uses
AES-256-GCM (see [docs/security/encryption.mdx](docs/security/encryption.mdx)).

Credentials are injected into agent containers per-request via the
sidecar over a Unix domain socket — they are never written to
container disks or passed as environment variables to the agent
process.

If you back up Crewship state via `crewship backup`, the resulting
bundle is Age-encrypted with your passphrase; nobody outside your
machine has the key.

## What LLM providers see

When Crewship sends a prompt to Anthropic / OpenAI / Google / etc.,
that provider sees the prompt and the response under whatever data
policy they publish. Crewship is the transport, not the controller
of that flow — you chose the provider and the credentials.

Provider documentation:

- [Anthropic Privacy Policy](https://www.anthropic.com/legal/privacy)
- [OpenAI Privacy Policy](https://openai.com/policies/privacy-policy)
- [Google AI Privacy Policy](https://policies.google.com/privacy)

If your workflow requires you to keep prompt contents inside the EU,
configure a provider with EU-region endpoints (Anthropic via AWS
Bedrock `eu-central-1`, Azure OpenAI EU resources, etc.) or
self-host an Ollama instance.

## GDPR / your rights

Crewship processes no personal data beyond what crash reporting
sends when enabled. The following GDPR rights apply to anything we
do collect:

- Right of access (Art. 15)
- Right to rectification (Art. 16)
- Right to erasure (Art. 17)
- Right to restrict processing (Art. 18)
- Right to data portability (Art. 20)
- Right to object (Art. 21)

For requests, email **privacy@unify.cz**. We respond within 30 days.

You can also lodge a complaint with the Czech DPA (ÚOOÚ) or your
local supervisory authority.

## Changes to this policy

This document is versioned in the repository. Material changes
trigger a CHANGELOG entry and an in-app notice in the next release.
Last reviewed: 2026-07-12.
