# Explicit provider choice and automatic runner preparation

## Problem and scope

The Dev2 agent `test` was created with Anthropic / Claude Code without an explicit provider choice. Its crew had no cached image and provisioning was idle. The agent-create hook only rebuilt an existing image, leaving first-time preparation until chat dispatch.

## Result

- New agent: Identity leads to **Choose provider**. No provider radio is selected initially. Choosing a provider reveals the matching model and runner; OpenAI selects Codex CLI, Anthropic selects Claude Code. Creating via either the footer or keyboard shortcut cannot bypass this choice. Choosing an agent template requires provider confirmation again. Editing an existing agent retains its configuration.
- Template crew: the final step requires a provider, shows the matching runner in review, and sends provider / runner / model together. The template deploy endpoint validates the complete selection and writes it onto the new agents in its transaction, before credential assignment. Existing API callers that omit execution overrides still deploy the template verbatim; onboarding's model-only overrides retain their existing semantics.
- Empty crews have no AI provider of their own. The choice occurs when their first agent is created.
- Adding an agent starts preparation even when no cached image exists. Template deployment also enqueues preparation. Existing adapter installation recipes and binary verification remain the source of truth; users do not have to select Claude or Codex in preinstalled tooling.
- If agents or environment settings change while preparation is in flight, the successful build compares its configuration and adapter snapshot with current state and enqueues the updated build. Failed builds are not automatically retried in a loop. Existing rate limits and dispatch-time recovery still apply.
- Installation prepares the runner; actual AI work starts on a message/task. A provider account/API credential is still required for authenticated AI use. A failed download, build or credential connection is not represented as a successful AI run.

## Verification

- 69 frontend files / 706 tests passed, including explicit provider selection, shortcut guarding, template deployment payload and preservation of edit behavior.
- Lint: zero errors, 32 existing warnings. Production export built successfully. Go vet passed; targeted API and devcontainer tests passed. Provisioning and provider regression tests also passed with the Go race detector (63.104 s).
- Real Dev2 browser: no console/API errors; unselected provider on new agent, OpenAI → Codex, required provider on template crew. No browser verification drafts were saved.
- Actual preparation: existing `test` crew prepared and started; a separate temporary crew with an OpenAI/Codex agent triggered preparation automatically. Both runtime containers executed their CLI as UID 1001: Claude Code `2.1.263`, `codex-cli 0.152.0`. No AI prompts or paid runs were started. Temporary agent/crew removed through the API and its runtime container removed afterward; reusable cached images retained.
- A bare-image login-shell probe did not find Codex: runtime PATH is assembled by `applyAgentLoginPath`, including `/opt/mise/data/shims`. The authoritative verification used the application-created runtime container and normal non-login `docker exec`, matching agent execution.
- The initial systemd reload reported a signal failure; automatic recovery completed, and API/UI plus both runner checks subsequently succeeded.

Logs: `/tmp/crews-provider-ui-final.log`, `/tmp/crews-provider-regression.log`, `/tmp/crews-provider-build.log`, `/tmp/crews-provider-browser.log`, `/tmp/crews-provider-go-all.log`, `/tmp/crews-provider-race.log`.
