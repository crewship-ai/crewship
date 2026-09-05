# UX audit — cluster C · Fleet configuration

`/crews` (crews, agents, skills), `/credentials`, `/integrations`. Analysis only,
2026-09-03, against `docs/ux/README.md`. No code was changed.

## How this was verified

- Throwaway server built from the clone 3 working tree (branch
  `onboarding-client-redesign`, uncommitted), `crewship seed`, then
  `demo@crewship.ai / password123`. **Port 8098, not 8095** — 8095 is held by
  the infra `crewship-dashboard` process on this box.
- Scale data created through the CLI: 100 crews (`crew-001…100`, 3 agents
  each, one LEAD) and 100 credentials (`cred-001…100`, mixed type/provider/
  tier). Totals after seeding: **103 crews, 308 agents, 112 credentials**.
- Playwright at 1440 / 820 / 390, dark theme. Screenshots:
  `/tmp/claude-1000/-srv-crewship-crewship-3/6a4b75ee-bcf4-4053-8a55-bedbcda82627/scratchpad/shots/`
  (`baseline/` = seed data, `scale/` = after the CLI run; `<name>-<width>.png`,
  `-tall` = full canvas height). Referenced below as `baseline/…` and `scale/…`.
- **Horizontal overflow: 0 px on every screen at every width** (`overflow.json`
  in each folder). That number is misleading — see the clipping findings: the
  roster and the integrations rail hide content behind `overflow-hidden`
  rather than scroll it.
- Code maps were produced for the whole area (21k lines under
  `components/features/crews`, 6.3k credentials, 9.9k integrations, 1.9k skills,
  2.6k mcp, 0.6k connectors). Line references below are to those files.

## 1. What each screen is for (README §1) and what it does today

### `/crews` — "everything a crew IS"

Selection-driven canvas: explorer tree on the left (`crews-explorer.tsx`),
`?crew=` / `?agent=` chooses the right pane (`crew-canvas.tsx`,
`agent-canvas.tsx`), a dock of six tabs at the bottom (`bottom-panel/`).

Against the §1 order:

1. **What needs me** — nothing. The crew canvas has no attention strip; the
   only "needs you" surface is the container-rebuild banner
   (`baseline/crew-overview-1440.png`). The agent canvas has a "Waiting on your
   decision" notice that **renders with zero buttons** because
   `agent-canvas.tsx:528-540` never passes `onOpenInbox`
   (`overview-tab.tsx:206`).
2. **Happening now** — status dots per agent in the explorer (RUNNING pulses).
   No live section on the crew canvas; "Recent activity" is a journal table.
3. **Objects** — the no-selection state is a **flat table** of every agent
   (`empty-roster.tsx`, `baseline/crews-roster-1440.png`) with no crew cards;
   crews only exist as tree nodes on the left.
4. **Outcomes** — none. No spend, no run counts, no sparkline on either canvas.
5. **Related** — see §3 below; the agent canvas is good, the crew canvas is
   almost bare.

### `/credentials` — the vault

