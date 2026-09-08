# Chat navigation and private continuations — Dev2, 2026-09-07

Implements the sidebar/continuation refinement in [Unified Chat](../unified-chat.md).
This is the working tree on `fix/chat-workspace-foundations`, base `56969d7e`, not
a merged release. Existing work remains uncommitted and preserved.

## Navigation

One New chat entry and common search replace the All/Agents/People/Groups tile
navigation. People, Agents and Team spaces are collapsible sections in one scroll
area. An agent appears once, with expandable session history and its own explicit
New session action. Activity & filters contains the secondary Direct/Routines/
Issues history scopes. Existing history pagination remains available.

Opening a human chat clears stale agent/session/draft highlighting. Section and
agent expansion state is remembered in sessionStorage per workspace and user;
an active agent session is revealed on reopen. Missing/failed history does not
implicitly create a session. Existing deep links, browser Back/Forward, independent
drafts and local new-session creation remain supported.

## Continuing with more participants

In a direct message, People → Add people opens Continue in a group. Select extra
colleagues and create a new private group; the original pair is included and the
original private history is not copied or opened to the new people.

In a private DM/group, People → Invite agent opens Continue with an agent. The
form explicitly states workspace visibility, includes current participants and
creates the new channel together with agent membership. History starts with the
agent join notice. Joining does not launch a model; a selected mention does.
Existing workspace channels retain their normal Add agent control.

POST `/conversations/{id}/continue` is transactional and durable-idempotent.
A committed operation whose response was lost reopens the same room on retry.
The UI saves unresolved operation IDs, locks their inputs and suppresses navigation
when dismissed. [Backend/privacy/backup contract](chat-private-continuations-2026-09-07.md).

## Verification

- Final Chat/conversation/route frontend suite: **659 tests in 72 files passed**.
- Production build, TypeScript and full ESLint passed; ESLint retains 32 existing
  warnings and no errors.
- Go vet passed. Focused store/race, HTTP, CLI/OpenAPI and actual migrated
  backup/fork/email-reconciliation/retry tests passed.
- OpenAPI documentation/inventory updated and tested: 614 API operations, 858 CLI
  commands.
- Final isolated browser: **37 scenarios passed**, no failures. This includes
  committed creation followed by a simulated lost response (503), exact retry
  identity, private history boundaries, participant changes, sidebar selection,
  new sessions and mobile navigation.
- Actual Dev2 CLI: **4 live checks passed**, including separate owner/Klára/Tomáš
  messages and Klára mentioning the newly invited Mařena. Her real reply matched
  COP-1’s stored status and link without changing the issue.
- Full `go test ./... -count=1 -timeout=30m` completed successfully (exit 0),
  including API, database, backup, CLI and documentation inventory.
- `go vet ./...`, the four executable agent invariants and final diff whitespace
  checks passed. Public Dev2 health returned `status: ok`.

## Deliberate boundaries

Private history is never automatically carried into a larger group or public
channel. Private agent execution is not introduced: agent work still belongs to
workspace visibility. Existing group administrators can add people to that group
under its normal history-access contract. Pinning, threads/reactions and corporate
capacity/SSO are separate work, not implied by this navigation refinement.

## Live evidence

[Private demo group](https://crewship-dev2.unifylab.cz/chat?conversation=cmtr9szsl184a363e579ab737bd7d0918&workspace_id=cmtplws8h000276fa560f)
and [new channel with Mařena](https://crewship-dev2.unifylab.cz/chat?conversation=cmtr9t06j68c1c6280a38c4b6b43a7cd9&workspace_id=cmtplws8h000276fa560f)
remain for review. [Sanitized CLI evidence](chat-continuation-live-dev2-2026-09-07.json)
and [repeatable scenario](../../../e2e/chat-continuation-live.md).

The standard Dev2 service reload applied migration `20260907131137`; its automatic
pre-migration backup is
`crewship.db.pre-migrate-v20260907111020-to-v20260907131137-20260907T132213Z.bak`
(17,920,000 bytes). No sibling instance or real issue was changed.

Final public-browser verification used normal Klára login, not an injected owner
session. Six checks passed with zero browser errors, including the actual new
sidebar, original two-person DM, three-person group, real Mařena reply, mobile
channel/drawer and cancelling the Add people dialog without creation. Screenshots:
[desktop](assets/chat-navigation-dev2/desktop.png),
[mobile channel](assets/chat-navigation-dev2/mobile.png),
[mobile sidebar](assets/chat-navigation-dev2/mobile-sidebar.png),
[Add people](assets/chat-navigation-dev2/add-people.png).
