# Fictional team demo seed

Extend the existing demo with six separately authenticated colleagues, illustrated
local avatars, real workspace RBAC roles, a shared channel and private greetings.
The existing owner and earlier fixtures are not renamed or modified.

| Colleague | Workspace role | Demonstration function |
| --- | --- | --- |
| Thomas | ADMIN | Platform administrator |
| Paul | MANAGER | Product lead |
| Peter | MEMBER | Software engineer |
| Anna | MANAGER | Design lead |
| Sofia | MEMBER | Quality engineer |
| Emma | VIEWER | Stakeholder |

These are six different work functions, using the four real non-owner RBAC roles;
the seed does not invent custom permission tiers. VIEWER may participate in Chat
under the current participant/workspace ACL. Role differences are tested against
actual work and administration routes, not a fictitious read-only Chat policy.

## Entry points

- `./dev.sh seed` includes the team demo and forwards extra seed flags. Pass
  `--with-team-chat=false` to omit it, or the correct `--profile` for the instance.
- `crewship seed --with-team-chat` includes it in the full seed.
- `crewship seed team-chat --state-dir <private-directory>` adds only the team to
  the currently selected authenticated OWNER workspace, without running the full
  seed or resetting any data.

The old four-account `--with-users` fixture remains separately available for its
existing callers. It is not replaced or assigned new fixed passwords.

## Data and safety contract

The same additive seeding function backs both entry points. Account emails are
fictional `.invalid` addresses scoped deterministically to the workspace. Creation
uses the ordinary provision and account-setup APIs, without signup being enabled
or email delivery. Every human message is sent through that account's own normal
session; owner messages cannot claim another author.

Random credentials, partial setup state and room IDs are stored outside Git in a
private directory, isolated by server/workspace and serialized by a lock. They
are never printed in the JSON result. Existing unowned accounts cause a failure;
`create_only` provisioning rejects an account collision before changing any
membership or setup token. An old server without this capability is rejected
before provisioning. Reruns verify identity and role rather than resetting an
account or silently restoring changed permissions.

All six portraits are bundled PNGs generated with the existing DiceBear Avataaars
package. No remote portrait service or real person's photograph is used. Missing
avatars are initialized; later user profile/avatar changes are preserved.
Stable message IDs and saved room references prevent duplicate demonstration
messages on a normal rerun. Agent execution is not started by this seed.

## Acceptance

Verify six actual memberships and exact roles, decodable distinct portraits,
authenticated messages from all six people, fixed direct pairs and repeatability.
Verify protected state permissions, concurrent invocation exclusion, no account
takeover, old-server refusal, role drift failure and absence of plaintext secrets
in command output. On Dev2, compare allowed and denied operations using harmless
validation/nonexistent-resource probes; inspect actual portraits in a browser.

Implementation and live evidence: [Dev2 verification, 2026-09-07](reports/demo-team-seed-dev2-2026-09-07.md).