Already close to the contract: `SubBar`, KPI row, donut, attention queue,
`Appear` stagger, master-detail (`baseline/credentials-1440.png`,
`credentials-detail-1440-tall.png`). Answers §1.1 ("Needs attention", "Missing
tool") and §1.3. Missing §1.5: nothing links out.

### `/integrations` — two products under one nav item

Tab "Notifications" = outbound channels (Slack/Discord/webhook). Tab "Tools
(MCP)" = Composio, which on a fresh instance is a setup card
(`baseline/integrations-mcp-1440.png`). The crew-scoped MCP servers that
`/crews → Settings → Integrations` lists (`linear`, `github`) **appear on
neither tab** — their only UI is the flag-gated legacy page
(`NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS`, off by default).

### `/skills` — catalog and assignment

Three-panel browser (`skills-browser.tsx`), virtualised grid, BM25 search,
result count. Reached from nowhere: no link from an agent or crew leads here
(`baseline/skills-1440.png`, `skills-detail-1440-tall.png`).

## 2. Dead ends (README §6), with evidence

### The recurring one: credential and tool gaps

A client must see, from the crew, which tool is missing and the one click
that installs it. Today:

| Where the gap is known | What the client sees |
|---|---|
| `GET /crews/{id}/integrations` returns `auth_status: "missing"` for both crew MCP servers (verified on the seeded Engineering crew) | Crew → Settings → Integrations prints the name only. `settings-tab.tsx:281,287` render `i.type` and `i.status`, fields the API does not send, so both are blank. `baseline/crew-settings-1440-tall.png` |
| `/credentials` readiness knows "the CLI that reads it is missing from a crew" (`use-credential-readiness.ts`) | Shown on `/credentials` only. The crew canvas never mentions it. The fix is prose: "add `<featureId>` and rebuild" (`credential-detail-sheet.tsx:1228-1238`), no button, no link to the crew |
| Dashboard fleet cards derive "N credential gaps" and an **Install** verb (`dashboard-overview.tsx:194,855,891`) | `/crews` does not reuse `deriveFleetHealth`; the same crew shows no gap on its own canvas |
| Agent Overview → Credentials cell | Only when the agent has **zero** credentials: "No credential assigned … its first run will fail" + "Open vault →" — leaves the screen, not one click (`overview-tab.tsx:146-154`) |
| `skill.credential_requirements` is fetched | Never rendered (`skill-detail-client.tsx:40`, absent from `skill-detail.tsx`); a skill installs onto an agent with no warning |
| Legacy MCP page | Has the right thing — "Credential missing" band with a real Connect button (`oauth-auto-connect.tsx:167-263`) — and is dark by default |

### Disabled primaries without a reason

- Create-crew `Continue` is `primaryDisabled={!stepValid}` with no text
  (`create-crew-dialog.tsx:372`); create-agent does it right
  (`create-agent-dialog.tsx:924`, "Create a crew first — leads need one";
  since #2170 an Agent needs no crew, so the hint only fires for a Lead).
- Crew Overview quick actions `Apply avatar style` / `Reset avatar overrides`
  are `disabled={agentsForCrew.length===0}` with only `opacity-50`
  (`overview-tab.tsx:132,138`, `scale/s-crew-050-1440.png`).
- Autonomy/behaviour buttons for non-admins (`crew-policy-controls.tsx:332,360`)
  — no reason, while the security and network sections on the same tab do
  state theirs.
- `Provision` disabled with a `title` only ("Save changes before provisioning",
  `crew-runtime-config.tsx:154`) — invisible on touch and keyboard.
- Skills `Install (N)`, `Uninstall (N)`, `Apply to crew`
  (`skills-detail-panel.tsx:390,582,741`), add-channel `primaryDisabled={!ready}`
  (`add-channel-dialog.tsx:440`), rotation `disabled={!value.trim()}`
  (`rotation-dialog.tsx:230`) — none say why.
- Classification chips: reason is `title` only
  (`credential-detail-sheet.tsx:1293`).

### Empty panes that do not say what will appear or how

- Explorer search with no match is a **blank column** — no "0 results", no
  clear (`scale/s-crews-search-nomatch-1440.png`).
- Explorer with zero crews: no empty state at all (`crews-explorer.tsx:167-377`).
- Agent cells say "Nothing matches this filter." when no filter is applied
  (Tools 0, Channels 0, Runs 0, Sessions 0 — `baseline/agent-overview-1440-tall.png`).
- Crew Roster "No agents in this crew. Use + Agent in the toolbar" — no button
  (`roster-tab.tsx:29`). Missions "No missions yet for this crew."
  (`missions-tab.tsx:34`). Settings "No integrations bound to this crew."
  (`settings-tab.tsx:270`).
- Bottom panel: all 13 tabs share one grey string with no icon and no action
  (`bottom-panel/shared.tsx:5`).
- Empty roster: "No agents yet — use the + Crew and + Agent buttons" is prose,
  and the `EmptyState` kit is used only by dead code (`crew-agents.tsx:65`).
- Credentials rail "Nothing matches these filters." — no clear-filters
  (`credentials-sidebar.tsx:584`, `scale/s-credentials-search-nomatch-1440.png`).
- Integrations rail and skills install/assign dialogs: "No agents found." /
  "No crews in this workspace." without a create action.

### Errors that lose the input or offer no retry

- `/crews` page fetch failures are **silent** (`crews/page.tsx:95-102`): a dead
  API renders an empty fleet with no error and no retry.
- `CanvasShell` "Could not load crew/agent" has no retry
  (`canvas-base.tsx:221-228`).
- Every bottom-panel tab: "Failed to load: `{error}`" with no retry.
- `use-agent-relations.ts:104-107` turns any failed fetch into `[]`, so a 500 on
  credentials renders the false alarm "No credential assigned — its first run
  will fail".
- `views/connection-detail.tsx:60` discards the hook error, so a failed agents
  fetch prints the security claim "None. Only Crewship itself delivers here".
- `/skills`: "Failed to load skills" with no retry and no refresh control
  (`skills-browser.tsx:691`).
- Good examples to copy: crew activity feed (`crew-activity-feed.tsx:207-224`,
  Retry button), credentials page load (`credentials/page.tsx:547-555`), the
  add-credential wizard's partial-failure band (`add-credential-wizard.tsx:399`),
  create-crew/agent refusal bands that keep the form.

### Reload does not resume

- `/crews`: only `?crew=` and `?agent=` survive. Crew tab
  (Overview/Roster/Missions/Files/Settings), agent tab, bottom-panel tab and
  open state, explorer search and collapse are all local state. Only the dock
  **height** persists (`bottom-panel/index.tsx:107`).
- `/credentials`: reads `?id=` once, never writes it; picking another
  credential leaves the URL on the old one (`credentials/page.tsx:143,499`).
  Filters, tier, status, sort, select-mode are lost.
- `/integrations`: `?tab=&section=` are kept; the selected connection, search
  and filters are not.
- `/skills`: **no URL state at all** — tab, four facets, query, selection.

### Internal leaks

- The setup guide agent `_crewship-setup-guide` ("Crewship Guide", crew `—`)
  is the **first row of the client's fleet roster**
  (`baseline/crews-roster-1440.png`). It has a `crew_id` that is not in the
  crews list, so it is absent from the explorer but present in the table.
- Raw model ids: `claude-haiku-4-5` in the agent header (`agent-canvas.tsx:481`),
  roster cards (`roster-tab.tsx:69`), create-agent `<option>` list
  (`create-agent-dialog.tsx:939`). `getModelLabel` is never used inside
  `components/features/crews/`.
- Raw enums: `pending_review` (`roster-tab.tsx:51`), `in_progress`
  (`overview-tab.tsx:105,161,170`), `{run.status}` / `{c.status}` /
  `{schedule.last_status}` in dock tabs, `STANDARD/RESTRICTED/SEALED`
  (`credential-detail-sheet.tsx:674,1305`), `ACTIVE/EXPIRED/CANCELLED` (`:1357`),
  `USE/ROTATE/REVEAL` (`:1091`), Composio `ACTIVE/INITIATED`
  (`composio/shared.tsx:66`) and the same value lowercased three views away
  (`tool-account-detail.tsx:83`), `PRO` (`skill-detail.tsx:81`), `PM` → "Pm"
  (`skill-card.tsx:194`).
- Cuids: `runs-tab.tsx:103` (`run.id.slice(0,12)`), credential bindings fall
  back to `crew_id` / `agent_id` (`credential-detail-sheet.tsx:950,957`),
  deliveries fall back to `channel_id` (`deliveries-view.tsx:148`), skills
  errors are built as `${agentId}: ${detail}` (`skills-detail-panel.tsx:303`).
- Internal vocabulary in copy: "(PRD §6 F2 / F4.2)" (`settings-tab.tsx:167`,
  visible in `baseline/crew-settings-1440-tall.png`), "harbormaster sync · deny
  on miss" (`:251`), `tool_profile` as a card subtitle (`config-tab.tsx:409`,
  visible in `baseline/agent-config-1440-tall.png`), "autonomy=full ×
  behavior_mode=block is rejected server-side" (`crew-policy-controls.tsx:387`),
  `CREWSHIP_ALLOW_PRIVATE_ENDPOINTS` (`crew-network-policy.tsx:273`),
  "We're replacing self-hosted MCP servers with a managed integration platform"
  on the Composio setup card, a hard-coded "v0.1.0-beta" footer on `/skills`.
- Header copy: `TTL —h`, `network: restricted`, `Created 9/3/2026` (US format)
  on every crew (`baseline/crew-overview-1440.png`); "6 missions" in the header
  next to a "Missions 0" card, because the Missions tab lists issues as
  missions (`baseline/crew-missions-1440-tall.png` shows the same six rows
  twice).
- Env var names as credential titles in the agent's Credentials cell
  (`CLAUDE_CODE_OAUTH_TOKEN`).

### Mobile 390

- Fleet roster: fixed grid `grid-cols-[1fr_140px_180px_120px_120px]` inside an
  `overflow-hidden` card. At 390 **the Agent column disappears** (name and
  avatar clipped; the LEAD badge overprints the crew cell) and Last active /
  Status are cut off (`baseline/crews-roster-390.png`,
  `scale/s-crews-roster-390.png`). Also broken at 820
  (`baseline/crews-roster-820.png`). Overflow reads 0 because it is clipped, not
  scrolled.
- `/integrations` has no mobile behaviour: hard `w-[280px]` rail, main column
  ~110 px, KPI cards render one glyph per line
  (`baseline/integrations-390.png`).
- `/skills`: rail is `hidden` on mobile and the expand button lives inside it,
  so facets and search are unreachable on a phone (`skills-browser.tsx:512`);
  the Playwright step to open a skill via search timed out for that reason.
- Skills resize handle is mouse-only despite `role="separator"`.
- `/credentials` is the one screen that handles 390 properly (rail overlay with
  scrim, `scale/s-credentials-detail-390-390.png`).

### Irreversible actions

Every live delete on `/crews` is a native `window.confirm`
(`crew-canvas.tsx:184`, `agent-canvas.tsx:289`, skill/connector removal in
`agent-canvas-managers.tsx:74,236`); the only `AlertDialog` in the area
(`crew-members.tsx:287`) is dead code. `/integrations` connection delete is
`window.confirm` (`integrations-layout.tsx:407`); Composio **Revoke / Remove
have no confirmation at all** (`connected-accounts-tab.tsx:114-131`).
`/credentials` uses `AlertDialog` correctly.

## 3. Cross-links (README §5)

### Crew

| Must link to | Today |
|---|---|
| chat | ✗ nothing on the crew canvas |
| agents | ✗ "Agents 3" card is not a link; Roster tab cards select in-page only |
| routines | ✗ |
| issues | ~ "Open issues" card → `/issues` **unscoped** (`overview-tab.tsx:62`); Missions tab "Open in /issues →" also unscoped |
| pages | ✗ |
| credentials | ✗ no credential surface on the crew at all |
| spend | ✗ no spend anywhere on `/crews` |
| journal | ✗ (agent has it; crew does not) |

The crew header's only action is **Files** (`crew-canvas.tsx:288`). The docstring
promises "Files, Add agent"; Add agent is not there.

### Agent (`baseline/agent-overview-1440-tall.png`)

| Must link to | Today |
|---|---|
| crew | ~ a `<button>` that selects in-page (`agent-canvas.tsx:466`), not an `<a>` |
| chat | ✓ header Chat → `/chat/<slug>` |
| runs | ✓ "Open in Journal" → `/journal?agent=<slug>` |
| skills | ✗ "Manage skills" opens a dialog; `/skills` is never linked |
| credentials | ~ "Open vault" → `/credentials` unscoped, no `?id=` |
| issues | ✗ **404**: "Open filtered by alex" → `/orchestration/issues`, which has no index page (`overview-tab.tsx:243`) |
| spend | ✗ |

### Credential

| Must link to | Today |
|---|---|
| crews that hold it | ~ "Used by → Slots" shows `CrewIcon` + name (`credential-detail-sheet.tsx:941`), **not a link** |
| agents that hold it | ~ avatars + names, not links (`:954-1012`) |
| integrations that need it | ✗ one flat sentence "Also referenced by one or more MCP server integrations." (`:1035`) |
| the crew missing its tool | ✗ prose only |

The only `<Link>` on `/credentials` is the attention row's `/inbox-v2` href
(`lib/credentials/overview.ts:175`).

### Integration / MCP / skill

- Connection → agents that may post: `<li>` with a `Bot` icon, not links, no
  `AgentAvatar` (`connection-detail.tsx:166-181`).
- Composio account → bound agents: names only (`tool-account-detail.tsx:120`);
  the empty state names "Agent access" as the fix and does not link to it.
- Skill → agents/crews: `AgentAvatar`s and `CrewChip`s are `<span>`s inside the
  card button (`skill-card.tsx:241-291`); "Agents Using" is a bare number.
- Nothing anywhere links to `/skills`, `/pages`, or a spend view.

## 4. Anatomy (README §2)

- **CrewIcon** ✓ explorer, crew header, credential slots. ✗ fleet roster crew
  column (plain text), skills `CrewChip` (bespoke pill), integrations (none).
- **Status pills** — the dashboard's `TONE_PILL` (`fleet-board.tsx`) is the
  reference. `/crews` has four other implementations: explorer text-only
  `STATUS_BADGE` (`crews-explorer.tsx:13`), roster dot+text `STATUS_COLORS`
  (`empty-roster.tsx:47`), agent-canvas `STATUS_STYLE` lowercase pills
  (`agent-canvas.tsx:94`), and a hand-built "Crew" chip (`crew-canvas.tsx:259`).
  Credentials/integrations/skills add **nine more** (`STATUS_STYLE` copied
  verbatim in `connections-view.tsx:30`, `connection-detail.tsx:27`,
  `deliveries-view.tsx:20`; two skill `SOURCE_*` maps that label `MANAGED`
  "Managed" in one and "Private" in the other; `MATURITY_BADGE` that renders
  `COMMUNITY` as "Beta" while the rail says "Community"). The house
  `StatusBadge` (`components/ui/status-badge.tsx`) is used only by the
  flag-dark legacy page.
- **SubBar** ✓ on all four screens. Crews' description is `${n} crews`
  ("1 crews") and stays at "100 crews" when there are 103.
- **DashboardCard / InlineEmpty** — not used; crew Overview uses its own
  `HealthCard`, agent Overview its own `DetailCell`.
- **EmptyState** kit — live only on `/credentials`.
- **AgentAvatar** ✓ everywhere on `/crews`; ✗ integrations agent lists.
- **getModelLabel** — never used in `components/features/crews/` (see leaks).
- **AnimatedNumber / Sparkline** — none on any of the four screens.
- **AlertDialog** — `/credentials` only (see irreversible actions).
- **Motion** — `motion/react` is used (canvas cross-fade, drawer, skills
  panel, credentials sort popover, audit rows) with **no `useReducedMotion`
  anywhere** outside the shared `Appear`; the agent header uses bare
  `motion.header layout` (scale tween) instead of `layout="position"`.
- Raw `<button>`s with custom classes instead of `Button`: crew header Files,
  Files tab "Open Files panel" (hover state equals rest), banner buttons,
  danger zone.

## 5. Scale (README §4) — 103 crews, 308 agents, 112 credentials

1. **The list API returns 100 rows unless asked, and the pages never ask.**
   `GET /crews`, `/agents`, `/credentials` without `limit` → 100; with
   `limit=1000` → 103 / 308 / 112. `/crews` says "100 crews"
   (`scale/s-crews-roster-1440.png`), `/credentials` says "100 secrets"
   (`scale/s-credentials-1440.png`). The list is newest-first, so the three
   seed crews and `crew-001`…`crew-003` fall past the window: `?crew=crew-001`
   toasts **"Crew not found"** and drops the param. (The audit run used
   `crew-101`, which does not exist — the same toast, for a different reason;
   `crew-001` is the honest example.) Worse: `?agent=agent-050-1` — an agent in
   a crew that IS listed — toasts "Agent not found" because the agents list is
   capped too (`scale/s-agent-101-1-beyond-cap-1440.png`); Crew 050's header
   says "3 agents" while its Agents card says 0 (`scale/s-crew-050-1440.png`).
   Every workspace over 100 agents loses agents from this screen and every
   deep link to them.
2. Explorer: no grouping, no sort (API order, newest first), no attention
   ordering, no result count, no cap/fold, no virtualisation — 300 agent rows
   plus 100 crew rows in one column. Search filters by agent name/slug/role
   and crew name, and the per-crew count becomes the **filtered** count, so
   "crew-05" shows Crew 050–059 each with "0" (`scale/s-crews-search-hit-1440.png`).
3. Fleet roster: a flat table of every agent, newest first, no paging, no
   grouping, no attention ordering (`scale/s-crews-roster-1440-tall.png`).
4. Toolbar pill "100 need rebuild" — correct, and the only place the fleet-wide
   gap is counted; the crews page itself does not sort those crews first.
5. Credentials: readiness is polled for at most 24 crews
   (`use-credential-readiness.ts:30`), so "Tools missing 28 across 24 crews" is
   silently partial on 103 crews. Rail renders all rows, no "showing N of M"
   (the section count is the filtered count). Overview caps attention at 6 with
   "See all →", good. "Needs attention" means two different things: rail = 5
   (status), overview = 33 (status + tool gaps).
6. Integrations: Composio catalog is capped at 40 server-side
   (`composio_handler.go:209`) while headings and KPIs quote the full total
   (`catalog-tab.tsx:43`, `integrations-layout.tsx:783`). Deliveries capped at
   200 with no paging. Connections table unvirtualised.
7. Skills: the best — Orama search, `VirtuosoGrid`, "Showing N of M". But the
   uninstall dialog fetches `/agents/{id}/skills` **once per agent** on open
   (`skills-detail-panel.tsx:443-457`): 309 requests on this workspace.
8. Search: `/integrations` has one search box that is dead on Deliveries and
   Preferences and up to three simultaneous boxes on Tools
   (`integrations-layout.tsx:539,827`, `tools-tab.tsx:84`, `triggers-tab.tsx:141`).

## 6. Prioritised list

**P1 — blocks a client**

1. Pass `limit` (or page) on the `/crews`, `/agents`, `/credentials` fetches and
   show the true total in the SubBar; until then any object past 100 is
   invisible and its deep link toasts "not found". Also derive the header's
   "N agents" from the same list the cards use so they cannot disagree.
2. Crew canvas: a **"Needs you" strip** above the tabs built from the data the
   dashboard already derives (`deriveFleetHealth`): rebuild needed, N credential
   tool gaps, MCP servers with `auth_status: missing`, agents in error. Each row
   carries a verb — **Install** (opens the credential wizard pre-filled with the
   crew slot), **Connect** (OAuth for the MCP server), **Build now**, **Inspect**.
   Fix `settings-tab.tsx` to render `auth_status` / `transport` instead of the
   non-existent `type` / `status`, as a danger pill "No credential · Connect".
3. Agent "Waiting on your decision" notice: pass `onOpenInbox` so the banner
   has its Approve / Open inbox buttons (`agent-canvas.tsx:528`).
4. Fix the 404: agent Issues cell → `/issues?assignee=<slug>` not
   `/orchestration/issues` (`overview-tab.tsx:243`).
5. Fleet roster at 390 and 820: replace the fixed 5-column grid with a
   one-column list (avatar, name, crew dot, status pill) under `md`, and make
   the table scroll inside `overflow-x-auto` rather than clip.
6. Hide `_crewship-setup-guide` (any `_`-prefixed slug, or agents whose crew is
   not in the workspace list) from the roster, or show it under its own
   "Assistant" group with a proper name.
7. `/integrations` at 390: collapse the rail into the same overlay drawer
   `/credentials` uses; stack the KPI row.
8. Surface the crew-scoped MCP servers somewhere in the live `/integrations`
   (a "Crew tools" section) or drop the "Manage workspace integrations →" link
   from the crew, which today lands on a page that does not list them.

**P2 — confusing**

9. Explorer: sort crews by attention (error → gaps → running → idle, then name),
   show `N crews · M agents` and "0 results — Clear" under the search, keep the
   per-crew count as the real count, fold crews after the first 6 expanded.
10. Crew canvas cross-links: make the three HealthCards links and scope them
    (`/issues?crew=`, Roster tab, `/routines?crew=`), add Chat (crew lead),
    Journal (`/journal?crew=<slug>`), Pages, Spend to the header meta line, and
    a Credentials section (slots bound to this crew, each "Install"/"Connect").
11. Agent canvas: crew name as a link to `/crews?crew=<slug>`, "Manage skills"
    footer links to `/skills?agent=<slug>`, Credentials footer links to
    `/credentials?id=<id>`, add a Spend cell (window + sparkline).
12. Credential detail: every slot and assignment row becomes a link
    (`/crews?crew=`, `/crews?agent=`), "MCP integrations" lists them by name
    with a link, readiness gap row gets a **Rebuild with `<tool>`** button that
    opens the crew's container features section.
13. Reasons next to every disabled primary listed in §2 (copy the
    create-agent `validationHint` pattern); convert `title`-only reasons to
    visible text.
14. One status pill: use the dashboard `TONE_PILL` shape (`rounded-full border
    px-2 py-0.5 text-micro font-semibold`, dot + word) via a shared
    `components/ui/status-pill` and delete the 13 local maps. Map enums to
    words in one place (`formatStatus`).
15. `getModelLabel` for every model id on `/crews`; readable dates
    (`formatRelativeTime` / locale-aware); drop `TTL —h` when unset;
    "network: restricted" → "Restricted network".
16. Missions tab: it lists issues as missions; either show real missions or
    rename the section and remove the duplicate list.
17. Retry on every error state (`CanvasShell`, the 13 dock tabs, skills load,
    integrations channels band, prefs) and stop coercing fetch failures to
    empty arrays in `use-agent-relations.ts` and `connection-detail.tsx`.
18. `AlertDialog` for crew/agent delete, connection delete, Composio
    revoke/remove, skill uninstall — say what is lost and where to recover.
19. URL as state: `?tab=` on both canvases, `?dock=` for the bottom panel,
    `?id=` written on `/credentials`, `?q=&tab=&domain=` on `/skills`,
    `?connection=` on `/integrations`.
20. Credentials readiness: raise `MAX_CREWS` or say "24 of 103 crews checked";
    make "Needs attention" mean one thing in the rail and the overview.
21. Composio catalog: "Showing 40 of N" with a pager, or fetch all.
22. `/integrations` search: pass it to Deliveries and Preferences, remove the
    per-tab duplicates on Tools.

**P3 — polish**

23. `useReducedMotion` on every raw `motion.*`; `layout="position"` on the
    agent header; `Appear` stagger on canvas sections.
24. Empty states as `InlineEmpty` (one line, icon, action) — "Nothing matches
    this filter" only when a filter is active, otherwise "No runs yet — start
    one from chat →".
25. Replace raw `<button>`s with `Button`; the Files tab primary's hover state
    equals its rest state.
26. Copy: remove "(PRD §6 F2 / F4.2)", "harbormaster sync", `tool_profile`,
    "server-side", the env-var flag names, the "We're replacing self-hosted
    MCP" roadmap sentence, "v0.1.0-beta", "Bulk operations live in the CLI"
    furniture; "1 crews" pluralisation; `PM` → "PM" not "Pm".
27. Skills: mobile facet rail reachable, keyboard/touch resize, one source map
    for `SOURCE`/`MATURITY` labels, batch the uninstall dialog's agent-skill
    lookups into one call.
28. Dead code to delete before anyone styles it: ~4.3k lines under
    `components/features/crews/` with no importer (`crew-members`,
    `crew-assignments`, `crew-journal`, `crew-card` — links to a non-existent
    `/crews/<id>` —, `crew-agents`, `crew-missions`, `crew-peer-conversations`,
    `schedule-editor`, `agents/agent-card`, …), plus `marketplace.tsx`, all of
    `components/features/connectors/`, `mcp/components/server-card.tsx`,
    `credentials/add-credential-dialog.tsx`. Note the dead files hold the
    better patterns (`AlertDialog`, `EmptyState`, `Table`).

## 7. What is already right (keep)

- `/credentials`: `SubBar` with live counts, KPI + donut + attention queue,
  `Appear` stagger, skeleton matching geometry, Retry on load failure,
  `AlertDialog` bulk delete, mobile rail overlay, the add wizard (visible
  blocker line, partial-failure band, "Use SUGGESTED_NAME" one-click fix), the
  reveal ceremony's visible conditions, hidden-not-disabled Reveal with the
  exact gate named.
- Agent Overview's three-band structure ("What it holds / can do / has been up
  to") and its filter chips.
- Create-agent's `validationHint`; create-crew's container warnings.
- Provisioning banner: Retry / Dismiss / Build now.
- Explorer's one-mark-per-state status dots and the guide-line nesting.
- `/skills` search, virtualisation and result count.

## 8. Assumptions and gaps in this audit

- Verified with `CREWSHIP_SKIP_SIDECAR=1` and no Docker, so no agent ran:
  RUNNING/ERROR states, the dock's Terminal/Exec tabs and Composio with a real
  key were judged from code, not screenshots.
- Ten of the 100 seeded credentials (`OPENAI` API keys) show "expired" because
  the server probed them (`last_error: Authentication failed (401)`) and set
  `status: EXPIRED`; the word "expired" for a key that never worked is a copy
  question for `lib/credentials/overview.ts:104-107`, not a seeding error.
- Light theme was not screenshotted.
- The legacy `/integrations` page was mapped from code only (flag off).
- Port 8095 from the brief was taken; 8098 was used and the throwaway server was
  stopped after the run (`start.sh` and `scenarios*.json` in the scratchpad
  rebuild it).
