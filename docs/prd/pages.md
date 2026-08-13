# PRD — Pages

Status: draft · 2026-08-12 · **Release 1.0 scope** (owner decision, 2026-08-12) · not yet implemented

> **The requirement being specified.** *"Every user, and every agent acting for a user, can
> compose a page out of panels. A panel's data is produced by a routine or by a script running
> inside a crew container. Panels carry per-crew ownership, so one page renders differently for
> different viewers. Pages reach the rest of Crewship — issues, inbox, routines, journal,
> notifications, integrations — and stay lightweight enough to scale."*
>
> Repo `file:line` references verified against `main` @ `0f8a24ff` on 2026-08-12. External
> claims carry a source. Re-verify both before implementing.

---

## 0. Verdict

**Build it, but not as a dashboard product.** The charts are the least defensible part of this
idea. Two things in it are defensible, and the design should be organised around them:

1. **The panel is a sensor, not a display.** A cheap script pushes a typed payload; a threshold
   on that payload wakes an agent, which writes its analysis back onto the same page. Nobody in
   the surveyed market ships this. It is also the only part that pays for itself in tokens.
2. **Permissions are per panel, not per page.** Grafana, Looker, Tableau, Power BI, Metabase and
   Superset all stop access control at the dashboard boundary — verified, not assumed (§2.3).
   A page that renders a different set of panels per viewer is a genuine gap in the category.

**And one correction to the record.** An earlier draft of this argument claimed the push model —
"the page has no datasource; a script pushes a ready payload" — was unusual. That was wrong. It
is a known sub-genre with a name, a lineage, and a documented reason it lost (§2.1). The design
must answer that reason explicitly or it will fail the same way. §5 is that answer.

---

## 1. What a Page is

A **Page** is a workspace-scoped, slug-addressable record holding an ordered list of **panels**.
A panel declares:

- a **schema** from a closed set of five (§3),
- an **owner** — a crew (this is the permission anchor, §7),
- a **producer** — the routine or script permitted to write its data,
- a **freshness contract** — how often data is expected, and what happens when it stops (§4).

A page holds **no query, no datasource, no connection string, and no credentials.** It cannot
reach a database. It renders the last payload a producer pushed, plus the metadata Crewship
attached to that push.

This is the load-bearing property. Everything a client might want on a page — a Postgres row
count, a Redis queue depth, a Google Drive month-end close, a calendar, an uptime check — is
reachable because the producer already runs next to that data inside a crew container with
credentials the page never sees. Adding a data source to Pages is a scripting job, not a
connector engineering job.

**Name.** One noun everywhere: `kind: Page`, route `/pages`, CLI `crewship page`, table `pages`.
The design artefact used three ("Sails" in the rail, `kind: Surface`, `crewship surface set`);
that must not survive into the implementation.

---

## 2. What the market actually does

Six research passes. The findings that changed the design are here; the rest is in §16.

### 2.1 Push-to-panel is not novel — it is a genre that lost

Every observability product surveyed keeps the query as the load-bearing abstraction. Push exists
everywhere, but always lands at **metric or event granularity into a store**, and the panel still
queries that store at render time:

| Product | Push mechanism | Granularity |
|---|---|---|
| Grafana | Live push (`/api/live/push/:streamId`, InfluxDB line protocol only), Pushgateway | metric / channel |
| Datadog | `POST /api/v1/check_run`, custom metrics, `POST /api/v2/series` | check / metric |
| New Relic | Event API; NerdGraph `dashboardUpdateWidgetsInPage` | event; **whole widget** |
| Elastic / Kibana | index a doc, then Canvas/Lens queries it; Vega can fetch a URL | document |
| Splunk / Honeycomb / SigNoz | HEC / Events API / OTel | event |
| VictoriaMetrics | `remote_write`, **with Prometheus staleness markers** | metric |

The closest single exception is New Relic's **markdown widget + `dashboardUpdateWidgetsInPage`**:
a named widget, no query, content pushed in. But it is text only and it is a GraphQL mutation.

The exact pattern — *CLI pushes a typed JSON blob keyed by widget id, dashboard holds zero
connection state* — is **Dashing / Smashing** (Shopify, ~2013, `POST /widgets/{id}` with
`{"auth_token": …, "value": …}`), and, still maintained and commercial, **Geckoboard's Push
Datasets API** (declare a typed schema once via `PUT /datasets/:id`, then push rows; the widget
has no connection of its own; limits 5 000 rows/dataset, 500 rows/request, 60 req/min).

**Why it lost.** A push-only panel holds one snapshot. It therefore cannot change its own time
range, cannot alert on history, and cannot correlate with another panel — the sender has to
re-implement all three. Grafana's model does those three things for free. Dashing is effectively
dead; that is evidence, not an accident.

**Implication for this design:** §5 must state where history, alerting and correlation live, and
they must not live in the panel.

### 2.2 The format question is already answered — by the repo and by the market

**Perses** (CNCF sandbox, 2024) made exactly this bet: dashboards as `apiVersion` / `kind` /
`metadata` / `spec` YAML, validated by CUE schemas per plugin kind, with optional Go and CUE
builder SDKs on top. Grafana is moving the same way from the other direction — Schema v2 models
dashboard elements as Kubernetes kinds, and the official Foundation SDK exists because Grafana's
own raw JSON is not something anyone wants to hand-author. Rill, Lightdash and Cube.js are YAML.
Metabase and Superset emit YAML on export specifically because it version-controls better. All
four home-lab dashboards (homepage, Glance, Dashy, Homer) converged on flat YAML independently.

Nobody defends hand-written dashboard JSON as an authoring format.

