# Crews workspace implementation — Dev2

2026-09-08 · issue #2463 · branch `feat/crews-workspace-memory`.

Implements the first complete release (delivery priorities 1–3) from
[the approved wireframe PRD](crews-wireframe-delivery-2026-09-08.md), with
knowledge proposals linked to the existing Inbox decision surface.
The HTML wireframes remain offline design references; the implementation
uses the application's components, tokens, API and permissions.

## Delivered behavior

| Surface | Implementation |
|---|---|
| Fleet and sidebar | Crew cards show purpose and agents. The existing avatar tree remains, idle badges are reduced, and search queries the server with paging. Detail URLs resolve entities outside the initially loaded roster. Files receives the resolved selection. |
| Agent | Overview, Work and Memory are separate tabs. Overview shows current runs, up to three scoped run metrics, five recent outcomes and five conversations. Historical peer messages are optional context, not approval counts. |
| Crew | Overview, Team, Work and Memory. Team loads a crew-scoped paginated roster. Overview limits activity to five entries with a Journal link. Runtime, policy, integrations and other administration remain accessible. |
| Edit | Create/Edit use the same agent and crew dialogs. Edits submit changed fields, preserve custom models, use explicit Save/Cancel and retain drafts on failure. Crew creation is purpose → team → review, with environment details in a disclosure. Billing bindings and crew administration explicitly retain their own save actions. |
| Chat forms | A visual form/field builder with an advanced JSON fallback uses the existing validation and limits. Suggestions and forms are accepted on both create and update. |
| Knowledge | New agent/crew inventory reads current files without launching a container. It separates agent, crew and configured workspace scopes. Empty files, no notes, oversized/unreadable notes and unavailable storage are different states. Personal profiles and persona files are excluded. |
| History/export | History is loaded separately from the current note; individual recorded snapshots can be opened. Missing projection never replaces current content with an empty file. Current notes can be downloaded; full archive export retains the administrator gate. |
| About me | The Memory tab and Settings Privacy share self-service preferences, individual/all preference deletion, existing peer notes and personalization consent. Reads and mutations use the signed-in user. |
| Personalization runtime | The authoritative workspace/user index determines the model even across crews. No stale crew-local fallback when the indexed reader is configured. Group chats omit personal opener profiles. Consent is checked before using either personal model or peer card. |
| Truthful counts | Run scopes are applied before aggregation and the 20,000-run cap; the cap is labeled. Work status totals use the complete filtered query before pagination. Partial inbox read failures are explicit and unavailable costs are omitted. |

## API changes

- `GET /api/v1/agents/{agentId}/memory` and `GET /api/v1/crews/{crewId}/memory`:
  current documents, per-scope availability, revision hashes and separate history paths.
  Workspace isolation, anchored directory reads, symlink exclusion and size limits apply.
- `/api/v1/runs/insights`: optional `agent_id` and `crew_id`; crew history uses
  recorded run ownership, so moving an agent does not move past work.
- `/api/v1/issues?counts=1`: `X-Status-Counts` supplements existing list totals.
- Agent create accepts `suggested_prompts` and `ask_forms` with update-equivalent validation.
- Agent inbox adds `unavailable` for failed component queries.
- Agent peer list/read/delete are self-only, including for owners/admins.
  General member memory-history routes reject personal `peers/` and `users/` paths.
  Explicit administrator export/compliance routes retain their own role policy.
- OpenAPI is regenerated, with concrete inventory response schemas.

## Existing limits shown honestly

This does not claim to finish the experimental peer-card generator: automatic
agent-specific personal profiles remain unavailable and the UI says so.
Existing profiles can still be read and forgotten. User-model facts currently
have no original-message evidence or per-run usage evidence; the UI does not
invent either. Those provenance additions are priority 4 in the PRD.

Knowledge proposals open the existing filtered Inbox, preserving its proposal,
diff, authorization and decision handlers. There is no second approval store.
A freely configurable dashboard builder is intentionally outside the PRD.

Persona and agent settings are separate backend operations. If the persona
save fails after settings succeeded, the editor reports the partial result and
retains the draft for retry. Billing access is explicitly managed independently.

## Verification

- Crews and related orchestration UI: 74 files / 844 tests passed.
- Focused API contracts: inventory, workspace/subject isolation, custom-model
  preservation, scoped aggregates/status counts, OpenAPI and personal-memory
  consent/group behavior covered by regression tests.
- Production static export and TypeScript check passed.
- Browser smoke against the actual Dev2 frontend with intercepted fixture API
  responses: agent/crew views, Memory/current knowledge, About me, shared edit,
  cancel, desktop and 390px mobile. No uncaught JavaScript errors or document
  horizontal overflow. Mobile dialog measured 390px wide at x=0; its footer is
  visible. This check created no live crews, agents, decisions or model calls.
- Full Go suite, vet and final deployment verification are recorded below when complete.
