# Changelog

All notable changes to Crewship are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 releases may introduce breaking changes in minor versions
(`0.x.0`); patch releases (`0.x.y`) are backwards-compatible fixes.

## [Unreleased]

<!--
  Backfill (#2086). The twenty-four entries between this marker and the next
  one chronicle eighteen PRs that merged with no changelog trace anywhere —
  more entries than PRs, because a PR that broke three separate things gets
  three. Written from their diffs, after the fact, rather than by their
  authors at the time. The `Changelog Guard` workflow now fails a PR that
  touches `internal/api/`, `cmd/crewship/`, `app/`, `components/`, `lib/`,
  `hooks/` or `stores/` without adding to this `## [Unreleased]` section —
  that section specifically, not the file, because it is the only one
  `RELEASING.md` cuts release notes from.

  The #2086 audit reported fifteen such PRs. Reading the window's merges back
  against this file turned up seventeen, and #2079 makes eighteen: it merged
  nineteen minutes after the audit list was cut (2026-08-26T20:52Z against the
  issue's 20:33Z), unchronicled like the rest, and it supersedes #2070 below —
  which is why leaving it out was not merely a gap but left a wrong entry
  standing with nothing to correct it. #2017, which the audit listed as
  missing, was already chronicled and is untouched.
-->

### Added

- **A provider is now a credential, and two new ones can be created (#2051).**
  The sidecar's LLM proxy routed on a hardcoded three-arm `strings.HasPrefix`
  switch over `/v1`, `/openai` and `/gemini`. It now routes on a compiled-in
  descriptor table (`internal/llmroute`, longest bounded prefix wins), and
  `credentials.provider` is carried end to end — into the sidecar boot payload,
  the credential loaders, the ledger key and the scrubber — so a provider is a
  row rather than a branch.

  Two arrive with it: **`OPENROUTER`** (`/llm/openrouter`, credential required,
  probed live against `GET https://openrouter.ai/api/v1/key`) and
  **`OPENAI_COMPAT`** (`/llm/openai-compat`, upstream read from the credential
  itself, unpriced). Every future provider is confined to `/llm/…`; the three
  legacy prefixes keep their exact paths and strip behaviour, pinned by eleven
  golden fixtures of the byte-identical outbound request.

  `crewship credential create` gains **`--base-url`** (required for
  `--provider OPENAI_COMPAT`, rejected for every other provider) and
  **`--auth-token-stdin`** (also on `credential rotate`), so an operator
  token never reaches the process table. Two local commands read the route
  table with no server: **`crewship provider route list`** and
  **`crewship provider route show <provider>`**. `/health` gains
  `provider_creds` and `config_fingerprint` beside — not instead of — the
  three legacy `*_creds` counters.

  ⚠️ **Behaviour change: proxied agent calls start billing for real.** See
  *Changed*, below. This is the release's most consequential line and it
  reaches operators who changed nothing.

- **One shell for every create surface, and a `/design` route to audit them
  (#2056).** Twelve create entry points that had each grown their own dialog
  are now one kit (`components/layout/create-surface.tsx`): four fixed widths,
  a bottom sheet below `sm`, ⌘↵ to submit, and a discard guard on Esc, overlay
  and header-close. `/design` is the scaffolding that tracks the migration and
  the browser-vs-CLI parity gap — it holds no data, calls no API, has no CLI
  command, and is meant to be deleted with `components/features/design/` once
  its table is empty. It is linked from the sidebar under System so the gap is
  visible rather than filed.

  Three things became reachable from the browser for the first time:
  **importing a crew manifest** into New crew (parsed client-side, and it names
  what it cannot create rather than pretending — "This file also declares 2
  agents and 1 credential", pointing at `crewship apply -f`); **per-agent
  tools and notification channels** on New agent; and **creating a label from
  the issue modal**. The crew wizard is four steps, not five — Runtime folded
  into Container — and "Empty crew" is now "Start empty".

  ⚠️ **Behaviour change: New crew now defaults to `network_mode: "free"`.**
  The wizard's initial state flipped from `restricted`; the server-side
  default in `internal/database/crew_defaults.go` is untouched, so anything
  that creates a crew *without* stating a mode still gets `restricted`. Only
  the wizard's pre-selection moved, and the copy beside it now says what free
  means ("Any host, plus your private network and localhost. Cloud metadata
  stays blocked.").

- **Multi-provider phase 1: two codecs, an embedded catalogue, and the CLI to
  see them (#2016).** `internal/llm`'s three-arm switch became a registry of
  `ProviderSpec` rows read by the aux-slot builder, the Keeper validator, the
  console picker and the error message that tells an operator what they may
  type. Three ids are registered — `anthropic`, `openai`, `ollama` — and
  lookup is case-insensitive, which the old switch was not while `internal/api`
  carried the uppercase enum one layer up. The two hand-written HTTP clients
  became configurable codecs, so any OpenAI-compatible backend (DeepSeek,
  vLLM, llama.cpp, Ollama's `/v1` shim) is a preset rather than a new file.

  A trimmed [models.dev](https://models.dev) snapshot ships embedded — eight
  providers, ~650 KB, refreshed by `go generate ./internal/modelcatalog/...` —
  and becomes the third step of the rate lookup, below the hand-verified table
  and its `<provider>/*` wildcard so `ollama/*` and `local/*` stay free.
  Three local commands read all of it with no server, token or workspace:
  **`crewship provider list`**, **`crewship provider check`** and
  **`crewship model price`** (which prints *which* lookup step produced the
  rate). `crewship model list` gains `--source auto|live|catalog` and
  `--search`, and answers offline for providers the server cannot reach.

  ⚠️ **Behaviour change: an evaluator slot can move provider, and a call
  within budget can now trip a cap.** See *Changed*, below.

- **Run a routine from a slash command, with a form for its inputs (#1987).**
  A routine may opt in with a `spec.slash` block (`enabled`, `label`,
  `label_cs`, `icon`); it then appears as `/<slug>` in the chat palette and in
  `crewship shell`, opening a typed form built from the routine's declared
  `inputs` — widget and coercion chosen from each input's JSON type, help text
  from its description, defaults formatted losslessly. In the REPL the same
  thing is `/<slug> key=value key=value`.

  A new **`routine.run`** capability decides who may ask, so a MEMBER can
  invoke a routine without being promoted to MANAGER
  (`crewship workspace member capabilities grant <user> routine.run`;
  it is in the `power` and `admin` bundles). Everything else still applies to
  an admitted caller — governance status, credential and integration
  preconditions, spend caps, concurrency.

  Three limits are worth knowing. The run endpoint is **synchronous**, and the
  REPL drives it through the CLI's 30 s default timeout, so a longer routine is
  cancelled client-side unless `CREWSHIP_HTTP_TIMEOUT` is raised. The
  routine's **output is not shown on either surface** — you read it in the run
  records. And the catalogue is capped at 50 routines, ordered by popularity,
  with a slug that collides with a platform command (`routine`, `issue`,
  `skill`, `credential`) dropped.

  `spec.slash` was documented before this and silently dropped by
  `crewship apply`, so export→edit→apply *stripped* a block set from the
  dashboard. It now round-trips. `crewship shell` also gains the server
  catalogue at all: `LoadServerSlashCommands` had no production caller, so the
  REPL half of the slash feature did not previously exist.

- **A page can be published, webhooked, exported, imported and deleted from
  the browser (#2054).** Those were CLI-only. New settings cards cover public
  links (with expiry and password; revoked rows are kept with their withdrawal
  time rather than vanishing), webhook mint/revoke, export and delete, plus
  revoking every access level for a subject in **one** DELETE — three
  sequential revokes leave a window open in between. Import renders a 422
  refusal as the worklist that caused it, not a toast.

  `crewship page create` gains **`--owner crew/<slug>`**, create-only on
  purpose: `page update` never sends it, so re-applying a manifest can never
  be a silent transfer of ownership. `crewship page get` now prints the
  authored half of each panel — `tab`, `public`, wake gates, `on_fail`,
  `refresh`, action count — which `--format json` already carried. The demo
  seed ships four pages, one per producer door (script, routine, agent,
  webhook) instead of showing one door of the four.

- **The wake-time system prompt now shows its own memory budget (#2135).** Every
  tier injected into a session's opening prompt — `Pins`, `Crew`,
  `Workspace`, `Agent` — now reports how many bytes of its allotted
  slice it used and what percent that is, plus a `Total` line, in a new
  `[MEMORY BUDGET]` block placed right before `[MEMORY INSTRUCTIONS]`. The
  wording matches `memory.write`'s existing overflow-guidance usage string
  byte for byte, unit included (`<used> of <cap> bytes, <pct>%`): both
  meters count `len(string)` bytes, and the budget these tiers are
  actually enforced against is itself byte-denominated, so "bytes" is the
  only label that describes what is measured and capped — an earlier
  draft of this meter said "chars" while still counting and enforcing
  bytes, which reads as accurate for pure-ASCII content but is wrong for
  anything else (this product carries Czech text throughout). A
  percentage that would round down to 0% for real, non-zero usage now
  floors to 1% instead. When the budget forced a tier's trailing content
  to be dropped — previously silent — the meter now says so by name
  (`Truncated to fit: Agent — trailing content in it was dropped, not
  just hidden.`), and truncation itself is now rune-aligned so a cut can
  never sever a multi-byte UTF-8 character and hand the model invalid
  text. A separate `Read incomplete: Workspace` clause covers a distinct
  failure the truncation notice can't: a stalled or slow workspace
  filesystem read that times out mid-scan, which previously came back
  indistinguishable from "there just wasn't much workspace memory" — the
  model is now told the read did not finish rather than being left to
  assume it saw everything. The default 15,000-byte budget, the per-tier
  allocation ratios, and the truncation policy itself are unchanged; this
  only makes the existing behaviour visible to the model, honestly.

### Changed

- **⚠️ Proxied agent calls start billing for real (#2051).** ⚠️ **Behaviour
  change: budget warnings and hard stops that have never fired on a crew can
  fire on the first deploy.** Every LLM call an agent made through the sidecar
  proxy recorded **zero tokens and $0** since the proxy was built:
  `parseLLMUsage` switched on a lowercase provider name while the proxy handed
  it the uppercase `ProviderType`, so no proxied response body was ever parsed
  — Anthropic wrote $0 rows, OpenAI and Gemini wrote none at all. Codec and
  ledger key are now separate fields on the route spec and both are correct.

  Nothing got more expensive. The spend was always there and was being
  recorded as nothing, so a ceiling that looked generous against $0 may be
  below a normal day. Before deploying, check `budget_limits` for any crew
  running proxied agents. Historic rows are **not** backfilled, so a rollup
  spanning the change is not comparable across it.

- **⚠️ `PATCH /api/v1/credentials/{id}` returns 400 where it returned 200
  (#2051).** An endpoint-backed credential holds a `{baseURL, apiKey, headers}`
  object; every other kind holds an opaque value. A PATCH that moves the
  credential across that line — `provider` to or from `OPENAI_COMPAT`, or
  `type` between `ENDPOINT_URL` and `API_KEY` — silently re-interprets bytes
  it did not send, so `value` must now be sent in the same request. A PATCH
  carrying a `value` also validates the endpoint URL for the first time, which
  it only ever did on create; a PATCH that used to accept
  `http://169.254.169.254/v1` now refuses it.

  Two smaller edges from the same PR. `credentials.provider` is folded through
  `credprovider.Canonical()` on every write, so a client that POSTs
  `provider: "github"` now reads back `"GITHUB"` — unrecognised strings are
  still stored verbatim, trimmed. And several `credential create` argument
  errors moved from a bare exit 1 to `cli.ExitValidation` (**exit 2**): a
  `--type`/`--auth-token` mismatch, a malformed `--header`, a missing
  `--value`. A script that branches on exit 1 will see 2.

  `OPENAI_COMPAT` is deliberately **not** validated on create — Crewship does
  not dial an operator-supplied endpoint from that path — and prints a warning
  saying so instead of "Key validated successfully". (#2057 later gave it the
  test it could safely have; see *Fixed*.)

- **⚠️ Rate ceilings moved, and an evaluator slot can change provider
  (#2016).** ⚠️ **Behaviour change: a call that used to sit inside a spend cap
  can now trip it.** Four fallback ceilings were below what the embedded
  snapshot says the provider actually charges and were raised — `openai`
  $20/$80 → **$150/$600**, `google` $2.50/$15 → **$4/$120**, `xai` $2/$6 →
  **$4/$12**, `mistral` output $6 → **$7.50** — and two rows were added
  (`openrouter`, `amazon-bedrock`). Models that previously billed at **$0**
  because nothing in the table named them (unknown OpenRouter slugs, hosted
  models with no row) now bill at a real or ceiling rate. The over-estimate is
  deliberate: under-billing weakens the budget signal exactly when it matters.

  Separately, `LoadAuxiliaryModels` now points each auxiliary slot at the
  first registered provider **whose key env is actually set**, before env
  overrides. An instance holding only `OPENAI_API_KEY` used to get six slots
  hardcoded to Anthropic that each failed at first use; it now gets working
  evaluators on OpenAI. An instance holding both keys keeps Anthropic —
  declaration order breaks the tie — and an instance holding neither keeps the
  shipped default and still errors loudly.

- **⚠️ A crew whose mise shims do not resolve now fails to provision (#2070).**
  ⚠️ **Behaviour change: a provision that used to succeed can now fail.**
  `mise reshim` exits 0 whether or not the shims it wrote point at anything.
  On crews provisioned before #1787 they pointed at a `mise` binary the agent
  could not read, and a dangling symlink is skipped **silently** by PATH
  lookup — so the pin served whatever the base image shipped. Measured on a
  real crew: `config.toml` pinned `terraform = "1.9"`, the agent ran
  `Terraform v1.15.7`, and nothing was logged anywhere.

  `InstallMiseTools` now verifies every shim resolves and returns
  `ErrMiseInstallFailed` naming the broken ones when they do not. Crews
  created before 2026-08-07 carry stale shims and **do not self-heal**, so
  expect their next provision to fail rather than quietly serve the wrong
  toolchain. Crews declaring no mise tools are untouched, and an empty shim
  directory is not treated as a failure. `docs/manifest/crew.md` gains the
  PATH ordering that makes a pin effective.

- **Crew template slugs are unique per workspace, not globally (#2028).**
  `crew_templates.slug` carried a global `UNIQUE` declared in v23; v26 added
  `workspace_id` and never rescoped it, so the column naming the owner played
  no part in deciding whether a name was free. Two workspaces could not both
  hold a template called `backend-team`, and — worse — a user template holding
  a builtin's slug made the seeder's `UPDATE` match nothing and its
  `INSERT OR IGNORE` collide, so that builtin was **never seeded again, ever**.

  Migration `20260820124407_crew_template_slug_workspace_scope.sql` rebuilds
  the table behind two partial unique indexes —
  `(workspace_id, slug) WHERE workspace_id IS NOT NULL` and
  `(slug) WHERE workspace_id IS NULL`. A single non-partial
  `UNIQUE(workspace_id, slug)` would not do: SQLite treats NULLs as distinct,
  so every builtin would be unique to itself no matter how many shared a slug.

  The old constraint was doing undeclared work — it guaranteed the six
  read/write sites matched at most one row, which is why four of them used
  `QueryRow` with no tie-break. The rule is now written down: **a workspace
  template shadows the builtin of the same slug, for that workspace only**,
  expressed as `ORDER BY (workspace_id IS NULL) LIMIT 1` in a shared constant.
  The tenant predicate was retightened at the same time: `is_builtin` has no
  `CHECK` tying it to `workspace_id`, so a row owned by one workspace could
  carry `is_builtin = 1` and match for every tenant — unreachable while the
  global UNIQUE stood, expressible the moment it was split.

  **Upgrade note.** The rebuild cannot fail on duplicate slugs: the old
  constraint is strictly stronger than both new indexes. It does drop one row
  class — a template whose `workspace_id` names a workspace that no longer
  exists. Such a row is an FK orphan the schema's own `ON DELETE CASCADE` says
  should not exist, and copying it would abort boot with
  `FOREIGN KEY constraint failed (787)` naming neither the table nor the row.

### Fixed

- **Backups were silently short, and `--replace` deleted through the same
  wrong filter (#2008).** `DiscoverScopedTables` recorded the *shortest*
  reverse-foreign-key chain from each table to `workspaces` and built a `WHERE`
  from it, without ever asking whether the column it landed on was nullable. A
  filter on a nullable column omits every row where that column is NULL. The
  bundle is written, `crewship backup verify` passes — it only checks the
  payload's SHA-256 against the sealed bytes — and the rows are simply absent
  at restore.

  Seven tables lost rows that way. `mission_tasks` through
  `assignment_id` lost **every task nobody had claimed**; `crew_mcp_servers`
  through `workspace_mcp_server_id` lost **every server a crew configured for
  itself**; `page_versions` through `author_agent_id` lost **every version a
  human saved**; `agent_credentials` and `agent_mcp_bindings` through
  `credential_id` lost every binding with no credential; `page_panel_data` and
  `page_panel_alerts` lost every panel not fed by a routine and every lapse on
  a panel with no `on_failure`.

  It was not even stable. `reverseFK` was built by ranging over a Go map, so
  two equidistant parents raced to claim a child and **the same binary against
  the same schema produced different filters on consecutive runs** — one run
  lost `mission_tasks`, the next lost `crew_mcp_servers`. And
  `discoverScopedTablesTx` in `replace.go` was a verbatim copy of the same
  walk, which is the one deciding what a `--replace` restore **deletes**.

  The walk now minimises `(nullable hops, total hops)` over the whole path,
  relaxes to a fixed point rather than doing a shortest-path sweep, and breaks
  every remaining tie deterministically. The `replace.go` fork is deleted.

  **Bundles written before this are still short and nothing detects it** —
  `Verify` compares a checksum and the manifest records no per-table row
  counts. That gap is #2009, and it is called out as a `<Warning>` in
  `docs/guides/backup.mdx`.

  ⚠️ **Upgrade note.** Migration
  `20260820074400_issue_counters_crew_not_null.sql` rebuilds `issue_counters`
  to make `crew_id` genuinely `NOT NULL`. There is no `DELETE` statement, but
  the copy is filtered and the original is dropped, so two row classes are
  **discarded and not recoverable in place**: rows with `crew_id IS NULL`
  (which name no crew, hence no workspace and no prefix, and which no code
  path reads) and rows whose `crew_id` names a crew that no longer exists
  (FK orphans that would otherwise abort boot with SQLite error 787). Every
  counter naming a live crew is carried across with `next_number` intact. No
  action is required before upgrading beyond the ordinary one — keep a copy of
  the database file.

- **The timeline's "Restore" restored nothing and said it had (#2070).**
  `POST /api/v1/…/checkpoints/{id}/restore` is non-destructive by design and
  says so in its own doc comment: no rows are mutated, no containers are torn
  down, no memory is rewound. It computes a **preview** and journals it as one.
  The dropdown item said `Restore` and, on any `res.ok`, raised
  `toast.success("Mission restored to checkpoint")`.

  What it threw away is the part that mattered. The response carries
  `warn_divergence` — the journal entries strictly newer than the checkpoint
  cursor, which is precisely the work a real rewind would have to abandon —
  and the handler discarded the body unread. The item is now labelled
  **Preview restore**, the body is parsed, and the result says
  "Restore preview — nothing has been rewound", naming how many later events a
  restore would abandon and that rewinding is not implemented yet.

- **`pins.md` and `learned-*.md` could be read empty (#1994, #2021).** All
  three writers opened with `O_CREATE|O_APPEND` and then wrote, which leaves
  the file on disk at **zero bytes** between two syscalls. The flock above them
  guards `<name>.lock`, so it serialises writers and does nothing at all for
  the three readers that take no lock: the memory audit watcher, the
  proposal-diff endpoint, and **agents reading their own learned rules**.

  A reader landing in that window got an empty file instead of the previous
  good contents — an operator's entire pinned set momentarily gone from the
  file the agent reads. It is not a rare race: reproduced at 101/300 and
  100/300 runs for the two consolidator writers on the six-hourly tick, and
  55/300 for the approve path. It also produced a CI failure that looked like
  a data race and was not, because `os.ReadFile` of an empty file returns a
  non-nil zero-length slice and the poll loop accepted it.

  All three now build the file in memory and go through
  `memory.WriteFileDurable` (tempfile → fsync → atomic rename → fsync parent).
  Append-only semantics are preserved verbatim, so hand annotations in
  `pins.md` survive, and `appendRules` pays nothing for it — it already re-read
  the whole file after every append to hand back the audit blob, and that read
  simply moved ahead of the write.

  Two corrections rode along. The exists-check read *every* `stat` failure as
  "absent", which under a whole-file replace would write the first-run header
  over content it never read; only `fs.ErrNotExist` qualifies now. And the
  canonical path is refused if it is a symlink — `os.ReadFile` follows one, and
  its target's bytes would have become the prefix of what was written back into
  the crew's learned rules.

  The guard that should have caught this class **had gone blind**:
  `crash_safe_writes_invariant_test.go` matched `os.OpenFile(` while these
  packages open through `*os.Root` handles, so every root-anchored write in
  them was unscanned and two allowlist entries had been matching nothing.
  Widened to `\.OpenFile\(`.

  Named side effect: the rename resets the file mode to `0o644`, so an
  operator `chmod` on `pins.md` no longer survives a consolidation tick.

- **Two Keeper aux slots were settable, validated, rendered — and read by
  nothing (#1986).** The **`curator`** slot was described everywhere, including
  in the console, as governing skill review *and memory consolidation*. The
  consolidator never read it: the summariser was built once at boot straight
  from `KEEPER_OLLAMA_URL` + `KEEPER_MODEL`, bypassing the slot, the resolver
  and the override store. So an instance with an `ANTHROPIC_API_KEY` and no
  Ollama logged "memory consolidation disabled" at boot while the Judge models
  card reported `curator` as configured and healthy — consolidation silently
  did not run, and mid-conversation compaction fell back to plain truncation on
  the same unadvertised wiring. Consolidation now resolves the slot per run,
  through the standard middleware stack so it still appears in the cost ledger,
  with the boot-time `KEEPER_*` client kept as a fallback so a local-judge
  install does not *lose* consolidation.

  The **`run_summary`** slot's timeout bounded nothing. `Router.RunVerdict()`
  returned provider and model and discarded the budget, and both production
  call sites hand the verdict a background context — so the operator's number
  sat on the card beside four rows where the identical control worked, while a
  hung provider could keep a verdict call outstanding indefinitely. The
  deadline is now applied in the one place both call sites share, and covers
  the model call only: a verdict answered at 19.9 s of a 20 s budget is not
  generated, billed and then dropped on the floor.

  ⚠️ **Behaviour change:** `crewship keeper aux set run_summary --timeout 45s`
  and `keeper aux set curator --provider …` now do something. The shipped
  `run_summary` default moves 15 s → **20 s**, because the first real deadline
  on a call must not be tighter than what it had been running under — it
  matters most on a fully local judge. Consolidation itself is a documented
  exception and stays on the provider's client timeout: it is batch work over
  hundreds of journal entries, and a 20 s cut-off would kill every local-model
  consolidation mid-flight.

- **One unrenderable chat message took down the whole page and dropped the live
  session (#2024).** There was no error boundary anywhere in the chat tree, so
  a throw while rendering a single turn propagated to the route segment
  boundary, which replaces the entire page with "Something went wrong". In
  practice that meant the chat client unmounted, every turn's state was
  discarded, a half-typed composer draft was lost, and the **WebSocket of the
  very session that had just degraded was dropped**. The shipped trigger was
  `TypeError: value.trim is not a function` — CLI init metadata is forwarded
  verbatim, so a key holding a string in one CLI release holds an object in the
  next, and a type assertion catches nothing.

  Now only that one message is replaced, inline in the transcript, with a card
  saying the rest of the conversation is unaffected and the session is still
  live. The boundary's reset keys include a **content** digest, not just
  `turn.id`: a streaming turn mutates in place under a constant id, so keying
  on identity alone would have wedged that message on the error card until a
  full page reload, even after the following tokens rendered perfectly. Keyed
  on content, a garbled message heals itself as soon as the next token arrives.

- **A page's `public` flag was silently cleared by reading it (#2054).** The
  panel wire shape was built from a record with no `public` column, so the flag
  travelled inward only. `crewship page export | crewship apply`, a hand-edited
  `page get -f json`, or the editor's own PATCH therefore **unpublished every
  public panel**. The published link kept resolving and rendered an empty page,
  and the next `crewship page publish` refused the page for having no public
  panels — pointing the operator at entirely the wrong thing.

  Five more from the same PR. A `metric.v1` sparkline drew straight through
  its gaps: the schema says a `null` marks a producer-known gap, and the
  renderer filtered nulls out of the array entirely, which also slid every
  later point leftwards and compressed the window's own time axis. A wake gate
  told the woken agent to run `crewship page set` — a binary that does not
  exist in the container it wakes in — and now names the sidecar `PUT` with
  `curl`. `crewship page set` threw away the 429's body, printing a bare rate
  limit instead of the reason, the scope ("this panel" and "this workspace"
  have different fixes) and the retry delay. **`-f quiet` was broken on all six
  page listings** — each hand-rolled a `tabwriter` table, which ignores the
  format, so `crewship page links x -f quiet | xargs` was fed column headers.
  And `crewship page rollback --to 0` answered "`--to <seq>` is required",
  because 0 is the flag's zero value; it now says what is actually wrong —
  versions are numbered from 1.

- **`OPENAI_COMPAT` was the one LLM provider that could not be tested, and it
  reported success (#2057).** Because such a credential is stored as
  `type = API_KEY`, it fell past every provider arm in `probeProviderInner` to
  a default that failed an `ENDPOINT_URL` type check and returned
  `{Valid: true, Error: "No validation available for this provider"}` **without
  dialling anything**. The operator pasted a base URL, pressed **Test**, got a
  green tick, and found out the endpoint was unreachable when an agent run
  failed. It is the provider where this matters most: the endpoint is the one
  part of it Crewship does not control.

  ⚠️ **Behaviour change:** `crewship credential test-stored <name>` and
  `POST /api/v1/credentials/{id}/test` now return a real failure for an
  unreachable host, a broken TLS chain, a wrong path prefix, or an endpoint
  serving no model list. The probe deliberately tests **reachability, not
  authentication** — it sends no `Authorization`, no stored key and no custom
  headers, because whoever holds `update` on a credential can repoint its
  `baseURL`, and sending the secret would turn Test into an exfiltration
  primitive. The unauthenticated body path (`POST /credentials/test`, no
  workspace and no role floor) still does not dial at all and says so.

- **The Add-MCP wizard's "Test" tested nothing and could not fail (#2078).**
  Its handler was a 400 ms `setTimeout` that set `ok: true` — no socket, no
  read of any field it claimed to check, and no failure path at any input. A
  typo'd endpoint, a command that does not exist, a host that is down: green
  tick every time.

  It is removed rather than wired, because it cannot be wired from where it
  stands: both real probes begin by selecting the transport, endpoint and
  command **out of the database**, and the wizard tests before it creates.
  There is no draft-test route for MCP as there is for credentials and
  notification channels. In its place the step says connectivity is checked
  after the server exists, and names the two surfaces that do it — the
  **Test connection** button on the server's row, and
  `crewship integration crew test <crew-slug> <integration-id>`.

- **A first integration grant silently revoked it from every other agent
  (#2070).** A workspace integration with **zero** agent bindings resolved for
  every agent; the moment any agent got one it flipped to opt-in and everyone
  else lost it — with no warning, no audit line and nothing on the
  integration's own page. It had been reachable only through
  `crewship integration bind`; the new create-agent form put a switch on it.
  #2070 could not reach the resolver, so it warned beside the switch, by name,
  which integrations were about to flip — warning rather than preventing,
  because making a first grant is legitimate.

  **Superseded inside this same unreleased window by #2079, below.** The
  audience is now a stored column instead of a row count, so a grant made on
  that form costs no other agent anything, and #2079 deleted the warning
  #2070 added. Nothing shipping here asks an operator to hesitate before a
  first grant; read the two entries as one story, not two.

- **An MCP server's audience is a stored column, not a binding count (#2079,
  closes #2072).** `ResolveAgentIntegrations` decided who could use a
  workspace MCP server by counting rows in `agent_mcp_bindings`. "Available to
  every agent" was never a stored state — it was the *absence* of bindings, so
  it evaporated the moment that absence ended: one
  `POST /api/v1/agents/{id}/integrations` anywhere in the workspace flipped the
  server to opt-in and revoked it from every agent relying on the default.

  `default_access` (`all` or `bound-only`) on `workspace_mcp_servers` and
  `crew_mcp_servers` replaces the inference, and **both** resolvers read it:
  `ResolveAgentIntegrations` (the console and `crewship integration resolve`)
  and `resolveAgentMCPServers` in `agent_config.go`, which is what the
  container actually gets. Fixing only the first would have shown an operator
  an access list the agent does not have — and the runtime copy was the worse
  of the two, because its binding count was not workspace-scoped at all, so a
  binding in a *different* workspace could revoke a server here. Both fail
  closed: only the exact string `all` opens a server to unbound agents. A
  binding is now purely additive — a credential, a config override, an
  opt-out — and cannot change what any other agent resolves.

  The audience is now sayable and visible:
  `crewship integration access <id-or-name> <all|bound-only>`, `--access` on
  `integration add` / `integration crew create` / `integration crew update`,
  `default_access` on the API, an `ACCESS` column on both `integration list`
  tables, and the integration's detail sheet naming it outright ("Available
  to: Every agent in the workspace" / "Bound agents only").

  Same pass: `mcp_tool_bindings` had no referential integrity, so deleting an
  integration stranded its per-tool toggles forever. Its `mcp_server_id` is
  polymorphic across two ID spaces, so a literal foreign key is not
  expressible; three triggers stand in for one — two `BEFORE DELETE` cascades
  and a `BEFORE INSERT` that rejects a toggle naming a server that does not
  exist — and the rows already orphaned are swept.

  ⚠️ **Behaviour change: the upgrade moves nobody's access, but the old side
  effect is gone for good.** Migration `20260826190607_mcp_default_access`
  defaults the column to `all` and then pins every server that already carries
  an agent binding to `bound-only`, freezing each server at the audience it
  effectively had — nothing is granted and nothing is revoked at upgrade time.
  After that, only an explicit change alters who can use a server. Anything
  that relied on binding one agent to keep a server private must now say so:
  `crewship integration access <id-or-name> bound-only`.

  ⚠️ **Behaviour change: a replace-mode restore now clears that workspace's
  per-tool toggles.** `mcp_tool_bindings` is deliberately excluded from
  backups, and `crewship backup restore --replace` deletes and re-inserts the
  workspace's server rows; the new cascade trigger takes the toggles with them,
  where before they survived by being orphaned and were then silently
  re-adopted by the re-inserted row. Toggles default to enabled, so a cleared
  set means every tool is on — re-disable the ones you want off with
  `crewship integration tools disable`.

- **The mission fork button called a route that does not exist (#2056).** It
  posted to `POST /api/v1/missions/{missionId}/fork`; the route is
  `POST /api/v1/checkpoints/{checkpointId}/fork`. Every fork 404'd, and the
  404 was rendered as the friendly *"Not yet wired to backend"* — so a broken
  call read as an unbuilt feature. It now forks and navigates to the new
  mission, and a 2xx that carries no new mission id is surfaced as an error
  rather than as success.

  The checkpoint id it passed was wrong too: it fell back to the journal row
  id, which could only ever 404. The id is now read from where the journal
  actually writes it, and Restore is **disabled** when there is none rather
  than firing a request that cannot succeed.

  Also from #2056: chat reactions moved off `localStorage` onto three real
  endpoints, so a reaction survives a different browser (the legacy
  `crewship-reactions` key is dropped on load and **not** migrated); the crew
  slug field enforces its rule as you type instead of 400-ing three steps
  later; the crew wizard's "Never" auto-stop actually means never, rather than
  omitting the field and inheriting the 4-hour default; New project stopped
  sending a summary and labels that the endpoint binds nowhere and silently
  discarded; and popovers inside dialogs scroll instead of clipping.

- **New routine had no Cancel on two of its four screens (#2076).** The
  `entry` and `fork` modes — one of which is the screen the dialog opens on —
  rendered an empty footer strip where every other create surface in the
  product puts Cancel. Esc, the header × and an overlay click always worked, so
  it was never a dead end; what was missing was the affordance, in the one
  place the shell's own contract promises it will always be. The hint reads
  `Esc to cancel` rather than the default `⌘↵ to confirm · Esc to cancel`,
  because ⌘↵ genuinely does nothing on a screen whose action is a row.

- **The crew and agent canvas tab strips said "tab" without meaning it
  (#2026).** They were plain buttons carrying `aria-selected`, which is not
  allowed on a button's implicit role — so the one fact the markup tried to
  convey, *which section you are on*, was dropped by the screen reader and
  raised an axe `aria-allowed-attr` violation. There was no tablist, no
  labelled strip and no tabpanel. All three now exist. Separately, the admin
  console's content pane was a scrollable region with no focusable child, so on
  the default Overview section a **keyboard-only admin could not scroll it at
  all** and anything below the fold was unreachable; and the chat crew link was
  distinguished from surrounding text by colour alone, at a token below the
  4.5:1 contrast floor.

  **Known gap:** the canvas tab strips still have no arrow-key roving focus —
  each tab is its own Tab stop. CodeRabbit raised exactly this on the PR and it
  merged unimplemented. Its sibling #2025 shipped that pattern for the Pages
  strip, so the two now differ.

- **Every Pages tab was announced without the panel it reveals (#2025).** The
  buttons carried `role="tab"` and the groups carried `role="tabpanel"`, and
  **nothing linked them** — so a screen-reader user activating a tab heard
  "tab, selected" and then had to hunt the document for whatever had appeared,
  which is the entire thing the tabs pattern exists to prevent. The two halves
  are now joined by id, and the strip gained full keyboard orchestration:
  arrow keys cycle, Home/End jump, and roving `tabIndex` makes the whole group
  one Tab stop instead of one per tab. There is no axe rule for a tab missing
  `aria-controls`, which is why this went unnoticed; the guard is explicit
  assertions instead. The bar is also suppressed entirely on an error, where
  stale cached data used to render it above a body that mounted no panels — a
  screenful of dangling `aria-controls`.

<!-- End of the #2086 backfill. Entries below were written with their PRs. -->

### Fixed

- **The `admin` family answered from a different database than the server you
  pointed it at.** `openAdminDB()` resolved `~/.crewship/crewship.db` and
  ignored `--server`, `CREWSHIP_SERVER` and `--profile` entirely, while its own
  comment claimed it "mirrors the resolution logic of the server" — false on
  every host where crewshipd runs with its own `DATABASE_URL`, which is every
  development clone (`file:./crewship.db`), every container and every
  multi-instance box. `crewship admin list-users` against a populated server
  printed `(no users — run 'crewship seed' …)` and exited **0**.
  `db migration-status` reported an unrelated schema version under the heading
  `Database:`. `memory log` / `memory show` audited an audit chain that was not
  the one under audit — and the two routes that answer those questions,
  `GET /api/v1/admin/memory/versions` and `…/{id}/content`, had no CLI command
  at all.

  `list-users`, `memory log` and `memory show` now read the server, through the
  routes that already existed. Everything with no route — `reset-password`,
  `promote`, `invalidate-sessions`, `sessions list`, `memory restore`,
  `db migration-status`, `db repair-ledger`, `db restore-snapshot`,
  `keeper eval` — names the file it resolved and **refuses to run when a server
  is named**, unless you pass the new `--local`. A server on `localhost` is not
  an exemption: the old rule warned only for a *remote* target, on the
  inference that a server on this host must use this host's data directory, and
  that inference is exactly the hole this was reproduced through.
  `db migration-status` and `keeper eval` also honour `DATABASE_URL` for the
  first time.

  The read-only diagnostics were the last holdout and are fixed with them:
  `crewship doctor` (the schema, consent and DSN checks) and
  `crewship telemetry status` opened `~/.crewship/crewship.db` regardless of
  `DATABASE_URL`, so on a dev clone they reported on a file the running
  instance had never written — the #2086 defect surviving inside the command
  you run to diagnose #2086. They resolve `DATABASE_URL` now. They are
  deliberately **not** gated: a diagnostic reports on the host you run it on,
  and one that switched itself off because `CREWSHIP_SERVER` was set would be
  unavailable exactly when something is wrong. What replaces the refusal is
  naming — the `db migration version` row and `telemetry status` both print the
  path they read and where it came from.

  **Breaking for scripts, in two different shapes.** The gated commands refuse
  only when something names a server, so `--local` is newly required from a
  shell that exports `CREWSHIP_SERVER`, on a machine that has run
  `crewship login`, or under a `--profile`. `admin list-users` is **not** gated:
  it branches on `--local` alone, so it now goes to the server and needs a login
  *unconditionally* — including on a fresh host with no CLI config and no
  `CREWSHIP_SERVER`, where it previously printed the table without one. That is
  the locked-out-operator case, and it is deliberate: a command that fell back
  to a local file whenever the login failed would be #2086 with an extra step,
  answering "who exists on the server" from a file that may belong to a
  different instance. Both failure paths name the fix in their own message, and
  the fix is one word — `crewship admin list-users --local` is the old
  behaviour, asked for explicitly. (#2086)

- **`crewship resume <chat-id>` never worked, and the guard that should have
  caught it could not see two thirds of the CLI.** `resume` asked for two flat
  chat routes — `GET /api/v1/chats/{chatId}` to find the agent that owns a
  chat, and a workspace-wide chat list for the picker — neither of which the
  router has ever registered. The lookup 404'd on every invocation and the
  user was told `could not determine agent for chat <id>`; the picker's 404
  fell through to a `/runs` fallback that was therefore the only code path
  that had ever run. Both now call routes that exist: the agent lookup walks
  `GET /api/v1/agents` → `GET /api/v1/agents/{agentId}/chats`, which is the
  only way a chat is addressable, and the picker reads `GET /api/v1/runs`
  directly, deduplicated so several runs of one chat no longer fill it with
  duplicates of the same session. No new server route was needed. The
  CLI↔route contract test is what should have failed years ago, so its
  extractor was hardened first: it now resolves paths assembled into a local
  (including with `+=`), the `api_helpers.go` wrappers, `Do`/`NewRequest`/
  `StreamSSE`/`StreamNDJSON`, package-level path consts, path helpers whose
  own parameters are filled from the call site (so `proposedPath(id,
  "approve")` is checked against the route registered for *approve* rather
  than collapsing to an unregistered `…/{}/{}`), and forwarders discovered
  from source rather than listed — 1013 call sites checked against
  the router's real registrations, up from ~450, with the vacuity floor moved
  to match. Two bounds this walk carries are now named in the errors and the
  docs rather than left to be discovered: an agent's chat list is a hard 100
  most recent with no page parameter, so an older chat must be resumed by
  run-id, and a 403 on every agent is reported as an access failure instead
  of exiting not-found with "chat not found".

- **A CLI render that failed mid-stream said only "broken pipe".** The
  formatter's JSON, YAML and NDJSON renderers returned the encoder's error
  untouched, and what usually reaches that return is a *write* failure — a
  closed pipe from a `| head -1`, a full disk — whose text names neither the
  format nor the fact that output was being serialised at all. Each renderer
  now names itself, and the NDJSON slice path names the row index, so a partial
  stream says how much of it a consumer already received. The context is added
  once at the renderer rather than at the four routing helpers and the ~110
  direct call sites above them, so the message reads the same whichever of them
  produced it. `gopkg.in/yaml.v3` flattens the cause into its own error type
  before we see it, so `errors.Is` cannot reach through a YAML render error —
  the text survives, and a test pins that difference rather than leaving it to
  be rediscovered.

- **A port-expose URL on Colima returned a bare `502` and explained nothing.**
  The capability-URL proxy dials the crew container on its Docker bridge IP,
  which is reachable only where crewshipd shares a network namespace with
  dockerd — true for Docker Engine on Linux and for OrbStack, false for
  Colima, Rancher Desktop and Docker Desktop, where the bridge exists only
  inside a VM. Every request timed out and rendered as `bad gateway`, leaving
  the one useful fact — the target address — visible only in the server log,
  which is not where a self-hoster debugging their own setup looks. The 502
  now names the target, and when a *dial* into a private address was
  blackholed it offers VM routing as one of the two situations that look
  identical from the proxy's side; a refused connection proves routing works,
  so the runtime is not blamed there, and neither is a slow response, which
  is no longer classified at all. The constraint is now documented on the
  port-expose API page and the `expose` CLI page. The unreachability itself
  is unchanged — this is diagnosis, not support for those runtimes.

- **A dead escalation sat under "Waiting on you" forever, as two rows.**
  `scopeOf` recognised exactly one terminal `payload.state` — `resolved` —
  but a `peer.escalation` also ends as `expired` (the deadline answered it)
  and `cancelled` (an operator withdrew it). Both carry the same entry type
  and the same `refs.escalation_id` as the ask, so each one missed twice: the
  terminal row was filed as a *fresh* ask, and it closed nothing, leaving the
  original `pending` row open beside it. Two escalations nobody could still
  act on produced four permanent entries. The terminal set is now explicit
  (`resolved`, `expired`, `cancelled`) and a test scans the Go emit sites, so
  a fifth state cannot be added to the API without this list hearing about it.
  A state the classifier has never seen is still treated as *open* —
  unrecognised is not the same as answered.

- **Clicking "Waiting on you 1" opened a list of five.** The Overview card
  counts open asks over the whole window; the Waiting scope it links to
  refetched with `entry_type` set to the ask types alone. The *answers* —
  `approval.granted` / `denied` / `cancelled` / `timeout` and
  `keeper.decision` — are filed under the Security facet, so they were
  excluded server-side, and the client-side join that retires an answered ask
  had nothing to join against. Approvals and keeper requests could therefore
  never be retired in that scope, and a grant arriving live could not retire
  one either, because the stream shares the query. The waiting fetch now asks
  for both halves; the answers never reach the feed, because the same
  narrowing files them under Completed. Because the journal pages by a fixed
  row count from the newest end, those extra rows would otherwise have pushed
  the oldest open asks out of the window — hiding them instead of listing
  them — so the Waiting scope alone now pages to the API's 500-row ceiling.

- **An exec on Apple Containers could be reported as running forever.** Both
  exec paths spool the CLI's output into memory, so `os/exec` gives the child
  a pipe and copies out of it in a goroutine — and `cmd.Wait` blocks on that
  goroutine until every write end of the pipe is closed. A descendant the
  `container` CLI leaves behind holds one, so a single orphan wedged `Wait`
  for its whole lifetime. Nothing recovered from that on its own: the exec
  entry is marked finished only after `Wait` returns, so `ExecInspect`
  answered "still running" indefinitely and the sweeper never reclaimed the
  entry. Waiting on the pipes is now bounded, and the command's real exit
  status is still what the caller sees.

- **A crew's issue prefix could mint an issue at an address no route can
  reach (#2035).** `crews.issue_prefix` had no charset or length check on any
  write path — `PATCH /api/v1/crews/{crewId}` stored it verbatim, and the only
  branch was `""` → `NULL`. The prefix becomes the leading half of the issue
  identifier, and that identifier is a **single URL path segment** on around
  twenty routes: get, patch, delete, comments, attachments, relations. So
  `--issue-prefix "A/B"` filed `A/B-1`, an issue that exists, lists, and can
  never be opened, and a space, `%`, `#` or `?` each broke the same segment
  their own way. The prefix must now match `^[A-Za-z0-9_-]{1,16}$` on write,
  with a 400 that names the field and states the rule; `""` still means "clear
  it". Validated on the API rather than in the CLI, so the web UI is covered
  by the same guard. **Prefixes already stored are not migrated and not
  refused on read** — they keep minting exactly what they mint today — but a
  prefix outside the rule cannot be written again, so a crew holding one has
  to move to a valid prefix the next time it changes.

- **A restore dropped columns your schema does not have, and said nothing.**
  Applying only the columns the target has is what lets a bundle from a newer
  Crewship restore onto an older instance, and every migration this project
  had written was additive, so the dropped column was always one the target
  genuinely did not need. A migration that *re-keys* a table breaks that
  assumption: a bundle taken before `issue_counters` moved from `crew_id` to
  `(workspace_id, prefix)` carries a key column the new table does not have,
  the statement degenerates to `INSERT OR IGNORE INTO issue_counters
  (next_number) VALUES (?)`, and the `NOT NULL` violation that follows is
  swallowed by `OR IGNORE`. No error, no row, and `rows_inserted` counts a
  row that never landed as nothing at all — indistinguishable from a bundle
  that never carried it. Restore now counts every discarded value, names each
  `table.column` and how many rows carried it, and warns; `--dry-run` reports
  the same skew before anything is written. The drop itself is unchanged —
  what is gone is the silence, which is what made this shape of loss
  (#1437, #1444, #1973) discoverable only months later.

- **Upgrading threw finished users back into the setup wizard.**
  `onboarding_skipped_at` was added without a backfill, and
  `OnboardingHandler.Status` reads a NULL there as "this completion was
  interrupted, reopen it". Sound on a fresh install; on an upgrade every
  pre-existing completion is NULL by construction, so anyone whose workspace
  happened to hold no agents — they pressed Skip, or they finished properly
  and later deleted their crews — was sent back to step one, and Status
  *persisted* the downgrade rather than merely rendering it. Backfilled: a
  completion recorded by a build that had no such column is, by definition,
  not one this build may reopen.

- **An OpenAI or Google workspace got a Claude model id.** The marker template
  the Guide is told to emit interpolated its suggested model from
  `providerRuntimeDefaults` (`gpt-5.5`, `gemini-2.5-pro`) while
  `validateCrewModel` checked against `llm.CuratedModels`, which contains
  neither. The Guide emitted the id it was handed, the validator missed it and
  substituted the Anthropic default, and the crew was created as
  OPENAI + CODEX_CLI + `claude-sonnet-5` — every field valid, the combination
  unrunnable at the adapter on every run. Both now read the same catalogue.
  A provider whose models live on the operator's own daemon (Ollama and
  anything self-hosted) is told to omit the field instead of being handed a
  Claude id to copy.

- **A crew name in Czech, Greek or Japanese could discard the whole
  proposal.** `crew_name` was still length-checked with `len()`, which counts
  bytes — the same fault fixed for `role`, one field over. 120 bytes is about
  60 accented characters, so an ordinary non-ASCII crew name silently dropped
  the entire marker and no card ever appeared. Counted in runes now.

- **`GET /system/runtime` reported `in_use` only to unprivileged callers.**
  The onboarding wizard gates its Crew step on that field and blocks Continue
  unless it is exactly `true`. It worked only because the probe sends no
  workspace context and so never resolves a role; an owner who did would read
  `undefined` and be stuck permanently behind a re-check button that could
  never clear. Present on both branches now.

- **The token landed after CLI pairing went to the wrong workspace.**
  `GET /api/v1/workspaces` sorts `created_at DESC` while every onboarding
  handler resolves the user's membership `ASC`, so taking the first row wrote
  the freshly paired credential to the *newest* workspace for anyone who
  belongs to more than one — and `autoAssignCredentials` links workspace
  credentials to agents at deploy time, so the crew launched with none and
  could not be repaired afterwards. The frontend already sorted for this
  reason; the CLI now does too.

- Wake automations compiled from an agent-authored page recorded the agent's
  id in `automations.created_by`, a user-attribution column with no foreign
  key to catch it.

- **The Credentials filter panel shut itself after every pick.** Each facet
  called `setFilterOpen(false)` on select, so combining a brand with a scope
  meant reopening the menu between them, and a facet held exactly one value —
  a switch wearing a filter's clothes. The rail now uses the shared
  `SidebarFilterPopover` from the sidebar kit, the same panel Issues runs on:
  the panel stays open, each facet carries its own reset row, and Escape
  closes it. `CredentialFilters` facets are lists, so values inside a group OR
  and the groups AND — "any Anthropic or GitHub certificate this crew can
  reach" is now one pass through the panel.

### Added

- **A database write looked exactly like `ls` in the run trace.** Sub-span
  kinds were derived from the tool NAME alone, and every shell call is the
  same tool — so `psql -c "delete from orders"` and a directory listing both
  rendered as an anonymous `bash` row, and the one question a trace exists to
  answer ("what did this run touch?") had no answer for infrastructure. Shell
  calls and MCP calls that reach a datastore now carry their own `db` kind and
  name the engine (`postgres`, `redis`, `mysql`, `mongodb`, and a dozen more)
  in `attributes.tool`, so the row shows the store's own mark rather than a
  terminal glyph.

  Classification reads the first executable of the command — past leading
  `VAR=value` assignments, a bare `sudo`, quotes and any directory prefix —
  and stops there. It deliberately does not split on `&&`, `|` or `;`, or
  descend into subshells and here-docs: doing that correctly means
  implementing shell quoting, and doing it *incorrectly* is how
  `echo "psql is great"` becomes a database span. Anything the shallow parse
  misses — `cd /app && psql …`, an engine not on the list, an MCP server
  called `prod-db` — keeps its old `bash` / `mcp_tool` kind. Under-classifying
  is the designed failure: a span that renders as a shell call is recoverable,
  a span that lies about what it touched is not.

- **The Go toolchain pins are now checked against each other on every PR, and
  the image can no longer download a compiler behind their back (#2064,
  partial).** Thirteen files name a Go version and nothing verified they
  agreed. One of the thirteen is the root `Dockerfile`, which is built by
  `release.yml` and `nightly.yml` and by nothing that runs on a pull request —
  so `FROM golang:<ver>`, the compiler for the shipped binary and a line
  Dependabot bumps on its own, was the one pin structurally beyond reach of
  the checks meant to catch it. #2060 was that bump: taken alone it would have
  released binaries built by 1.27.0 while every CI pin and the vuln gate
  stayed on 1.26.6. It was caught by hand.

  `scripts/go-toolchain-pin.sh` parses `go.mod`'s `toolchain` directive, the
  Dockerfile's `FROM` tag, `GO_VERSION` in ten workflows and the literal
  `go-version` in `codeql.yml`, and fails naming the file and line of every
  disagreement. It runs in CI's `Shell` job, which a Dockerfile-only PR does
  reach — `paths-ignore` never covered `Dockerfile`. `GO_VERSION` is in the
  set deliberately: `golangci-lint` and `govulncheck` are pinned *to* it, so a
  CI toolchain that drifts from the image is the vuln gate grading a compiler
  that is not the one shipping. `go.mod`'s `go` directive is deliberately
  outside it — the language floor is a separate promise and stays at 1.26.

  The Dockerfile now states `ENV GOTOOLCHAIN=local` in its Go stage. The
  official golang-alpine images already default to it, so nothing changes
  today; the point is that an inherited upstream default is a fact that
  happens to hold rather than an invariant, and written down it survives a
  base-image swap and can be checked. `local` and not a version literal:
  `local` makes the `FROM` tag the sole authority and never downloads, where
  the default `auto` fetches whatever `go.mod`'s `toolchain` line names, and a
  literal `GOTOOLCHAIN=go1.27.0` would be one more copy to keep in sync that
  fails backwards — a forgotten update would silently *download* the old
  toolchain and undo a base-image bump with every check still green.

  Worth stating because it is the reason the check is static: under `local`
  the `toolchain` directive is ignored outright, so `toolchain go1.27.1`
  against a `golang:1.27.0-alpine` base builds with 1.27.0 and exits 0. Only
  the `go` directive can fail a build, and that one stays at 1.26. No image
  build, not even nightly's, would ever report this drift — it has to be read
  off the source.

  This closes the specific hazard from #2060, not the general one. A
  PR-triggered image build — the other half of #2064 — remains open, so
  breakage that only an actual `docker build` can surface (a missing `COPY`
  as in #849/#886, a `pnpm prisma generate` regression, the `web/out` release
  gate from #1567) is still first caught by nightly.

- **`crewship oauth` — the connect flow had no CLI at all.** Six registered
  endpoints (`providers`, `initiate`, `exchange`, `loopback`, `discover`,
  `auto-connect`) were reachable only from the dashboard, so connecting an
  integration was the one setup step an agent could not perform.
  `crewship oauth connect` runs the loopback leg and then **waits for the
  credential to reach `ACTIVE`**, because there is no completion endpoint and
  the credential's status is the only truthful signal — a wait that runs out
  exits non-zero and names the status it is stuck in rather than printing a
  tick over tokens that never arrived. `oauth authorize` + `oauth exchange` is
  the leg for a browser that cannot reach the API host, and `exchange` sends
  the `--state` token, which is what lets the server recover the PKCE verifier
  it stored; the web UI omits it, so that path fails against any provider that
  enforces PKCE. `oauth auto-connect` treats the server's
  `status: "needs_client_id"` — a `200` that creates nothing — as a failure,
  and prints the `credential create` to run instead.

- **`crewship credential create --type OAUTH2` could not set the OAuth app.**
  `POST /api/v1/credentials` has accepted `oauth_client_id` and its endpoints
  since the flow was written; the CLI exposed none of them, so an `OAUTH2` row
  could only be minted through the web UI and `crewship oauth` had nothing to
  operate on. New `--oauth-provider` fills the authorize URL, token URL and
  scopes from the same catalogue `crewship oauth providers` prints;
  `--oauth-client-id/-secret/-auth-url/-token-url/-scopes` cover a provider the
  catalogue does not carry. The row is created empty and `PENDING`, no value is
  invented for it, and nothing is probed — there is no token yet to probe. The
  flags are refused on any other `--type`, where the server would have dropped
  them silently, and refused alongside `--value`/`--value-stdin`, which would
  otherwise fill the same column twice and discard one of the two. Filing a
  token obtained elsewhere as `--type OAUTH2 --value <token>` is untouched.
  `--oauth-client-secret-stdin` keeps an app secret out of `argv` — it outlives
  every token it issues, and an argument is readable by anything that can see
  the process table. Both refusals key off the flag being *named*, not off it
  carrying a value, so an explicitly empty source cannot slip past them and be
  dropped in silence; and both are decided before anything reads stdin, which
  is what makes `--value-stdin --oauth-client-secret-stdin` a refusal instead of
  a race between two readers over one stream.

- **`crewship consolidate proposed` — the human half of memory consolidation.**
  `consolidate run` triggered the extraction; the four review endpoints
  (`explain`, `diff`, `approve`, `reject`) had no CLI, and `explain` and `diff`
  had no consumer anywhere — not even the web UI, which only wires the
  approve/reject buttons. `approve --diff` fetches the preview *before* it asks
  to confirm, which is the pairing the server's byte-equality guarantee between
  preview and write was built for. `reject --reason` says out loud that the
  server does not persist the reason yet, rather than implying an audit trail
  that is not there. The help names both things the API cannot tell you:
  proposals only exist under `CREWSHIP_CONSOLIDATE_HITL=1`, and since no
  endpoint lists them, the id comes from
  `crewship inbox list --kind memory_consolidation`.

- `crewship onboarding proposal create --agent "Name:Role"` (repeatable)
  names a bespoke roster, so the CLI can finally reach the branch the Guide
  actually takes. `--template-slug` is no longer required — give one or the
  other. The role is split on the first colon only, because a role is a
  sentence and routinely contains more.

- **Everything the onboarding Guide built belonged to the Guide.** A routine
  or page authored during setup was attributed to `_crewship-setup`, the
  server-created crew the Crewship Guide itself runs in, because the sidecar
  injects `author_crew_id` from its own IPC config — the gate that stops
  Crew B claiming to be Crew A, correct for every crew except the one whose
  entire job is building for others.

  `author_crew_id` is not a label. `internal/pipeline/egress_gate.go` checks
  a routine's HTTP steps against the AUTHOR crew's allowlist, so a routine
  written to poll `seznam.cz` was gated on the Guide's network policy and
  could only be unblocked by widening the Guide; `internal/pipeline/executor.go`
  runs agent steps in the author crew's container, so the work ran as the
  Guide rather than as the crew meant to do it; and the Guide's own
  `autonomy_level` is `full` (it must be — it creates Pages), so a person's
  routines took up permanent residence in the most privileged crew in the
  workspace, outliving onboarding by months. Pages had a fourth version of
  the same fault: a panel names its producer as `agent/<slug>`, and
  `discover_capabilities` could only see the Guide's own roster, so pages
  built at setup pointed their panels at the Guide.

  A `kind='setup'` crew may now name the crew it is authoring FOR
  (`target_crew_slug` on `/internal/pipelines/save`, `/internal/pipelines/test_run`
  and `/internal/pages/save`; `crew` on the `save_routine`, `save_page` and
  `discover_capabilities` MCP tools) and, in exchange, may own nothing at
  all. Both halves are load-bearing: without the second, naming a crew is
  an option the model forgets to take and the orphans come back. The
  exception stays narrow — an ordinary crew naming another crew is still
  403, the same cross-crew escalation the original gate exists to stop, and
  the target must be a non-setup crew inside the workspace the caller's
  token is already bound to. Ownership ordering (crew first, then its work)
  is now enforced by the slug simply not resolving rather than by the
  prompt remembering, and the refusal lists the workspace's real crew slugs
  because a slug is derived server-side and the Guide never sees what its
  proposed name became. The autonomy gate continues to ask about the ACTOR,
  not the owner — a brand-new crew defaults to `guided`, and holding a page
  the Guide is plainly permitted to create would leave setup unable to
  finish its own job.

- **A crew's first message could be answered by total silence when its
  devcontainer needed a build.** The server deferred the message (streamed a
  `crew_provisioning` build card, returned the `ws.ErrCrewProvisioning`
  control-flow sentinel) and relied on the CLIENT to notice the build
  finished and resend — but completion was only broadcast on the workspace
  realtime channel, fanned out to whoever happened to be subscribed at that
  instant. A client that hadn't yet opened its second WebSocket connection,
  hadn't resolved `workspaceId`, or had simply closed the tab never saw the
  frame, and the HTTP poll backstop only sped up once it observed the very
  signal it had just missed. On a cache-hit build (~90ms) the completion
  frame was routinely gone before anything was listening.

  The server now owns the resume. `chatbridge.Bridge.HandleChatMessage`
  attaches the deferred send to the crew's provisioning job
  (`api.ProvisioningHandler.AttachPendingMessage`); the job's own completion
  point runs — or fails — it directly, streaming the outcome on the chat's
  own session channel (`hub.BeginSessionRun`), the one channel a client is
  always reliably subscribed to. At-most-once is structural, not a flag: the
  job tracks at most one pending message per chat (a manual resend or a
  second deferred send on the same chat while the build is still running
  *coalesces* onto the latest content instead of queuing a duplicate), that
  slot is drained atomically with the job's terminal-state transition, and
  the bridge's existing per-chat run exclusivity (`tryMarkRunStart`) means a
  resumed run racing a live manual send never persists twice — whichever
  loses gets `ws.ErrAgentBusy` and stays silent rather than double-answering.
  A failed build now surfaces a real `error` chat event instead of silence or
  a false success.

  The client-side auto-resume (a `useProvisioningStatus` poll that called
  `sendMessage` again once a crew's status flipped to `completed`) is
  removed — it was the unreliable mechanism this fix replaces, and the test
  that shipped it (`onboarding-setup-chat.test.tsx`, stubbing
  `useProvisioningStatus`/`useWorkspace`/`RealtimeProvider`) proved the
  client's wiring against a working feed and never exercised the race at
  all. The "Resend now" button survives as a manual fallback only, safe even
  mid-build because of the coalescing above.

- **A crew created by onboarding could not run an agent at all, and had not
  been able to since 2026-04-15.** Four `INSERT INTO crews` statements omit
  `devcontainer_config` — the two onboarding paths, recipe install, and the
  agent-facing `CreateCrew`. A crew with no config was refused by the
  provisioning gate, never had an image built, and fell through to bare
  `debian:bookworm-slim`. The agent then died with `exit 127`,
  `stdbuf: failed to run command 'claude': No such file or directory`.

  Commit `8780f3c4` (PR #154) deleted the pre-provisioned agent-runtime image
  and switched the platform default to bare Debian, adding these columns with
  no backfill. Seed data got its own config six weeks later; the templates
  never did — which is why `./dev.sh seed` produced working crews and the
  wizard did not, and why four months passed before anyone noticed.

  The default is applied where the config is **read**, not at the four write
  sites. Adding it to the INSERTs is the obvious fix and the wrong one: this
  repo has already paid for that lesson twice (`internal/crewstart` exists
  because thirteen callers each assembled their own config and three forgot
  `CachedImage`), and a read-time default cannot be missed by a creation path
  that does not exist yet.

  Three readers are deliberately NOT defaulted: the crew editor (a GET-merge-
  PATCH would silently freeze the default into every crew the first time an
  operator toggled a security flag), backup collection (a restore must
  reproduce what was stored), and the manifest export that reads them. A crew
  running on the default reports `devcontainer_config_defaulted` so it is
  visible rather than silent.

- **The chat said "an error occurred processing your message" while the server
  was building the crew's image.** The provisioning handshake returned an error
  purely as a control-flow sentinel; it was logged at ERROR and rendered as a
  generic failure stacked under the informative build card. It is now a typed
  sentinel (`ErrCrewProvisioning`), logged at Info, and the user is told the
  environment is being built once and to resend.

- **An agent whose container lacks its CLI now says so.** Adapter-exec failures
  had no classifier: `exit 127` reached the user as "agent exited with code 127
  — check the journal for details", while the one fact that resolves it — the
  binary is not installed — sat in the container's stderr and was discarded.
  Failures are now typed, the container output rides the error event as
  metadata, and a missing binary is distinguished from a crash.

- **A credential could not be checked, and was reported as valid.**
  `probeProviderInner` short-circuited every `sk-ant-oat` token with "OAuth
  token accepted (cannot validate via API)" having contacted nothing — and that
  claim was disproven by `probeAnthropicOAuthToken` forty lines away in the
  same package. It backs `/credentials/test`, the "Test now" button and
  `crewship credential test-stored`, so for the one credential type onboarding
  accepts, every tool anyone had answered "fine" without asking. The CLI
  carried its own copy of the same wrong assumption and skipped before it even
  reached the server.

  Probing is now shared, and it has three outcomes rather than two: accepted,
  refused, and **could not ask** — which is rendered as neither. The probe also
  uses the model the user actually chose; it hardcoded a cheap Haiku, so a
  token with no entitlement to the chosen model passed onboarding and failed on
  the first message.

- **"I could not determine whether the image is present" no longer reads as
  "the image is missing."** `ensureImage` collapsed an errored `ImageInspect`
  into "absent" and told the operator to reprovision a crew whose image was
  never gone. The error-distinguishing variant already existed one function
  below and was unused.

### Added

- **A container-boot conformance test that needs no API key.** The sidecar's
  `/health` is served from in-memory state, so a real container, a real sidecar
  and a real health probe cost nothing to run. It asserts uid 1001 resolves,
  `claude --version` succeeds inside the container, and `/health` answers 200 —
  the exact facts the four-month regression broke. ~9 s warm; it belongs on
  every PR, because a nightly cannot protect a branch that has already merged.

  Deliberately not modelled on the existing runtime-conformance harness's
  four-byte ELF stub: that shortcut is right there, where nothing execs the
  sidecar, and exactly wrong here, where the sidecar booting is the point.


- **Tabs on a page (#1935).** A panel may declare `tab: Odezva`, and the page
  grows a bar under the breadcrumb — several screens instead of one long
  scroll. One optional key on the panel and **no `tabs:` block**: adding a tab
  is one word on the panel that needs it, and there is no second list that can
  disagree with the panels about which of them exists. Bar order is first
  appearance in the panel list; a panel that declares none lands on the first
  tab; a page where nothing declares one has no bar and renders exactly as it
  did before.

  A tab HIDES panels, which is why this is not only a layout feature. Every tab
  carries the **worst freshness state of its own panels** — `failed` over
  `stale` over `never_produced` over `fresh` — as a glyph beside its name and
  never as colour alone, and the page's own freshness summary is computed over
  **every** tab rather than the visible one, so it does not move when the tab
  does. Without both, a critical panel could sit failing on the third tab while
  the page read fine, which is the silent-old-numbers failure the freshness
  contract exists to prevent.

  A tab whose panels are all sealed to a reader still appears on that reader's
  bar, carrying its placeholders: the page has the same shape for everyone, and
  a bar that reflowed per viewer would disclose, by what it left out, whose data
  was on it. `tab` therefore rides on the sealed placeholder too — page
  structure, like `span`, never the panel's data.

  The selected tab is in the URL (`/pages/sit?tab=odezva`). **Print ignores
  tabs**: paper cannot be clicked, so a printed page renders every tab's panels
  in bar order under their tab names, with no bar. On a phone the bar scrolls
  sideways rather than wrapping. A tab name that is blank, longer than 32
  characters, carries a control character, collides with another differing only
  in case, or is the ninth tab on a page, is refused at save with the reason.

  Carried end to end: YAML → CLI → API → the manifest kind (including drift
  detection, on sealed panels too) → `page export`/`import` → the in-app editor.
  The editor was **dropping `icon:` on every save** the same way it would have
  dropped `tab:` — `PATCH` replaces the panel set wholesale, so a key the editor
  does not mention is a key the save deletes — and both now round-trip.

- **Attachments on issues — and the agent working the issue can read them
  (#1768).** Attach a crash log, a screenshot, a repro bundle or a diff to an
  issue over the API (`GET`/`POST` `…/issues/{ident}/attachments`,
  `GET`/`DELETE` `…/{attachmentId}`), over the CLI (`crewship issue attach` /
  `attachments` / `attachment` / `detach`), or from an agent through its
  sidecar (`GET`/`POST /issue/{ident}/attachments`,
  `GET /issue/{ident}/attachments/{id}`). The agent half is the point: a file
  an agent cannot read is decoration, and until now the only way one reached an
  agent was a human pasting its contents into a comment. Every attach and detach
  goes through the shared issue-event emitter, so it lands on the timeline **and**
  in the journal — which is what makes it notifiable.

  Content reaching an agent is treated as attacker-controlled: both the file's
  **text** and its **filename** are wrapped by `internal/untrusted` before they
  leave the handler (`ignore previous instructions.txt` is a shorter payload than
  the file, and it shows up in every listing). Binary comes back base64, budgeted
  at 512 KiB, text at 128 KiB, both with an explicit `truncated` flag — a silent
  truncation is worse than a refusal.

  The type is resolved from the file's **extension** against an allowlist; the
  request's own `Content-Type` is discarded, because honouring it is how a stored
  file becomes stored XSS served from your own origin. `.html` and `.svg` are
  absent deliberately. Downloads carry the resolved type plus `nosniff` and
  `Content-Disposition: attachment`.

  Blobs are content-addressed at
  `attachments/<workspace>/<sha[0:2]>/<sha>` — every component derived from bytes
  we computed, so a filename of `../../../etc/passwd` is stored as the label
  `passwd` and path traversal is not expressible rather than merely refused.
  Identical bytes are de-duplicated **within a workspace and never across one**:
  a cross-tenant shared blob would make workspace erasure undecidable and would
  turn write-time de-duplication into an existence oracle. Deletion is
  reference-counted, so removing a file from one issue never removes it from
  another that carries the same file. Cascade deletes (an issue hard-deleted)
  never reach that refcount, so `crewship issue delete` runs a reclaim pass
  derived purely from the table.

### Changed

- **Go toolchain moved 1.26.6 → 1.27.0, and every place that names it now
  agrees (#2060).** Dependabot bumps the root `Dockerfile` alone, which is the
  one Go version no PR ever exercises — the image is built only by `release.yml`
  and `nightly.yml`. Taken by itself that bump would have shipped release
  binaries compiled by 1.27.0 while all eleven CI pins, `go.mod`'s `toolchain`
  directive and the `Go Vuln Scan` gate stayed on 1.26.6, so the published
  artefact would have been the only thing built by a toolchain nothing had
  verified. The pins move together instead: `GO_VERSION` in ten workflows,
  the literal in `codeql.yml`, and `toolchain go1.27.0` in `go.mod`. The `go`
  directive stays at 1.26 — the language floor is a separate promise to
  consumers and nothing here needs 1.27 semantics.

  No advisories are cleared or introduced by the move; govulncheck reports zero
  either side of it. This is a build-toolchain change, not a security fix.

  Two analysis tools had to move with it, both for the same underlying reason:
  they vendor a copy of `golang.org/x/tools`, and an `x/tools` older than the
  standard library it is asked to analyse dies on syntax it does not know.
  `golangci-lint` goes v2.1.6 → v2.13.1 — v2.1.6 answers a 1.27 target with 908
  phantom `typecheck` errors on valid code, and v2.12.2 panics outright.
  `govulncheck` goes v1.1.4 → v1.7.0, panicking the same way
  (`unexpected expr: *ast.KeyValueExpr`). Both pins are now documented as
  coupled to `GO_VERSION` and must be re-checked whenever it moves.

  The govulncheck failure is worth recording because of *how* it presents: it
  is not reproducible on demand. v1.1.4 only builds SSA for packages the
  vulnerability database hands it candidate symbols in, so the same tree
  scanned clean on one run and crashed on the next — measured here at one of
  each. A green local scan is therefore not evidence that the pin is
  compatible. What holds the line is the run step's existing rule that any
  exit code other than 0 or 3 is a hard failure, so a crashed scan reports as
  a broken gate instead of falling through as "no vulnerabilities found".

- **The builtin crew templates ship models that are not being retired.** All
  twelve template files pinned dated snapshots — `claude-sonnet-4-20250514` on
  43 agents, `claude-opus-4-20250514` on one, `claude-haiku-4-20250514` on
  three — and those ids retire on 2026-06-15. Every crew the onboarding wizard
  can deploy was seeded against a model with an end date, on the first screen a
  new install ever shows.

  The bump is per tier, not a blanket replace: the threat modeller keeps its
  Opus, the secrets sweeper and the documentation writers keep their Haiku, and
  everything else moves to Sonnet. A template's model choice is a cost decision
  someone made deliberately, and flattening 47 pins onto one model would have
  quietly repriced twelve crews.

  A test walks the YAML exactly as the seeder does and holds two lines for
  every agent in every template: no dated suffix, and the id must appear in
  `llm.CuratedModels("anthropic")`. Bare aliases are the convention here —
  a dated id pins a snapshot that will be withdrawn, and the alias will not.

- **The onboarding preview no longer names a model of its own.** Five
  hardcoded `"Claude Sonnet 4.6"` strings sat in the preview component, so the
  same screen could show the picker's model, the template's model and the
  preview's model and have all three disagree. It resolves one id through
  `getModelLabel` now — the same function the rest of the UI labels with.

- **First-run now asks for the password twice.** `/bootstrap` creates the one
  account that owns the workspace, before any session exists, and a typo was
  only discoverable at the next sign-in — by which point the way back in is a
  password reset a fresh install may not be able to send. `/signup` already
  confirmed; this brings the more consequential form in line.

- **Pairing the CLI is no longer a dead end.** Step 3's green line reads "CLI
  paired. You can finish below or jump to `crewship setup` in the terminal",
  and Launch stayed disabled until a token was pasted into the browser — so
  the terminal route it offers was unreachable. The client gate was stricter
  than the server: `validateOnboardingCredential` returns nil on an empty
  value, so launching without one is a supported path and the CLI lands the
  credential afterwards. Browser mode still requires it, because there is no
  terminal there to add it from later.

- **Step 3 asks one question, then its consequence.** "How will you work?" was
  asking two unrelated things on one screen — how the human drives Crewship,
  and which credential the agents use — and ran off the bottom of the viewport
  doing it, while the two steps before it fit in a third of it. The server says
  as much in its own comment: `pairing_mode` "drives how the human works, not
  the agents".

  With a CLI in the picture the second question has a second answer, so in CLI
  mode the credential block collapses to one line — "Add the token in the
  terminal", with "Or add it now" to expand it — and the toolchain picker moves
  inside, because choosing a toolchain only means something once a key is being
  pasted. Browser mode has no terminal to fall back to, so there it stays open
  and required, with no terminal instructions at all. The whole step now fits
  above the fold.

  An empty token is a valid answer once paired; a half-typed one is not, and is
  rejected rather than stored as a credential that loads but never works.

- **The Claude Code model picker offers only what has been verified with the
  adapter, and is pinned to the backend's curated list.** It was a third independent copy of "which models
  exist", alongside `internal/llm/models_curated.go` (which the backend already
  serves at `GET /api/v1/models`) and the CLI's own adapter defaults — three
  lists, three different contents. The Go list is the source of truth now, and
  a test parses it to enforce that the picker never offers an id it does not
  carry.

  What the picker offers is deliberately narrower than curated: Claude Code
  lists Sonnet 5 and nothing else, because that is what has actually been run
  end to end with the adapter. An earlier version of this list was populated
  from Anthropic's published catalogue, which offered five models of which one
  was tested — publishing a model is not the same as having verified it.
  Widening the list means verifying the adapter first.

  Superseded aliases still answer at the API and can be set through the CLI
  or the API — they are just not offered as a starting point. They keep their
  display names through a label-only table: `getModelLabel` resolves by
  scanning adapters, so without it an existing workspace on
  `claude-sonnet-4-6` would have silently relabelled to "Claude Sonnet 4.6
  (Cursor)" — the only other adapter that registers it.

  `crewship setup`'s adapter defaults had drifted to Sonnet 4.6 while the web
  picker moved on, so the two setup paths for the same adapter handed out
  different models. They match again — which matters more now that the wizard
  points paired users at the terminal path.

- **Sign-up and the first-run admin screen join the split shell, and the setup
  wizard stops moving underneath you.** `/signup` and `/bootstrap` were centred
  cards next to a split `/login` they link to directly; both now mount
  `AuthSplitShell` with the animated mark and their own panel copy.

  Onboarding deliberately does **not** get the brand panel. It already has a
  live preview of the workspace and crew you are building, which is worth more
  than a logo, so it gets the same visual language applied to what is already
  there: the cropped mark in the lockup, and the preview on a surface of its
  own — tinted toward the brand with the mark as a watermark — where it used to
  be `bg-muted/20`, within a hair of the form column, so the split read as one
  page with a hairline down it.

  The pane is lit rather than decorated: an inset highlight on the edge the
  two panes share, and the ground falling away beneath it. The first attempt
  reached for a large soft brand-blue radial glow, which is what every AI
  product has shipped since 2024 and read as unserious next to a form people
  have to fill in carefully. Depth is what makes a split read as two panes.
  Stacked below `lg` the highlight moves to the bottom edge, because that is
  where the seam actually is.

  **The unauthenticated forms are usable with a thumb.** The shared `Input` is
  `h-9` and `Button`'s default size is `h-9` — 36px, under every touch
  guideline there is — and sign-in, sign-up, first-run and setup are the
  screens most likely to be met on a phone. A `.touch-form` scope raises them
  to 44px, keyed on `pointer: coarse` rather than on viewport width: width
  missed the iPad, which is over any phone breakpoint you would pick and still
  a finger. `components/ui` is untouched, because the same controls sit in
  dense authenticated tables where 44px would wreck the row rhythm.

  Two more phone fixes: the crew empty state reserved a full card's height on
  a screen where the preview is below the form and off-screen while you type,
  and it told you to pick a crew "on the left" when stacked there is no left.
  The adapter step's token label and its help link stacked into a run-on at
  390px; they sit on separate lines there now.

  Two fixes found by walking the wizard on a nuked instance rather than by
  reading it. **The lockup drifted between steps** — the form column was
  centred as a whole, so the logo and stepper slid as the step content changed
  height, measured at y=101 on Workspace, y=137 on Crew and y=66 on Adapter.
  Both columns are top-anchored now; measured steady at y=71 across all three.
  And **the preview's empty state was a thin strip**, leaving the pane looking
  ~85% empty on step one — which reads as a failed render, not an empty state —
  and making the layout jump when the real crew card arrived. It now reserves
  that card's height and says what will land there instead of pointing at a
  control.

- **The sign-in screen is now a split, with the brand mark animated on the
  right.** `/login` was a centred card on a flat gradient. It is now a
  two-pane shell: the form in a readable column on the left, and on the right
  the Crewship mark blown up until the panel is its tile.

  The mark moves because it is **taken apart, not redrawn**. The logo is one
  `<path>`, but that path is three subpaths — three sails. `lib/brand-mark.ts`
  splits them so each carries its own motion, and the geometry on screen stays
  byte-identical to the logo we already ship. Two of the three subpaths begin
  with a *relative* `m`, chained to wherever the previous sail ended, so the
  split walks the path tracking the current point and rewrites those movetos
  as absolute; lifting them out verbatim would silently pile the sails at the
  origin. The split runs at module load rather than into a checked-in
  generated file, so a redrawn logo cannot leave a stale copy behind.

  Each sail runs three independent sines — bob, swell about its foot, heel
  about its foot — on periods that do not divide into each other, so the loop
  never visibly repeats and the sails never sync into a pulse. A specular
  sweep is clipped to the live union of the moving sails, built from the same
  matrices that fill them, so it cannot drift off the mark.

  `prefers-reduced-motion` settles the canvas to one composed frame and
  schedules nothing; a hidden tab cancels the loop; and where there is no 2D
  context the panel falls back to the shell's CSS gradient rather than to a
  blank rectangle. Below `lg` the panel becomes a short brand banner above the
  form — the headline is hidden there, because over a 12 rem banner it ran
  straight through the sails.

  `<CrewshipLogo tight />` crops the viewBox to the mark's own bounds. The
  default 1024 box is the *tile's* box — the silhouette fills about 62% of its
  width and 58% of its height, and the rest is padding the squircle needs.
  Shown without a tile that padding is most of the element, which is why the
  sign-in lockup's 28px mark read as a few grey pixels with no legible sails.
  The lockup now uses the bare cropped mark at 36px. The tight mark is not
  square (about 1.07:1), so size it on one axis and let the other follow.

  None of the auth logic moved: `safeRedirectPath`, the
  `/system/setup-status` first-run gate, the four banner states and the
  signup-allowed flag are untouched. `SAIL_PATH` moved from
  `components/branding/crewship-logo.tsx` to `lib/brand-mark.ts` and is
  re-exported, so importers are unaffected.

### Fixed

- **Deploying a crew template links credentials for the agent's own provider.**
  `autoAssignCredentials` filtered the workspace's credentials with a hardcoded
  `provider = 'ANTHROPIC'` while the agents it links them to carry whatever
  provider their template pinned. For an Anthropic crew the two agreed by
  accident and nothing looked wrong. For any other provider the query matched
  nothing and every agent in the crew deployed with **zero credentials** — no
  error, no failed request, just a crew that does not work; and a workspace
  holding an Anthropic key handed that key to a Google agent, which is worse,
  because it loads and then fails at call time.

  It reads the agent's own `llm_provider` now and falls back to `ANTHROPIC`
  only when the column is empty, matching the default the write side applies.
  A lookup that errors leaves the agent unlinked rather than guessing. The
  onboarding wizard reaches this path for every builtin template, so it was one
  adapter choice away on the most common flow in the product, and nothing
  covered it.

- **The wizard's model choice reaches the crew it deploys.** Picking a model on
  step 3 did nothing for four of the five crew options: `req.LlmModel` was read
  only by the branch that builds a blank or single-agent crew, so every builtin
  template deployed with the model its YAML pinned and the select was
  decoration. `deployCrewTemplate` takes the override now.

  It applies **only when the chosen provider matches the agent's** — writing a
  Gemini id onto a `CLAUDE_CODE` agent breaks it outright, which is worse than
  the template's default. An override with no resolvable provider is ignored
  for the same reason, and the zero value deploys the template verbatim, which
  is what every other caller passes.

- **The first-run window is documented as it behaves.** Three source comments
  and the changelog stated bootstrap closes five minutes after `crewship
  start`. It does not: it stays open until an admin account exists, and the
  finite window is opt-in through `CREWSHIP_BOOTSTRAP_WINDOW` for instances
  reachable from the internet before anyone claims them. One of the comments
  named a symbol that is not in the tree. Four docs pages also said the closed
  endpoint answers 403 where the server answers 410, as did an assertion in
  `e2e/onboarding-fresh.mjs` — the code was right in every case and only the
  prose and one stale check were wrong, which is the kind of drift that gets
  believed because it is checked in.

- **`e2e/onboarding-fresh.mjs` runs past its 24th check.** A module-scope
  `const URL` shadowed the global constructor, so `new URL(...)` threw partway
  through and the eleven checks after it had never executed — in a script that
  reports its own pass count, which made a truncated run look like a short one.
  Nothing in CI runs this script, so it went unnoticed; it completes now, 37
  checks against a fresh database. Two of the newly-reachable checks were
  themselves stale: the 410 above, and a `console.anthropic.com` link no
  onboarding component renders any more (the step deep-links into the adapter's
  own CLI-auth docs, deliberately not to the API-key page, because onboarding
  rejects raw API keys).

- **A failed `crewship apply` no longer reports success.** Apply is fail-fast,
  so on an error the counters describe a *prefix* of the plan — but they were
  printed under the word `Applied:` regardless, with the error on a later line.
  A production run that uploaded none of its ten crew files reported
  `Applied: 7 created, 3 updated` and was filed as done; the routine went on
  running against the stale script the deploy existed to replace.

  `Applied:` is now reserved for a run that finished. A failed run prints
  `FAILED after N created, …  — the manifest was NOT fully applied.` and then a
  `NOT APPLIED` block naming the item it died on and every item behind it that
  was never attempted. The counters say how far the run got; only that list
  answers the question the operator actually has, which is *which of them
  landed*.

- **`crewship apply` stops silently dropping routine DSL fields.** `spec:` was a
  closed struct and the wire body an 11-key allowlist, so `guardrails`,
  `integrations_required`, `concurrency_key`, `max_concurrent`, `outputs`,
  `display_name`, `agentless`, `hooks`, `eval`, `resources`, `execution_tier`
  and `parallelism` were dropped between the file and the server — no error, no
  warning, no plan diff. The allowlist was justified by a comment claiming the
  server rejects unknown keys; `pipeline.Parse` is a plain `json.Unmarshal` and
  does not, so it bought nothing.

  The export half was the dangerous one: `crewship export` decoded the *stored*
  definition through the same struct, so a field set via `routine save` or the
  dashboard vanished from the exported YAML and the next `apply` **deleted** it
  from the live routine. An `agentless: true` token-zero guarantee could be
  revoked by editing an unrelated line.

  `spec` now carries an inline catch-all, the pattern its own steps have always
  used, so any `routine.v1.json` key rides through in both directions. Typed
  fields still win every collision, and `schedules`/`webhook` still stay out of
  the definition. Because a typo is now forwarded rather than dropped, apply
  warns at plan time for every `spec` key the DSL has no field for.

- **The crew-file 409 names a command that exists.** Overwriting a file under
  `/crew/shared` on a stopped crew answered "start the crew and retry", which
  reads as `crewship crew provision` — a command that builds an image and, on a
  cache hit, reports `provisioned` while the container stays `stopped`.
  Following the message reproduced the 409. It now names `crewship crew start`
  (below) and rules out the wrong turn it used to invite. `crew provision` says
  `provisioned (container image ready)` and points at the same command, for the
  same reason.

### Added

- **`crewship crew start <crew>` — start a crew's container on purpose.** There
  was no way to. `crew provision` builds an image and stops; the container was
  only ever created lazily by the crew's first agent run, so the only route to a
  running crew was to run an agent at it with a throwaway prompt — spending
  tokens for a side effect. That gap is what made the crew-file 409 unanswerable.

  `POST /api/v1/crews/{crewId}/container-start` runs the same three steps the
  dispatch path runs before an agent — EnsureProvisioned, then the crew's full
  resolved config, then `crewstart.Start` — so a crew started this way is the
  crew a run would have started: its provisioned image, its mounts and limits,
  its declared sidecars. That sequence is copied rather than reinvented on
  purpose; `internal/crewstart` exists because thirteen call sites once each had
  their own idea of what starting a crew meant and disagreed invisibly.

  Idempotent (starting a running crew returns its container and exits `0`, so a
  deploy can start-then-write without branching), synchronous (the caller's next
  action depends on the answer), and it provisions a cold crew first rather than
  starting it onto the bare runtime image. Degradation the start survived — a
  provider with no sidecar support — comes back as `notices` and is printed, not
  logged.

  It reports the start to the orchestrator's idle-TTL reaper. Every other
  `EnsureCrewRuntime` caller gets that for free because an agent run follows and
  reports the activity; this is the first path whose whole purpose is a start
  with no run behind it, so an unreported container would have outlived its
  `container_ttl_hours` until crewshipd restarted and rediscovered it.

  It verifies rather than asserts. `EnsureCrewRuntime` is get-or-create and
  normally restarts a stopped container, but not always — after two
  consecutive stops it can return the id of a container that is still
  `exited`. Answering `"status": "running"` there would be the very defect
  this command exists to fix in `crew provision`, so the handler polls the
  runtime before reporting success and returns `502` if the container never
  comes up. (A known sequence — stop, stop, refused write, start — still fails
  to restart; it is now a loud failure instead of a silent one, and is pinned
  as `xfail` in `scripts/test-harness/test-crew-lifecycle.sh`.)

  The CLI waits up to 20 minutes (`--timeout` to change it) rather than the
  client's 30-second default. The default would not merely time out: Go's client
  cancels the request, which cancels the handler's context, which tears down the
  image build it was waiting on — leaving `context deadline exceeded` and no
  container on exactly the never-provisioned crew this command exists to rescue.

- **`crewship crew stop <crew>`.** The counterpart to `crew start`. Until now a
  crew container could be started deliberately but only stopped by accident —
  an idle TTL expiring, or a network-policy edit dropping it as a side effect —
  so an operator who started three crews to land a restore had no way to give
  the memory back.

  `POST /api/v1/crews/{crewId}/container-stop` proxies to crewshipd, which
  already stops the runtime AND the crew's declared sidecars in one operation.
  Reimplementing the sidecar half here would be a second, slightly different
  teardown, which is how one once reached into another tenant's Postgres
  (#1732). Named volumes survive, so data does. Stopping an already-stopped
  crew succeeds, and the crew is dropped from the idle-TTL reaper's
  bookkeeping so it no longer logs an expiry for something a human stopped.

  Because container memory and CPU limits are fixed at create time, stop-then-
  start is also how a resize takes effect on a running crew.

- **`crewship apply` warns at plan time when a crew it writes files into is
  stopped.** Files under a crew's shared tree are owned by the container user,
  so overwriting one needs the container up; against a stopped crew the save
  answers 409, and because apply is fail-fast that lands mid-run, after earlier
  resources are already committed. The plan knows both halves twenty seconds
  earlier, so it now says so — naming the crew, the file count, and
  `crewship crew start <crew>`.

  Advisory, not an error: the crew may be started between the plan and the
  apply, including by whoever is reading the line. A probe that cannot answer
  stays silent rather than warning on a guess, and a crew being created by the
  same apply is never warned about.

- **`crewship apply --no-delete`.** Refuses any run whose plan contains a
  delete, before a single request is issued, printing what it would have
  destroyed. Sync mode makes deletion the default for anything that fell out of
  the manifest, and some deletions are not a rollback away: removing an agent
  takes its memory and its Composio OAuth binding with it, and that binding is a
  browser consent no manifest can replay.

  It deliberately outranks `--yes` — `--yes` is the flag every automated
  invocation already carries, so a guard it could switch off would not be one —
  and it fails `--dry-run` too, so the rehearsal and the performance agree.
  This turns "this apply deletes nothing", previously a claim a human made by
  reading a plan carefully enough, into one CI can check.

  Enforced twice, against two different plans: once by the CLI against the plan
  it rendered, and again inside `manifest.Apply` against the plan it builds from
  its own fresh read immediately before executing it. Only the second one closes
  the window — a resource that disappeared server-side between the two reads
  adds a delete the rendered plan never showed, and `--yes` (which every CI
  invocation carries) would have waved it through. SDK callers get the same
  guarantee via `manifest.Options{NoDelete: true}` → `ErrDeletesRefused`.

### Changed

- **`OTEL_EXPORTER_OTLP_ENDPOINT` is treated as the base URL it is, and
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is honoured (#1870).** The standard
  says the generic variable is a base URL for every signal and the signal
  path is appended to it — including when it already carries a path. We
  appended only when there was no path, and ignored the signal-specific
  variable entirely.

  Two consequences. A backend documenting a project-scoped prefix, such as
  Langfuse's `/api/public/otel`, expects spans at
  `/api/public/otel/v1/traces`; we posted to the bare prefix, where a
  collector answers 200 and drops the payload — indistinguishable from
  working. And an operator who set `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, the
  correct way to pin an exact URL, was not listened to at all.

  **If you configured a bare prefix, traces move** — to where the backend
  wanted them. A path that already ends in `/v1/traces` is not doubled, so a
  fully-written-out endpoint is unaffected. To pin any exact URL, use
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`.

  The startup line now prints `traces_url` — the resolved destination, signal
  path included — instead of the configured endpoint. Logging what was
  configured is what let a misrouted exporter read as healthy for a release.

- **The OTLP endpoint keeps its own `/v1/traces` default.** opentelemetry-go
  1.45 changed `WithEndpointURL`: a URL with no path of its own used to fall
  through to the exporter's default signal path, and now resolves to `/`. Since
  `OTEL_EXPORTER_OTLP_ENDPOINT` is a base URL — that is the standard, and what
  our setup guide tells you to configure — every collector would have started
  receiving spans on its root route, where a 200 and a discarded payload look
  exactly like success. `telemetry.Init` appends the signal path itself now, so
  the upgrade is invisible to operators and the SDK's default stops mattering
  to us. An endpoint that already carries a path keeps it verbatim, as before.

  The setup guide also claimed the SDK appends `/v1/traces` to a project-scoped
  prefix such as `/api/public/otel`. It never did, in any version — if you
  configured a bare prefix, set the full URL your backend documents.
- **WAL checkpointing moved off the request path.** Every commit appends
  frames to the `-wal` sidecar, and SQLite folds them back inside whichever
  write transaction happens to cross the threshold — so the cost landed on a
  random agent. The daemon now disables the inline autocheckpoint
  (`database.WithManagedWAL`) and runs a dedicated checkpointer that does the
  work on a goroutine nobody is waiting on. At 100 concurrent agents, three
  runs per policy, p99 write latency went **26.1 ms → 8.9 ms** and the WAL
  ended at 0 instead of 13.6 MB.

  Two findings are recorded in `internal/database/checkpoint.go` because both
  contradict the obvious guess: a `PASSIVE`-only checkpointer is **worse than
  doing nothing** (it folds frames back but never resets the `-wal` file, which
  grew to 98 MB), and the win comes from *who pays* for the checkpoint, not
  from checkpointing more often. Only the long-lived daemon disables
  autocheckpoint; short-lived CLI commands keep SQLite's built-in behaviour,
  and the pairing is enforced by a test.

- **The port-exposure purge no longer stalls every writer.** The sweeper runs
  every 30 s and issued one unbounded `UPDATE`, so its cost scaled with the
  backlog: measured at 41 ms of held write lock for 5,000 rows and **486 ms
  for 50,000**, with a concurrent live write blocked for 449 ms. That is the
  same sweeper whose lock hold took logins down on 2026-05-25 and is why
  `busy_timeout` was raised to 30 s. It now drains in bounded batches,
  releasing the lock between each, and logs the remaining backlog if it ever
  stops at its iteration cap.

  Bounding is right here because the job is *frequent* and its backlog grows.
  It is the wrong move for a large infrequent job: chunking a daily
  20,000-row sweep measured **worse** (150 ms → 405 ms total, live-writer p95
  4.1 ms → 21.5 ms), because re-acquiring the write lock costs more than the
  shorter holds save. Both numbers are in the code comment so the pattern is
  not copied blindly.

- **The two audit tables that grew forever now have retention windows
  (#1887).** `credential_audit` and `audit_logs` were the only tables in the
  schema with no pruning at all — `pipeline_runs`, `inbox_items` and
  `journal_entries` are all swept, these two never were. `credential_audit`
  gains a row on every credential read by every agent; on a dev instance with
  almost no real use it was already the second-largest table in the database.

  The defaults differ deliberately. `credential_audit` keeps **90 days**,
  matching the pipeline-run window — it is operational telemetry, and the
  answer an operator actually reads (`last_used_at`, the `last_used_ips` ring)
  lives on `credentials` and is untouched. `audit_logs` keeps **everything**,
  because it is the compliance trail and `docs/security/gdpr.mdx` says audit
  records have to survive the operator's own retention obligations. Crewship is
  self-hosted: that duty is theirs to know, so the mechanism ships and the
  decision does not. Both are overridable per workspace, where `NULL` means
  "use the default" and `0` means an explicit "keep forever" — a distinction
  `run_retention_days` does not make, because nobody has a legal duty to retain
  a pipeline run.

  Upgrading never deletes anything on its own: the migration pins every
  workspace that already existed to an explicit "keep forever", so the 90-day
  default only ever applies to workspaces created after it. Otherwise the first
  restart after the upgrade would have swept a year of credential history away
  before the API that sets the override was even listening.

  The sweep deletes in bounded batches so no single statement can stall every
  writer, and says how much backlog is left if it stops at its cap. Since
  `audit_logs` is unlimited by default, it also warns when a workspace has no
  window and the table passes a million rows — an operator should hear about
  that from a log line, not from a full disk.

- **The credential audit view stops sorting the whole table to answer one
  page (#1889).** `credential_audit` had no `workspace_id`, so scoping it to a
  tenant meant joining through `credentials` — a shape no index could serve.
  SQLite either walked the table in time order probing `credentials` for every
  row, or gathered the entire matching set and built a temporary B-tree to sort
  it before `LIMIT` threw almost all of it away; the page's `COUNT(*)` then ran
  the same join again. That cost grows with the table, and this is the one
  audit table with no retention sweep at all.

  The column is now carried directly, with
  `(workspace_id, occurred_at DESC)` behind it, matching how `audit_logs` and
  `keeper_request_events` have always worked. The single writer derives the
  value from the credential inside the same `INSERT`, so it cannot disagree
  with the row it describes and no caller has to know the workspace to write an
  audit row. Existing rows are backfilled. No behaviour change — the same rows
  come back.

- **Sixteen foreign keys indexed, thirty-two deliberately not (#1890).** With
  foreign keys enforced, deleting a parent row scans every child table whose
  referencing column does not lead an index — once per deleted row, holding the
  write lock. 48 columns were in that state. The ones now indexed are those
  where the parent is genuinely hard-deleted *and* the child table grows;
  `DELETE FROM missions WHERE crew_id = ?` drove three of them on its own,
  because it deletes many missions in one statement and re-checks every child
  of `missions` for each.

  The 32 skipped are the interesting half. Nine reference `users`, and nothing
  in the tree hard-deletes a user row — so an index there is write cost with no
  delete to accelerate. The rest are settings and small config tables where a
  scan costs nothing. Both exclusions are pinned by tests, so they stay
  decisions rather than oversights, and a ratchet makes the remaining count
  visible if a new table adds foreign keys nobody sized.

- **A task whose assignment link failed is now FAILED, not stranded (#1892).**
  `scheduleTask` inserted the assignment and then linked it to the task in two
  separate statements. A failure between them logged a warning and reported
  success, leaving the task `IN_PROGRESS` with no assignment — a state
  `resolveReadyFromTasks` never re-picks, so the work sat there forever with no
  operator-visible symptom. Both writes are now one transaction, and a link
  failure surfaces as a failed task. The dependent-task cascades
  (`unblockDependentTasks`, `failDependentTasksRecurse`) also stopped issuing
  one standalone `UPDATE` per row: each level is now a single batched
  transaction, so a mid-cascade failure unblocks nobody instead of half the
  graph.

- **`presence.Track` no longer fabricates a transition.** The prior status was
  read and the new one written in two statements, so a losing writer could emit
  `online → busy` for a transition that had already happened, or report a
  `prev` that was never the state it overwrote. The journal is consumed as a
  transition log and is hash-chained, so a wrong `prev` is worse than a
  redundant row. Read and write now share one transaction; the journal emit
  stays outside it.

- **Eleven redundant indexes dropped
  (`20260810120000_drop_redundant_indexes`).** Each was a leading-prefix
  duplicate of a longer index on the same table, so the planner could never
  choose it while every write still maintained it — on `missions`, `chats`,
  `assignments`, `credentials`, `journal_entries` and others.
  `idx_journal_trace_id` and `idx_journal_trace` turned out to be the same
  index under two names. A schema invariant test now fails the build if the
  shape is reintroduced.

- **One attachments table, and `chat_attachments` is gone (#1768).** The schema
  carried two attachment tables that no product code had ever read or written —
  `chat_attachments` and `workspace_files` (both migration v57) — while the chat
  composer's live attachment path wrote blobs and **no metadata row at all**, so
  a chat attachment had no recorded size, checksum, MIME type or uploader and
  could not be listed or deleted through any API. The new `attachments` table
  replaces `chat_attachments` (dropped in the same migration) and holds all owner
  kinds through an exclusive-arc foreign key, so a row can never decay into an id
  that resolves to nothing. `ProxyHandler.AgentChatAttachment` now writes its row;
  the chat blob's location is deliberately unchanged, because
  `/output/<agentSlug>/attachments/…` is the agent-visible contract.

  `workspace_files` is **not** dropped here: it is a path→metadata index rather
  than an attachments table, and three v144 timestamp-regression guards use it as
  their canary. Removing it belongs with #1768 item 8, together with moving those
  guards.

### Fixed

- **Two crews with the same issue prefix no longer wedge each other (#1797).**
  An identifier is `<prefix>-<n>`, where the prefix is the crew's
  `issue_prefix` or the first three letters of its slug — so `engineering` and
  `engine` both derive `ENG` with nothing configured. The number came from a
  counter keyed **per crew**, while identifiers are unique **per workspace**, so
  both crews minted `ENG-1` and the second one's insert was rejected.

  It was not a one-off 500. The counter increment and the issue insert shared
  one transaction, so the rejection rolled the increment back too: the losing
  crew asked for the same identifier on every subsequent create and **could
  never file an issue again**, with no message naming the cause. Any workspace
  with two such crews had one of them silently out of service.

  The counter is now keyed on `(workspace_id, prefix)`, which is the namespace
  it feeds. Two crews sharing a prefix share one sequence and interleave
  (`ENG-1`, `ENG-2`) instead of colliding — no validation to remember at each
  write site, and no legitimate crew refused for its name. The migration
  collapses colliding counters onto the highest of them, which also unwedges a
  crew that was stuck, and seeds each prefix above the identifiers it has
  already minted so a crew that changed its prefix cannot restart into an
  existing range. The two duplicate identifier generators (the REST create and
  the agent/recurring path) are now one.

- **`crewship crew update --issue-prefix` (#1797).** `issue_prefix` has been
  accepted by `PATCH /api/v1/crews/{id}` since v38 and had no CLI flag at all;
  it was reachable only from the web UI. Pass an empty string to clear it and
  fall back to the slug.

### Security

- **cel-go can receive security updates again (#2067).** It had stopped, and
  nothing said so. cel-go renamed its module: `v0.31.0` declares
  `module github.com/google/cel-go`, but `v0.32.0` and everything after declare
  `module cel.dev/cel-go`, served from a different origin repository. v0.31.0 —
  where we were — is therefore the last release reachable under the import path
  we used, and Dependabot cannot cross a rename because doing so means editing
  source. It failed the whole `go_modules` job with `go_module_path_mismatch`
  instead, in a job log nobody reads, on every run since at least 2026-08-24.

  This matters more than a stale pin because CEL evaluates expressions that
  come from pipeline definitions, and because the *advisory* channel was
  affected too: a future advisory would be published against `cel.dev/cel-go`
  and would not have matched a requirement naming the old path. The gate meant
  to catch a stale dependency was looking at the wrong name for it.

  Imports move to `cel.dev/cel-go` and the module to v0.32.0. No behaviour
  change is intended — the two paths serve identical content for the same tags.

- **A crew no longer starts on a known-stale runtime image, whichever way the
  host got there (#2006, #2019).** ⚠️ **Behaviour change:** a start that used to
  succeed can now fail.

  Crewship pulls the runtime image **by digest** and then re-creates the
  `repo:tag` alias itself, because everything downstream — `ContainerCreate`
  included — addresses the image by tag. When that re-tag failed and an older
  copy of the tag was still on disk, the tag kept pointing at the **old**
  manifest: the container ran the old image, and the `provisioning.step` journal
  attested the newly pulled digest as verified. The journal half is fixed first
  — the digest recorded is now read back off disk, so a run is never attributed
  to a manifest it did not execute, and no digest at all is recorded when the
  daemon answers 404 for the tag. A read-back that *fails* is not read as a 404:
  a timeout says nothing about what the tag resolves to, so the decision falls
  back on what was proved before the pull.

  Execution is now fixed too. That state is bit-for-bit the state a **failed
  pull** over a stale local copy reaches, which has refused to start since
  #1825 — so one route stopped the fleet while its sibling shrugged and started
  the wrong image with an error log. Both routes now go through one decision:
  refuse by default, with the existing host-wide opt-out
  `CREWSHIP_ALLOW_STALE_RUNTIME_IMAGE=1`, and the error names both digests, what
  happened, and how to fix it properly. This route is the more recoverable one —
  the manifest you wanted is already on disk and merely unnamed — so the error
  hands over the exact `docker tag` that names it.

  **Who is affected:** a host whose registry answers the digest check while its
  local tag resolves to something else. An air-gapped or offline host is not
  affected (no digest answer, so nothing is provably stale) and neither is a tag
  that is simply absent. **If a start now fails**, re-pull or run the `docker
  tag` the error prints; if you would rather run the older image than stop the
  fleet, set `CREWSHIP_ALLOW_STALE_RUNTIME_IMAGE=1`. The opt-out relaxes
  execution only — it still journals the **local** digest with
  `payload.pinned: false`, because a tamper-evident log that attests a digest
  which never ran is worse than one that records nothing.

- **Go toolchain bumped 1.26.5 → 1.26.6, clearing eight advisories (#1959).**
  Seven were in the standard library and reachable from real call paths —
  `crypto/tls` post-handshake message flooding (GO-2026-6090), `net/http`
  missing `ReadHeaderTimeout` on the unencrypted HTTP/2 check (GO-2026-6089)
  and its IDNA Punycode label handling (GO-2026-5026), quadratic
  `net/url.resolvePath` (GO-2026-6218), unbounded recursion in `encoding/xml`
  (GO-2026-6088) and `encoding/asn1` (GO-2026-5972), and JavaScript regexp
  context tracking in `html/template` (GO-2026-6091). The eighth,
  GO-2026-6222, was excessive memory allocation decoding VP8L in
  `golang.org/x/image`, reachable from avatar upload, and is fixed by that
  module's 0.44.0 → 0.45.0 bump.

  Dependabot proposed the toolchain move in the `Dockerfile` alone; the
  version is pinned in **thirteen** places (`go.mod`, the `Dockerfile` and
  eleven workflow pins), and only moving them together changes what the
  released binary and CI are actually built with. `govulncheck ./...` now
  reports zero.

  The govulncheck allowlist is emptied in the same change. Its five entries
  were all `docker/docker` advisories, and that module left the graph when the
  container provider moved to `github.com/moby/moby` — a dead entry would have
  silently re-accepted those IDs had it ever returned transitively. The
  allowlist arm of the gate now has unit coverage in
  `scripts/security-yml-test.sh`, extracted verbatim from the workflow the way
  the scheduled-report gate already was.

- **Capability tokens are hashed at rest (#1888).** `port_exposures.token` and
  `pipeline_webhooks.token` were stored in the clear and looked up by equality.
  Neither `/exposed/{token}/…` nor `POST /api/v1/webhooks/{token}` has any
  authentication in front of it — by design, the token *is* the authorization
  — so anyone who could read the database file walked away with every live
  exposure URL and every configured webhook on the instance. Both columns now
  carry a SHA-256 digest and are resolved by digest, the way `cli_tokens` has
  worked since Patch J.

  **Existing tokens keep working.** The migration hashes the cleartext already
  in the row rather than rotating it: invalidating would have broken every
  published exposure URL and every already-configured sender, on every
  instance, and nothing suggests the cleartext leaked. What changes is that the
  cleartext is then overwritten — neither column can be dropped (both are
  `NOT NULL UNIQUE`, which SQLite's `DROP COLUMN` refuses, and one is still
  named by the create path) so each holds `redacted:<row id>` afterwards.

  **The digest is unkeyed SHA-256** (hex, behind an `sh1:` scheme prefix), not
  an HMAC. A key protects a digest whose input space is small enough to
  enumerate offline; both of these tokens are 32 bytes of `crypto/rand`, so the
  preimage search is 2^256 wide either way and a key buys nothing. It would
  have cost a great deal: an earlier revision of this work keyed the digest off
  `ENCRYPTION_KEY`, and the documented master-key rotation
  (`CREWSHIP_ENCRYPTION_KEY_VERSION` + `ENCRYPTION_KEY_V2` +
  `POST /admin/reencrypt`) **retires that variable as its final step** — after
  which every presented token would hash under a different scheme than every
  stored digest, with the cleartext already overwritten and nothing to recover.
  The keyed `hk1:` scheme never shipped and is not supported; a **dev instance**
  that ran the earlier revision must re-create those webhooks and re-request
  those exposures.

  A row the backfill cannot hash is no longer stranded either. The backfill
  continues past a failed `UPDATE` (SQLite has one writer; a moment of
  contention used to abort the loop and 404 every remaining webhook until the
  next restart) and, while any row still holds cleartext, the webhook lookup
  falls back to it — bounded to rows whose digest is missing, and re-hashing
  each one it resolves. On the exposure side, an `ACTIVE` row whose hashing
  failed is hashed at boot and keeps serving; one that nothing can resolve is
  flipped to `EXPIRED` instead of being reported live with a future expiry.
  Revoking an exposure now drops it from the registry by row id when the row
  carries no digest — the previous fallback read a column that always holds
  `redacted:<id>`, so a revoke could answer `200` while the capability URL kept
  reverse-proxying into the crew container until the process restarted.

  One behaviour change follows from it: a webhook's token is now **shown once**,
  in the create response. `GET`/`PATCH` and the list endpoint no longer return
  it, because it is no longer recoverable. A webhook whose token was lost has to
  be re-created, and `crewship routine webhooks url <id>` now says so and exits
  non-zero instead of printing `…/api/v1/webhooks/` with nothing after the
  slash.

  For port exposures the cleartext never reaches disk at all: the create path
  writes the spent marker and the digest directly, rather than inserting the
  live token and redacting it a moment later. That window was short — one
  request — but a crash, a WAL checkpoint or a backup taken inside it captures
  a working capability URL, which is the whole exposure being closed. If the
  process dies between the insert and the registry write, the row is left
  resolving for nobody, which is the right direction to fail.

  `pipeline_waitpoints.token` is **not** covered. It is the same kind of
  secret, but it is also that table's primary key, the handle
  `inbox_items.source_id` and a WAITING run's `waitpoint_token` carry, and the
  value `GET …/pipelines/waitpoints` reads back out of the column to rebuild
  the public callback URL. It is a retrievable shared secret by contract rather
  than a show-once credential, so hashing it means redesigning that contract
  first; tracked separately.

- **`workspace_members.role` is constrained to the roles that exist (#1893).**
  `crew_members.role` has carried a `CHECK` since v99; the column deciding what
  a member can do across a whole *workspace* carried nothing and would accept
  any string. The API validates today, so this is defense in depth — but the
  read tier is where it would have mattered: `canRole`'s write tiers switch
  over the known roles and deny anything else, while its `read` tier accepts
  any non-empty string. Write fails closed, read fails open, and only the
  schema can refuse the value independently of whichever write path appears
  next.

  Enforced with `BEFORE INSERT` / `BEFORE UPDATE` triggers rather than a
  `CHECK`, because SQLite cannot add one without a full table rebuild — not a
  trade worth making on the table that decides workspace ownership. The
  difference is deliberate: a stored legacy row keeps working and is refused
  only when something tries to write it back, where a rebuilt `CHECK` would
  have refused to apply at all and failed the boot.

- **The escalation wait endpoint now scopes to the caller's token binding.**
  `GET /api/v1/internal/escalations/{id}/wait` loaded the escalation by id
  alone, with no workspace or crew predicate — and when the escalation's type
  is `CREDENTIAL`, that handler **decrypts the resolution and returns the
  secret in the clear**. A crew-bound (`crwv1`) sidecar token could therefore
  wait on another crew's escalation, in another workspace, and be handed its
  plaintext the moment a human approved it. Both halves leaked: an
  already-`RESOLVED` foreign escalation returned immediately, and a foreign
  `PENDING` one long-polled and was delivered on resolve.

  The lookup now carries the predicate its sibling `CreateEscalation` has
  used since PR-F24: crew-bound callers match their own `crew_id` exactly (a
  sibling crew in the same workspace is as foreign as another tenant, which
  is what `crwv1` tokens exist to enforce), workspace-bound callers match
  their workspace, and the unbound master token stays unrestricted. A refusal
  is the same `404` as an unknown id rather than a `403`, so the endpoint is
  not an existence oracle.

- **The Docker API surface the server can reach is now specified, published
  and gated (#1826).** The server talks to Docker directly, which is
  root-equivalent on the host. `DOCKER_HOST` already let an operator put a
  filtering proxy in front of that, and `docker/docker-compose.prod.yml`
  already shipped one — but nobody had ever derived the endpoint list behind
  it, and it was wrong: **`COMMIT` was missing**, so crew provisioning behind
  the proxy we ship would have failed with a `403` on `POST /commit`, and the
  BuildKit path (`BUILD` + `SESSION`) was unaccounted for entirely.

  The real surface is **31 Engine API endpoints** — not the 25 previously
  assumed. `ContainerCommit`, `ContainerPause`, `ContainerUnpause`,
  `CopyFromContainer`, `ImageList`, `ImageRemove`, `Info`, `Ping` and
  `ServerVersion` were all reached and unlisted; `ContainerLogs` and
  `NetworkRemove` were listed and never called. `scripts/docker-api-surface`
  re-derives the list from the call sites on every CI run and fails on drift in
  **both** directions — a new call nobody declared (the proxy would deny it in
  production) and a declared endpoint nobody calls (we would be asking
  operators to grant a permission we do not need). It also catches a package
  newly importing the Docker SDK, and a subprocess executed with `DOCKER_HOST`
  pinned, which is a second client of the same socket that no compile-time
  check would see.

  New guide: [Docker Socket Proxy](https://docs.crewship.ai/guides/docker-socket-proxy).
  It is deliberately unflattering about the ceiling. The proxy filters URL
  paths and never request bodies, so it **cannot** block `Privileged: true` in
  a `ContainerCreate` — the workspace `allow_privileged_credentials` gate stays
  the real fence. Its granularity is a path prefix, not an endpoint:
  `CONTAINERS: 1` also opens `POST /containers/prune` and
  `POST /containers/{id}/update` for every container on the host. And the verbs
  we genuinely need (`ContainerCreate`, `ExecCreate`/`Start`/`Attach`,
  `CopyToContainer`) are most of what an attacker would want. What it does
  remove is real — image build, `commit`, swarm, plugin install, `/system`
  prune, registry auth — and that is the claim the page makes, no larger.

- **Internal tokens are now bound to a crew, closing the
  credential-metadata enumeration leak (#1159).** The `?crew_id` scope on
  `GET /api/v1/internal/credentials` (#1031) was opt-in and fail-open: a
  workspace-bound `X-Internal-Token` holder could omit `crew_id` (get the
  whole workspace's credential metadata) or forge a sibling crew's id and
  enumerate it. A per-crew sidecar now receives a **crew-bound** token —
  `crwv1.<workspace_id>.<crew_id>.<hex(HMAC-SHA256(master, ctx ||
  workspace_id || crew_id))>`, `internaltoken.DeriveCrewToken` — carrying
  the workspace binding (PR-F24) plus a crew binding. `requireInternal`
  rejects a `?crew_id` that disagrees with the token's crew (403) on every
  internal route and exposes the bound crew via
  `InternalTokenCrewFromContext`; the credential listing scopes to that
  cryptographic crew in preference to the forgeable query parameter. The
  loopback exemption for the crew-less in-process `TokenSyncer` is
  connection-based (`r.RemoteAddr`), never header-based. Crew-less runs
  fall back to the workspace-bound `wsv1` token unchanged. A follow-up
  audit pins the closure end-to-end — real derived token → `requireInternal`
  → handler, from a Docker-bridge origin — and adds a sentinel for the one
  surviving residual: a crew-less (workspace-bound) caller still gets its
  own workspace's credential metadata, because that is the token's true
  scope and because an empty listing would make the sidecar's credential
  reaper evict every provider key the container booted with.
- **Privileged crews now emit a provision-time WARN (#1032).** A crew
  provisioned with `--privileged` (e.g. DinD) collapses the UID 1001/1002
  boundary that isolates the sidecar's IPC token and injected credentials
  from the agent process. The full remediation is out of scope; the log
  line surfaces the trust downgrade in ops the moment such a crew is
  provisioned.

- **`POST /workspaces/{id}/skills/generate` spent the wrong tenant's Anthropic
  key.** The handler took its workspace from `r.PathValue("workspaceId")`, but
  `RequireWorkspace` resolves the tenant with `?workspace_id=` taking priority
  over the path — so the two could disagree. An OWNER of workspace A could send
  `POST /api/v1/workspaces/<B>/skills/generate?workspace_id=<A>`: the middleware
  validated A's membership and let the request through, then the handler
  evaluated A's role against workspace B and looked up, decrypted, and spent B's
  Anthropic API key. It now reads `WorkspaceIDFromContext`, the only value
  membership was ever checked against. A source guard
  (`TestNoHandlerReadsWorkspaceFromPath`, AST-based, allowlist with reasons)
  fails the build on any new handler that re-reads the path, so the class is
  closed rather than the one instance.

- **Six write paths persisted foreign-workspace references.** `project_id` and
  `milestone_id` on issue create/update, `project_id` on recurring-issue
  create/update, `crew_id` and `project_id` on triage-rule create/update, and
  `lead_id` on project create/update all went into their INSERT verbatim. This
  is the #1471 class on the columns that fix did not reach: the caller never
  requests the foreign row, they name it, and the read path resolves it — so an
  issue listing rendered another tenant's project name, and a triage rule could
  route this tenant's incoming issues into another tenant's crew on every match.
  `assertFKInWorkspace` now carries a per-table membership query (it previously
  supported only `agents` and `crews`, and projects/milestones were documented
  as "deliberately not listed", which is why those handlers validated nothing),
  and `fkInWorkspaceOrReject` makes a call site one line. Found by the new
  cross-workspace fence matrix rather than by review.

- **An agent could escape its crew's `autonomy_level` by creating a new crew
  (#1768).** Six `/api/v1/internal/*` routes let an agent create something that
  keeps acting after the request ends — a crew, a persistent agent, a mission,
  a cron schedule, a skill — and none of them consulted the calling crew's
  policy. Each handler justified skipping the check with a claim about its
  caller ("enforced upstream") rather than a check; nothing ran it. The sharpest
  consequence: `POST /internal/crews` never set `autonomy_level`, so the new
  crew took `DEFAULT 'guided'` (migration v101) — an agent held to `strict`
  could create a `guided` crew, create an agent inside it, and act there
  unbounded. **Two things close that, and neither is the blocking:** `strict`
  now refuses `crew_create` outright, and an allowed crew **inherits the
  creating crew's autonomy level** instead of the column default, so no created
  crew is ever more permissive than its creator. That property — not any
  particular cell of the decision matrix — is what closes the escape, and
  `TestAutonomyInvariant_ChildCrewNeverOutranksCreator` pins it across all four
  levels of the creating crew so a future re-tuning of the matrix cannot
  silently reopen it. Every arm (refused, held, allowed) writes an audit row
  carrying `decision`, `autonomy_level` and `policy_action`; there is no silent
  allow. When the policy resolver is unwired the gate holds rather than
  proceeding — a wiring bug fails closed.

### Changed

- **A `container` CLI call that finished microseconds before its deadline no
  longer reports a timeout (#2030).** The `internal/provider/apple` half of the
  process-group fix below is a user-visible behaviour change, not only an
  internal cleanup. `killProcessGroup` is `runCLIWithin`'s `cmd.Cancel`, and it
  used to return a bare `ESRCH` when the process group emptied between the
  lookup and the signal — the case where the command finished in the instant its
  deadline fired. `os/exec` wraps any error from `Cancel` other than
  `os.ErrProcessDone` as `exec: canceling Cmd: …` and, because the command
  itself exited 0, hands that to `Wait`; `runCLIWithin` then saw
  `ctx.Err() == DeadlineExceeded` and reported
  `container …: timed out after 20m0s`, throwing away output the command had
  already produced. That `ESRCH` is now mapped to `os.ErrProcessDone`, which
  `os/exec` treats as "the process already finished, don't inject a needless
  error", so the call returns its real stdout and a nil error. Callers or
  operators matching on the old timeout string for these races will stop seeing
  it — those calls were successes misreported as timeouts.
- **Agent-driven creation is now gated on the crew's `autonomy_level`, and on
  the default level (`guided`) some of it blocks (#1768).** This is a
  behaviour change on default settings, not only a bug fix — a crew that has
  never had its policy touched will see agents stopped where they previously
  were not.

  On `guided`, **`POST /internal/crews` and `POST /internal/agents` are
  blocking**. The row is still written, but inert: a new crew is pinned to
  `autonomy_level=strict` (so nothing can be created inside it) and a new agent
  is written `status=PENDING_REVIEW` (so the chat bridge will not start it).
  The call answers `202 Accepted` with `pending_review: true` and an
  `approval_id`. The operator gets a **blocking, ADMIN-addressed inbox
  waitpoint** and a row on the approvals queue.

  Release a held item with `crewship approvals approve <id>` (or deny it, or
  the `/approvals` page — `POST /api/v1/approvals/{id}/decide`, OWNER/ADMIN).
  Approving flips the sentinel: the agent becomes `IDLE`, the crew is restored
  to **the creating crew's** level — never higher. **Denying does not delete
  anything**, it just never releases, and so does letting the hold lapse: the
  seven-day timeout leaves the artefact inert rather than turning into a green
  light.

  On `guided`, **missions and cron schedules are not blocking**. They proceed
  and leave a non-blocking inbox notice: a mission creates no principal and is
  pinned to the caller's own crew, and a schedule stays an operator-editable
  row (`PATCH .../pipeline-schedules/{scheduleId}`), so in both cases a hold
  bought the same visibility a notice does while stopping ordinary work. At
  `strict` a mission is held (it can be planned, not started) and a schedule is
  refused outright.

  To tighten or loosen this per crew:

  ```bash
  crewship policy set --crew <slug> --level strict  --reason "…"  # refuse crew/agent/schedule creation
  crewship policy set --crew <slug> --level trusted --reason "…"  # unchanged for crew/agent; missions + schedules go journal-only
  crewship policy set --crew <slug> --level full    --reason "…"  # nothing blocks; crew/agent still leave a notice
  ```

  `strict` refuses `crew_create`, `agent_create` and `routine_schedule_create`
  with a structured `403` naming the level, so the CLI can suggest the
  `policy set` that would unblock it. `full` blocks nothing but still leaves a
  non-blocking notice for a new crew or agent — full autonomy is still
  autonomous, but a new permanent principal is the change an operator wants to
  see having happened.

- **Agent-created missions are now bounded by two live instance settings
  (#1768).** Letting `guided` — the default level — start a mission without a
  hold rested on missions being bounded by the #1757 delegation caps. They are
  not: the mission engine dispatches its task list through a path that never
  passes the delegation insert, and both files say so. `POST /mission/create`
  now answers `403` past either of:

  | Setting | Default | Bounds |
  |---|---|---|
  | `mission.max_tasks` | `16` | Tasks one agent-created mission may carry. `0` allows task-less missions only. |
  | `mission.max_active_per_crew` | `6` | Agent-created missions one crew may have in a non-terminal state. `0` switches agent-driven mission creation off. |

  The refusal body names the `setting` an operator would change, and no mission
  or task row is written. Both are read live from `app_settings`, so
  `crewship instance settings set mission.max_tasks 24` applies to the next
  call, not the next restart, and neither number can be raised from the request
  — there is no such field on the route.

  The second number is the one that matters, because **mission creation
  recurses**: a mission created with no tasks makes the engine run its lead as
  a planning turn, that turn is the one dispatch shape that keeps the sidecar,
  and the planning brief we send it offers "create a new sub-mission" as an
  option. A per-mission task cap alone would have bounded nothing. The crew's
  live-mission budget does, because every mission an agent creates lands in the
  crew its token is bound to.

  Scope, stated rather than implied: this covers the **agent** door only.
  Missions created through the dashboard/JWT API are neither capped nor counted
  against the agents' budget — an operator planning work is making a decision —
  and issues (which share the `missions` table) never count.

### Fixed

- **Leaving a workspace kept every crew membership (#1976).** `RemoveMember`
  deleted the `workspace_members` row and nothing else, so each `crew_members`
  row the departing user held in that workspace outlived their departure —
  and those rows grant on their own. Crew membership alone opens crew-owned
  pages (`pages_authz.go`) and crew credentials (`credentials_loaders.go`),
  and `CrewRoleFromDB` folds `crew_members.role` into
  `effectiveRole(workspace, crew)`. So a user removed while holding a per-crew
  ADMIN override and later re-added as a plain MEMBER came back **as crew
  ADMIN**: `AddMember` inserts a workspace_members row and never looks at
  `crew_members`, so nothing on the way back in could notice the elevation
  nobody granted.

  The removal now deletes both, in one transaction — both or neither, because
  workspace-removed-but-crew-attached is exactly the state being fixed. The
  purge is scoped through `crews` (`crew_members` has no `workspace_id` of its
  own), so the same person's crews in other workspaces are untouched, and it
  runs *after* the page-owner transfer, whose "else the crew the departing
  user belonged to" fallback reads the very rows being purged.

  **Behaviour change:** re-adding someone to a workspace no longer restores
  their crews. A returning member must be added back to each crew by hand —
  including any per-crew role override they used to hold.

- **Cancelling a devcontainer build on macOS could leak the process tree
  forever (#2030).** `AppleContainerBuilder.Build` left `cmd.Cancel` at
  `os/exec`'s default, `Process.Kill()` — the direct `container` process and
  nothing under it — while its own watchdog killed the whole process group.
  Both woke on the same cancellation, and when exec's kill landed first the
  child could already be an exited, unreaped zombie by the time the group kill
  asked which group it was in. Darwin cannot answer that: XNU's `getpgid(2)`
  goes through `proc_find`, which excludes exited processes, so it returns
  `ESRCH` where Linux still reports the group. The kill then fell back to
  re-killing the corpse, the CLI's helpers kept the write end of stdout, the
  log scanner blocked on a pipe nobody would ever close, and `cmd.Wait()` was
  never reached. A cancelled provision leaked the goroutine, the pipe and the
  process tree indefinitely.

  The group id is now derived from what the command was started with — `Setpgid`
  makes the child its own group leader, so pgid == pid for the pid's whole life,
  including as a zombie — instead of being looked up at kill time, and `Cancel`
  is that same group kill, so exec no longer sends a second, racing signal. The
  watchdog also keeps signalling after a cancellation instead of stopping after
  one attempt; that closes a narrow gap, a group member that forks between the
  kernel walking the group and the signal landing, rather than being a general
  rescue — SIGKILL is uncatchable and the pgid no longer moves, so the first
  kill already reaches everything in the group. A holder that has left the group
  entirely (via `setsid`) is still unreachable and still wedges the build, as it
  does today; note also that once a build is cancelled the idle-timeout branch
  no longer runs. `internal/provider/apple`'s copy of the helper had the same
  lookup-at-kill-time shape on its timeout path and got the same fix. The
  regression tests reap the direct child before killing, which makes Linux's
  `getpgid` answer `ESRCH` too — so a macOS-only hang is now provable on the
  Linux runners that gate every PR.
- **"Waiting on you" counted things nobody was waiting on (#1876).**
  `scopeOf` — the classifier the Activity rail, feed and status segments all
  read — put every human-source journal row in the `waiting` bucket. But the
  journal is an EVENT LOG: an `approval.requested` row stays in it after the
  approval was granted, and `peer.escalation` is emitted for both the ask
  (`escalation_handler.go:255`) and its resolution (`:607`,
  `escalation_autoresolve.go:172`), separated only by `payload.state`. So on
  any instance that resolves what it asks — which is every working instance —
  the segment could read `Waiting 4` beside an Overview card reading `0`. Two
  answers to one question on one screen, and the wrong one was the reassuring
  direction to be wrong in.

  `scopeOf` now takes the same view the card takes: a human-source row is
  `waiting` only while its ask is still OPEN. A resolved escalation says so on
  its own face; an approval or keeper request is closed by a *different* entry
  type, so the join runs over the window (`answeredAsks` → `scopeCounts`,
  `entriesInScope`, `scopeByEntry`). The card's `openAsks` is now that same
  bucket rather than a second opinion about it, and a test pins
  `openAsks(feed).length === scopeCounts(feed).waiting`. An ask whose answer is
  outside the window, or that carries no id to join on, stays in `waiting` —
  over-reporting one row beats hiding something a person is blocking on.
- **The crew bottom panel's Docker tab could never show a container (#1697).**
  It fetched `GET /api/v1/system/runtime` — the HOST runtime inventory — and
  read `data.containers` off it, a field that endpoint has never sent. The
  guard was `Array.isArray(data?.containers) ? data.containers : []`, so the
  absent field became an empty list and the tab rendered "No containers
  running." on every crew, forever, with every container up: the line that
  would have surfaced the mismatch was the same line that swallowed it.

  Nothing served the data, so there is now something that does.
  **`GET /api/v1/crews/{crewId}/containers`** returns the crew's live
  containers — its agent runtime *and* its sidecars — with state, CPU, memory
  and the runtime row's agent count, read straight from the container runtime;
  `crewship crew containers <crew>` is its CLI counterpart. `/system/runtime`
  keeps answering the host question it was built for.

  The client no longer has a fallback to swallow anything: a response without a
  `containers` array is reported as a broken contract, not as an empty crew, so
  the next rename fails loudly instead of quietly. Absent numbers stay absent
  end to end — a stopped container and a runtime without stats support both
  report `null`, rendered "—", never a `0%` that would draw an idle container
  where nothing was measured.

- **The status chips on `/issues` could only ever select one status.** The
  chip row was handed the list `useFilteredIssues` had *already* narrowed by
  status, so picking "Backlog" dropped every other chip's count to 0 — and a
  chip whose count is 0 is not rendered at all, which put the second status
  permanently out of reach. The "All" pill had the mirror-image bug: it
  advertised the filtered count as the total. The hook now returns
  `{ visible, statusFacet }` — `visible` is what the board and list render,
  `statusFacet` is the same set with the status filter left out, and it is
  what the chips count. The code comment above the call site had described
  the correct behaviour ("counts derive from the pre-status-filter set")
  since it was written; only the code disagreed.

- **Bulk editing from the issues list view did nothing.** `IssuesListInline`
  — the wrapper `/issues` actually renders in list mode — never forwarded
  `workspaceId`, so `IssuesListView.handleBulkUpdate` returned at
  `if (!workspaceId) return`: select rows, pick a status, no request, no
  error, no feedback. That also made the refusal reporting added in #1563
  unreachable from the real UI. The wrapper now requires `workspaceId` (and
  forwards `onBulkAction`), and the regression is pinned at the wrapper
  level rather than only on the inner view, which the old tests had been
  handing the prop themselves.

- **Renaming a label was a 500 for everyone.** `PATCH /api/v1/labels/{labelId}`
  built its statement with `newUpdate()`, which always emits `updated_at = ?`
  first; the `labels` table has only `created_at`, so SQLite answered "no such
  column: updated_at" on every call since the endpoint shipped. The statement is
  now built from the columns the table actually has. Worth noting how it
  surfaced: a route that 500s cannot be tested for tenancy — the UPDATE never
  executed, so nothing could be said about its `WHERE` clause — and fixing the
  500 is what made the fence assertion on that route real.

- **A local-model endpoint now works whatever shape it was stored in.** One
  `ENDPOINT_URL` credential was consumed by three code paths that each expected a
  *different* shape of the same string: a bare root for `llm.Ollama` (which
  appended `/api/chat`), `.../v1` for the OpenCode provider block, and the full
  `.../v1/chat/completions` for `llm.OpenAI` (which used the value verbatim as the
  POST target). Our own documentation tells operators to store the `.../v1` form —
  point the Keeper governance model at that credential and the judge POSTed to
  `.../v1/api/chat`, got a 404, and, Keeper being fail-closed, **denied every
  credential request**. The credential's own Test button stayed green throughout,
  because the reachability probe strips `/v1` before falling back to `/api/tags`.
  A new `internal/llm/endpoint` package normalizes any pasted shape — bare
  `host:port`, trailing slash, `/v1`, `/v1/chat/completions`, `/api/chat` — to a
  mount root, and each provider appends the path for the wire it speaks, so the
  mismatch is now unreachable by construction. A reverse-proxy mount prefix
  (`https://gw/ollama`) and an Azure-style unversioned deployment are both
  preserved, as is an `?api-version=` query.

- **A reasoning model no longer silently denies everything as the Keeper judge.**
  Ollama returns a reasoning model's chain of thought in `message.thinking`,
  separately from `message.content`. A model that spends its token budget
  thinking (verified against Ollama 0.32.5 with `qwen3:4b`) answers with empty
  content, `done_reason: "length"`, and HTTP 200 — which the provider reported as
  a successful, empty, `end_turn` completion, so the fail-closed judge parsed
  nothing and denied, with no error to explain it. `Response` now carries
  `Thinking`, and a budget-truncated answer reports `max_tokens` instead of
  `end_turn`, so callers can tell "the model said nothing" from "the model
  reasoned and never reached an answer".

## [1.0.0-rc.1] — 2026-07-12

### Security

- **X-Internal-Token is now bound to a workspace (PR-F24) — closes the
  documented symmetric cross-tenant bypass.** Sidecars no longer
  receive the process-wide master internal token. At sidecar start the
  orchestrator derives a workspace-bound token
  (`wsv1.<workspace_id>.<HMAC-SHA256(master, workspace_id)>`,
  `internal/auth/internaltoken`) and injects it via the stdin
  `IPCConfig`. The `internalAuth` middleware validates the binding on
  every `/api/v1/internal/*` request: tampered tokens fail the
  constant-time MAC check, and the binding is enforced as a **mandatory
  request scope** rather than an optional `?workspace_id` check. For a
  bound token `requireInternal` rejects a `?workspace_id` that disagrees
  with the binding (403) and **injects** the bound workspace when the
  caller omits the query — so every handler that filters by
  `?workspace_id` (webhook secret, list credentials, agent/chat resolve,
  crew/agent create) is tenant-scoped automatically, with no
  legacy-unscoped fall-through. Path-param mutations that don't read the
  query (chat message-count / title, run finalize, credential status)
  constrain their `WHERE` clause by the bound workspace (foreign rows →
  404, never mutated). Handlers scoped by a body-carried `workspace_id`
  (`cost/record`, `journal/emit`, `pipelines/save`, plus the issue /
  mission / assignment / query / escalation create handlers and the
  confidence report) enforce the same binding in-handler via
  `assertInternalTokenWorkspace`. The unbound master token — which
  authorizes every workspace and so retains cross-tenant power if leaked
  from a container — is now **pinned to a loopback origin**: a master
  token presented from a Docker-bridge / LAN IP is refused (403), capping
  the blast radius of a leaked master to the host trust boundary
  (`CREWSHIP_INTERNAL_ALLOW_ANY=true` relaxes the pin for reverse-proxy
  setups). Pre-fix, an agent that captured the token inside its container
  could aim internal routes at ANY workspace by picking the
  `?workspace_id` it wanted (or simply omitting it on the routes that
  ran unscoped) — the "symmetric case" left open by the earlier Keeper
  Phase 2 asymmetric fix below. Derivation is stateless (no persistence;
  derived tokens roll with the master each boot) and the master token
  remains valid for host-side trusted callers (chat bridge, webhook
  secret resolver, LLM proxy) that reach the API over loopback and never
  enter a container. See the updated "Tenant isolation" section in
  `docs/security/threat-model.mdx` and the retired "known exception"
  block in `docs/api-reference/internal.mdx`.

- **Cross-tenant scoping on Keeper Phase 2 internal endpoints.** The four
  `/api/v1/internal/keeper/*` handlers (skill-review, behavior,
  memory-health, negative-learning) now (a) include `workspace_id` in
  the `self_learning_enabled` lookup WHERE clause and (b) reject any
  request where the body `workspace_id` disagrees with the request
  context `workspace_id` set by `internalWsCtx`. Pre-fix an internal-
  auth caller could pass an `agent_id` from workspace A while claiming
  workspace B in the body and read the gate flag — asymmetric cross-
  tenant bypass. The symmetric case (caller picks one workspace
  consistently across query + body) is closed by the workspace-bound
  `X-Internal-Token` entry above (PR-F24); the former "known
  exception" block in `docs/api-reference/internal.mdx` is retired
  accordingly.

- **Lessons memory tier hardened against agent-author writes.** The
  generic `memory.write(tier="lessons", …)` dispatcher path returned
  cap=0 and bypassed every governance layer the F4.4 evaluator path
  enforces (schema validation, idempotency by ID, atomic-rename,
  flock). Agent-author writes to the lessons tier are now rejected
  with an error that points at the F4.4 negative-learning endpoint as
  the supported entry point. Tombstone test
  `TestDispatch_Write_LessonsTier_Rejected` pins the contract.

- **Security hardening v1 wave.** Two-tier CLI tokens (standard +
  HMAC-keyed admin) with fine-grained PAT-style scopes, per-crew role
  overrides and per-agent ownership gates, structured 403s with a denial
  audit log, one-shot setup token on `/api/v1/bootstrap`, internal API
  refuses non-private client addresses, sidecar credential proxy strips
  plaintext token fields, `NET_RAW` dropped from default crew container
  capabilities, `/crew/init.sh` auto-exec now opt-in per crew, and
  bootstrap/pair tokens widened to 256-bit (#462).

- **May–June external-audit remediation waves.** Prod `docker.sock` is
  brokered through a filtering proxy (#478); memory writes are scanned
  *before* persist, including invisible-format (Cf) codepoint evasion
  (#502, #477); a TOCTOU symlink window in `writeCredentialFiles` closed
  (#464); path traversal, IDOR, cross-crew access, MCP SSRF,
  prompt-injection and info-leak findings fixed across four passes
  (#476–#508, #612, #619, #620, #651, #733, #752).

- **RBAC chokepoint: route-table declared authz.** Previously
  un-gated control-plane mutation endpoints now enforce role checks
  (#792); every route's permission gate is declared in the route table
  and pinned by an invariant test so new endpoints can't ship ungated
  (#824, closes #809/#811); admin console + system endpoints get a
  uniform ADMIN+ floor (#893); scoped CLI-token permissions are enforced
  at the same route-table chokepoint (#888).

- **Ingress trust fence.** Untrusted external content (webhook bodies,
  poll payloads) is neutralized before it can enter an agent prompt
  (#819, #808 M0), extended to the remaining mission/task and
  crew-context ingress sites (#918); `tool_result` events from **all**
  adapters now pass through one shared injection-scan chokepoint (#950).

- **Per-agent sidecar identity.** Each agent in a crew receives its own
  derived auth token, closing intra-crew identity spoofing against the
  sidecar control plane (#826, closes #812), and escalations are
  attributed to the bound agent rather than a caller-supplied `from`
  field (#796). Per-agent memory identity fixes landed in the dev2
  validation sweep (#779, CRE-137…146).

- **Credential revocation is now effective at runtime.** The sidecar's
  in-memory credstore reaps revoked credentials within ~60s (#795), and
  revoking a credential also removes its file-based `/secrets` materials
  from running containers (#903, closes #814).

- **Webhook trigger hardening.** Every pipeline webhook dispatch
  requires an HMAC signature (#501) with replay auto-dedupe (#506) and a
  rate-limit floor (#507); empty-secret HMAC configs are rejected and
  optional signed-timestamp replay defense added (#789), with per-agent
  `require_timestamp` to close the body-only replay window (#822,
  closes #815).

- **New crews default to restricted egress**, and egress-policy denials
  are loud instead of silent (#793); `http` pipeline steps enforce the
  crew egress policy and resolve credentials from the vault rather than
  inline secrets (#778).

- **Memory file-path hardening.** Centralized pathsafe fencing on memory
  paths plus capped search allocations close 13 CodeQL HIGH findings
  (#926); memory-read and writer-lock opens use `O_NOFOLLOW`, closing a
  symlink TOCTOU (#936, closes #934/#935).

- **Log-injection neutralized centrally.** One CR/LF + control-character
  neutralizer covers server and sidecar log sinks (#938); secret
  redaction is wired through slog (#488) and raw token values in logs
  replaced by fingerprints (#486).

- **deb/rpm packages are GPG-signed** and the public key published for
  apt/dnf verification (#942, closes #932).

- **Go toolchain bumped 1.26.0 → 1.26.5** across the release cycle for
  stdlib CVEs, including the crypto/tls GO-2026-5856 advisory
  (#570, #907).

### Added

- **Pipeline resume-from-step at boot.** The executor now persists
  `current_step_id` + the step-outputs map at every step boundary, and
  boot recovery re-enters previously in-flight runs from the next
  unfinished step instead of stamping them `interrupted`: completed
  steps are restored (not re-executed), the in-flight step re-runs
  with at-least-once semantics, DAG runs recover at wave granularity,
  and runs parked on a `wait` approval step re-attach to their
  original pending waitpoint token so the approval card stays
  answerable across restarts. `interrupted` remains the fallback when
  persisted state is insufficient (missing pipeline, definition drift,
  non-resumable mode), with the reason recorded in `error_message`.
  Operator escape hatch: `CREWSHIP_PIPELINE_RESUME=off` restores the
  old stamp-everything-interrupted behaviour. See
  `docs/guides/routines.mdx` § Durability and restart recovery.

  Hardening pass on the resume scan: (1) the boot scan now runs
  BEFORE the cron scheduler starts and is additionally fenced on
  process-boot time + the live run registry, so a scheduled run fired
  at boot can never be "resumed" into a second concurrent execution
  under the same run id — `RunRegistry.Acquire` also rejects
  duplicate run ids outright instead of silently overwriting the live
  entry; (2) a resumed run that loses the concurrency-slot race
  retries with capped exponential backoff instead of being
  permanently stamped interrupted; (3) migration v114 stamps the
  pipeline's definition content hash onto each run row at start, and
  resume refuses (→ `interrupted: definition changed`) when the
  definition was edited in place even if every step id survived;
  (4) a waitpoint that timed out during downtime now fails the
  resumed run with `timed out` instead of misreporting `denied`.

- **Domain metrics on `/metrics` (W10).** The Prometheus endpoint now
  exposes operator-facing series next to the existing process gauges:
  `crewshipd_assignments{status}`, `crewshipd_assignment_queue_depth`
  / `_queue_crews` / `_queue_depth_max` (aggregated — no per-crew
  labels by design), `crewshipd_pipeline_runs{status}`,
  `crewshipd_agent_run_events_total{event}`,
  `crewshipd_llm_calls_total{provider}` +
  `crewshipd_llm_cost_usd_total{provider}` from the paymaster ledger,
  `crewshipd_containers_tracked` / `_reporting`, and
  `crewshipd_db_migration_version`. Label sets are closed (unknown
  values fold into `other`), the DB-derived block is cached for 15s,
  and migration v113 adds the two status indexes the counts ride on.
  Documented in `docs/observability/metrics.md`.

- **PR-G / PR-F UI surface.** Three React panels expose previously
  backend-only governance toggles: `CrewPolicyControls`
  (`autonomy_level` × `behavior_mode` × `max_ephemeral_agents`),
  `AgentLearningToggle` (per-agent `self_learning_enabled` flag from
  migration v106), `AuxStatusSection` (read-only diagnostic of the
  five auxiliary model slots). Plus four keeper P2 review queue
  sub-tabs in the admin panel, a GDPR admin export/delete panel,
  inbox approve-hire button, and codemirror markdown editor for the
  agent memory tab.

- **Migration v106 — per-agent `self_learning_enabled` flag** with the
  standard audit triple. Consumed by the F4.4 negative-learning and
  F6 persona-suggest ALLOW paths: when the flag is OFF, the proposal
  queues a blocking inbox row with the full proposal payload instead
  of auto-applying.

- **Migration v107 — GDPR cascade primitives.** `data_subject_id`
  columns on `memory_versions` and `inbox_items`, plus the
  `gdpr_actions` audit table. New `DELETE /api/v1/admin/users/{id}/data`
  cascade endpoint + `GET` Art. 15 export bundle. Idempotent — each
  invocation lands a new `gdpr_actions` row.

- **`MemoryProvider` interface and `LocalDispatcher` reference impl**
  for future pluggable memory backends. Additive — existing production
  call sites still route through the built-in dispatcher; swap lands
  as PR-F17.

- **`AgentBrief` sub-agent briefing primitive.** Replaces the
  all-or-nothing `SkipConvHistory` boolean with a curated `Mission` +
  `SharedMemory` + `Constraints` slice written to the child's
  `BRIEF.md`; picked up by the orchestrator's prompt assembly.

- **Declarative deployment manifests (SPEC-2).** `crewship apply` covers
  14 manifest kinds end-to-end with validation (duplicate-slug detection
  at validate time), plus auto-managed sidecar credentials (SPEC-4)
  (#454, #456, #587).

- **Routines platform wave.** Governance review gate
  (proposed → approve, with OWNER/ADMIN `skip_governance_gate`),
  describe-first authoring from chat, `save_routine`/`list_routines` as
  native MCP tools, precondition gates and declared required
  integrations, CEL expressions, deferred dispatch, hooks and input
  streams (#743, #715, #739, #755). Follow-on: first-class token-zero
  `script` steps (#849) with portable export/import bundles (#913),
  per-step retry policy with backoff + CEL classifier (#882),
  auto-parallelized independent steps with bounded DAG waves (#872),
  single-step fixture runs (#854), `routine init` scaffold/clone (#860),
  run-result retrieval (#844), run→files listing (#891), client-facing
  shareable/redacted progress views (#877), offline `validate` parity
  with server-side checks (#901), and non-blocking `notify` steps with
  outbound email + signed-webhook channels and anti-spam caps
  (#843, #859, #910).

- **Managed integrations via Composio.** Catalog, OAuth connect flow,
  per-agent binding, tool exposure and triggers (#696), a flag-gated
  default connector for all agents (#699), portal-safe Add-app UX
  (#703), and agents are made aware of their connected integrations
  (#704).

- **Chat overhaul.** Faithful history with reload, grouped
  tool/reasoning UI, sub-agents and multi-user group chat (#702);
  resumable streams so a reply is never lost to a refresh (#757); smooth
  streaming reveal (#758); unread badges, activity ordering and
  agent-replied notifications (#760); mid-turn steering of an in-flight
  run, long-conversation compaction instead of truncation, cross-session
  conversation search (BM25), and LLM model discovery with live model
  switch (#630 wave).

- **Fleet operations UI.** The Journal Runs tab is reworked into a fleet
  ops overview (#750), a global Activity Bar + readable run-activity
  timeline (#701), context-aware bottom dock across Issues, Routines &
  Activity (#710), and a drillable agent-native run trace — tool-call
  sub-spans, waterfall, persisted per-step input/output with lazy fetch
  and syntax highlighting (#852, #853, #874), plus per-step
  container-ready timing (#930).

- **Inbox redesign.** Gmail-style triage with formatted detail,
  noise/secret hardening and CLI parity (#708), collapsible group tree
  with bulk select/resolve (#692), refined toolbar (#747), and
  agent-proposed credentials with one-click human approval (#706).

- **Ephemeral ("hired") agents surfaced in Crews & Agents** with full
  lifecycle controls (#693); hired agents automatically receive an
  Anthropic credential so they can actually run (#680).

- **Slash commands + per-user capabilities.** Server-driven slash
  palette with CLI/UI parity, gated by per-membership capability grants
  (#595).

- **Packaging: deb/rpm packages + systemd unit** for server installs
  (#927, #858 phase 4), and **Windows support via a CLI-only
  `crewship-cli` build** — zip archives, `O_NOFOLLOW` platform split,
  gated self-update (#949, closes #945).

- **`crewship self-update`** — one upgrade command per install channel
  (brew/deb/rpm/tarball) (#915), including systemd server-mode upgrade
  orchestration with health-checked restart (#929, #858 phase 5).

- **Downgrade/version safety.** A version-skew guard refuses to boot a
  binary over a newer DB schema (#912), and
  `crewship db restore-snapshot` provides the matching downgrade
  recovery path (#924); backups got a disaster-recovery rewrite —
  `--replace` mode, schema-driven FK discovery, user reconciliation
  (#594).

- **Local-model support.** Ollama-style local endpoints for the OpenCode
  adapter with BYOK egress (#951), and a first-class `ENDPOINT_URL`
  credential type so local model endpoints live in the vault like any
  other credential (#957, closes #955).

- **Devcontainer-based provisioning.** BuildKit feature-image
  provisioning (#675), proactive auto-provision so crews are runnable on
  first dispatch (#731), deduped feature catalog preferring canonical
  publishers (#732), and persisted BuildKit stderr tails so failed
  builds are debuggable (#884).

- **Agent-authored skills.** Agents can draft skills that route through
  inbox review into a GENERATED catalog (#734).

- **CLI: agent-ready contract.** Structured errors, stable exit codes,
  `--wait`, env-var auth and API parity for driving Crewship from
  agents (#782); native server profiles for multi-instance targeting
  (#737); unified `crewship nuke` subcommands with full workspace
  teardown (#748); confirmation prompts + `--yes` on six destructive
  commands (#579); `--max-turns` on run/ask + agent schedule flags
  (#753); `routine logs --show-outputs` for post-hoc step debugging
  (#828); `me preferences` + privacy commands (#754).

- **Cost controls.** Cache-stable prompt assembly, per-run turn caps
  with a loop guard, and cheap-model routing for aux/sub-agent calls
  (#751).

- **Operator observability.** Opt-in pprof + Pyroscope push profiling
  (#552), runtime log-level toggle and disk-health reporting (#784), and
  every run surfaces the model it actually resolved to.

- **Crew memory from mission outcomes (F4.5).** Mission results are
  distilled into crew memory with provenance (#546); an evolving
  per-operator user model personalizes agent behavior over time
  (#630 wave).

- **Settings & workspace management.** Real workspace switcher (list,
  select, persist, create) (#597), editable profile with password change
  and workspace member role management (#883), avatar upload (#900).

- **Release engineering.** The release smoke pipeline runs again on a
  real trigger plus a pre-merge package smoke (#941, closes #933), and
  nightlies publish per-run immutable pre-releases instead of a rolling
  tag (#895).

### Changed

- **Frontend RBAC mirrors backend.** `AgentLearningToggle` derives
  `canEdit` from `abilities.can("manage", "Agent")` (matching the
  backend PATCH permission gate); `CrewPolicyControls` mirrors via
  `abilities.can("update", "Crew")`. Update-only users see the
  toggle disabled instead of hitting 403 at save time.

- **`Promise.all` → `Promise.allSettled`** in `CrewPolicyControls.load`
  so a quota-fetch network error doesn't poison the required policy
  fetch.

- **One request-builder for every dispatch path.** Chat, missions,
  routines and peer delegation now build agent requests through a single
  shared factory, which also activated the previously dead HITL approval
  path and fixed mission/peer MCP + prompt divergence (#825, closes
  #810); pipeline executor wiring was likewise unified behind one
  factory (#773).

- **Telemetry defaults to opt-in on stable releases** with an explicit
  onboarding consent step (crash-reporting opt-out remains for
  pre-release channels) (#645).

- **First-run bootstrap UX.** The setup-token gate is replaced by an
  n8n-style first-run flow (#593), and the bootstrap window now stays
  open until the first admin exists, with the finite time window as an
  opt-in (#785).

- **UI chrome unified for 1.0** — consistent sub-bars, sidebars and
  toolbars across pages (#749); agent access consolidated under a single
  Skills & Tools surface (#698) with a curated built-in tool profile per
  adapter (dead harness tools removed) (#705).

- **Routine `test_run` is gone** — `dry_run` returns an honest execution
  plan plus the declared capability manifest instead of pretending to
  execute (#743 wave).

- **First-party MCP tools are eager-loaded**, removing the per-run
  ToolSearch discovery tax (#745).

- **Dispatch/runtime performance.** Deferred-run dispatch is async,
  removing the one-run-per-run-duration cliff (#857); warm crew hits
  skip the host-wide Docker `ContainerList` (#876); crew containers
  prewarm on claim, off the critical path (#902).

### Fixed

- **`crewship seed` now bootstraps against the selected `--profile`, not
  `CREWSHIP_SERVER`.** The seed flow's unauthenticated bootstrap POST (and
  the `--nuke` confirmation, smoke test, and backup warmup) resolved their
  target with `ResolveServer`, whose precedence is `--server` >
  `CREWSHIP_SERVER` > config. Every *authenticated* call in the same command
  uses `EffectiveServer`, under which an explicit `--profile` /
  `CREWSHIP_PROFILE` wins over `CREWSHIP_SERVER`. So in a shell that exports
  `CREWSHIP_SERVER` (the documented multi-clone convention), `crewship seed
  --profile prod` sent bootstrap to `CREWSHIP_SERVER` while everything else
  went to the profile server — silently splitting one seed across two
  instances and surfacing as a bogus `DB already initialized` when
  `CREWSHIP_SERVER` pointed at an already-bootstrapped instance. The seed
  flow now routes every call through one `seedTargetServer()` helper backed
  by `EffectiveServer`.

- **Episodic indexer now starts at server boot.** The journal-embedding
  sweeper (`episodic.NewIndexer`) was fully implemented and tested but
  never constructed in production, so `HybridRecall` always queried an
  empty vector index. The server now starts the sweeper at boot when an
  embedder is configured (`KEEPER_OLLAMA_URL`). Without one, episodic
  recall runs in **sparse-only mode** and says so: a WARN at boot, an
  `episodic: vector|sparse-only` field on `GET /healthz`, and a
  matching `crewship doctor` check with the enable hint.

- **Stuck-QUEUED assignment sweeper now runs in production.** The
  crash-recovery sweeper (`StartStuckQueueSweeper`) was implemented
  and tested but never started — QUEUED assignments stranded by a
  crash between "row set QUEUED" and the next completion-path pump
  stayed queued forever after a restart. Server boot now starts the
  sweeper alongside the other background loops (scan every 60s, rows
  count as stuck after 10min queued; goroutine exits cleanly on
  shutdown) and logs a `stuck-queue sweeper started` line at boot.

- **Inbox `fetch()` network-error handling.** Both `wrap("approved")`
  (approve-hire) and `wrap("retried")` (routine retry) in
  `inbox-list.tsx` now wrap `await fetch(…)` in `try`/`catch`. Pre-
  fix, an offline / DNS / CORS preflight failure cleared the busy
  state with no user toast (silent success).

- **Free-form `reason` text scrubbed from app logs.** The
  `agent_learning` PATCH handler logged the operator-entered reason
  verbatim; switched to `reason_len` so centralized logs no longer
  collect PII / business context that's already on the DB audit row.

- **Inbox enqueue failures surface as 500.** The `self_learning=OFF`
  gate path in both the F4.4 negative-learning handler and the F6
  persona-suggest handler was swallowing `inbox.Insert` errors,
  returning 200 with neither lesson nor inbox row.

- **CI build break from a leaked merge-conflict marker** in
  `internal/api/agent_config.go` (PR-D `agent.status` + PR-E
  `system_prompt_legacy` collision during parallel-agent push churn).

- **Credential escalation no longer fakes success.** When a credential
  proposal can't be staged, the agent gets an error instead of a
  silent false-success (#787).

- **Recurring issues actually fire.** The dispatcher was never wired
  into server boot (#791); follow-up adds creator attribution, durable
  fire idempotency and UTC consistency (#823), and issue creators are
  recorded and displayed everywhere (#774).

- **Schedulers are at-most-once.** Cron and deferred pipeline fires
  dedupe via idempotency keys (#788), the agent scheduler gets the same
  guarantee (#820), and scheduled fires honor `target_pipeline_version`
  (#777).

- **Container-start failures are classified** instead of collapsing into
  one masked generic error, so operators see the real cause (bad image,
  legacy volume, credential prep, …) (#790).

- **Crash recovery for assignments and chats.** RUNNING assignments
  orphaned by a crash are recovered at boot (#768), orchestration loops
  re-attach to IN_PROGRESS missions (#641), webhook-triggered runs
  dispatch asynchronously instead of blocking the receiver (#769), one
  active run per chat is enforced regardless of sender (#765), and a
  chat turn never ends in silence — zero-output and restart-interrupted
  replies are surfaced (#770).

- **Memory writes are durable and fail closed.** MCP memory-write
  failures no longer report success; memory tools are only advertised
  when the backing store is healthy; restart-agents matches the right
  container (#786).

- **dev2 validation sweep (CRE-137…146).** Memory identity, keeper
  workspace scoping, webhook HMAC, cost attribution and CLI
  parity/fidelity fixes from a full-instance validation pass (#779).

- **Keeper F4.1 skill-review sweep bills a real workspace** instead of
  attributing its LLM spend to a phantom one (#970, CRE-138).

- **Full archives ship a Linux ELF sidecar on every platform.**
  Darwin-built archives previously bundled a Mach-O sidecar that could
  never run inside Linux crew containers (#968, closes #953); archives
  now include `crewship-sidecar` + `entrypoint.sh` at all (#914), and
  sidecar remediation hints are install-channel-aware with the Homebrew
  libexec layout supported (#925).

- **HTTPS login worked around a CSRF cookie-name mismatch** — the CSRF
  cookie is re-sent under the server's real `__Host-` name (#875).

- **Crew shared files reach `/crew/shared`.** Bundled `files:` never
  arrived for agentless crews (#870); delivery is container-routed with
  overwrite semantics and works for standalone SPEC-2 Crews (#928);
  re-applying an unchanged file no-ops and succeeds on a stopped crew
  (#940).

- **OpenCode adapter production parity** — usage keys, model surfacing,
  JSONL schema and EOF resilience (#948).

- **Raw internal errors no longer leak to chat/comments**; hook warnings
  surface on the run instead (#771); journal poison entries are dropped
  rather than retried forever, and dev logs are capped (#783); chat WS
  sends are guarded against the server's 64 KiB frame cap (#764).

- **Scheduler resolves agent config via loopback** instead of the
  public Next.js URL, fixing scheduled routines behind proxies (#709);
  `crewship system keeper` injects the workspace so it stops 400ing
  after the admin-floor change (#909).

- **Pre-C1 legacy Docker resources are auto-migrated** on use and an
  ops command detects + prunes orphaned pre-C1 crew resources, fixing
  the "failed to start agent container" wall after old seeds
  (#736, #738).

## [0.1.0-beta.4] — 2026-05-19

**Routines 2026, declarative manifests, security hardening.** Substantial
beta covering observability (OTel spans, prompt-cache token plumbing),
ADLC phase-7 signal (typed feedback API + thumbs UI), continuous online
grading (sampler worker), per-routine guardrails, declarative workspace
manifests with sidecar services, and security CI cleanup. v0.1.0-beta.3
was skipped — this tag bundles everything from beta.2 → beta.4 on `main`.

### Operator upgrade notes

- **Backup `crewship.db` before upgrading.** Migration v97 recreates
  the `eval_runs` table via the standard SQLite RENAME → CREATE →
  `INSERT...SELECT` → DROP pattern to widen the `kind` CHECK constraint
  for the new `online` sampling kind. The migration runs in a
  transaction so a mid-migration crash atomically rolls back, but it
  has not been benchmarked on a production-sized eval suite — schedule
  the upgrade during a quiet window.
- **`CREWSHIP_ALLOWED_ORIGINS`** must be set in production env config
  for browser-driven POSTs (Next.js → daemon cross-port). `dev.sh` now
  emits it automatically alongside other managed keys; systemd-driven
  prod deploys must add it to their unit env file.
- **Online eval sampler runs on every server boot** with a 60-second
  tick. Routines without `eval.online.sample_rate > 0` are zero-cost
  deterministic skips. Operators introducing `sample_rate: 1.0` on
  high-throughput routines should size their grader budget; the sampler
  enqueues at the routine's rate but the grader cost is per-eval.
- **Shadow features available but require operator config:**
  - **Prompt caching:** ledger + telemetry plumb provider-reported
    `cached_input_tokens` once an `API_KEY`-typed Anthropic credential
    is provisioned (Claude Code CLI tokens don't go through this path).
  - **OTel routine spans:** `routine.run` / `routine.step` /
    `agent.invoke` / `llm.call` spans emit when `OTEL_EXPORTER_OTLP_ENDPOINT`
    is set; collector wire-up is operator's choice — any OTel-compatible
    backend consumes the GenAI semconv format natively.
  - **Per-routine input-guard action policy:** DSL
    `guardrails.input.prompt_injection.action: block | sanitize | log`
    only fires for routines that opt in.

### Added — Observability

- **OpenTelemetry GenAI spans** wired across the hot path:
  `routine.run`, `routine.step`, `agent.invoke`, `llm.call` with the
  prescribed `gen_ai.*` + `crewship.*` attributes. New
  `StartRoutineRunSpan` + `StartRoutineStepSpan` helpers
  (`internal/telemetry/spans_routine.go`). Trace tree mirrors DSL
  composition; `call_pipeline` nests as a child step. Panic recovery
  pattern preserves the original crash stack across nested defers
  via `telemetry.PanicWithStack` so post-mortem traces point at the
  real explode site, not at the re-panic line. (#447)
- **Prompt-cache token plumbing** through provider → ledger → OTel.
  Anthropic's `cache_read_input_tokens` + `cache_creation_input_tokens`
  and OpenAI's `prompt_tokens_details.cached_tokens` now surface on
  `llm.Response`, flow into `paymaster.CallResponse.CachedInputTokens`,
  land in `cost_ledger.cached_input_tokens` / `cache_creation_tokens`,
  and stamp `gen_ai.usage.cached_input_tokens` on every LLM span.
  Anthropic tools array gets a `cache_control: ephemeral` breakpoint
  by default — the single highest-leverage cache hit for agent
  workloads. (#447)

### Added — Feedback (ADLC phase-7)

- **Typed per-message feedback API** (`/api/v1/feedback`) with six
  signals (helpful, not_helpful, inaccurate, unsafe, edit, regenerate)
  bound to `trace_id` for eval-mining correlation. Migration v96
  introduces `message_feedback`. POST is UPSERT-idempotent; DELETE is
  idempotent (204 on missing row); GET is workspace + per-user scoped.
  Body capped at 16 KiB via `MaxBytesReader` before JSON parse;
  per-field caps at 4096 chars on `reason` and 256 chars on id fields. (#447)
- **Frontend optimistic-update store** (`stores/feedback-store.ts`)
  with per-(turn, signal) Promise-chained serialization so a fast
  thumb-toggle can't race between POST and DELETE. State is namespaced
  by `user.id`; switching accounts on the same browser clears the
  previous user's votes. (#447)
- **Trace_id WS propagation** — `internal/chatbridge/bridge.go` stamps
  the active OTel trace id onto the `done` event metadata;
  `hooks/use-chat.ts` lifts it onto `ChatTurn.metadata.trace_id`. The
  feedback POST flows it through so every signal lands indexed against
  the routine run that produced the message. (#450)

### Added — Online eval sampler

- **Continuous production grading** via `internal/quartermaster/online_sampler.go`.
  Worker scans completed `pipeline_runs` every 60s, picks rows at the
  routine's configured `eval.online.sample_rate`, and enqueues a
  `kind='online'` eval row. Schema-layer idempotency via partial
  `UNIQUE INDEX uq_eval_runs_online_pipeline_run`; (ended_at, id) tuple
  cursor handles sub-millisecond pipeline_run completions without
  orphaning siblings; doubling-skip backoff on entropy outages capped
  at 10 ticks. Wired into `cmd/crewship` server start. (#447, #449)

### Added — Guardrails

- **Per-routine input-guard action policy**
  (`guardrails.input.prompt_injection.action`) with `block` (default) /
  `sanitize` / `log` modes. Sanitize uses offset-based replacement via
  new `Finding.MatchEnd` field — earlier substring-based redaction
  silently let through long jailbreak matches and synthetic unicode
  findings like `"U+202E"`. (#447)
- **`on_guardrail_triggered` hook dispatch** via context-attached
  `GuardListener` callback. Lookout stays zero-dep on the hooks
  package; the pipeline runner bridges them. Listener receives the
  full findings slice. (#447)

### Added — Tooling

- **`crewship apply` / `crewship export`** for declarative workspace
  manifests with sidecar service declarations (Redis, Postgres, MySQL,
  MongoDB). Migration v95 adds `crews.services_json`. (#448)
- **Playwright E2E specs:** `e2e/feedback.spec.ts` (8 contract tests)
  and `e2e/feedback-ui.spec.ts` (browser-side fetch via real NextAuth
  cookie + CSRF defense pin via spoofed Origin → 403). (#450)

### Added — Installation

- **Auto-generate secrets on first run.** `crewship start` writes
  NEXTAUTH_SECRET + ENCRYPTION_KEY to
  `~/.local/share/crewship/secrets.env` when missing. End users no
  longer touch env files for the happy path. (#446)

### Fixed

- **Online sampler was dead code in PR #447** — `NewOnlineSampler`
  had test coverage but no production call site. Wired into bootstrap. (#449)
- **Sampler SQL queried non-existent `completed_at` column.** Real
  column is `ended_at`. The test fixture matched the bug so unit
  tests passed; real schema check on dev-VM smoke caught it. (#449)
- **Code-scanning alerts.** All open CodeQL + Grype findings closed. (#445)
- **Privacy leak in `GET /api/v1/feedback`** — earlier draft scoped
  only by workspace membership; now scoped by `user_id` AND workspace. (#447)
- **Sanitize bypass via mixed zero-width characters.** ScanInput
  emitted a Finding only for the FIRST zero-width rune; subsequent
  ZWNJ/ZWJ/BOM in the same payload survived sanitize. Now emits one
  Finding per occurrence. (#447)
- **OnlineSampler data race** on watermark cursor between concurrent
  Start callers — `go test -race` reproduced. Added `sync.Mutex`;
  `Start` now wrapped in `sync.Once`. (#447)
- **Sampler panic-naked.** A panic in `runOnce` would kill the
  daemon. Added deferred `recover()` in `tickWithBackoff` that logs
  + lets the loop continue. (#447)

## [0.1.0-beta.2] — 2026-05-18

**First public beta release.** APIs and data models may break across
minor bumps until v1.0. See `RELEASING.md` for upgrade and rollback
guidance.

> v0.1.0-beta.1 was burned by a series of release-pipeline iterations
> (cosign version pin, pnpm toolchain mismatch in the Dockerfile,
> Windows cross-compile, missing direct deps for Turbopack,
> port_exposures test flake). The "release immutability" toggle was
> enabled mid-iteration and permanently reserved that tag name even
> after deletion. The first public tag is therefore v0.1.0-beta.2.

### TL;DR for beta testers

- Install: `brew install crewship-ai/tap/crewship` (macOS) or
  `docker pull ghcr.io/crewship-ai/crewship:v0.1.0-beta.2` (Linux/Docker).
- One adapter is production-ready in beta: **Claude Code (Anthropic)**.
  Codex / Gemini / OpenCode / Cursor / Factory Droid have scaffolds
  but lack parity testing — see README "Beta status & limitations".
- Telemetry (Sentry crash reporting) is **enabled by default** during
  v0.1 beta to give the solo maintainer signal from real installs.
  Disable any time with `crewship telemetry off`. Reverts to opt-in
  for v1.0 GA. See `RELEASING.md` Telemetry section.
- Storage is SQLite-only in v0.1; PostgreSQL is on the v0.2 roadmap.

### Added — Release infrastructure

- **Auto-snapshot before migrations.** `database.SnapshotBeforeMigrate`
  takes a `VACUUM INTO` copy as `<db>.pre-migrate-vN-to-vM-<UTC>.bak`
  whenever a migration is pending. Keeps 10 newest snapshots; opt out
  with `CREWSHIP_SKIP_MIGRATION_BACKUP=1`.
- **Migration lint in CI.** `.github/workflows/migration-lint.yml` +
  `scripts/lint-migrations` enforce append-only ordering — versions
  strictly increase, no rename of a version already shipped to `main`.
  In-tree Go tests guard monotonicity and uniqueness on every PR.
- **GHCR multi-arch Docker images.** linux/amd64 + linux/arm64,
  cosign keyless signed via GitHub OIDC. Tags published per release:
  `:vX.Y.Z`, `:vX.Y`, and `:latest` (last one ONLY on clean semver tags
  — pre-release tags never bump `:latest`).
- **Nightly channel.** `.github/workflows/nightly.yml` rebuilds on every
  push to `main`: `:nightly` and `:main-<sha>` Docker tags, plus a
  rolling `nightly` GitHub pre-release with prebuilt binaries.
- **One-line installer.** `scripts/install.sh` detects OS+arch, verifies
  sha256 + cosign signatures, installs to `~/.local/bin` (no sudo) or
  `/usr/local/bin`. Until the project website is live, fetch direct from
  the repo: `curl -fsSL https://raw.githubusercontent.com/crewship-ai/crewship/main/scripts/install.sh | bash`.
  The short `crewship.ai/install` redirect will land alongside the
  website launch.
- **Update notification.** `internal/update` queries GitHub Releases API
  daily (cached in `~/.crewship/cache`). CLI prints upgrade banner at
  startup; web UI surfaces a dismissable banner via
  `GET /api/v1/system/version`. Optional `GITHUB_TOKEN` to lift the
  60/h unauthenticated rate limit to 5000/h.
- **Sentry crash reporting (opt-out by default in beta).** New
  `internal/crashreport` package wraps `getsentry/sentry-go` behind a
  consent gate stored in `app_settings`. DSN injected at link time via
  ldflag from `SENTRY_DSN` GitHub Actions secret. Strict client-side
  scrubbing of headers, query strings, request bodies, User field, and
  device/runtime/culture contexts; server-side regex rules in Sentry UI
  cover email/Bearer/`sk-*`/`ghp_`/`xox*-` patterns in error messages.
  `CREWSHIP_SENTRY_DSN` env var redirects to a self-hosted/own Sentry.
- **`crewship telemetry on/off/status`** sub-commands manage consent at
  runtime; `status` shows the resolved endpoint host plus DSN source
  (vendor default vs env override). First-run prompt removed — beta
  default is enabled.
- **Sentry alert-rule provisioner** (`scripts/sentry-setup-alerts.sh`):
  idempotent bash script that calls the Sentry REST API to create the
  "New issue (beta)" and "Spike — 50+ events/hour" alert rules.
- **PR + repo hygiene.** Stale-bot workflow (issues 90d, PRs 44d, generous
  opt-out labels), PR template Migration Safety checklist,
  `scripts/setup-branch-protection.sh` one-shot for required checks +
  linear history. Hotfix runbook in `RELEASING.md`.
- **CODE_OF_CONDUCT.md** (Contributor Covenant 2.1 by reference) +
  `ee/README.md` scaffold for future dual-licensed enterprise add-ons.

### Added — Connectors (catalog → install → MCP)

- **`ConnectorCatalog`** tile-grid UI for browsing the bundled manifest
  catalog under `manifests/` (`feat/connector-catalog-impl`).
- **`SchemaForm`** five-field-type renderer (text/secret/select/toggle/
  number) with per-field validation and defaults.
- **`ConnectorConnectSheet`** wires SchemaForm into the install flow —
  validates inputs, persists credentials via the sidecar, hands off
  OAuth where applicable.
- **Backend connector handlers** — `ParseManifest`, `Validate`,
  `Resolve`, `MaterializeMCP`, `LoadAll`; HTTP routes for List / Get /
  Verify / Install (incl. credential persistence + OAuth handoff).

### Added — Auth + onboarding overhaul (PR #314)

Pre-beta sweep: account recovery, device pairing, split-screen
onboarding wizard, session-rotation + lockout primitives.

### Added — CLI: AI-first 2026 (15 new commands and flags)

Major CLI surface expansion aligning Crewship with the 2026 agent-CLI playbook (long-running workflows, plan/act separation, headless scripting, real-time dashboards, model-tiering control). All additions live in `cmd/crewship` and `internal/cli`; one server endpoint added (`GET /api/v1/runs/{id}`).

**New top-level commands:**

- **`crewship -p "..."`** — headless one-shot prompt to the default agent. Sets quiet by default, exits non-zero on agent error. Pipe-friendly: `cat issue.md | crewship -p "summarise"`.
- **`crewship plan <prompt>`** + **`--plan`** flag on `run`/`ask` — plan/act separation. Read-only architect mode that outputs a step-by-step plan + files-to-touch + risks without executing tools. Prompt-engineered (no backend mode), so it composes with every adapter.
- **`crewship resume [chat-id|run-id|pr-url]`** — pick up the last session, an explicit one, or the session that produced a GitHub/GitLab/Bitbucket PR. No-arg form opens a `huh`-styled picker over the 10 most recent CLI sessions.
- **`crewship wait <run-id>`** — block until a run reaches a terminal status. Status-aware exit codes (0 done, 1 failed, 2 cancelled, 3 timeout). Use in scripts: `crewship wait $(crewship ask --no-stream -q "..." | jq -r .id) && echo done`.
- **`crewship tui`** — real-time Bubble Tea dashboard. Three panels: running runs, pending approvals, live journal stream (SSE-pumped). Keys: `q` quit, `r` refresh, `Tab` focus.
- **`crewship recap <chat-id>`** — LLM-generated summary of a chat session via the default agent. Output is a 4-section markdown brief (outcome / decisions / open threads / next prompt). Tunable bullet count via `--bullets`.
- **`crewship shell`** — interactive REPL. Slash commands: `/help`, `/agent <slug>`, `/workspace <slug>`, `/cd`, `/plan` (toggle), `/effort <level>`, `/think` (toggle), `/clear`, `/history`, `/quit`. `@file` fuzzy expansion inlines file content into prompts.
- **`crewship me`** — your missions + your pending approvals + your recent runs (3 parallel REST calls).
- **`crewship today`** — today's runs and spend.
- **`crewship now`** — live status: running runs, idle/busy agent counts, pending approvals.
- **`crewship cost forecast`** — projected cost before you spend tokens. Two modes: `--prompt @file` (token-count heuristic) or `--from-history <agent>` (average of last 20 runs). Renders rate table for Sonnet 4.6 / Opus 4.7 / Haiku 4.5 with output-ratio tuneable (`--output-ratio`, default 2.0×).
- **`crewship diff <run-a> <run-b>`** — side-by-side comparison of two existing runs (status, agent, output diff). Distinct from `eval compare` which re-runs an eval scenario.
- **`crewship notify`** — desktop notifications group. `enable` / `disable` / `status` / `test` / `send <title> <body>`. Auto-fires on long-running run completion (≥30 s) and pending approvals. Uses `osascript` on darwin, `notify-send` on linux, BurntToast on Windows (no-op when missing).
- **`crewship slash`** — manage user-defined slash commands. `slash list` enumerates loaded files; `slash init` scaffolds `~/.crewship/commands/review.md` as a starter.

**New flags on existing commands:**

- **`--format=ndjson`** (global) — line-delimited JSON output, pipe-friendly for `jq -c` / `fx` / stream-processing tools. Plumbed through `Auto` / `AutoDetail` so every list/detail command supports it uniformly.
- **`--plan`** on `run` / `ask` — plan-mode without a separate command.
- **`--effort=minimal|low|medium|high|xhigh`** on `run` / `ask` — reasoning effort passthrough, threaded into chat-creation metadata.
- **`--show-thinking`** on `run` / `ask` — surfaces full reasoning blocks on stdout (not the 100-char truncated stderr peek).

**User-defined slash commands** (`~/.crewship/commands/*.md`)

Markdown files with YAML frontmatter become first-class CLI subcommands at load time:

```markdown
---
name: review
description: Review a diff
agent: viktor
plan: true
vars:
  - target
---
Review this ${target} for $args.
```

`name`/`description`/`agent`/`effort`/`plan`/`vars` are honoured. `$VAR` / `${VAR}` substitution against positional args. Built-in commands always win on collision (the loader skips + warns).

**Server surface (one endpoint added):**

- **`GET /api/v1/runs/{id}`** — single-run lookup used by `wait`, `resume`, `diff`. Reuses the existing `journal.ListRuns` + enrichment path; 404 for unknown ids (cross-tenant masked).

**New internal helpers** (single-responsibility, all unit-tested):

- `internal/cli/runs.go` — `GetRun(ctx, id)`, `PollRun(ctx, id, interval, onTick)`, `ParsePRURL(s)`, `RunDetail`.
- `internal/cli/notify.go` — `OSNotify(title, body, level)`, `NotificationsEnabled(cfg)`, GOOS dispatch matrix.
- `internal/cli/slashcmd.go` — `LoadSlashCommands()`, `ParseSlashFile(path)`, `SlashCommand.Render(args)`, frontmatter loader.
- `internal/cli/repl.go` — `REPL` struct with slash-dispatch, `ExpandAtFiles(line)`, `ApplyPlanShellPrefix`.
- `internal/cli/tui/` (package) — Bubble Tea Model/Update/View, SSE journal pump with reconnect, lipgloss styling.
- Formatter: `NDJSON(v)`, `WriteNDJSONRow(v)`, `"ndjson"` routing in `Auto` / `AutoDetail`.

**Tests added (~30 new tests):**

- `runs_test.go` — `IsTerminal`, `ParsePRURL` (5 hosts), `GetRun` (200/404/empty-id), `PollRun` (3-poll convergence).
- `notify_test.go` — `NotificationsEnabled` (nil/false/true), `OSNotify` no-panic guard.
- `slashcmd_test.go` — frontmatter parse, no-frontmatter fallback, `$VAR` / `${VAR}` / `$args` substitution, name validation.
- `repl_test.go` — slash dispatch, unknown-slash warning, `@file` expansion (existing/missing/`@-`), plan shell prefix idempotency.
- `formatter_ndjson_test.go` — slice → multi-line, single object → one line, `WriteNDJSONRow`, `Auto` routing.
- `cmd_run_metadata_test.go` — `SetEffort` validation (5 levels + uppercase + whitespace + invalid), `ChatCreationBody` (default vs plan vs plan+effort), `ApplyPlanFlag` idempotency.

**Documentation:**

- README links to new commands inline (TODO: separate `docs/cli/` page in a follow-up).
- This CHANGELOG entry doubles as the design rationale for each addition.

### Added — Routines: Eval framework (PR follow-up to #281–#284)

Cross-tier consistency framework that makes routines a credible **agentic-program primitive**. Three new pieces and one resurrected runner:

- **13 eval scenarios** seeded under the `eval-` prefix (`cmd/crewship/seeddata/eval_scenarios.go`). Each is a normal routine with rigorous gates — no special test-mode code path. Categories covered: pure transformation × 2, classification, format compliance, reasoning chain, prompt-injection refusal, RAG faithfulness, cross-family LLM judge, cost guardrail, boundary handling, DAG trajectory, idempotency / concurrency, tier-escalation loop. Cross-family graders (Sonnet judges Haiku) mitigate self-preference bias on rubric-graded scenarios.
- **`crewship eval scenarios`** — batch runner: sweep eval-* routines × tier list × N runs, output pass-rate matrix in `table` / `json` / `yaml` / `markdown`. Use `--scenarios slug,slug` to scope, `--tiers fast,smart` to compare worker tiers, `--runs N` for variance, `--fail-fast` for early-exit on regression.
- **`crewship eval compare <slug>`** — head-to-head: run ONE scenario back-to-back on two tiers, report a verdict (`AGREE-PASS` / `AGREE-FAIL` / `DIVERGE-A-PASS` / `DIVERGE-B-PASS` / `AMBIGUOUS`) plus side-by-side outputs. Designed for *gate-pass agreement*, not text identity (two LLM runs are essentially never byte-identical).
- **`tier_override` field on `RunInput`** + JSON body `{"tier_override":"..."}` on the `/run` endpoint. Replaces every `agent_run` step's `complexity` for the duration of one run; step-level `model_override` still wins. Plumbed through CLI as `crewship routine run --tier-override fast|smart|...`.
- **JSON Schema gate enforcement** in `internal/pipeline/executor.go validateOutput`. Previously a no-op (`"documentation only"`); now uses `github.com/santhosh-tekuri/jsonschema/v5` (draft 2020-12). Distinct reason prefixes per failure class: `schema invalid:` (author bug), `output not valid JSON:` (worker didn't follow contract), `schema validation:` (output failed constraints).
- **LLMRunner restored** (`internal/pipeline/runner_llm.go`) as opt-in fallback. Removed in commit `8408f3e6` when OrchestratorRunner shipped; restored here so the eval suite is runnable on a workstation without a fully provisioned crew container stack. Selection at boot: `CREWSHIP_PIPELINE_RUNNER=llm_direct` (explicit override) → `--no-docker` (auto-fallback) → OrchestratorRunner (default; production unchanged).
- **`schemas/routine.v1.json`** picks up `outcomes`, `concurrency_key`, `max_concurrent` so IDE validation matches the server-accepted DSL surface.

Tests: 8 schema-gate cases, 9 tier-override sub-cases, 10 eval-CLI helper tests, 13 eval-scenario parse+validate tests — 40 new test cases total, all under `-race`.

### Added — Routines (PR #281 + #282)

Routines are AI-authored, workspace-scoped declarative workflow recipes — one declarative layer that any crew can invoke for what previously required a patchwork of infra-as-code scripts, scheduled jobs, cron entries, chat-bot triggers, and ad-hoc shell SOPs. Authored once (preferably by a smart model) and executed many times by the cheaper runtime tier.

User-facing label is **Routine**; backend identifiers (`pipelines` table, `internal/pipeline` package, `/api/v1/.../pipelines/...` HTTP routes) remain unchanged for backwards compat. Three-layer architecture: **Routine** (atomic) → **Recipe** (Marketplace template, future) → **Cyclic Issue** (recurring user issue, future).

#### Frontend

- **New `/routines` page** as a clone of `/orchestration`. 3-column layout: filter sidebar with saved-view facets (status / usage / authored-by / show ephemeral), 4 main tabs (Routines list / Graph / Timeline / Activity), right detail panel with 7 sub-tabs (Overview / Editor / Runs / Versions / Schedules / Webhooks / Waitpoints).
- **Sidebar entry** *Routines* under *Work* (icon `ScrollText`).
- **Orchestration tab** *Routines* — 5th tab in `/orchestration` for in-context discovery, reusing the existing detail sheet so users don't lose mission context.
- **DSL editor dialog** — paste/edit JSON with 3 starter templates. **Test & Save** runs `/test_run` first; on pass calls `/save`. Skip-test-gate checkbox surfaces only for OWNER/ADMIN roles.
- **Run / Test Run / Dry Run / Cancel** action toolbar.
- **Live waterfall** — Runs sub-tab subscribes to `pipeline.step.*` WebSocket events; auto-expands the most recent run on first visit.

#### Backend

Five database migrations: v78 (`pipelines` + `workspaces.execution_tiers_json`), v79 (`pipeline_versions` + `pipeline_waitpoints`), v80 (`pipeline_schedules`), v81 (`pipeline_run_idempotency`), v82 (`pipeline_webhooks`).

- **6 step types**: `agent_run`, `call_pipeline`, `http`, `code`, `wait`, `transform`.
- **DAG with `needs[]`** — independent steps execute in parallel; leaf-node final-output preference for multi-leaf graphs.
- **Conditional `if`** — any step can carry a template-rendered boolean; false → step skipped.
- **Two-tier execution** — workspace `execution_tiers_json` resolves `complexity` annotation to `(adapter, model)`; tier override flows through to the CLI adapter's `--model` flag.
- **Versioning + rollback** — every save creates a new immutable version; rollback creates a new HEAD pointing at the target's definition.
- **HITL waitpoints** — DB-backed approval primitive with timeout sweeper and boot-time recovery scan reporting stranded entries.
- **Cron schedules** + **HMAC-signed webhooks** + **idempotency keys** for safe redelivery.
- **Bundle export/import** for cross-workspace transfer.
- **Workspace-scoped `POST /api/v1/workspaces/{ws}/pipelines/save`** for UI authoring (MANAGER+ role); `skip_test_gate` flag honoured only for OWNER/ADMIN.
- **8 stability bug fixes** with regression tests under `-race`: DAG completion bookkeeping, multi-leaf output picker, waitpoint lost-wakeup, webhook rate-limiter race, idempotency stale-row leak, SSRF-via-redirect, cross-workspace agent execution, template validation breadth, exponential-backoff jitter.

#### CLI (17 routine subcommands)

| Group | Commands |
|-------|----------|
| Core | `list`, `get`, `save`, `run`, `dry-run`, `delete`, `runs` |
| Versions | `versions`, `rollback --to N` |
| Bundles | `export [--include-history]`, `import [file.json]` |
| Runs | `cancel`, `watch [--json] [--once]` |
| Authoring | `validate [file.json]` (offline DSL check, CI-friendly) |
| Schedules | `list`, `create`, `update`, `enable`, `disable`, `now`, `delete` |
| Webhooks | `list`, `create`, `url`, `delete` |
| Waitpoints | `list`, `show`, `approve`, `reject` |

The `pipeline` alias is preserved — every `crewship routine X` invocation also works as `crewship pipeline X`.

#### Documentation

- `docs/guides/routines.mdx` — user guide (concepts, three authoring paths, DSL anatomy, all step types, two-tier execution, triggers, HITL, validation gates, observability, RBAC, troubleshooting).
- `docs/cli/routine.mdx` — per-subcommand reference.

#### Seeded routines

`./dev.sh seed` now populates 5 starter routines on a fresh workspace: `summarize-text`, `fetch-and-summarize`, `pr-review-structured`, `daily-status-digest`, `incident-triage`. Each is independently runnable with default inputs.

### Added — Core platform

- Self-hosted runtime: single Go binary with embedded Next.js UI, embedded
  SQLite DB, and a sidecar proxy for credential injection.
- Crew Journal — append-only event stream as canonical source of truth
  for every observable action; FTS5 search; SSE streaming to the
  `/journal` UI.
- Paymaster — hierarchical LLM cost budgets (workspace → crew → mission →
  agent), per-call ledger written before the LLM request leaves the box.
- Lookout — guardrails: prompt-injection detection, JSON-schema tool-arg
  validation, output parsing, secrets redaction.
- Harbormaster — human-in-the-loop approval queue with sync and async
  modes, configurable timeouts, full decision history.
- Cartographer — checkpoint/fork/restore over journal cursor; non-
  destructive restore returns divergence warnings instead of mutating.
- Quartermaster — eval suite with trajectory replay, regression detection,
  and an LLM-as-judge that uses rubric-shuffle anti-bias.
- Hooks framework — 15 lifecycle event types with shell, HTTP, and
  subagent handlers; `allowedShell=true` required at register time.
- Backup — AGE-encrypted, portable `.tar.zst` bundles at workspace and
  crew scope; retention rotation; advisory locking; forward-compatible
  manifest.
- Keeper — credential gatekeeping with AES-256-GCM versioned encryption
  and an Ollama-backed LLM evaluating per-request access.
- Multi-runtime container support — auto-detection of Docker, Podman,
  Colima, OrbStack, Rancher, nerdctl. Apple Containers on macOS Tahoe+.
- CLI adapters — Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor
  CLI, Factory Droid, all wired into the orchestrator dispatch table.
- Crew templates — Engineering, Quality, DevOps, and Research crews seed
  ready out of the box; `crewship template apply <slug>` to deploy.
- Issue tracker — Linear-style with labels, projects, sub-issues, and
  bulk operations; `crewship issue …` CLI.
- Multi-workspace support; OWNER/ADMIN/MANAGER/MEMBER/VIEWER server-side
  RBAC enforcement (UI for tier assignment ships in v0.2).
- OpenTelemetry GenAI spans with W3C trace-context propagation; OTLP HTTP
  exporter; every journal entry carries `trace_id`/`span_id`.
- Devcontainer provisioning with mise-managed runtimes, shared cache
  images, and 24-hour registry-digest checks.
- Per-IP rate limiting (10 req/min on auth endpoints, 120 req/min on the
  general API), security headers, single-use OAuth state with 15 min
  expiry.
- Goreleaser pipeline: cross-compiled binaries (Mac amd64+arm64, Linux
  amd64+arm64, Windows amd64), keyless cosign signatures, SPDX +
  CycloneDX SBOMs, Homebrew tap auto-publish.

### Security — Pentest 2026-05-14 hardening pass

Internal pentest of `dev2` (`dev-server:8082`, build `a78e8ac`)
produced 11 findings across 7 surfaces. All fixes have PoCs that
confirm the bypass before and the block after (reports gitignored
under `.pentest-2026-05-14/`).

- **F-001 (HIGH):** SSRF in skills import via DNS-resolved hostname
  bypass — blocked.
- **F-002 (MEDIUM):** SSRF error messages leaked internal network
  state — generic error masking.
- **F-003 (MEDIUM):** `/metrics` exposed without auth — now gated.
- **F-004 (LOW):** Next.js SPA fallback masked 404 for sensitive paths.
- **F-005 (INFO):** Inconsistent path-traversal validation — unified.
- **F-006 (MEDIUM):** No backend Origin check on state-changing routes.
- **F-007 (HIGH):** Rate limiter bypassable via X-Forwarded-For
  rotation — IP resolution hardened.
- **F-009 (LOW):** Scrubber regex bypassable via zero-width characters.
- **F-011 (HIGH conditional):** Devcontainer features could request
  `Privileged` / dangerous `CapAdd` — denylist applied.
- **F-012 (MEDIUM):** `CREWSHIP_DISABLE_RATELIMIT=true` shipped in dev
  `.env.local`.
- **F-A1/A3/A4 (HIGH):** Workspace-IDOR on relations + parent_issue_id
  — workspace-scope enforcement.
- **F-B4 (LOW):** Capability-URL proxy leaked `Referer` to upstream.
- **G-002:** Memory injection guard hardening.

### Security — Pass-2 quickfixes

Four backlog items bundled (each <70 LOC, independently revertible):

- Sidecar credential reads now emit audit events.
- Emoji reactions XSS — payload validation tightened
  (`emoji_reaction_test.go` covers 24 cases including real XSS strings).
- `/admin/backups/metrics` redacted to drop cross-owner workspace IDs.
- WebSocket frames capped at 1 MiB; fan-out N-amplifier closed.

### Security — Supply chain

- All release artifacts signed with cosign keyless via GitHub Actions
  OIDC (SLSA-3-ish provenance chain). Verify with
  `cosign verify-blob --certificate-identity-regexp ...`.
- SBOMs in SPDX and CycloneDX shipped with every release.
- `migration-lint` CI gate prevents the rebase-collision class of
  schema-divergence bug that bricks customer DB on upgrade.
- Goreleaser builds are reproducible (`-trimpath`, fixed `GOFLAGS`).
- `gitleaks` + `govulncheck` + `grype` run on every PR via
  `.github/workflows/security.yml`.

### Changed

- **README** rewritten for honest beta status — every feature labeled
  ✅ stable / 🟡 early / 🚧 WIP. Adapter scaffolds for non-Anthropic
  CLIs explicitly marked WIP rather than equal-billing alongside the
  production-tested Claude Code adapter.
- **Distribution channels** documented in `RELEASING.md` — stable /
  beta / nightly with their respective Docker tag policies. `:latest`
  Docker tag only moves on clean semver tags; pre-releases NEVER
  overwrite `:latest`.
- **Hotfix workflow** documented in `RELEASING.md`: cherry-pick onto
  release branch, fix-forward (never untag), forward-port to `main`.

### Removed — Repo hygiene (PR #344, #348)

- `.claude/context/prd/*` and `.claude/context/wireframes/*` —
  ~52 000 lines of pre-implementation design docs untracked.
  Mintlify (`docs/`) is now the canonical user-facing docs source.
- `internal-docs/audit-archive/*`, `internal-docs/wireframes/*` —
  archived audit reports and HTML wireframes.
- `mockups/activity-rail-v{2,3}.html` — wireframes for the
  activity-rail feature shipped in #287.

### v0.2 roadmap

The following ship as packages but are not yet auto-wired into the
runtime in v0.1; they activate via manual API calls today and become
default behaviour in v0.2:

- Episodic memory — vector recall over the journal (selective embedding,
  SQLite BLOB cosine).
- Consolidate — daily Consolidator that extracts learned rules into
  crew memory + Compactor that rolls up low-signal old entries.

The following are planned for v0.2 but not in v0.1 at all:

- PostgreSQL primary database (SQLite is the only supported backend in
  v0.1).
- Kubernetes container provider.
- Skills marketplace (local skill imports work today).
- Workspace-scope memory tier (3-tier today: agent, crew, session).
- Stripe-backed billing tiers / edition gating (v0.1 ships fully
  Apache-2.0 with no edition gating).
- UI for assigning ADMIN/MANAGER/VIEWER workspace roles (server-side
  enforcement is already wired).
- Crew-to-crew handoff with critique exchange.

### Notes

- This is the first tagged release. Public APIs and data models may
  still change in `0.x` minor versions before `1.0`. Pin a commit SHA or
  a specific `v0.x.y` tag if you ship to production.
- The `release` branch tracks deployable state (a 5-minute systemd timer
  on the dogfood prod VM polls it). Push `main:release` to deploy.

[Unreleased]: https://github.com/crewship-ai/crewship/compare/v0.1.0-beta.2...HEAD
[0.1.0-beta.2]: https://github.com/crewship-ai/crewship/releases/tag/v0.1.0-beta.2