The recurring complaint about the Kubernetes envelope is boilerplate — which is why every
ecosystem that adopts it eventually adds a generator (Helm, Tanka, Grafonnet, Foundation SDK,
Perses' CUE SDK). Worth knowing; not a v1 problem.

**Crewship already has this system.** `internal/manifest` parses `apiVersion: crewship/v1`
(`internal/manifest/schema.go:33`) across 20 kinds (`schema.go:45-66`), with per-kind
Validate/Plan/Export and a dependency rank table. A Page is the 21st kind, not a new framework.

### 2.3 Per-panel permissions are rare, and the two ways to build them are not equal

Verified absent in Grafana (community moderator, verbatim: *"I don't believe you can lock down
access on a per-panel basis. Only at the dashboard level"*), Looker, Tableau, Power BI, Metabase
and Superset. All six deliberately stop at the dashboard and push finer granularity down into the
**data** layer (row/column security) rather than the **rendering** layer.

Two products do render per-viewer panel sets, by opposite mechanisms:

- **Salesforce Lightning App Builder — Visibility Rules.** Each component carries a boolean
  condition (user field, Custom Permission, profile, record field). The most mature example.
  Notably it still cannot condition on Permission Sets — a long-open request — which is what
  happens when visibility rules are an expression language instead of an ACL.
- **Directus Insights.** No panel-ACL UI at all: panels are rows in `directus_panels`, and the
  ordinary collection-permission system is applied to *that table*. Permission logic stays in one
  place and stays auditable.

Retool, Appsmith and Budibase achieve per-component hiding only by writing
`{{ current_user.groups.includes(…) }}` into each component's `Hidden` property. That is cheap to
build and it is the documented road to permission sprawl — rules scattered across component
properties, discovered at audit time.

**Implication:** take the Directus shape. A panel's visibility is a property of the panel row
(`owner_crew_id`) resolved by the ordinary membership check, never an expression a page author
writes.

One failure mode to design against explicitly: Grafana's most common permission complaint is a
dashboard that *opens* but whose panels fail inside it, because dashboard permission and data
source permission are independent layers. A panel the viewer cannot see must never render as an
error.

**But silent reflow is probably also wrong.** Six mature BI products independently chose to stop
at the dashboard boundary, and the research did not surface their reason. The likeliest one is a
usability argument, not a technical one: **a page whose content differs per viewer is hard to
talk about.** "Look at the panel in the top right" fails; a screenshot in a ticket misleads;
two people reading "the same" page are not reading the same page. That cost is real and it is why
the category avoids this.

Design answer: **hidden, not invisible.** A panel the viewer may not see leaves a sealed
placeholder in its grid slot — "Hidden · crew Účetní" — so the page has the same shape for
everyone and the difference is legible. Leaking the *existence* of a panel and its owning crew is
a much smaller disclosure than the confusion of a silently different page. Where even existence is
sensitive, the page author can move the panel to a separate page.

### 2.4 Agent-authored UI: the constrained-schema principle, and two real incidents

**Adaptive Cards** state the principle this design needs, verbatim: *"There is no 'code behind'
with Adaptive Cards… Card authors cannot embed custom/arbitrary code with their payloads, and as
a result an Adaptive Card host never needs to run third party code."* Plus: *"Card authors own
the content, host apps own the look and feel."* Slack Block Kit is the same bet. OpenAI's Apps
SDK goes further — the widget code is developer-authored ahead of time and the model supplies
only data.

Two incidents define the hard limits:

- **CamoLeak / CVE-2025-59145** (CVSS 9.6, Oct 2025). A hidden injection in a GitHub PR comment
  made Copilot Chat find secrets in a private repo, encode them, and exfiltrate them one
  character at a time through markdown **image** URLs proxied by GitHub's own trusted Camo
  image proxy — which is exactly why CSP did not stop it. Fixed by disabling image rendering.
- **Slack AI** (Aug 2024). An attacker posting only in a public channel got Slack AI to render a
  link that, when a victim clicked it, exfiltrated private-channel content via the query string.

Both are the same shape as a Pages narrative panel: untrusted data → agent → rendered content a
different human trusts. Rules in §8.

The closest existing product to "agent creates a persistent, shared, permissioned data page" is
**Databricks AI/BI Genie**, which re-runs every query under the *viewer's* Unity Catalog identity
rather than the author's. The combination this PRD describes — agent-authored, persistent,
per-panel permissioned, with narrative plus actions — does not exist in one product today.

### 2.4b The pattern has a name, and the prior art says "stay closed"

What this PRD describes is **server-driven UI** (SDUI): the server sends a validated spec plus
data, the client renders it from a closed component vocabulary. Using the established name
connects the design to real prior art. Two data points, in tension, and both matter:

- **Airbnb's Ghost Platform** — a closed core set of section types plus a declared extension
  mechanism. Structurally the same shape as this PRD.
- **Spotify's HubFramework** — deprecated January 2019. The published postmortem is the sharpest
  argument for the closed set: it *"went fully generic too early: a small set of primitive
  components that could be composed into anything. Maximum flexibility, minimum readability."*

There is no open standard schema for web SDUI to adopt. DivKit (Yandex, Apache-2.0) is the only
mature open-source SDUI renderer, but it is a general layout engine (`DivContainer`, `DivText`,
composed arbitrarily) — the exact generality Spotify's postmortem warns against.

### 2.4c Adopt vs build — checked, and closer than assumed

Every embeddable renderer was evaluated as a dependency, not as a product:

| Candidate | Blocker |
|---|---|
| **Perses React packages** (`@perses-dev/*`, Apache-2.0, `0.55.0-beta.1`) | The only real candidate. Embeds without a server (working reference app, static plugin imports, no module federation). But peer deps declare `react: ^17 \|\| ^18` — **not 19**; forces `@mui/material ^6.1.10` + Emotion against our Tailwind v4 + shadcn; 22 direct deps including ECharts, CodeMirror and drag-and-drop built for a dashboard *editor*, not a 5-type *reader*; open CSS-conflict issue `perses/perses#894` |
| **Grafana Scenes** (`@grafana/scenes@8.13.6`) | Not standalone. Peers on `@grafana/ui` (11.5 MB unpacked), `@grafana/data`, `@grafana/runtime`; the Grafana team state it "relies on the Grafana runtime" and needs a Grafana backend to query and a plugin host to load from. A hosted product wearing a library's name. |
| **Adaptive Cards React** (`adaptivecards-react@1.1.1`) | Last published 2022-09-20. Theming is a `HostConfig` JSON object, not our tokens; the aesthetic is Teams cards. |
| **JSONForms / RJSF / uniforms** | Write renderers. No display primitives — no metric tile, no status pill, no sparkline. Only JSONForms declares React 19. |
| **Superset / Metabase embedding SDKs** | Both mount an iframe against a live backend and fetch the bundle from it. Incompatible with static export; Metabase's is Pro-gated. |
| **Tremor** (`@tremor/react@3.18.7`) | React 18 peer; v4 beta un-promoted since Dec 2024; acquired by Vercel, roadmap unclear. Worth raiding for component ideas, not importing. |

**Verdict: build.** Nothing on offer is simultaneously a real library (not a hosted product),
React-19-clean, Tailwind-native, and scoped to a closed display vocabulary. The gap between what
exists and what is specified is exactly the size of the feature — the signature of a case where
adopting costs more than it saves. Honest caveat: on React 18 with an MUI-based frontend,
embedding Perses would be a defensible call. It is our stack, not Perses, that decides this.

### 2.5 Rendering costs nothing new

Measured: recharts 3.10.1 is 151 KB gzip; `motion` is 45 KB gzip; react-grid-layout is 22.8 KB
gzip. All three are avoidable here. Layout is declared in YAML, not dragged — and dashboards do
not drag on phones anywhere, they reflow to one column. Panels that are numbers, pills, tables
and text need no chart engine; hand-written SVG is established practice for exactly this class.

There is also a static-export argument: `output: "export"` means recharts' `ResponsiveContainer`
measures with a client-side `ResizeObserver`, so a chart panel is blank until hydration. Hand SVG
paints in the initial HTML.

**Budget: ~0 KB of new dependency weight** (§9).

---

## 3. The vocabulary — five panel schemas, closed

The recurring panel taxonomy across Grafana, Perses, Rill, Cube, Evidence, Observable and the
four home-lab tools reduces to: stat, time-series, table, text, status/link, inside a layout
container. Pages takes exactly five, and the set is **closed** — a new panel kind is a server
release, never a user-supplied string.

| Schema | Payload core | Renders |
|---|---|---|
| `metric.v1` | `{value, unit?, delta?, target?, sparkline?[]}` | one number, delta, optional target and sparkline |
| `series.v1` | `{unit, labels[], series[{name, values[]}]}` | bar (v1), line/area (v1.1); **one unit per panel** |
| `status.v1` | `{items[{name, state: ok\|warning\|critical, label}]}` | status grid; state carries glyph + text, never colour alone |
| `table.v1` | `{columns[{key,label,align?}], rows[]}` | table; collapses to a card list in a narrow container |
| `narrative.v1` | `{blocks[{kind: paragraph\|list, text}], verdict?, actions?[]}` | prose + declared action buttons (§8) |

Rules carried from the palette analysis in the design artefact, and confirmed against
`app/globals.css`:

- **Max 5 series**, sixth merges into "other". Colour belongs to the entity, not to the ordinal —
  a filter that removes a series must not recolour the rest.
- **One unit per panel.** Two units is two panels. Dual axes are the most common chart error and
  cannot be defended at render time.
- **Status colours are reserved.** Green "running" must never also mean "series 3".
- Legend always; direct labels at ≤ 4 series.

### 3.1 The escape hatch — one sandboxed `embed` panel, and no plugin system

A closed set of five cannot "build anything", and the ambition behind this feature is that it
should. That tension has to be resolved in the design, not left to be discovered.

**Do not build a plugin system.** Grafana's panel-plugin catalog holds **130 plugins** after 15+
years of an open ecosystem actively cultivated by a company monetising it, behind a mandatory
signing pipeline. Perses' technically cleaner runtime-pluggable system (CUE schema + module
federation, no backend rebuild) has produced **~20** panel types. For a small team that is a
multi-month toolchain investment against a catalogue that may never reach double digits.

**Do add exactly one `embed` panel**, rendered in a cross-origin sandboxed iframe. Evidence:

- **Embed-by-URL is the single most universally shipped escape hatch.** Datadog has a dedicated
  iframe widget; Glance ships `iframe` and `html` widgets; Dashy ships iframe/embed/API-response
  widgets; Notion embeds via a vetted 1 900-domain allow-list. Grafana killed its Angular HTML
  panel in the v10 deprecation and the community immediately built unofficial iframe panels to
  route around it — **demand did not go away, it went unsupervised.** That is the failure mode of
  shipping no hatch at all: users build one outside the security model, and we lose the choice of
  how it is built.
- **The technique is now standardised, not research.** MCP Apps (spec `2026-01-26`) and the OpenAI
  Apps SDK converged on the same shape: host ↔ cross-origin sandbox-proxy iframe ↔ inner view
  iframe, with a per-resource CSP (`connectDomains`, `resourceDomains`, `frameDomains` defaulting
  to `frame-src 'none'`), privileged APIs blocked, and all view→host traffic as auditable JSON-RPC
  rather than free-form `postMessage`. Theming is a small CSS-variable contract; sizing is
  negotiated, not free.
- **It absorbs the whole demand tail in one panel type** — iframe, maps, calendar, video, images,
  custom HTML — which the ranked demand evidence shows is otherwise a queue of one-off requests.

Costs, stated plainly: a second origin to host, a proxy to maintain, and the panel's contents are
outside our design system — an `embed` panel is as beautiful as whatever was put in it. It is
also the only reversible choice. Adding it later is additive (one panel type); building a plugin
system first and finding nobody uses it is not.

**Recommended placement: v1.2, not v1** — but the schema must reserve the type name from the first
migration so the closed enum does not need a breaking change to admit it.

**Prerequisite bug, independent of Pages.** `--chart-1..5` are defined **identically** in `:root`
and `.dark` (`app/globals.css:45-49` and `:173-177`). One of the two themes is therefore using a
palette tuned for the other background. The design artefact's separate finding — blue↔purple are
indistinguishable under protanopia, and green↔cyan fall below the ΔE threshold even for normal
vision — is consistent with that. Fix in its own PR before Pages ships a chart.

---

## 4. The freshness contract

A dashboard that silently shows old numbers is worse than no dashboard. This is where push
models fail quietly (Prometheus Pushgateway keeps a pushed metric forever unless the job deletes
it), and where the push-native monitors get it right (Uptime Kuma, Healthchecks.io, Gatus and
Better Stack all flip state when an expected ping does not arrive).

Pages takes the monitor behaviour, not the Pushgateway behaviour:

1. **Every panel declares `sla`.** A panel without one does not validate. There is no default
   that means "never mind".
2. **Three states, computed server-side, never by the producer**: `fresh` (within SLA), `stale`
   (past SLA), `failed` (producer's last run failed, or an explicit failure push).
3. **Stale renders degraded** — value dimmed, age shown in absolute terms, not "a while ago".
   It never renders as if it were current.

**Three rules the implementation had to pin because §4 left them open** (2026-08-12):

- **The boundary is `age >= sla` → stale.** Tested at exactly the boundary and one nanosecond
  before it.
- **`fresh` and `stale` are never stored.** They are functions of the clock, so
  `page_panel_data.state` holds only the producer's own verdict — `CHECK (state IN ('ok','failed'))`
  — and there is deliberately no column a producer could write a timestamp into. Freshness is
  computed from the server's `produced_at` on every read.
- **The newest payload always survives the age cut.** Without this, a producer dead for eight days
  has nothing inside the seven-day window, the sweep deletes its last payload, and the panel flips
  from *"stale, last value 12:40"* to *"never produced"* — the system forgets a failure instead of
  reporting it, which is the exact dishonesty this section exists to prevent.
4. **`on_failure: {issue: crew/<slug>}`** opens an issue on the owning crew. A page that quietly
   stops updating must generate work for a human.
5. Every panel footer carries provenance: producer, run id, timestamp. Server-attached, not
   producer-claimed.

---

## 5. Where history, alerting and correlation live

This is the section that answers §2.1 — the reason the Dashing genre lost.

**They do not live in the panel.** They live where Crewship already keeps them:

| Concern | Grafana's answer | Pages' answer |
|---|---|---|
| History | query engine over a TSDB | `internal/journal` — every push emits an entry; the journal is the queryable record |
| Alerting | alert rules over queries | `internal/automation` — journal event + matcher → routine |
| Correlation | multi-panel query joins | `internal/chain` — the causal walk added by #1836 |
| Ad-hoc time range | re-query with new range | **not supported, by design** |

The last row is a real limitation and must be stated in the docs, not discovered by a user.
Pages answers *"what is happening now, and what did the system do about it"*. It does not answer
*"show me last March"*. When somebody needs last March, the honest answer is a routine that
queries and pushes a `series.v1` panel — and that routine is manifest-managed, journaled and
reviewable, which is more than a Grafana panel's query string ever is.

A bounded ring of previous payloads is retained per panel — **200 payloads with a hard age cut at
7 days, whichever comes first** (§10b.3; an earlier draft of this paragraph said 24 and was never
corrected when §10b.3 settled on 200). Enough for a sparkline and for "what did this look like
before it broke", not enough to be a time-series database. Retention follows the
`run_retention_days` convention
(`internal/database/migrate_consts_v158_run_retention_days.go:13`): nullable INTEGER on
`workspaces`, NULL = instance default.

**The wake gate is the payoff.** A panel declares:

```yaml
wake:
  - when: any(state == "critical") for: 5m
    agent: crew/devops
    writes: incident
```

Under the covers this compiles to an `automations` row — the substrate delivered by #1836 already
does journal event → in-memory matcher → coalesced enqueue, with `debounce_seconds` (default 10),
`max_per_hour` (default 60) and depth pricing against loops
(`internal/database/migrations/20260807160000_automations.sql:31-35`). Pages does not build a
new eventing path; it emits `page.panel.*` journal entries and writes automation rules.

Because unknown journal types are forwarded by design
(`internal/journal/feed_filter.go:33-35`), `page.*` entries reach the activity feed with no
change to the filter.

---

## 6. Format — two layers, both already in the repo

**Layer 1 — the page definition, authored by a human, YAML.**

⚠ The example below is written **at the v1.1 feature level** — it shows `wake:` and `actions:`,
which land in v1.1 (§12). The v1 parser sets `KnownFields(true)` and therefore rejects them today,
by design: a spec that names a field the server does not implement must fail loudly rather than
be silently ignored. Read the example as where the format is going; read §12 for what parses now.

```yaml
apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila .201
  slug: fleet-201
spec:
  panels:
    - id: sluzby
      schema: status.v1
      title: Jede to?
      owner: crew/lookout            # permission anchor, not a label
      producer: script/watch-services.sh
      sla: 30s
      span: 8
      wake:
        - when: any(state == "critical")
          agent: crew/devops
          writes: incident
    - id: incident
      schema: narrative.v1
      owner: crew/devops
      producer: routine/incident-rozbor
      refresh: on:wake
      sla: 1h
      span: 12
      actions:                        # declared here, never authored by the agent
        - run_routine: restart-api
        - create_issue: {project: infra}
```

**Layer 2 — the panel payload, produced by a machine, JSON**, validated against a published
schema in the existing `schemas/` directory alongside `schemas/routine.v1.json` (which already
carries `dsl_version` and is embedded via `schemas/embed.go`).

Human writes YAML, machine writes JSON, both validated. No third DSL. This matches Perses'
architecture and reuses two systems that already exist here.

---

## 7. Permission model

The requirement: creator owns the page; a page created by an agent is owned by the human who
authorised the agent; crew members inherit visibility of their crew's panels; the owner can grant
read and write to others.

### 7.1 The rules

1. **A page has exactly one owner, and it is either a user or a crew.** `owner_user_id` xor
   `owner_crew_id`. An agent-created page records the authorising human as owner and the agent as
   `created_by_agent_id`; an agent never owns permissions, it acts under one. A crew-owned page is
   the natural home for a crew's own status board and needs no personal owner at all.
1b. **When a user owner leaves the workspace, the page transfers to a crew, it is not deleted.**
   Target crew, in order: the crew that owns the most panels on that page, else the crew the
   departing user belonged to. The transfer emits a journal entry and notifies workspace
   ADMIN/OWNER through the existing notification path, so a role that does not leave can reassign
   the page. A page must never be orphaned and must never silently vanish.

   ⚠ **An earlier draft ended that list with "else none", which is the orphan the same rule
   forbids.** Corrected 2026-08-12 during implementation. The database enforces it rather than
   trusting the handler: `pages.owner_user_id` carries `ON DELETE RESTRICT`, so **transferring the
   page is a precondition of deleting the user**, not a cleanup step that might not run.

   **This lands on a path outside Pages:** the user-erasure handler in `internal/api` must
   transfer or delete a departing user's pages before deleting the row, or the delete now fails.
   That is deliberate — a silent cascade is how a page would vanish — but it is work the erasure
   path owns, and a GDPR deletion that errors out is worse than one that transfers first. Track it
   as its own item.
2. **A panel's visibility is its owning crew's visibility.** `panel.owner_crew_id` is not
   decoration; it is the ACL. If the viewer is not a member of that crew (and holds no explicit
   grant), **the panel's payload is omitted server-side, before serialisation, and what is
   serialised in its place is a sealed placeholder** — the panel id, its grid slot and its owning
   crew's display name, and nothing else. It is never rendered as an error.

   ⚠ **An earlier draft of this rule said "the grid reflows", which contradicts §2.3.** Corrected
   2026-08-12 after a conformance audit caught it. §2.3 argues the case at length and wins: a page
   that silently changes shape per viewer cannot be talked about, and "look at the panel top right"
   stops meaning anything. Reflow is wrong; the placeholder holds the slot. Leaking the existence
   of a panel and its owning crew is a far smaller disclosure than the confusion of a silently
   different page — and where even existence is sensitive, the author moves the panel to its own
   page.
3. **Explicit grants layer on top**, in one table, not in panel properties:
   `page_grants(page_id, subject_type ∈ {user, crew}, subject_id, level ∈ {read, write})`.
   `write` means editing the page definition. Grants are issued by the page owner or by a
   workspace ADMIN/OWNER — that is the "who may look at this page" control.
   **A grant widens access to the page, never to a crew's data.** A grantee still sees only the
   panels their own crew membership and workspace role already permit; a page owner cannot use a
   grant to leak their crew's panel to somebody outside it. (An ADMIN sees the crew panels anyway
   — `effectiveRole` takes the max of workspace and crew role,
   `internal/api/helpers.go:481-486` — so this rule costs an admin nothing and closes the
   privilege-escalation path for everyone else.)
4. **Producer authority is separate from viewer authority.** Only the declared producer may write
   a panel's payload. A crew member who can *see* a panel cannot *write* it.
5. **Everything is decided server-side.** The client receives only panels it may see. There is no
   client-side hiding, because a hidden-but-delivered panel is a data leak.

### 7.1b Agents as grant subjects — three verbs, not one

A page is worked on by more than one agent, and which agents those are is configuration, not
convention. That requires splitting what the first draft treated as a single "producer" field
into three independent verbs:

| Verb | Means | Typical holder |
|---|---|---|
| `read` | may see the page and its panels | humans, crews, an agent that summarises it |
| `produce` | may push payloads **into named panels** | the routine or script that owns those panels |
| `write` | may edit the page spec — add, remove and re-arrange panels | the agent in charge of the page; a human owner |

`page_grants.subject_type` therefore becomes `{user, crew, agent}` and `level` becomes
`{read, produce, write}`. `produce` additionally carries the panel ids it covers, so an agent
granted `produce` on one panel cannot overwrite another agent's panel on the same page.

**The invariant that makes this safe: an agent's authority is a subset of the authorising human's,
never a superset.** A grant to an agent is evaluated against the granting human's own rights at
use time, not at grant time — if that human loses access to a crew, every agent grant they issued
narrows with them. Without this rule, "grant my agent access" is a privilege-escalation primitive.

**Agent identity comes from the token, never from the request body.** The sidecar already
overwrites caller-supplied identity fields with the token-derived one
(`internal/sidecar/identity.go:26-39`, `internal/sidecar/pipelines.go:21-25`); a page write takes
the same path, so agent A cannot claim to be agent B.

**Every grant change is journalled** (`page.grant_added` / `page.grant_removed`, actor and
subject recorded) — an ACL nobody can audit is not a security control.

Three rules decided 2026-08-12:

1. **Only a human issues a grant.** An agent with `write` may rebuild the page freely but can
   never widen who reaches it — not even to an agent in its own crew. This closes the escalation
   path where an injected agent grows its own blast radius one grant at a time. When an agent
   needs another agent's help, it asks a human, and that request is a normal inbox item.
2. **Layout and data are separate authorities.** An agent with `write` may place a panel owned by
   a crew it cannot see. It does not receive that panel's data — the server filters it exactly as
   for any other viewer and the agent sees the sealed placeholder. This is what lets an agent
   assemble a cross-crew page for a team whose numbers it is not entitled to read, which is the
   orchestration role the feature exists for. `write` is authority over arrangement, never over
   content.
3. **An unauthorised push is a signal, not noise.** A `produce` attempt on a panel the caller does
   not hold returns **403**, writes a journal entry, and notifies the page owner. It is equally
   likely to be a misconfiguration or an injection, and both deserve a human's attention on the
   first occurrence rather than the hundredth.

CLI, complete and symmetric with the API:

```
crewship page grant  <slug> --agent <agent-slug> --level produce --panels sluzby,zatizeni
crewship page grant  <slug> --crew  <crew-slug>  --level read
crewship page grant  <slug> --user  <email>      --level write
crewship page revoke <slug> --agent <agent-slug>
crewship page grants <slug>
```

### 7.2 What this costs

This is greenfield in this repo, and the PRD should not pretend otherwise. Per-resource ACLs are
an explicit recorded non-goal of the current RBAC:
`internal/database/migrate_consts_v100_rbac_extensions.go:53-56` defers "full per-row ACL" to
Phase 3, and `migrate_consts_v109_member_capabilities.go:19-21` names per-resource ACLs as an
excluded non-goal. The only precedent is `saved_views(user_id, shared BOOLEAN)`
(`migrate_consts_v42_v45.go:71-85`, read filter at `internal/api/saved_view_handler.go:52`) —
an owner plus a workspace-wide boolean, not a grant list.

So Pages either (a) ships with the SavedView boolean and no grants, or (b) builds the first grant
table in the codebase. The user requirement asks for (b). Recommended: **build it, scoped to
Pages only** — one table, two subject types, two levels — and do not generalise it into a
platform ACL until a second consumer exists.

New capability `page.create` joins the closed set in `internal/api/capabilities.go:24-83`, with
the frontend mirror in `lib/capabilities.ts` (hand-maintained, `lib/capabilities.ts:1-19`).

---

## 7.3 Public pages — a page for someone with no account

Decided 2026-08-12: a page can be published to someone outside the workspace — the accountant, the
client, an external auditor — optionally behind a password. This is the highest-risk surface in
the feature and it gets its own rules.

### 7.3.1 It is a different product, not a permission level

A public page is served from a **separate URL space** (`/p/{token}`) that shares no session, no
cookie and no workspace context with the app. Nothing about it is "the same page with a looser
grant" — it is a distinct rendering path with its own middleware, and that separation is what
makes it auditable.

### 7.3.2 Six rules

1. **Read-only. No actions, ever.** A public page renders no buttons. A button behind a public
   link is remote code execution with a URL for a credential. `PageAction` is stripped server-side
   before serialisation, not hidden in CSS.
2. **Opt-in per panel, not per page.** Publishing a page publishes only the panels explicitly
   marked `public: true`. Default deny. Publishing must never be a bulk action over panels the
   author has not looked at.
3. **Only a human publishes.** An agent can build the page; it cannot make it public, and it
   cannot add a panel to an already-public page without that panel being separately marked by a
   human. This is §7.1b rule 1 taken to its conclusion — an agent may not widen reach, and
   "public" is the widest reach there is.
4. **Every public link expires.** A required expiry, default 30 days, maximum 1 year. Tokens are
   high-entropy, revocable individually, and one page may have several so revoking the
   accountant's link does not break the client's.
5. **Provenance is stripped by default.** Run ids, agent slugs, crew slugs and producer names are
   internal vocabulary. A public panel shows the value, the unit and the age — nothing that maps
   our org chart for a reader outside it. An author may opt provenance back in per page.
6. **Not indexable, rate limited, and logged.** `X-Robots-Tag: noindex`, `Referrer-Policy:
   no-referrer`, a per-token request cap, and a journal entry for each token's first view per day
   so the owner can see the link is being used and by roughly whom.

### 7.3.2b Staleness on a public page — show the age, never the reason

Decided 2026-08-12. The two halves pull in opposite directions and both are right:

- **Show the age.** An outsider acting on a stale number is the worst version of the failure this
  whole PRD exists to prevent — internally someone would have caught it, externally they will
  invoice on it. A public panel always carries when its data was produced.
- **Hide the reason.** Failure text is internal vocabulary: container names, routine slugs, OOM
  traces, crew names. A public failed panel reads *"Data nejsou aktuální — poslední hodnota
  12:40"* and nothing more. The detail stays on the internal page for the people who can act.

### 7.3.3 Password

Optional per token. Stored hashed with the same primitives the auth layer already uses — never
reversible, never in the URL. A wrong password must be rate limited per token, and the failure
must not distinguish "wrong password" from "unknown token".

### 7.3.4 What stays out

No public actions, no public writes, no public streaming, no embedding of a public page inside a
third-party site in 1.0 (that is a `frame-ancestors` decision that deserves its own thought).

## 8. Agent-authored pages — the security rules

Each rule maps to evidence in §2.4. These are requirements, not guidance.

1. **The agent fills a schema; it never emits markup, HTML, CSS or code.** `narrative.v1` accepts
   typed blocks, not a markdown blob. (Adaptive Cards: "no code behind"; host owns look and feel.)
2. **No images in agent-authored content. None.** Not sanitised — absent from the schema.
   (CamoLeak exfiltrated through a *trusted first-party* image proxy; CSP did not help.)
3. **No free-form links.** A narrative block may reference an internal Crewship entity by id
   (issue, run, page, agent) and the renderer builds the URL. It may not carry a URL.
   (Slack AI's private-channel exfiltration was a rendered link.)
4. **Actions come from the page's declared allow-list only.** The YAML declares which routines and
   issue templates a panel may offer; the agent selects an index and supplies parameters that are
   validated server-side against that action's schema. The agent cannot author an action.
5. **The confirmation dialog is drawn by host chrome, never by panel content**, so an injected
   panel cannot fake or bypass it.
6. **Every action click carries a server-verified token bound to (user, page, panel, action
   index).** A rendered button is not evidence of authorisation. (Slack signs every interactive
   callback; the server recomputes.)
7. **Calibrate friction to blast radius.** Read-only and reversible actions do not prompt.
   Anthropic's own containment write-up reports Claude Code users approve ~93 % of prompts —
   universal confirmation is a rubber stamp, not a control.
8. **Treat the platform's own agent as an untrusted producer.** Its context may already contain
   injected content read from a container or integration; server-side scope validation and audit
   logging apply identically. (Confused-deputy framing; OWASP LLM01/LLM05/LLM06.)
9. **Lethal-trifecta check per panel.** A panel that both displays untrusted data and can trigger
   an action must not additionally be able to emit outbound URLs or webhooks.
10. **Text renders through a React element renderer, never `innerHTML`.** No
    `dangerouslySetInnerHTML` anywhere in the panel registry.

The write path itself is already the right shape in this repo: an agent writes through the
sidecar, which overwrites caller-supplied identity fields with the token-derived one
(`internal/sidecar/identity.go:26-39`, `internal/sidecar/pipelines.go:21-25`). Panel writes must
use the same pattern — the producer identity comes from the token, never from the body.

---

## 8b. Actions — the interactive half

Actions are not a v1.1 nicety. A page that cannot *do* anything is a report; the requirement is a
surface you operate. This section is therefore load-bearing.

### 8b.1 The vocabulary

Block Kit, Adaptive Cards and amis converged independently on the same small set: an id, a label,
a semantic style triad, an opaque payload merged with visible input values, an optional confirm
step, and a distinction between "this calls the server" and "this only affects the client".
Adaptive Cards' `Action.Execute` — a named remote operation identified by a `verb` — is the exact
analogue of "run this routine".

```ts
type PageAction = {
  id: string                                  // unique within the page
  kind: "call" | "link" | "toggle" | "custom"
  label: string
  style?: "default" | "primary" | "danger"
  confirm?: { title: string; body: string; confirmLabel: string; cancelLabel: string }
  // kind: "call"
  routine?: string                            // declared in the page spec, never chosen at click time
  params?: Record<string, JSONValue>          // fixed payload
  inputs?: InputSpec[]                        // collected from the user before dispatch
  onSuccess?: { toast?: string }
  // kind: "link" → internal entity ref only (§8 rule 3); "toggle" → local panel state
  // kind: "custom" → a handler registered in our own client registry at build time
}
```

`kind: "custom"` exists from day one on Airbnb's advice — their SDUI system ships an extension
point for action types precisely so it does not have to be retrofitted. It resolves to a handler
**we** registered at build time, not to user-supplied code, so it costs nothing in safety.

### 8b.2 The button posts an index, not a routine

This is the design decision that makes §8 rule 4 verifiable rather than aspirational:

```
POST /api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}
```

The request body carries only the collected `inputs`. **The server resolves `actionId` against the
stored page spec** and dispatches the routine named *there*. A caller — a compromised client, an
injected narrative panel, an agent — cannot name a routine at click time, because the wire format
has no field for one. The allow-list is not a check we remember to perform; it is the only path
that exists.

### 8b.3 Dispatch: 202, not a held connection

`POST /api/v1/workspaces/{ws}/pipelines/{slug}/run` is **synchronous** — it holds the HTTP
connection for the entire run and returns the full `RunResult`
(`internal/api/pipelines_exec.go:193-224`; the CLI uses a long-timeout client for exactly this
reason, `cmd/crewship/cmd_pipeline.go:584`). No existing frontend surface has a long-request
strategy. A Pages button on a ten-minute routine would hang.

The server already has the answer, unused by any client:

- **`Idempotency-Key` header** → `IdempotencyStore.LookupOrReserve`, atomic, 24 h TTL,
  pipeline-namespaced since v153 (`internal/pipeline/idempotency.go:26-77`). Read at
  `internal/api/pipelines_exec.go:131`. **Zero frontend call sites send it today** — Pages is the
  first consumer. Client generates a UUIDv4 per logical click, the Stripe pattern.
- **`debounce_key` / `delay_seconds`** → `enqueueDeferredRun` returns **202**
  `{status:"SCHEDULED", pending_id, fire_at, coalesced}` immediately
  (`internal/api/pipeline_deferred.go:92-98`), with `(pipeline_id, debounce_key)` coalescing in
  `pending_runs` (`internal/pipeline/pending_runs.go:83-85`).
- **`concurrency_key`** → `ErrConcurrencyLimitReached` becomes 429 + `Retry-After: 5`
  (`pipelines_exec.go:211-218`) — the "already running" answer.

So the Pages action endpoint enqueues and returns 202 with a run or pending id. Double-click,
idempotency and long runs are one construction, not three.

⚠ **Open question for implementation:** Stripe's rule is that a replayed idempotency key with
*different* parameters must be rejected, not silently accepted. Verify whether
`LookupOrReserve` compares params; if it does not, a Pages action must hash its inputs into the
key.

### 8b.4 What the UI reuses

Nothing here needs new infrastructure — the routines surface already does all of it:

| Need | Reuse | Location |
|---|---|---|
| "is it running", app-wide, already mounted | `useActiveRoutineRuns()` / `bySlug` | `hooks/use-active-routine-runs.tsx:156`; provider `app/(dashboard)/layout.tsx:53` |
| Inline progress after a click | `<PipelineRunActivity workspaceId slug runId awaiting/>` | `components/features/activity/pipeline-run-activity.tsx:30` |
| Approve/reject a parked run | `usePendingApproval` + `RoutineApprovalBanner` | `hooks/use-pending-approval.ts`; `routines-detail-panel.tsx:493` |
| Cancel | `useCancelRoutineRun` | `components/features/routines/live-run-row.tsx:42` |
| Parameter form from a **server-declared** schema | `SlashActionModal` + its `Field` switch | `components/features/chat/composer/slash-action-modal.tsx:57,185` |
| Reading a refusal | `readApiError` / `apiErrorMessage` | `lib/api-error.ts:30,51` |
| Toasts with an action button | sonner, mounted once | `components/providers.tsx:39`; idiom `routines-detail-panel.tsx:214-224` |
| Destructive confirm | shadcn `AlertDialog` | `components/ui/alert-dialog.tsx`; usage `profile-section.tsx:944` |

**Net-new frontend dependencies: none.** `react-hook-form` is deliberately *not* added — the repo
has no forms library, `SlashActionModal` proves a server-declared field switch is sufficient, and
adding one would be a new dependency for a problem that is already solved here.

### 8b.5 The bug this must not reintroduce

`apiFetch` **resolves on 4xx/5xx**; it rejects only on transport failure. Issue #1563 was four
mutations claiming success the server refused. The fix established four rules
(`components/features/orchestration/task-actions.ts:18-73`): check `res.ok` before any success
toast; say the server's own words; never destroy the state a retry needs; `catch` covers transport
only. **There is no lint rule enforcing this** — only per-site tests.

Mitigation for Pages: exactly one action executor function, unit-tested, used by every button.
The convention cannot drift if there is only one place it lives.

## 9. Rendering

| Concern | Choice | Cost |
|---|---|---|
| Layout | CSS Grid, `col-span-n` from YAML `span`; `@container` queries for in-panel reflow | 0 KB |
| `metric`, `status` | hand-written inline SVG + CSS | 0 KB |
| `table` | semantic `<table>`; collapses to card list under a narrow container | 0 KB |
| `narrative` | typed blocks → React elements | 0 KB |
| `series` | existing `recharts@^3.10.1` (`package.json:137`), bar only in v1 | already paid |
| Animation | CSS transitions + `@keyframes`, gated on `prefers-reduced-motion` | 0 KB |
| Dispatch | `Record<PanelSchema, ComponentType<PanelProps>>` registry keyed on the validated `schema` field | 0 KB |

The registry pattern is the same one Perses, Grafana Scenes and react-jsonschema-form use:
validate, then look up a closed enum in a flat map, then render. No `eval`, no dynamic `import()`
of a user-supplied path, no `dangerouslySetInnerHTML`.

Mobile and tablet first: single column below the tablet breakpoint, no drag-to-arrange on any
touch target (nobody ships that — react-grid-layout's own issue tracker treats disabling drag on
mobile as the expected configuration).

Reuse `components/ui/section-card.tsx` — already documented as *"the canonical container for
grouped content inside a page"* — rather than inventing a panel shell.

Second choice, if recharts' known React 19 `ResponsiveContainer` bug (a minified `displayName`
breaks its internal `isChart` check, producing blank charts) bites in testing: swap `series` to
visx (~40–80 KB gzip), which is a net reduction against today's baseline.

---

## 9b. Shell and visual language — copy Routines, invent nothing

Pages sits in the sidebar next to Dashboard, Inbox, Issues and Routines, and must read as the
same product on first glance. The Routines and Credentials surfaces already define the language;
this section pins what Pages takes from them so no part of it gets re-invented.

### 9b.1 Three zones, and Pages is the second surface on the shared filter panel

```
[icon rail]  [filter rail 280px]              [main]
AppSidebar   SidebarToolbar / Search /        Overview  ·  or a single page
             FilterButton / Collapse
             ── STATUS  ────────── n
                All · Fresh · Stale · Failed · Never produced
             ── OWNER   ────────── n
                per crew
             ── PAGES   ────────── n
                the list, per-item icon + right-side badge
```

Everything in the middle column already exists in `components/layout/sidebar-kit.tsx`:
`SIDEBAR_WIDTH` (`:29`), `SidebarToolbar` (`:34`), `SidebarSearch` (`:43`),
`SidebarFilterButton` (`:105`), **`SidebarFilterPopover` (`:164`)**, `SidebarFacet` (`:279`),
`SidebarFacetOption` (`:319`), `SidebarCollapseButton` (`:378`), `SidebarActiveChip(s)`
(`:403`, `:430`), `SidebarSection` (`:439`), `SidebarRow` (`:514`).

**This is a hard requirement, not a preference.** Issue #1776 is open: five surfaces (Inbox,
Routines, Credentials, Integrations, Skills) still hand-roll their own filter popover and have
drifted in both appearance and behaviour — Credentials' `set({category}); setFilterOpen(false)`
makes combining two facets impossible. #1777 lifted the panel into the kit with Issues as the
parity proof. **Pages must be the second surface on the shared panel, never the sixth hand-rolled
one.** Facets are multi-select and the panel stays open after a pick.

