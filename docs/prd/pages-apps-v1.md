# Pages Apps v1 — current contract

Status: implementation and hardening in progress under #2472. The existing dev3
Operations Lab is a reviewed development demonstration. It uses the explicit
same-origin exception and is not evidence of production process isolation.

This document defines the current product and acceptance criteria. Historical
P0–P5 implementation notes are in [the archived chronology](pages-apps-v1-history-2026-09-09.md).
Current delivery evidence and remaining work are in [the hardening handoff](pages-apps-hardening-handoff-2026-09-09.md)
and [the September 10 validation](pages-apps-validation-2026-09-10.md).
The earlier [audit](pages-apps-review-audit-2026-09-09.md) and
[independent review](pages-apps-independent-review-2026-09-09.md) remain dated evidence,
not a list of defects necessarily still present in the current implementation.

## Customer outcome

An authorized crew agent creates and edits a custom React interface over existing
Page data. A human reviews the source/preview and explicitly publishes it. A reader
opens `/pages/{slug}`, sees data and its age, and can invoke a declared routine only
with the existing server permissions. One portable YAML carries the Page definition
and source project to another installation; target bindings must be resolved there.

The first supported use cases are an operations health overview and a run overview
with a declared action. Repeated collection is a deterministic script/routine, not
a periodic LLM call. Data, displayed UI, action and resulting data share the existing
workspace/crew authorization model.

## Supported surface and trust

- React/TypeScript, one offline Vite profile, custom CSS/layout/animation. The
  installed dependency set and lockfile are fixed. No arbitrary package install,
  custom compiler configuration, public asset directory, SSR or per-Page server.
- The source codec is portable; the current compiler profile restricts paths to
  ASCII letters/digits and `_ . / @ + -`, with no hidden paths. Save reports the
  offending filename. MCP read returns the profile constraints and exact SDK.
- SDK: snapshots, bounded history, declared actions, and status of the caller's
  own action receipts. No general API token, shell, arbitrary network request or
  routine identity supplied by iframe code.
- Authors are trusted and their code must be reviewed. Sandbox/CSP does not make
  arbitrary malicious code safe, prevent every exfiltration channel or enforce
  a portable per-frame memory/CPU budget. Secrets must not enter source/payloads.
- Publication freezes the UI artifact and action declaration. It records routine
  definition hashes, but the routine and its scripts retain their own lifecycle.
  The host confirmation says current definitions/scripts execute and warns about
  observed definition drift; enqueue stores the reviewed and current fingerprints.
- The UI may keep a verified open artifact during temporary storage 503. It must
  remove executable content on authorization failure, withdrawal or Page loss.
  A later publication cannot silently revive a withdrawn cached version.

## Browser and installation contract

| Environment | Custom applications | Panel Pages |
|---|---|---|
| Desktop Chrome/Edge (Chromium), separate runtime site | Supported; stop-loop contract tested | Supported |
| Firefox, Safari/WebKit, iOS and other/mobile engines | Disabled with a clear browser requirement | Available |
| Explicit same-origin development mode | Reviewed demo only, visible isolation limitation | Available |

Runtime uses a second **registrable site**, shared by all Pages in the installation;
not a domain per Page. Both browser-facing origins need reachable DNS and trusted
TLS. The same Go service serves Studio and the restricted runtime bootstrap route.
A sibling subdomain or another port does not meet the production separation rule.
See [installation and operations](../guides/pages-apps-operations.mdx).

## Source, build and publication lifecycle

SQLite owns pointers; immutable source snapshots and Git checkpoints hold exact
source/definition bytes; immutable artifacts hold compiled JS/CSS. Retention keeps
checkpoint hashes unchanged and records shallow boundaries so discarded ancestors
can be reclaimed. Backups carry those boundaries and validate SQL/file integrity.

The default retention window is up to 64 revisions/builds and 32 publications per
Page, bounded by workspace history budgets (128 recent builds/revisions and 64
publications), plus required current-draft/live/running roots. It is an upper bound,
not a promise to retain that many entries regardless of workspace capacity.
When storage pressure requires it, optional history may be reclaimed before a save
or build. Active roots are never silently removed. If active content itself exceeds
a hard quota, an administrator must reduce active content or restore elsewhere.
`page project compact` reclaims eligible history; `--discard-history --yes` explicitly
reclaims optional history across the workspace. `page project fsck <slug>` verifies
referenced sources/checkpoints/artifacts and exits unsuccessfully on corruption.

Builds use one admission slot, no network/crew mounts, a read-only rootfs, UID 1001,
resource limits and an in-container deadline. Compiler errors remain compiler errors;
a completed artifact has a separate persistence budget, and infrastructure failures
are reported as interrupted. Git compression excludes object writers without taking
away the shared lease used by artifact readers and build completion.

A new build requires a tools-image fingerprint matching the server's compiler,
SDK and dependency manifests. A release upgrade installs a matching image before
building; existing immutable publications do not rebuild automatically. Preserve
old image digests and source backups for rollback. V1 SDK changes must preserve
compatibility or introduce an explicit new profile/migration; a runtime identifier
alone does not promise byte-identical rebuilds across releases.

A publish retry returns the receipt of the earlier successful operation together
with the currently observed live version/published state. It does not reactivate a
withdrawn publication or imply that the historical receipt remains live.

## Acceptance criteria and evidence

The following must be measured on a concrete build, with commands/run IDs and actual
results recorded. A passing transport unit test is not a live-agent measurement.

1. A workspace administrator asks a crew agent in chat for a small operations Page.
   The agent creates and edits it through MCP without manual source replacement.
   Target: a usable preview within 15 minutes; record elapsed time and manual steps.
2. A human reviews and publishes the preview. Saving, building or agent output alone
   must never publish. A stale concurrent edit/publication gets a legible conflict.
3. An authorized user invokes a declared action, observes its receipt/result and sees
   updated Page data. Another user cannot read the first user's receipt.
4. A separate reader sees the published application; after revocation or withdrawal,
   cached executable content is removed. Other workspace/crew data remains sealed.
5. Fresh installation, real build, publication, backup/restore and server restart work
   using the documented dependencies and configuration. Browser DNS/TLS reachability
   is checked separately from server-local resolution.
6. Ordinary panel Pages render without waiting for application metadata. Supported
   Chromium can remove an infinite-loop frame without losing the host UI. Unsupported
   browsers receive the documented panel path, not a silently unsupported runtime.
7. Workspace quota/retention, source validation, publication retry, build completion
   during maintenance and retained-checkpoint restoration have regression coverage.
8. The real Docker/MCP/browser CI lane executes its required tests and rejects skips;
   repository verification passes with a suitable explicit test time budget. Delivery
   is divided into reviewable PRs with changelog entries and actual completed review.

Track authoring completion time, manual interventions, build success/failure reasons,
save/build/publication conflicts, storage interruptions and action-to-data outcomes.
These are acceptance measurements; no customer conversion or competitor advantage
is claimed without separate evidence.

## Non-goals

Marketplace, arbitrary npm ecosystem, extra frontend frameworks, business databases,
new permanent app servers, generic API SDK, automatic infrastructure provisioning,
and execution of arbitrary untrusted authors' code are outside v1. New cosmetic
scope must not displace the remaining agent, installation and reviewed-delivery gates.
