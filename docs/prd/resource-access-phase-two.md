# Resource access phase two: crews, credentials and human audiences

Public-Review: https://github.com/crewship-ai/crewship/issues/3273

Status: review draft, 2026-10-18. Tracking: [#3273](https://github.com/crewship-ai/crewship/issues/3273).
This proposes future behavior; none of its new operations, subjects, routes or
migration steps is approved or enabled by this document.
The current contracts remain [member resource policy](../specs/member-resource-policy.md),
[credentials vault](../specs/credentials-vault.md),
[restricted workflows](../specs/restricted-workflows.md) and [Pages](../specs/pages.md).

## Purpose and compatibility

Allow an administrator to share one crew or credential with selected humans,
and explain exactly what that share permits. Support direct users and explicit
human audiences drawn from a crew or workspace without giving those humans the
crew's runtime identity, secrets or unrelated conversations.

Extend the current versioned `access_grants` authority and its live issuer
checks. Do not introduce a competing `resource_grants` authority. Supporting new
resource and subject kinds requires a reviewed extension of the existing model;
the current member-bound schema does not already implement these audiences.
Saved preset choices remain informational; canonical grants authorize use.

Guest remains a restricted access mode. Its account-wide route ceiling,
including mixed trusted/restricted memberships, remains in force. New shares
work only through individually integrated routes. Existing trusted role and
capability defaults remain unchanged; this proposal introduces no selected-only
mode for trusted members and no replacement permission catalog.

Adoption requires explicit administrative action. Existing roles, crew ranks,
agent rights, credential assignments, provider logins and Pages ACLs acquire no
new human grants. Matching old permission bits remain Custom; they do not prove
that anyone selected or consented to a new preset.

## Proposed vocabulary and operation boundaries

View/Use/Manage and See/Use/Manage are presentation bundles with immutable,
versioned definitions. They are not a universal numeric privilege ladder.
Individual operations stay explicit. Manage must not silently include execution,
secret reveal, destructive actions or administration of other people's shares.
The following is a proposed bounded starting catalog, subject to review.

| Bundle | Proposed scope | Still requires separate authority |
| --- | --- | --- |
| Crew View | Crew identity and a minimal list of its live agent identities | Conversations, activity bodies, outputs, costs, files and credential metadata |
| Crew Use | View plus an explicitly selected set of agent Chat or Run operations in this crew | Each target agent, routine, delegation, provider and execution-profile check |
| Crew Manage | View plus named non-secret settings and selected human membership edits | Workspace roles, grant issuance, runtime privilege changes, credential management and destructive operations |
| Credential See | Credential identity, type and a reviewed safe metadata projection | Values, encrypted payloads, secret fields, injected headers, paths, login payloads and hidden dependencies |
| Credential Use | See plus permission to request a specific approved binding to a target the caller independently manages | Target management, slot/scope validation, delivery eligibility, leases and execution authorization |
| Credential Manage | See plus selected metadata/lifecycle operations, with rotate and delete separately confirmed | Reveal, grant issuance, arbitrary binding changes and unrelated runtime authority |

Crew Use does not mean all present or future agents may be invoked automatically.
An implementation must publish whether the selection names individual agents or
an explicitly consented dynamic audience before offering that bundle.
Cross-agent Delegate is a separate explicit operation, never an incidental
consequence of managing a crew or sharing Chat/Run.

Crew Manage must not let a recipient add themselves to a subject crew and thereby
obtain unrelated grants. Membership changes that enlarge authority need an
independent accepted administration rule and a preview of the resulting audience.
Until that rule is approved, those changes remain outside delegated Manage.

Credential Use authorizes a bounded binding request, not a password read and not
permission to boot any agent with the vault's complete contents. A binding change
must identify the exact credential, destination, slot and scope, and explain its
delivery consequences. Workspace-wide bindings are not a default consequence of
an individual share. Agent/crew/workspace delivery precedence and existing lease,
status, provider-account and protected-configuration checks remain authoritative.

The implementation must distinguish existing credential delivery assignments
from the new human authority to administer a binding. A human share does not
become an `agent_credentials` assignment merely because its subject is a crew.
No fallback may select a different, broader credential after the chosen one is
hidden, revoked, expired or unavailable.

## Reveal and execution are separate

See and Use return no plaintext. Manage does not grant Reveal. Reveal retains
its interactive-human boundary, workspace opt-in, role and `credentials:reveal`
requirements, fresh authentication, reason, sensitivity and acknowledged audit.
SEALED credentials remain non-revealable. CLI tokens, agents and sidecars do not
become reveal callers through any proposed bundle.

The proposed See response is closed: resource ID, deliberately shared display
label and credential type. Usernames, custom fields, provider-account identity,
delivery targets, lease contents and internal paths are excluded by default.
Raw provider or runtime errors must not become metadata. The sharing preview
warns that an author-written label can itself contain sensitive information.

Creation and rotation may themselves disclose a newly supplied/generated value.
That is a separate disclosure consequence to review before delegating those
operations; it cannot be hidden inside a label saying only Manage.
Provider login and refresh payloads remain outside the metadata projection.

Execution requires the actual human's live authority intersected with the exact
selected recipe and all target agent, delegation, provider, credential and
runtime ceilings. A View/Use share does not select an execution principal or
supply missing provenance. Private origin audiences and result authority remain
independent of crew membership and credential possession.

Unavailable qualified execution fails closed. There is no fallback to a trusted
full agent runner, shared container or unrestricted credential delivery.
[#3274](https://github.com/crewship-ai/crewship/issues/3274) still owns the broker/profile
choice; this proposal neither selects it nor activates Guest execution.

## Human subjects and inheritance

Every new share identifies its workspace, live resource incarnation, subject
kind and incarnation, operation set/version, issuer and creation provenance.
All subject expansion is into human memberships of that same workspace.
Agents, containers and team service identities are not human subjects.

| Subject | Proposed live expansion | Required fences |
| --- | --- | --- |
| User | One current local workspace membership | Active account, exact membership incarnation and revision |
| Crew | Current human members of the named local crew who also have current local workspace membership | Crew incarnation, membership changes, local membership incarnation and issuer |
| Workspace | Current human memberships of the named workspace | Workspace incarnation, local membership changes and explicitly approved Guest inclusion |

The share dialog must state whether future joiners receive access. Proposed
safe default: dynamic crew/workspace audiences are unavailable until their
inclusion rules are accepted; an administrator can instead select explicit
current users. An accepted dynamic audience must explain the expansion before
confirmation and invalidate affected admissions when membership changes.
No cross-workspace identity match, nested crew expansion or agent membership
may substitute for a local human membership.

A crew share does not automatically expose crew-scoped credentials, agent
transcripts or regular run history. Credential metadata needs its own explicit
share or a separately accepted bounded metadata projection. An agent identity
projection must not expose hidden resource references through links or counts.

Inheritance is limited to approved edges and operations. It cannot cross a
workspace, imply credential Use from crew View, imply Delegate from Run, or
imply Page data access from an action. Multiple valid grant paths retain their
own issuers; removing one path must neither erase nor invent another.

## Live authority, incarnations and revocation

Each integrated operation rechecks the authenticated caller and credential,
route ceiling, scopes, applicable role/capability floors, local membership,
resource/subject incarnation and every relied-on issuer. Issuers retain the
current active trusted local OWNER/ADMIN requirement and global Guest ceiling.
Managing an object does not make its recipient a grant issuer.

Retain issuer ID and original issuance time on unchanged shares. An unavailable
issuer makes its share inert; an unrelated policy edit must not reissue it.
Explicit reissue identifies the affected operations, uses the current issuer
and records new provenance. Reissue may authorize new work; it must not revive
an old revoked execution or detached receipt.

Removal and recreation create new membership/resource incarnations. IDs, names,
slugs, restored metadata or equal permission bits alone must not resurrect an
old admission. Restore/fork and erasure need an explicit compatible design;
imported grant JSON is not authority or evidence of an original issuer.

Revocation fences new reads, mutations, admission and subsequent protected
execution boundaries. It cannot undo an external operation already completed.
The accepted runtime contract must define cancellation, reconciliation and any
remaining external effect before activation. Cached explanations are not live
authorization; failed checks must not reuse a previous successful decision.

This proposal adds no default human grant expiry. Invitation expiry, credential
leases and a human grant's lifetime are different contracts. The lifetime of a
new bounded human run after valid admission remains undecided: run-scoped consent
with live actor/policy checks, exact original credential lifetime, or continued
unsupported status require a separate decision. No fresh credential for the same
user may silently substitute for missing original source authority.

## Administration, explanations and audit

Use the existing member-policy and access-explanation family as the integration
starting point. Extend it only through reviewed versioned resource/subject
schemas; no second canonical permission service is introduced by this draft.
Only current authorized administrators may inspect other people's saved shares.
Self explanations use the authenticated identity, not a query-selected actor.

For each requested operation explain saved intent, direct/via-crew/via-workspace
paths, live membership and issuer state, then independent blocking gates.
Distinguish permission denied, conditional execution, operationally unavailable,
expired and stale incarnation. Display names only when independently discoverable.
Hidden objects remain opaque in errors, totals, search, suggestions and events.
No explanation includes a secret, ciphertext, token, injected header or path.

A grant write previews exact incarnations/revisions, bundle version/hash,
canonical before/after rights and issuer provenance, future audience expansion,
secret-disclosure and destructive consequences. Apply recomputes those conditions
inside the writer transaction with policy changes and content-free audit.
A confirmation digest is not authorization. A conflict requires new review;
API, CLI and UI must not refresh consent or retry writes silently.

Audit must identify the authenticated actor, affected resource/subject
incarnations, operation and issuer changes, versioned bundle and outcome, without
secret material. Failed mandatory audit rolls back authority changes. Execution
and reveal keep their distinct audit contracts; saving a share is not proof of
a successful delivery, run or reveal.

## Proposed API, CLI and UI delivery

First extend closed policy/preview/apply schemas and their OpenAPI components;
endpoint names and command syntax remain proposals pending transport review.
Every accepted operation needs API and CLI parity, including explicit reissue,
conflict handling, safe machine output and effective explanation.
Object Share and member Access views should edit the same canonical authority.

An object-oriented CLI could expose `share crew`, `share credential` and their
list/unshare forms, backed by the same versioned administrative workflow.
Human subject syntax must distinguish user, crew and workspace unambiguously.
A command must show audience and binding/disclosure consequences before apply;
no automatic lookup-and-retry may repair a stale membership or consent token.
Reveal continues using its existing interactive policy, not these commands.

The UI must show explicit operations rather than imply that Manage includes Use.
Inherited paths are inspectable, saved inactive shares are labelled, and changing
workspace/resource/subject clears stale previews. Hard access loss clears
protected content. A transient error cannot restore previously denied content.
List pagination, counts, related references and realtime fan-out use the same
visibility predicate before disclosure. Bound query counts and response sizes.

## Rollout and required acceptance

Deliver reviewed code prerequisites before enabling a new authority surface.
Schema must be append-only, with workspace-qualified integrity and leading FK
indexes, complete backup/category handling, ID remapping and scoped erasure.
Do not ship partial generic import that can manufacture authority. Preserve
existing records and explain unsupported restore cases before any mutation.
No automatic backfill, Pages migration, credential fan-out or role conversion.
Removal of an opt-in policy is a reviewed change, not a rollback that resurrects
old grants, attempts or unrelated concurrent edits.

| Area | Required acceptance before that surface is enabled |
| --- | --- |
| Compatibility | Existing trusted defaults and explicit agent/project/routine rights unchanged; no inferred presets or new credential delivery |
| Subjects | Direct user, crew and workspace positives; foreign, removed/re-added, suspended and nonlocal humans denied; future-joiner consent checked |
| Issuers | Mixed issuers, demotion, removal, suspension and Guest elsewhere; inert provenance retained and explicit reissue cannot revive old work |
| Crew | Each allowed setting, identity and invocation operation tested independently; private activity hidden; no self-membership escalation |
| Credential | See, binding Use, lifecycle changes and Reveal separated; provider/SEALED rules, scopes, lease/revocation and slot precedence retained |
| Concurrency | Exact revisions/incarnations, stale preview, simultaneous subject/issuer/resource change, audit failure and uncertain outcomes |
| Exposure | Dedicated lists, detail, related links, counts, search, files, events and explanations reveal only authorized projections |
| Execution | Exact caller/source/recipe/provider intersection and revocation before effects; no generic fallback; deferred/resume guarantees separately demonstrated |
| Lifecycle | New/restore/fork/erasure paths, preserved inert records and missing provenance fail closed without changing unrelated authority |
| Transport | Real authenticated API and built CLI positives/negatives, UI draft/consent invalidation; synthetic checks distinguished from runtime acceptance |

## Decisions explicitly left open

The following cross-cutting reforms are not approved by this proposal:

1. A workspace-wide deny/allow action catalog, including role denials,
   delegatable allow bounds and OWNER exceptions.
2. A selected-routine/Page mode for trusted members instead of current defaults.
3. A separate consumption authority table instead of extending `access_grants`,
   including complete live issuer provenance.
4. Permanent grant deletion on publication/specification changes versus retained
   inert shares with explanations and deliberate reissue.
5. Unifying Page data ACLs with action grants or migrating existing Pages data.
6. A new canonical effective-action UI catalog versus extending existing
   `/access/me` and administrative `/access/effective`, retaining ACL and
   conditional checks in either case.

For this phase, reviewers must additionally decide the exact Crew Use/Manage
operations, which Credential Manage operations may reach restricted humans,
dynamic audience/future-joiner inclusion and any metadata inheritance.
The bounded defaults above are recommendations, not accepted decisions.
Broker/profile selection and bounded-human lifetime remain separate open gates.
Publishing this review draft records a proposal, not accepted policy. Explicit
decisions must be recorded before implementation; acceptance and activation
require their own reviewed work.