### 9b.2 The main pane mirrors the Routines overview

Routines' Overview is the reference composition and Pages' index should be recognisably the same
page: a band of four stat tiles, then paired cards below.

- Stat tiles — `components/layout/stat-card.tsx:18` `StatCard{title, value, subtitle, icon}`.
  For Pages: **PAGES**, **STALE NOW**, **UPDATED TODAY**, **NEEDS ATTENTION**.
- Cards — `SectionCard` (`components/ui/section-card.tsx`, documented as *"the canonical
  container for grouped content inside a page"*) or `DashboardCard`
  (`components/features/dashboard/dashboard-card.tsx`) for the tile shell.
- Card header idiom, copied exactly: small icon + 11px uppercase tracking-wider label on the
  left, right-aligned muted status word — `38 total`, `none scheduled`, `no runs yet`,
  `nothing pending`, `all clean`. The right-hand word is always the *answer*, never a repeat of
  the label.
- Page shell: `PageShell` / `PageHeader` (`components/layout/page-shell.tsx:32`,
  `page-header.tsx:7`); in-page tab switching `ToolbarStrip` (`toolbar-strip.tsx:40`) or `SubBar`
  (`sub-bar.tsx:82`).
- Header line: icon + name + `·` + a dense count summary, exactly as Routines does
  (`38 routines · 0 runs`) and Credentials does (`12 secrets · 2 waiting on a tool`).
  Pages: `12 pages · 3 stale`.

### 9b.3 Empty states are instructions

Routines never renders a blank card. It renders a centred icon and two lines that name the next
action — *"Nothing has run yet. Pick a routine on the left and press Run, or give it a
schedule."* Use `components/layout/empty-state.tsx:16` and write the same kind of sentence for
every Pages empty state. A panel with no data yet says how to make data arrive.

### 9b.4 The em-dash rule — the app already solves our hardest problem

On the Routines overview, **RUNS TODAY** shows `0` and **SUCCESS · 7D** shows `—`. That is not an
inconsistency, it is the distinction this whole PRD is about: `0` is a measured zero, `—` is *no
basis to compute*. The design language already separates "we looked and there was nothing" from
"we have nothing to look at".

Pages inherits it verbatim, and it maps one-to-one onto the freshness states in §4:

| State | Rendering |
|---|---|
| `fresh` | the value, full contrast |
| `stale` | the value dimmed, with an absolute age next to it |
| `failed` | `—` plus the failure, in the destructive tone |
| never produced | `—` plus the empty-state sentence |

**Do not invent a fourth glyph for "no data".** The product already has one.

### 9b.5 Nav registration

`components/layout/app-sidebar.tsx:39-66` holds four groups: Plan (Dashboard, Inbox, Issues,
Routines), Run (Activity, Journal), Build (Crews, Skills, Credentials, Integrations), System.
**Pages belongs in Plan, after Routines** — it is where a person goes to see the state of their
work, not a thing they build once. Icon comes from `CONCEPT_ICON`, pinned by
`lib/__tests__/concept-icons.test.ts`.

### 9b.6 The list of things Pages must not re-invent

Filter popover · card shell · stat tile · empty state · the "no data" glyph · toast surface ·
confirm dialog · run-progress rail. Every one of them exists; each re-invention is a new way for
the same control to behave differently, which is the exact debt #1776 is tracking.

## 10. Data model

Era-2 migrations, one `.sql` per file under `internal/database/migrations/`, filename
`<YYYYMMDDHHMMSS>_<lower_snake_case>.sql`, versions strictly ascending above 169
(`internal/database/migrate_registry.go:42-68`; `migrations/README.md:24-25`).

```
pages(id, workspace_id, slug, name, description,
      owner_user_id NULL, owner_crew_id NULL,   -- exactly one of the two
      created_by_agent_id NULL,
      spec_json TEXT NOT NULL,                  -- the validated spec
      created_at, updated_at)
  UNIQUE(workspace_id, slug)
  CHECK ((owner_user_id IS NOT NULL) <> (owner_crew_id IS NOT NULL))
  owner_user_id ON DELETE RESTRICT              -- transfer before deleting a user (§7.1 rule 1b)

page_panels(id, page_id, panel_id, schema, title,
            owner_crew_id NOT NULL,             -- NULL would read as "visible to everyone"
            producer_kind, producer_ref,        -- kind ∈ {routine, script, agent, webhook}
            sla_seconds, span, config_json)
  UNIQUE(page_id, panel_id)
  owner_crew_id ON DELETE RESTRICT              -- a removed crew must not silently widen access

page_panel_data(panel_id, seq, payload_json, produced_at,
                producer_run_id, state)   -- ring: newest 200, hard age cut 7d (§10b.3)
  PRIMARY KEY(panel_id, seq)

page_versions(page_id, seq, spec_json, author_user_id, author_agent_id, created_at)
  PRIMARY KEY(page_id, seq)                -- last 50 kept; rollback target

page_public_tokens(id, page_id, token_hash, password_hash NULL, expires_at NOT NULL,
                   show_provenance, created_by_user_id, revoked_at NULL, last_seen_at)
  -- created_by_user_id NOT NULL: only a human publishes (§7.3.2 rule 3)
  -- expires_at NOT NULL: every public link expires (rule 4)

page_grants(page_id, subject_type, subject_id, level, panel_ids NULL,
            granted_by_user_id, granted_at)
  PRIMARY KEY(page_id, subject_type, subject_id, level)
  -- subject_type ∈ {user, crew, agent};  level ∈ {read, produce, write}
  -- panel_ids: JSON array, only meaningful for level = produce (NULL = all panels)
  -- granted_by_user_id is NOT NULL: only a human issues a grant (§7.1b rule 1)
```

JSON-in-TEXT is the house style (`migrate_consts_v109_member_capabilities.go:22-24`), and size
caps are enforced in Go at the handler, never as a DB `CHECK` — the whole schema contains only two
`length()` constraints and neither is a size cap. Follow
`internal/api/memory_config_handler.go:171-201` (`MaxBytesReader` → decode → explicit 400) with
the richer 422-plus-rejection-envelope shape from `internal/sidecar/memory_write.go:47-55` for
oversized payloads. **Proposed caps: 64 KiB per panel payload, 256 KiB per page spec.**

⚠ `/api/v1/internal/*` bypasses the global `BodyCap` middleware — any internal Pages write
endpoint must bound its own body explicitly (the repeated warning at
`internal/api/crew_messaging.go:60`).

---

## 10b. Lifecycle, limits and portability

All decided 2026-08-12.

### 10b.1 Editing and versioning

**The editor already exists.** CodeMirror 6 is in `package.json:27-40` — including
`@codemirror/lang-yaml` and `@codemirror/lint` — and `components/features/routines/
routine-editor-tab.tsx` already wires it up for routines. The Pages editor is the same component
with the YAML mode and a linter fed by our own schema. Authoring is therefore: the CLI, the
in-app editor, or an agent — three doors onto one document.

**Every save is a version.** `page_versions(page_id, seq, spec_json, author_user_id,
author_agent_id, created_at)`, following the `pipeline_versions` precedent. Several agents may
rewrite one page, and the one who breaks it is rarely the one who notices — so
`crewship page rollback <slug> --to <seq>` is not a nicety. Retain the last **50** versions.
Panel *data* is not versioned; the payload ring in §5 is the only history it gets.

**Rollback restores structure, never numbers.** A panel brought back by a rollback renders
**dimmed, in a "waiting for first data" state**, even if rows for it survive in the ring. Old
payloads are never resurrected and shown as current — that is precisely the lie §4 exists to
prevent, and a rollback is exactly when someone is most likely to believe what they see.

**The save gate, and why import skips it.** Routines cannot be saved without a passing
`test_run` less than five minutes old (`ErrTestRunGateFailed`,
`testRunFreshness = 5 * time.Minute`, `internal/pipeline/store.go:24-38,119-133`). But
**imports skip that gate by design** — *"a marketplace [bundle]…"*
(`internal/api/pipelines_crud.go:336`). Pages copies both halves:

- **Authoring** (CLI, editor, agent) validates the spec against the schema and checks that every
  declared producer and owner resolves. Cheap, synchronous, no render run. It stops an agent
  saving a page that names a routine which does not exist.
- **Import** skips it, exactly as routines do — a bundle from elsewhere is bound at import time
  and its references are checked there instead (§10b.2).

### 10b.2 Templates = export/import, the currency the marketplace already uses

**Revised 2026-08-12.** An earlier draft proposed a built-in catalog kind following
`CrewTemplate` / `Recipe`. That was the wrong precedent. The right one is **routines**, which
already ship the marketplace mechanism:

- `POST /api/v1/workspaces/{ws}/pipelines/{slug}/export` produces a portable bundle with
  workspace-specific ids stripped, explicitly *"so a marketplace consumer can import via
  POST …/import"* (`internal/api/pipelines_crud.go:163-178`).
- Import is *"marketplace install flows + cross-workspace transfer"*
  (`pipelines_crud.go:240`).

A "page template" is therefore **not a separate noun**. It is a page spec exported as a bundle:
producer references become declared placeholders, ids are stripped, and import binds them to
local crews and routines. The same document is a page here and a template there.

```
crewship page export <slug> > weekly-close.page.yaml
crewship page import weekly-close.page.yaml --slug uzaverka --bind crew/ucetni=crew/finance
```

**Marketplace readiness in 1.0 means three things**, and no more: the export bundle carries no
workspace ids, every external reference the page needs is *declared* (so the importer can see
what it must bind before installing), and import is a single transaction that either binds
everything or refuses. Whether the marketplace lists pages is a marketplace decision; the bundle
being clean is ours.

The unbound-reference case is the one to get right: importing a page whose producer routine does
not exist locally must not create a page full of dead panels. Import either binds it, or refuses
and says which reference it could not resolve.

### 10b.3 Hard limits

Chosen against SQLite's single-writer reality, not plucked from the air. The dangerous number is
push frequency: 24 panels × 100 pages × one push every 5 s is 2 880 writes per second, which no
amount of index tuning survives.

| Limit | Value | Why |
|---|---|---|
| Pages per workspace | 100 | soft, admin-raisable; stops an agent loop producing thousands |
| Panels per page | 24 | beyond this nobody reads it anyway |
| Payload size | 64 KiB | enforced in Go at the handler (§10) |
| Spec size | 256 KiB | as above |
| **Push rate per panel** | **12/min sustained, burst 30** | one push per 5 s is the artefact's own fastest producer |
| Push rate per workspace | 600/min | the real backstop; per-panel limits do not compose |
| Over the limit | **429 + `Retry-After`** | the pattern `pipelines_exec.go:211-218` already uses |
| Public page views per token | 600/h | §7.3.2 rule 6 |
| Payload ring | **200 payloads, hard age cut at 7 days**, whichever comes first | see below |
| Versions per page | 50 | |

**The *rate* rows live in `config/rate-limits.yml`; the *size and count* rows are Go constants.**
That split is deliberate and was left implicit in an earlier draft, which read as though every
number belonged in the YAML and contradicted §10 ("size caps are enforced in Go at the handler").
Rates are operational and get tuned by whoever runs the instance; sizes and counts are contract —
a payload cap that varies per deployment is a payload cap no producer can code against.

For the rate rows, that file is the existing
declarative surface — `{name, requests, window, scope, applies_to[]}` per limit, with `window`
in `1s|10s|1m|1h|1d` and `scope` in `ip|user|agent` — parsed by `internal/ratelimitcfg`. Pages
adds its entries there so the limits are visible, reviewable and adjustable in Settings rather
than compiled in. The table above is the proposed default set, not a hard-coding.

⚠ That file's own header records a limitation to design around: *"MVP: per-process, neskaluje přes
více instancí."* With one binary this is exact; with N replicas every cap becomes N×. Panel push
limits must therefore also be enforced where the write lands — a cheap `produced_at` check
against the panel's minimum interval, in the same transaction — so the floor holds regardless of
how many processes are serving.

On retention: "about a week" is right for the *age* bound but cannot be the only bound. A panel
pushed every 5 s produces ~120 000 rows in a week — per panel. So the ring is bounded by **count
first, age second**: keep the newest 200 payloads, and drop anything older than 7 days even if
that leaves fewer. A sparkline needs about 30 points; 200 is already generous. Configurable per
workspace as `page_retention_days`, following `run_retention_days`
(`migrate_consts_v158_run_retention_days.go:13`).

### 10b.4 When the ground moves

A panel never disappears quietly. If its producer is deleted, its owning crew is removed, or the
agent holding `produce` is dismissed, the panel switches to **`failed`** with a stated reason —
"producer routine `x` no longer exists" — and stays on the page. A page is a fixed structure with
a template behind it; silently shrinking it would mean the page lies about what it is supposed to
show. `on_failure` fires as it would for any other failure.

### 10b.5 Export and backup

- **`crewship export`** carries the page **spec** only. Export moves configuration between
  installs; panel data is state and belongs to the install it came from.
- **Backup** carries spec, grants, versions and panel data — the backup bundle is already a
  whole-instance snapshot including containers, and a page whose numbers vanish on restore would
  be a page nobody trusts afterwards.

### 10b.5b Liveness — every page is live, and `stream: true` is deleted

Decided 2026-08-12 on architectural grounds, and it makes the spec **smaller**.

The first draft had a per-panel `stream: true` flag, and §13 obstacle 4 warned that realtime is
not free. Re-examined against what the Hub actually is, the flag was solving a problem we do not
have: **the client already holds exactly one websocket for the whole app**
(`internal/ws/hub.go:61`, `HandleUpgrade` `:797`, auth by ws-token frame `:899`), and channels are
just subscriptions multiplexed over it, authorised by `DBChannelAuthorizer.CanSubscribe`
(`internal/ws/channel_auth.go:46-104`).

So an open page subscribes to **one channel, `page:{pageId}`**, and a push broadcasts an
invalidation on it. That is not a socket per panel or a socket per viewer — it is one more
subscription on a connection that already exists. Cost per additional live panel: zero.

Consequences, all simplifications:

- **`stream: true` is removed from the schema.** Every page is live. There is no slow mode to
  configure and no fast mode to abuse.
- **The push rate limit (§10b.3) is the only throttle**, and it is where it belongs — on the
  producer, not on the viewer.
- **Broadcast carries no payload**, only "panel X changed". The client re-reads through the normal
  authorised path, so the per-panel permission filter (§7.1) cannot be bypassed by a broadcast
  reaching a subscriber who should not see the data. A channel that shipped payloads would need
  its own copy of the filter — this way there is only ever one.
- Fallback stays honest: if the socket is down, the page polls at its shortest SLA. The realtime
  banner already tells the user which mode they are in
  (`components/layout/realtime-status-banner.tsx`).
- `page.panel.updated` joins `VALID_REALTIME_TYPES` (`hooks/use-realtime.tsx:115-146`), which
  silently drops unknown types — a step that is easy to forget and produces a page that simply
  never updates.

### 10b.5c Inbound webhooks — a producer that cannot run the CLI

A panel should be writable by anything, not only by something that can execute the `crewship`
binary — a cron on someone else's box, a Zapier step, a PLC gateway, a GitHub Action.

`POST /api/v1/page-webhooks/{token}` with the payload as the body. (This section first named
`POST /api/v1/pages/webhooks/{token}`; that pattern cannot be registered — Go's ServeMux rejects
it as a conflict against `POST /api/v1/pages/{slug}/public` and `.../rollback`, both of which match
`/api/v1/pages/webhooks/public`, and the failure is a panic at boot. The endpoint therefore gets
its own top-level space, as every other token-addressed door already has: `/api/v1/webhooks/{token}`
for a pipeline, `/api/v1/public/pages/{token}` for a published page. See
`internal/api/router_pages_webhooks.go`.) The shape is copied from
`pipeline_webhooks` rather than invented: tokens are **SHA-256 hashed at rest** (since #1888,
`internal/pipeline/webhooks.go:23-46`), holding the token is the authorisation, and the token is
bound to exactly one panel — so a leaked token can write one panel and nothing else.

The webhook is a `produce` grant in a different coat, and it obeys every rule that grant does:
issued only by a human, rate limited per panel, revocable, and every write journalled with the
token id as the actor.

### 10b.5d The Dashboard strip

Pages earns a strip on the Dashboard — `DashboardCard` titled **PAGES**, listing the most
recently updated pages the viewer may see, with the stale count as the right-hand status word
(`3 stale` / `all fresh`, per the idiom in §9b.2). It is the cheapest way for the feature to find
people's hands, and it is a read-only view over data the page index already returns. Ships after
the Pages surface itself, not inside the first slice.

### 10b.6 Notifications

New category **`pages.stale`**, in the `system` group (`internal/notify/categories.go:103-122` —
`AllCategories` is derived from `CategoryGroups`, so a category with no group fails CI). It
notifies the page owner. Default **on** for the owner, off for everyone else; `on_failure` → issue
remains the escalation path for anything that needs work rather than awareness.

### 10b.7 Language

A page inherits the workspace's `preferred_language` (already on the workspace record). The
producing agent is instructed to write panel titles and narrative in that language. Nothing is
translated at render time — a page shows the words its producer wrote, which is the only way the
provenance stays honest.

### 10b.8 Print and PDF

A print stylesheet ships in 1.0: `@media print` collapses the rails, forces the single-column
grid, prints the provenance footer and the age of every panel, and breaks between panels rather
than through them. That covers "send the monthly close to the accountant" via the browser's own
Print-to-PDF, and costs a stylesheet.

Server-side PDF generation (a headless browser in the binary, scheduled email delivery) is **not**
in 1.0. It is a rendering service with its own failure modes, and the print stylesheet is what
makes it cheap to add later if anyone actually asks.

## 11. API and CLI surface

Repo rule: every endpoint gets a CLI command, and the acceptance test drives the CLI.

| Endpoint | CLI |
|---|---|
| `GET /api/v1/pages` | `crewship page list` |
| `GET /api/v1/pages/{slug}` | `crewship page get <slug>` |
| `POST /api/v1/pages` | `crewship page create` |
| `PATCH /api/v1/pages/{slug}` | `crewship page update <slug>` |
| `DELETE /api/v1/pages/{slug}` | `crewship page delete <slug>` |
| `PUT /api/v1/pages/{slug}/panels/{id}/data` | `crewship page set <slug>/<panel> --data -` |
| `GET/PUT/DELETE …/grants` | `crewship page grant` / `revoke` |
| `POST /api/v1/internal/pages/{slug}/panels/{id}/data` | sidecar path for container scripts |
| `POST/GET …/{slug}/webhooks`, `DELETE …/{slug}/webhooks/{id}` | `crewship page webhook create` / `list` / `revoke` |
| `POST /api/v1/page-webhooks/{token}` | none, deliberately — §10b.5c is the door for a producer that cannot run the binary |

`crewship page set <page>/<panel> --data -` reading JSON on stdin is the single write path, and
it is what appears in every producer script. Provenance is attached server-side.

Registration constraints, all CI-gated:

- Mutations must register through `authedMut` / `authedAdmin`, not raw `authed(...)`
  (`internal/api/rbac_routes.go:224-268`); `internal/api/route_authz_invariant_test.go` fails the
  build otherwise.
- A `scopeForRoute` case is required (`rbac_routes.go:104-171`) or the scope resolves to `""`
  and the build fails.
- Regenerate the golden: `go test ./internal/api -run TestMutationRouteRolesMatchManifest
  -update-route-roles`.
- Ownership-gated mutations use the `roleSelf` sentinel (`rbac_routes.go:69-74`), as
  saved-views DELETE already does (`router_orchestration.go:159`).
- OpenAPI schemas under `internal/apidocs/`.

**A routine writing a panel needs a governed verb.** `internal/pipeline/crewship_step.go:119-178`
refuses any verb without a `PolicyAction` at *save* time (`ErrCrewshipVerbUngoverned`). So
`page.write` requires a new `policy.Action` constant (`internal/policy/types.go:70-203`) with a
decided cell for all four autonomy levels in `DecideAction`, plus the existing parity test
`TestCrewshipVerbs_EveryPolicyActionIsDeclared`.

---

## 11b. Wire decisions — pinned 2026-08-12

Two agents building against this PRD in parallel each found the same class of gap: the document
named a concept without fixing its shape on the wire. Every one of these is now pinned, because
an ambiguity here becomes a client and a server that both pass their own tests.

1. **Routes are workspace-unscoped: `/api/v1/pages/...`**, with `wsCtx` supplying the workspace,
   following `saved-views`, `missions`, `runs`, `journal` and `automations`. Pipelines' scoped
   shape (`/api/v1/workspaces/{ws}/pipelines`) is the older pattern. The CLI already appends
   `workspace_id` (`internal/cli/client.go:208-218`).
2. **`page create --file` sends the parsed spec as JSON**, not the YAML verbatim. §10 stores
   `spec_json TEXT` described as *the validated spec*, and §10b.1 requires validation at authoring
   time, which the server cannot do on an opaque string. The CLI parses; the server validates.
3. **SLA is `sla_seconds` (integer) on the wire**; `sla: 30s` is YAML sugar the CLI converts.
   One representation in the database, one on the wire, one for humans.
4. **Provenance is a nested object**, not flat fields: `provenance: {producer, run_id, produced_at}`.
   Flat fields would collide with payload keys the moment a producer emits `produced_at` itself.
5. **`page delete` requires `--yes`**, consistent with every other destructive CLI command
   (`confirmAction`). A delete an agent cannot script non-interactively is unusable.
6. **The size cap is enforced at the handler**, and a client-side pre-check may only be additive.
   The same 422 must arrive on the sidecar path, where no client pre-check exists.
7. **`table.v1` rows are keyed objects** (`{colKey: value}`), matching `columns[{key,…}]`.
   Positional arrays may be accepted as a convenience but keyed is canonical and is what the
   schema validates.
8. **There are four panel states, not three.** §4's `fresh` / `stale` / `failed` plus
   **`never_produced`**, and the **server sends it** — it knows there is no `page_panel_data` row,
   and having the client infer it from an absent field is how two clients end up disagreeing.
9. **`metric.v1` `delta` is directionless by default.** It renders with a sign and an arrow in the
   muted tone. An optional `delta_good: "up" | "down"` opts into success/destructive colour.
   Green-up on an error rate would be a lie, so the payload has to say which way is good.
10. **`metric.v1` `target` is a ceiling** rendered as a ratio meter clamped to 0–100 %.
11. **`status.v1`: `name` is the row title, `label` is a detail line.** The state word renders
    separately from `label`, so "glyph + text, never colour alone" holds even when a producer
    writes something unrelated in `label`.
12. **`table.v1` carries a row cap of 200.** §10b.3 caps bytes only, and 64 KiB is roughly a
    thousand rows — more than anyone reads and more than we will virtualise.
13. **CLI flags, pinned after the documentation pass found them unspecified.** A command whose
    flags are undefined cannot be implemented to the line, so:
    - `page update <slug> --file <yaml>` — symmetric with `create --file`; same parse-and-validate
      path, and the update is what creates a new row in `page_versions`.
    - `page revoke <slug> --user|--crew|--agent <ref>` — fully symmetric with `grant`. An
      asymmetric revoke is how a grant becomes impossible to remove.
    - `--bind old=new` is **repeatable**, not comma-separated. Slugs may contain a comma far more
      plausibly than they may contain a repeated flag, and a bundle with six references needs six.
    - `page list --status <fresh|stale|failed|never_produced> --owner <crew>`, both repeatable,
      mirroring the rail facets in §9b.1 so the CLI and the UI filter by the same vocabulary.
    - `page export <slug>` takes no history flag. Export carries the spec only (§10b.5); a flag
      that offered to include data would contradict that section.
14. **The sealed placeholder has a wire shape** (§7.1 rule 2). A panel the viewer may not see is
    serialised as `{panel_id, span, sealed: true, owner_crew_name}` and nothing else — no schema,
    no payload, no producer, no SLA. The renderer keys on `sealed`, not on a missing field, so a
    serialisation bug can never be mistaken for a permission decision.
15. **The page index carries a freshness rollup**, or every derived number on the overview is an
    em dash. Each row in `GET /api/v1/pages` carries `panel_states: {fresh, stale, failed,
    never_produced}` as counts, plus `last_produced_at` — which is deliberately **not**
    `updated_at`, because §10 defines that as the spec's modification time. A page whose spec was
    edited an hour ago and whose data last arrived a week ago must not read as "updated today".
16. **`metric.v1` `sparkline` points are evenly spaced by contract.** A producer that pushes
    irregularly must send a `series.v1` panel instead. Even spacing is only honest if the producer
    guarantees it, so the schema states it rather than implying it.

The staging below is deliberately harsher than the first draft. §13 lists ten obstacles; a v1
that takes all of them at once is not the "lightweight" thing this feature was asked to be. Each
stage must be independently shippable and independently useful.

### v0 — the smallest thing that is real

The only genuine invention here is **a named, typed, permissioned blob a producer can write and a
page can render.** Everything else is layering. v0 ships that and nothing else.

- Tables `pages`, `page_panels`, `page_panel_data`. `owner_user_id` xor `owner_crew_id`.
- **Three schemas: `metric.v1`, `status.v1`, `table.v1`.** No charts — therefore no recharts, no
  static-export hydration gap, and the palette bug (§3) is off the critical path.
- Page definition is YAML **posted to the API** (`crewship page create --file`), *not* a manifest
  kind. Same document shape; the 11-list manifest integration is deferred.
- Producer push via CLI and sidecar; provenance attached server-side.
- `sla`, three freshness states, degraded stale rendering.
- Per-panel crew filtering with sealed placeholders (§2.3); `page.create` capability.
- Sharing: **owner + `shared` boolean**, the SavedView precedent. No grant table yet.
- CLI parity for every endpoint; docs in the same PR.

### v1 — actions, which is what the surface is for

- `PageAction` vocabulary (§8b.1); the index-not-slug dispatch endpoint (§8b.2).
- 202 dispatch with `Idempotency-Key` + `debounce_key`; 429 as "already running" (§8b.3).
- One tested action executor; confirm via `AlertDialog`; progress via `PipelineRunActivity`.
- Parameter collection from a server-declared field schema, `SlashActionModal` pattern.
- `page_grants` — the first per-object ACL in the codebase (§7.2), owner/admin issued.
- Owner-departure transfer to crew + admin notification (§7.1 rule 1b).
- `on_failure` → issue on the owning crew.
- `narrative.v1`, text only, no actions.
- Bounded payload ring; `page_retention_days`.
- `kind: Page` in the manifest; `crewship apply` / `--dry-run`.

### v1.1 — the sensor, which is the actual differentiator

- `wake:` gates compiling to `automations` rows.
- `narrative.v1` actions, with the full §8 rule set and the governed `page.write` verb (§11).
- `refresh: on:wake`, `on:panels-changed`.

### v1.1b — reach beyond the workspace

- Public pages: `/p/{token}`, per-panel opt-in, expiry, optional password (§7.3).
- Print stylesheet (§10b.8).
- In-app CodeMirror YAML editor with schema linting, reusing the routines editor (§10b.1).
- Page templates from the built-in catalog (§10b.2).

### v1.2 — reach

- `series.v1` (bar first, then line/area), after the `--chart-1..5` palette fix has landed.
- `embed.v1` — the sandboxed escape hatch (§3.1).
- Inbound panel webhooks (§10b.5c); Dashboard strip (§10b.5d).
- Export/import bundles for the marketplace (§10b.2); `crewship export` support.

### Non-goals

- Ad-hoc time-range selection (§5).
- A query language, datasource plugins, or credentials in the page layer.
- User-supplied components, JSX, HTML, or CSS.
- Drag-to-arrange on touch devices.
- A general platform ACL (§7.2).

---

## 12b. Parallelism — which slices can run at once

Pages is release-1.0 scope (owner decision, 2026-08-12) and will be built by several agents at
the same time. Slices only parallelise where they do not share a file. The collision points are
few and specific.

**Serialised — everything waits on this.** Slice 1 (migrations + `pages`/`page_panels`/
`page_panel_data` + the three payload schemas). Nothing else can start until the table shapes and
the payload JSON schemas are merged, because every other slice reads them.

**After slice 1, these run in parallel — disjoint files:**

| Track | Owns | Touches nobody else's files |
|---|---|---|
| **A · API + CLI** | `internal/api/pages_*.go`, `cmd/crewship/cmd_page.go`, `internal/apidocs/` | shares only `rbac_routes.go` + the route-roles golden — **serialise the registration commit** |
| **B · Read UI** | `app/(dashboard)/pages/`, `components/features/pages/` | consumes the kit read-only |
| **C · Panel registry + the three panels** | `components/features/pages/panels/` | pure components, the easiest track to hand an agent |
| **D · Permissions** | crew filter + placeholder + invariant tests | needs A's handlers — start after A's first merge |
| **E · Actions** | action endpoint, executor, `useApiMutation` | needs A; the endpoint is new, no collision |

**Known shared files — every agent must be told about these three:**

1. `internal/api/rbac_routes.go` + `internal/api/testdata/route-roles.txt` — the golden is
   regenerated wholesale (`go test ./internal/api -run TestMutationRouteRolesMatchManifest
   -update-route-roles`). Two agents regenerating it concurrently will conflict every time. One
   route-registration commit, done once.
2. `components/layout/app-sidebar.tsx` — one line, one agent.
3. `app/globals.css` — the palette fix (§3) is its own PR and must not be bundled into a slice.

**Prerequisite work that is not Pages and can start immediately, in parallel with slice 1:**
`useApiMutation` (§8b.5) and the `--chart-1..5` palette fix (§3). Both are independently useful
and both unblock later tracks.

## 13. Obstacles, ranked

Verified against the tree; each is real work, not a caveat.

1. **No durable named-artifact primitive exists.** `pipeline_run_step_outputs` is keyed by
   `(run_id, step_id)`; crew shared files are a filesystem with no query surface; the routine
   DSL's `outputs` is documentary only (`schemas/routine.v1.json:36-38`). A Page is a new noun
   with new tables.
2. **No per-object ACL exists**, and it is a recorded non-goal (§7.2). `page_grants` is the first.
3. **A routine cannot write a page without a governed verb** (§11).
4. **Realtime is not free.** `hooks/use-realtime.tsx:115-146` silently drops event types outside
   its allowlist, and the `journal:{workspaceId}` WS channel — the obvious live-update carrier —
   has zero subscribers today and no `Last-Event-ID` replay. SSE
   (`internal/api/journal_handler.go:84`) is the only gap-free stream and it is journal-shaped.
   v1 polls; streaming is v1.2 and needs a decision, not an assumption.
5. **Adding a manifest kind touches 11 hand-maintained lists across 4 Go files** plus docs and
   fixtures: `schema.go:45-66`, `parse.go:81-103` (`isEmpty` — silent regression if forgotten),
   `parse.go:105-165`, `parse.go:238-384`, both hard-coded kind strings at `parse.go:381,383`,
   `apply_kinds.go` plan block, `apply_kinds.go:375-431`, `apply_kinds.go:436-454`,
   `plan.go:236-269`. The rank table is capitalised while kinds emit lowercase — bridged by
   `snakeToDocKind` (`plan.go:300-309`), and it has already caused an ordering bug once
   (`plan.go:272-280`).
6. **Manifest export is dead code for SPEC-2 kinds.** `ExportSavedViews`, `ExportRoutines`,
   `ExportProjects` have no non-test callers; `crewship export` knows only Crew and Workspace
   (`cmd/crewship/cmd_export_manifest.go:89,122`). `ExportPages` would ship tested and unreachable
   unless the CLI grows a kinds path — hence v1.2.
7. **Route registration is CI-gated in four places** (§11).
8. **Manifest apply is client-side over the public REST API** (`schema.go:14-18`) — full public
   CRUD must land before the manifest kind can exist. Sequence the work accordingly.
9. **Frontend mirrors are hand-copied** — `lib/capabilities.ts`, `lib/notification-categories.ts`.
10. **Do not repeat the SavedView slug mistake.** SavedView drift-detects on `name`, not slug,
    because the table has no slug column (`kinds/saved_view.go:216-220`). Pages are
    slug-addressable from the first migration.

---

## 14. Test plan

Repo rule: red test → fix → green, and it must fail on current `main`.

- **Go, table-driven** — spec validation (unknown schema, missing `sla`, two units in one
  `series.v1`, >5 series, span out of range); payload validation per schema; freshness state
  transitions across an SLA boundary with an injected clock; payload cap → 422 envelope;
  ring eviction at N+1.
- **Permission invariants** — the highest-value tests in this PRD:
  - a viewer without crew membership receives a response that **does not contain** the panel
    (assert on the serialised body, not on a rendered flag);
  - a `read` grant does not widen crew visibility;
  - a `write` grant cannot write panel *data*;
  - a `produce` grant scoped to panel A cannot write panel B on the same page;
  - an agent with `write` **can** add a panel it cannot read, and **does not** receive that
    panel's data in any subsequent read (§7.1b rule 2 — one test, both halves);
  - an agent cannot issue a grant by any route, including on its own crew (§7.1b rule 1);
  - an agent grant narrows automatically when the granting human loses the underlying access;
  - an unauthorised `produce` returns 403, writes a journal entry **and** notifies the owner
    (§7.1b rule 3);
  - a producer token cannot claim another agent's identity via the request body;
  - agent-created page records the authorising human as owner.
- **Security** — the §8 rules as executable tests: a narrative payload containing an image block,
  an external URL, or an undeclared action index is rejected at the API boundary; an action token
  bound to another user/panel is refused.
- **Route gates** — `route_authz_invariant_test.go`, `route_read_scope_invariant_test.go`,
  regenerated `internal/api/testdata/route-roles.txt`.
- **Manifest** — parse coverage table entries (`internal/manifest/parse_cov_test.go:115,149`),
  and `examples/manifests/full-complete.yaml` extended (exercised by `examples_validate_test.go`).
- **Frontend** — Vitest for the panel registry (every schema resolves; unknown schema renders the
  fallback, never throws); Playwright for the per-viewer reflow (two accounts, same page URL,
  different panel counts).
- **Acceptance drives the CLI binary**, per repo rule.

---

## 15. Open decisions

1. **Grant table now, or the SavedView boolean first?** The requirement says grants. Building the
   first ACL in the codebase is the single largest v1 risk. Alternative: ship v1 with
   owner + `shared` boolean, add `page_grants` in v1.1 once the rest is proven.
2. ~~What happens when the owner leaves?~~ **Decided 2026-08-12:** transfer to a crew, notify
   ADMIN/OWNER to reassign (§7.1 rule 1b).
3. ~~Can a crew own a page?~~ **Decided 2026-08-12:** yes, `owner_user_id` xor `owner_crew_id`
   (§7.1 rule 1). Owner or admin issues view grants (§7.1 rule 3).
4. ~~Panel payload ring depth~~ **Decided:** 200 payloads, hard age cut 7 days (§10b.3).
5. ~~Should `stream: true` exist?~~ **Decided:** deleted. Every page is live over the single
   existing websocket; the producer's rate limit is the only throttle (§10b.5b).
9. **Can an agent create a page unprompted?** Owner decision 2026-08-12: yes — it counts against
   the workspace cap and records the authorising human as owner, like any other agent-created
   page. Open sub-question: should an agent-created page arrive in the owner's inbox for
   acknowledgement, or appear silently? Leaning toward an inbox card the first time a given agent
   creates a page, and silence thereafter.
6. **Public pages: how is the password delivered?** Owner decision 2026-08-12 was "keep it as
   simple as possible for 1.0". Simplest is one token, one optional password, no per-recipient
   naming — at the cost of never knowing who looked. Named per-recipient tokens are a small
   addition later; the schema in §10 already allows several tokens per page, so this is a UI
   decision, not a migration.
7. ~~Does a public page show staleness?~~ **Decided:** age yes, reason no (§7.3.2b).
8. ~~Templates: catalog or user-authored?~~ **Decided:** neither — export/import bundles, the
   mechanism the marketplace already uses (§10b.2).

---

## 16. Sources

Push model: Grafana Live and Pushgateway docs; Datadog service-check and dashboard APIs; New
Relic NerdGraph `dashboardUpdateWidgetsInPage`; Kibana Canvas/Lens/Vega; Geckoboard Datasets API;
Dashing/Smashing; Uptime Kuma, Healthchecks.io, Gatus, Cronitor, Better Stack heartbeats;
VictoriaMetrics staleness markers.

Format: Perses dashboard API and CUE plugin schemas; Grafana JSON model, Schema v2, Foundation
SDK, Grafonnet, grafana-operator; Rill, Lightdash, Evidence.dev, Observable Framework, Cube.js;
Metabase and Superset serialization; homepage, Glance, Dashy, Homer.

Permissions: Grafana community thread on per-panel permissions; Grafana data source permission
docs; Salesforce Lightning Visibility Rules and the Permission Sets IdeaExchange request; Directus
`directus_panels` discussion; Retool, Appsmith, Budibase, ToolJet, Windmill, Airtable Interfaces,
Notion, Coda; Looker `access_filter`/`access_grant`; Metabase row-and-column security; Power BI
RLS role asymmetry.

Agent-authored UI: Adaptive Cards design principles; Slack Block Kit and request signing; OpenAI
Apps SDK security and privacy; MCP Apps sandbox proxy; CVE-2025-59145 (CamoLeak); Slack AI
exfiltration (PromptArmor); OWASP LLM Top 10 2025; Simon Willison on the lethal trifecta and
markdown exfiltration; Anthropic on containing Claude; LangGraph human-in-the-loop; Databricks
AI/BI Genie.

Rendering: bundlephobia measurements for recharts 3.10.1, motion, react-grid-layout, visx,
ECharts, uPlot, Observable Plot, Nivo; recharts React 19 `ResponsiveContainer` issues; Perses
plugin registry; Grafana Scenes; react-jsonschema-form registry pattern.
