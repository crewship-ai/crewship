# ADR: the operator `crewship` CLI does not belong in the agent image

Date: 2026-11-13 · Status: **proposed**

## Context

While a device-monitoring package was being deployed, a lead agent could not attach
sub-issues (`issue.link` is refused for chat-originated runs). One suggested
workaround was to install the operator `crewship` CLI in the crew image so that
agents could call the API directly.

## Decision

The agent image does not ship the operator CLI.

- **Agents already have a governed path.** The sidecar (`/usr/local/bin/crewship-sidecar`,
  mounted read-only) exposes the actions an agent may take as MCP tools:
  `create_issue`, `update_issue`, `comment_issue`, `list_issues`, `save_page`,
  `run_routine`, `save_routine` and others. Delegation and escalation stay with
  the orchestrator. Each call carries the agent's own
  crew-bound token, the run's chat-origin authority and the crew's autonomy decision.
- **The operator CLI needs a person's or workspace token.** Putting one in a crew
  container hands every crew member, and every prompt it reads, an identity
  wider than the agent's. That is the bypass the task forbids: no master or
  human tokens and no alternate paths around `chat_issue_action_not_supported`.
- **The real gap is fixed in the governed path.** `issue.create` accepts
  `parent_identifier` and creates and attaches a sub-issue atomically. A
  `crewship` step that the autonomy level holds now waits for an inbox approval
  instead of failing.

## Consequences

- Packages declare the tools their agents need (SSH, Git and Python for both
  crews; ADB or Ansible where a package needs them) as image features, such as
  `apt-packages`. The platform does not install them.
- Root setup belongs in features at build time. At runtime the container runs
  as UID 1001 with `no-new-privileges` and all capabilities dropped. Provisioning
  now creates the passwd entry for UID 1001 when the base image lacks one.
- An operator who wants scripting inside a crew can create a scoped credential
  explicitly and accept that every member of the crew can read it. The platform
  does not do this by default.
