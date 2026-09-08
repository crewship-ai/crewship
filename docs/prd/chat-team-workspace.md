# Team Chat: identity, work context and activity

Date: 2026-09-07. Owner-directed extension of [Unified Chat](unified-chat.md).
This phase makes the existing Chat useful for people planning work together,
with explicitly invited agents and optional workspace activity. It uses the
same queue, journal, permissions and inbox, without introducing another work
tracker or changing issue/routine orchestration semantics.

## Identity and layout

Human and agent rooms use recognizable portraits, room-type icons, named
authors, timestamps, day dividers and compact adjacent messages from the same
author. Human portraits use current profile images with deterministic colored
initials as fallback. Agent portraits reuse their stored image/style/seed and
crew style. Rendering a portrait never creates or overwrites an avatar.
An avatar does not imply that its owner is online.

DM navigation displays the other participant's current identity relative to
the viewer. Profile metadata must not reveal a former participant who no longer
belongs to the workspace. Avatar fields are optional for older API responses.
Shared messages use the existing safe agent-chat Markdown renderer for lists,
code and resource links. System activity has the explicit author Crewship,
not a fictional human author or an agent reply.

## Status questions for agents

On an explicit structured mention, assignment preparation supplies a read-only
snapshot of up to 20 issues and 20 public, non-ephemeral routines in the same
workspace. Issue identifiers named in the triggering message are prioritized,
including older issues outside the recent sample. Routine status comes from
its latest stored run. Each item carries a link to the actual work record.

The snapshot is timestamped and marks truncation. It excludes issue descriptions,
private routine definitions, run output and credential data. Names/titles and
history remain untrusted data in the prompt. Answers must distinguish this
preparation-time snapshot from a new live lookup, acknowledge missing/truncated
data, and use existing authorized tools when a fuller or fresher answer is
needed. Asking about status never changes an issue as a side effect.

## Activity subscriptions

A workspace channel can opt into issue updates and routine execution updates.
Both are off initially; the channel creator manages them in the People/context
dialog or CLI. Private human groups/DMs cannot subscribe to workspace activity.
Current members can inspect the settings. Enabling a family begins with new
events, without silently importing old workspace history.

The existing journal is the durable source. A persisted workspace sequence
cursor and atomic message/outbox projection make restart/replay idempotent.
Issue/routine entries contain a concise event/state and a validated link;
raw output, private discussions and journal payloads are not copied into chat.
These system posts never generate agent jobs or implicit mentions. Normal
read/mute/inbox behavior applies. Backups carry the subscription settings but
restore them disabled so a different journal sequence space cannot replay work.

The channel is a place to notice and discuss work. Issues and routines remain
the authoritative records; the activity feed is not a copy of every transcript
or an unlimited retrospective summary. Threads, reactions, retention/SSO,
per-project routing and notification digests remain separate follow-ups.

## Demonstration and acceptance

Two explicitly labelled demo colleagues have separate accounts and logins;
synthetic planning messages are authored through each actual account. Their
names never imply that they are real employees or the owner's identity.
Generated credentials are kept outside Git in a private operator directory.

Verify avatars/fallbacks, grouped messages, Markdown and mobile layout in a
browser; existing drafts, ACLs and old links must keep working. Verify activity
enable/disable, no historical flood, workspace/privacy boundaries, replay and
restore behavior, and absence of model jobs from system messages. Test a real
agent's answer against a known issue state on Dev2, and compare a real issue or
routine event against its channel post. Record what ran separately from this
contract; passing these checks is not a corporate capacity or retention SLA.

Implementation and live acceptance: [Dev2 report](reports/chat-team-dev2-2026-09-07.md).
