---
name: routine-author
display_name: Routine Author
version: 1.1.0
category: AUTOMATION
description: Author a Crewship routine (repeatable declarative workflow) from a natural-language goal. Use when asked to build, create, or automate a repeatable routine or workflow ("make a routine that…", "automate X", "set up a recurring job that…").
---

# Routine Author

A playbook for turning "make a routine that does X" into a valid, saved Crewship
draft — grounded in what this crew actually has, reviewed and published by the user.

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
> - First call `get_routine_draft` for the intended slug. This loads the same
>   saved draft the user edits, or a revision-zero baseline for a new recipe.
>   Preserve that envelope, edit its `document`, then call `save_routine_draft`.
> - Return the saved `editor_url`. Do not publish through `save_routine` during
>   this authoring flow: that legacy tool still changes the live recipe.
> - A conflict means somebody edited the draft or its published base. Keep
>   your proposed changes and explain the conflict; never reload and blindly
>   overwrite the other editor's work.

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

5. **Save a draft with its proposed start.** Use `get_routine_draft` with
   `{ "slug": "my-routine" }` (and `crew` only when authorized to author for
   another crew). Keep `id`, `slug`, `revision`, `base_pipeline_id` and
   `base_revision` from the returned `draft` unchanged. Fill `draft.document`:

   ```json
   {
     "slug": "my-routine",
     "name": "My routine",
     "description": "A short purpose",
     "definition": {"dsl_version": "1.0", "name": "my-routine", "steps": []},
     "trigger": {"kind": "schedule", "cron": "0 9 * * 1-5", "timezone": "Europe/Prague"}
   }
   ```

   The empty steps above are only the envelope example; supply the actual DSL
   you authored. Use `{"kind":"manual"}` for an on-demand start, or a proposed
   one-time `{"kind":"once","fire_at":"...RFC3339..."}` when requested.
   Call `save_routine_draft` with `{slug, draft}`. Crew and acting-agent identity
   are injected by the sidecar; do not copy identity fields from another crew.
   Saving a draft does not validate or execute steps, create a live routine,
   activate a schedule, or raise a schedule-approval request. Never claim it did.
   The legacy `save_routine` tool remains available for existing direct-publish
   integrations; its `activation:"draft"` means a disabled trigger, NOT an
   unpublished recipe. Do not use it as a substitute for `save_routine_draft`.

6. **Hand the SAME draft to the user.** Return the `editor_url` from the saved
   response, with a short summary and the saved revision. Explain that it is
   unpublished and its proposed schedule is inactive. The user opens Recipe /
   Test / Publish to review, validate without execution, and explicitly publish.
   Publication checks capability changes and existing recurring presets. The
   legacy `proposed` governance state is not the same as an unpublished draft.
   Do not invent a first-fire time or say work ran merely because saving passed.

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

- The save response contains a nonempty draft ID, its saved revision, `published:false`, and an `editor_url`. Validation and publication still belong to the user review flow.
- The plain-language summary you give the user matches the saved DSL — same
  trigger, same steps, same destination.
