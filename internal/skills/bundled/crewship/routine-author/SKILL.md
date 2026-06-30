---
name: routine-author
display_name: Routine Author
version: 1.0.0
category: AUTOMATION
description: Author a Crewship routine (repeatable declarative workflow) from a natural-language goal. Use when asked to build, create, or automate a repeatable routine or workflow ("make a routine that…", "automate X", "set up a recurring job that…").
---

# Routine Author

A playbook for turning "make a routine that does X" into a valid, saved Crewship
routine — grounded in what this crew actually has, tested before it ships.

## When to Activate

- The user asks you to **build / create / automate a repeatable workflow**:
  "make a routine that…", "automate X", "set up a recurring job", "every morning do Y".
- Distinguish from a one-off task: a routine is worth authoring when the work
  repeats or needs a trigger (schedule / webhook / event). For a single ad-hoc
  job, just do the work — don't author a routine.

## Procedure

1. **Clarify only the genuinely ambiguous essentials.** Ask at most 2–3 questions,
   then default the rest. The three that usually matter:
   - **Trigger cadence** — manual, on a schedule (when?), webhook, or event.
   - **Where the output goes** — a Slack channel, an issue, a file, a return value.
   - **What to do on failure** — retry, alert someone, or just stop.
   Do not interrogate. If the goal is clear, proceed with sensible defaults
   (manual trigger, return the result, stop on failure).

2. **Ground in what this crew actually has.** Read the `[CONNECTED INTEGRATIONS]`
   block in your prompt. Use ONLY integrations listed there, and declare each one
   the routine needs in `integrations_required` (lowercase connector slugs like
   `"github"`, `"slack"`). Read `[AVAILABLE ROUTINES]` and **reuse/compose**
   existing routines with a `call_pipeline` step where one already fits — don't
   re-build what's there.

3. **Prefer linear steps.** A short, top-to-bottom sequence is easier to read,
   test, and approve. Avoid branching (`if:`), DAG `needs:`, and loops unless the
   goal genuinely requires them. Keep it to the fewest steps that do the job.

4. **Write valid DSL.** The `definition` is a JSON object:

   - Top level: `dsl_version` (always `"1.0"`), `name`, `description`,
     `inputs[]`, `outputs[]`, `integrations_required[]`, `egress_targets[]`,
     `steps[]`.
   - Step types (the `type` field selects the shape):
     - `agent_run` — `{ "id", "type":"agent_run", "agent_slug", "prompt", "complexity":"fast|moderate|smart" }`
     - `http` — `{ "id", "type":"http", "http": { "method", "url", "headers", "body", "credential_ref": {"type":"slack"} } }`
     - `transform` — `{ "id", "type":"transform", "transform": { "input":"{{ steps.x.output }}", "expression":".field" } }` (pure-Go jq subset, no LLM)
     - `wait` — `{ "id", "type":"wait", "wait": { "kind":"approval", "approval_prompt":"…" } }` (also `datetime` / `event`)
     - `call_pipeline` — `{ "id", "type":"call_pipeline", "pipeline_slug":"other-routine", "inputs": {…} }`
   - **Templating**: reference inputs as `{{ inputs.name }}` and a prior step's
     result as `{{ steps.<step-id>.output }}`. Steps run in order by default.

   Minimal example:

   ```json
   {
     "dsl_version": "1.0",
     "name": "daily-standup-digest",
     "description": "Summarize yesterday's commits and post to Slack.",
     "inputs": [{ "name": "repo", "type": "string", "required": true }],
     "integrations_required": ["github", "slack"],
     "steps": [
       { "id": "summarize", "type": "agent_run", "agent_slug": "alex",
         "complexity": "fast",
         "prompt": "Summarize commits in {{ inputs.repo }} since yesterday." },
       { "id": "post", "type": "http",
         "http": { "method": "POST", "url": "https://slack.com/api/chat.postMessage",
                   "credential_ref": { "type": "slack" },
                   "body": "{{ steps.summarize.output }}" } }
     ]
   }
   ```

5. **Save and test.** Call the **`save_routine`** tool with
   `{ name, description, definition, sample_inputs }` — do NOT curl the save
   endpoint. The tool validates (a fast dry-run) before saving. **If it returns
   an error, READ it**, fix the DSL (bad template path, missing input, wrong
   step shape), and retry — do not hand the user a routine that never passed
   validation. Use `list_routines` to check existing routines before authoring
   a duplicate.

6. **Tell the user the real outcome.** A routine is **risky** and lands as
   `proposed` (a MANAGER must approve it before it can run) when it contains an
   `http` step, a `code` step, declares `egress_targets` or `credentials_required`,
   or names an integration the crew hasn't connected. A routine built from only
   `agent_run` / `transform` / `wait` / `call_pipeline` with all integrations
   already connected goes **live** immediately. Say which one happened — never
   claim a proposed routine is live.

7. **Present a short readable summary.** Describe the trigger and each step in
   plain language ("On a manual run: 1) Alex summarizes the repo's commits,
   2) the summary is posted to Slack"). Never dump raw JSON at the user.

## Pitfalls

- **Never use an integration the crew hasn't connected.** If it's not in
  `[CONNECTED INTEGRATIONS]`, you can't use it — propose connecting it, or pick
  another approach.
- **Never invent or hardcode a credential.** Reference credentials by type via
  `credential_ref` (e.g. `{"type":"slack"}`); the runtime resolves them. If a
  needed credential is missing, raise a **CREDENTIAL escalation** per the
  credential instructions in your prompt — don't paste a token into the DSL.
- **Don't propose branching/DAG for v1.** Ship the linear version first.
- **Don't claim a routine is live when it's proposed.** Check the save response.

## Verification

- The save response shows `test_run` passed (not a validation or runtime error).
- The plain-language summary you give the user matches the saved DSL — same
  trigger, same steps, same destination.
