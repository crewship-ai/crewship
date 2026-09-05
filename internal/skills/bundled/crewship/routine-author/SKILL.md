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

> **Author from what you already know — do NOT probe or verify.** Everything you
> need is already in your prompt: `[CONNECTED INTEGRATIONS]`, `[AVAILABLE ROUTINES]`,
> and the DSL reference below. Writing a routine is a **paper exercise** — you
> *describe* steps, you don't *perform* them now. So:
> - **Do NOT** run a command to "check" / "list" integrations, routines, or
>   endpoints — you already have them in your prompt.
> - **Do NOT** fetch the source URL to "see what it returns." The routine's own
>   `agent_run` step extracts the data at RUN time; put "extract the top 5 …" in
>   that step's prompt and move on.
> - **Do NOT** test a webhook, ping a host, or re-fetch the routine after saving
>   to "verify." None of that is authoring.
> - Creating the routine is **exactly ONE action**: call the `save_routine` tool
>   with the finished DSL (see step 5 — do not curl the save endpoint). Call it a
>   second time only if it returns a DSL error — then fix the JSON and retry.
>   Target ~two messages total: a one-line plan, then the save + a plain summary.
>   A third probing command means you're stalling — stop.

1. **Clarify only the genuinely ambiguous essentials.** Ask at most 2–3 questions,
   then default the rest. The three that usually matter:
   - **Trigger cadence** — manual, or on a schedule (when, and in what timezone?).
     A routine you save with NO trigger and no explicit "manual" reads as an
     oversight, not a decision — see step 5.
   - **Where the output goes** — a Slack channel, an issue, a file, a return value.
   - **What to do on failure** — retry, alert someone, or just stop.
   Do not interrogate. If the goal is clear, proceed with sensible defaults
   (manual trigger, return the result, stop on failure).

2. **Ground in what this crew actually has.** Read the `[CONNECTED INTEGRATIONS]`
   block in your prompt. Use ONLY integrations listed there, and declare each one
   the routine needs in `integrations_required` (lowercase connector slugs like
   `"github"`, `"slack"`). Read `[AVAILABLE ROUTINES]` and **reuse/compose**
   existing routines with a `call_pipeline` step where one already fits — don't
   re-build what's there. Also read the `[CONTAINER RESOURCES]` block: it lists
   the datastores (e.g. Postgres at a host/port) and installed CLIs your crew's
   container already has. If the routine uses any of them — typically from an
   `agent_run` step that opens a DB connection or shells out to a CLI — **declare
   them in the top-level `resources` block** (`resources.datastores[]` /
   `resources.tools[]`). These can't be inferred from the step graph, so without
   the declaration the manifest is incomplete and the run-time resource
   precondition gate has nothing to check against.

   **Raw URL or webhook given (e.g. a Discord/Slack webhook link)?** Don't hunt
   for an integration and don't test it — just use a plain `http` step with that
   URL and add its host to `egress_targets` (e.g. `["discord.com"]`). No
   `credential_ref` is needed for a webhook whose secret is in the URL itself.

3. **Prefer linear steps.** A short, top-to-bottom sequence is easier to read,
   test, and approve. Avoid branching (`if:`), DAG `needs:`, and loops unless the
   goal genuinely requires them. Keep it to the fewest steps that do the job.

4. **Write valid DSL.** The `definition` is a JSON object:

   - Top level: `dsl_version` (always `"1.0"`), `name`, `description`,
     `inputs[]`, `outputs[]`, `integrations_required[]`, `egress_targets[]`,
     `credentials_required[]`, `resources`, `steps[]`.
   - `resources` (only when the routine touches container datastores/CLIs):
     `{ "datastores": [{ "type":"postgres", "name":"app-db", "note":"writes table runs" }],
        "tools": [{ "type":"ansible", "name":"deploy.yml" }] }`.
     `type` is the engine/tool family (`postgres|redis|mysql|mongodb|other` for
     datastores; `ansible|terraform|kubectl|bash|python|other` for tools).
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

5. **Save with a trigger — always, in the same call.** Call the **`save_routine`**
   tool with `{ name, description, definition, sample_inputs, trigger, activation }`
   — do NOT curl the save endpoint, and do NOT save the routine first and
   attach a trigger afterward; the trigger is created in the SAME transaction
   as the routine, or not at all. `trigger` is REQUIRED — either a real one:

   ```json
   "trigger": {"kind": "schedule", "cron": "0 9 * * 1-5", "timezone": "Europe/Prague",
               "catchup_policy": "once", "max_consecutive_failures": 5}
   ```

   or, when the goal genuinely has no cadence (a one-off / on-demand routine),
   the explicit no-op:

   ```json
   "trigger": {"kind": "manual"}
   ```

   Never just omit `trigger` — that reads as an oversight, not a decision, and
   the routine page shows it as a warning. Add `"activation": "draft"` at the
   top level (a sibling of `trigger`, not inside it) whenever the routine acts
   autonomously in a way a human should sign off on before it ever fires
   unattended — this creates the trigger disabled and raises exactly one
   approval item in the workspace's inbox instead of letting it fire.

   The tool validates (a fast dry-run) before saving. **If it returns an
   error, READ it**, fix the DSL or trigger (bad cron, unknown timezone,
   missing input, wrong step shape), and retry — do not hand the user a
   routine that never passed validation. Use `list_routines` to check
   existing routines before authoring a duplicate.

6. **Tell the user the real outcome — routine AND trigger.** A routine is
   **risky** and lands as `proposed` (a MANAGER must approve it before it can
   run at all) when it contains an `http` step, a `code` step, declares
   `egress_targets` or `credentials_required`, or names an integration the
   crew hasn't connected. A routine built from only `agent_run` / `transform`
   / `wait` / `call_pipeline` with all integrations already connected goes
   **live** immediately. Say which one happened — never claim a proposed
   routine is live.

   Separately, report the trigger from the save response's `trigger` field:
   - `trigger.kind == "manual"` — say the routine has no automatic trigger and
     runs only when invoked.
   - `trigger.kind == "schedule"` and `trigger.approval_required` is false —
     state `trigger.first_fire_at` as the plain-language first run time
     ("next Monday at 9:00 AM Europe/Prague").
   - `trigger.approval_required` is true (activation was "draft") — say the
     trigger is disabled pending approval, that ONE item is now in the
     workspace's inbox, and what the first run WOULD be
     (`trigger.first_fire_at`) once a MANAGER activates it.

   Your final message must always include this: what was created, when it
   first runs (or that it's manual, or awaiting approval), and whether the
   routine itself is active or awaiting review.

7. **Present a short readable summary.** Describe the trigger and each step in
   plain language ("On a manual run: 1) Alex summarizes the repo's commits,
   2) the summary is posted to Slack"). Never dump raw JSON at the user.

8. **Run a saved routine when asked.** To invoke an existing routine, call the
   **`run_routine`** tool with `{ slug, inputs }` — do NOT shell out to curl or
   re-improvise the work by hand. The run executes synchronously and returns the
   run result/status; report the real outcome to the user. Use `list_routines`
   to find the slug first if you don't have it.

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
