# Crewship -- Connections Epic Structure (CONNECTIONS-EPICS.md)

**Verze:** 1.0
**Datum:** 2026-05-04
**Status:** Draft -- pripraveno pro Linear epic creation
**Companion doc:** `CONNECTIONS.md` (full PRD)

> Pet epicu, 26 ticketu. Estimates: XS = pul dne, S = 1-2 dny, M = 3-5 dni, L = 1-2 tydny.
> Priority: P0 = MVP-blocker, P1 = MVP must-have, P2 = MVP nice-to-have.

---

## Critical path

```
EPIC 1 (BE foundation) ──┬─→ EPIC 2 (Credentials FE)
                         ├─→ EPIC 3 (Marketplace FE)
                         └─→ EPIC 4 (Lifecycle)

EPIC 2 + EPIC 3 ─────────→ EPIC 5 (Recipes)
```

**EPIC 1 musi prvni** -- bez datove vrstvy budou FE prototypy lhat. EPIC 5 zavisi na hotovem credential + MCP API, takze posledni.

---

## EPIC 1: Connections data foundation (BE)

**Goal.** Datova a API vrstva pro vsechny novy features: status taxonomie, per-tool granularita, kuratovany registry, rotation lifecycle, audit signal. Bez breaking changes na existujicich endpointech.

**Owner.** Backend Go.

**Estimate.** ~3 weeks total.

### Tickets

#### CRE-XXX-1.1 -- DB migration: status taxonomy + audit signal columns
**Priority:** P0 -- blocks vse ostatni
**Estimate:** S
**Acceptance:**
- [ ] `internal/database/migrate.go` migration vXX prida do `credentials`: `last_used_ips TEXT` (JSON array, max 5), `expires_at TEXT` (ISO 8601 UTC)
- [ ] Status enum rozsireny o `DETECTED` a `STALE` (computed v Go layer, ne DB column -- Stale = `last_used_at < now - 90d`)
- [ ] Backfill: `expires_at = created_at + 365d` pro existujici rows (matchuje GitLab default)
- [ ] Migration rollback: drop columns
- [ ] Test: `migrate.Test_vXX_credentials_audit` pokryva forward + backward

#### CRE-XXX-1.2 -- mcp_tool_bindings table + CRUD endpoints
**Priority:** P0 -- per-tool toggles jsou diferenciator
**Estimate:** M
**Acceptance:**
- [ ] Migration vXX+1 vytvori `mcp_tool_bindings` (schema viz CONNECTIONS.md sekce 3.5)
- [ ] `GET /api/v1/crews/{cid}/integrations/{sid}/tools` vraci list (z DB nebo live z `mcp/list-tools`)
- [ ] `PATCH /api/v1/crews/{cid}/integrations/{sid}/tools/{tool}` toggle enabled
- [ ] `POST /api/v1/crews/{cid}/integrations/{sid}/tools/refresh` re-fetch z MCP serveru
- [ ] Sidecar respektuje `enabled = false` -- tool je vyfiltrovan z tools dostupnych agentovi
- [ ] Tests: handler tests + integrace test ktery overi ze disabled tool neni v `mcp/list-tools` agent payload

#### CRE-XXX-1.3 -- mcp_registry curated table + Smithery sync job
**Priority:** P0 -- bez kuratorovaneho registry je marketplace prazdny
**Estimate:** L
**Acceptance:**
- [ ] Migration vXX+2 vytvori `mcp_registry` (schema viz CONNECTIONS.md sekce 3.5)
- [ ] Seed: 50+ initial entries (top Smithery + manualni Anthropic-verified)
- [ ] `GET /api/v1/mcp-registry?category=&trust_tier=&q=&featured=` vraci filtrovane vysledky
- [ ] `POST /api/v1/mcp-registry/sync` (admin only) -- fetch z upstream Smithery (`registry.smithery.ai/v1/servers`), upsert s `upstream_source = 'smithery'`
- [ ] `GET /api/v1/mcp-registry/{id}` vraci detail vc. tool list (zjisteno z `package_name`'s mcp.json)
- [ ] Sync nikdy neprepise `trust_tier` ani `is_featured` (manualni curation)
- [ ] Tests: sync idempotency, conflict resolution, search relevance

#### CRE-XXX-1.4 -- credential_audit table + last_used IP tracking
**Priority:** P0 -- last-used signal je table-stake (GitLab/GitHub/Stripe)
**Estimate:** M
**Acceptance:**
- [ ] Migration vXX+3 vytvori `credential_audit` (event_type, credential_id, agent_id, ip_address, occurred_at, metadata_json)
- [ ] Sidecar pri kazdem credential use posle event (debounced 60s per credential, ne kazdy http call)
- [ ] Po prijmu eventu update `credentials.last_used_at` + push IP do `last_used_ips` ringbuffer (max 5)
- [ ] Detail Sheet API endpoint vraci posledni 50 audit eventu pro credential
- [ ] Tests: ringbuffer behavior (push 6 IPs, last 5 stay), debounce window

#### CRE-XXX-1.5 -- credential_rotations table + grace overlap logic
**Priority:** P0 -- biggest enterprise diferentiator
**Estimate:** L
**Acceptance:**
- [ ] Migration vXX+4 vytvori `credential_rotations` (schema viz CONNECTIONS.md sekce 3.5)
- [ ] `POST /api/v1/credentials/{id}/rotate` -- {value, grace_seconds (0|86400|custom)} -- vytvori rotation row, update credential.value
- [ ] `GET /api/v1/credentials/{id}/rotations` -- history
- [ ] `DELETE /api/v1/credential-rotations/{id}` -- cancel grace overlap (delete old_value immediately)
- [ ] Cron job (kazdou hodinu) maze old_value pri `expires_at <= now`, sets `status='EXPIRED'`
- [ ] Sidecar fallback: pri 401 + active rotation -> retry s old_value, mark `last_error` na credentialu pokud i to selze
- [ ] Tests: rotation lifecycle, fallback retry, cron expiration

#### CRE-XXX-1.6 -- Recipes API (hardcoded for MVP)
**Priority:** P1
**Estimate:** S
**Acceptance:**
- [ ] `internal/recipes/recipes.go` -- 3 hardcoded recepty (Code review / Triage / Research)
- [ ] `GET /api/v1/recipes` vraci list
- [ ] `POST /api/v1/recipes/{slug}/install?workspace_id=...` -- prijima `{credentials: {ANTHROPIC_API_KEY: "...", ...}}` body, atomic create credentials + MCP server + crew (vse v jedne tx, rollback pri partial fail)
- [ ] Tests: atomicita pri credential test failure, dry-run preview endpoint

---

## EPIC 2: Credentials surface redesign (FE)

**Goal.** `/credentials` page redesign. Shell + list + detail Sheet + Add wizard + bulk operations.

**Owner.** Frontend.

**Depends on:** EPIC 1 (CRE-XXX-1.1, 1.4, 1.5).

**Estimate.** ~2.5 weeks.

### Tickets

#### CRE-XXX-2.1 -- Credentials shell: KPI strip + tabs + filters + provider grouping
**Priority:** P0
**Estimate:** M
**Acceptance:**
- [ ] `app/(dashboard)/credentials/page.tsx` rewrite: PageShell + KpiCard strip (4 cards) + 2-tab strip + filter chip row
- [ ] KPI counts pocitane z `credentials` array client-side (Active/Expiring30d/Errors/LinkedAgents)
- [ ] Provider grouping: collapsible `<details>` per `provider`, default expanded if `<5 groups`
- [ ] Filter chips (Provider/Scope/Type) jsou native `Select` komponenty z `components/ui/select.tsx`
- [ ] Mobile: KPI strip vertical stack, tabulka schovana ve prospech list-row varianty
- [ ] `motion/react` AnimatePresence na tab transition

#### CRE-XXX-2.2 -- Credential row component s status taxonomy + last-used signal
**Priority:** P0
**Estimate:** M
**Acceptance:**
- [ ] `components/features/credentials/credential-row.tsx` (nova komponenta)
- [ ] 5-color pulse dot (Available gray / Detected blue / Connected emerald / Error red / Stale amber)
- [ ] Brand logo 24 px (provider-icons + react-icons/si)
- [ ] Masked last-4 chars (`(...4a2f)`)
- [ ] Last-used relative time
- [ ] Used-by chip (klik otevre Sheet -- viz 2.3)
- [ ] Hover/select checkbox (pro bulk -- viz 2.5)
- [ ] Ellipsis menu: Edit / Rotate / Move scope / Revoke

#### CRE-XXX-2.3 -- Credential detail Sheet (4 tabs)
**Priority:** P0
**Estimate:** M
**Acceptance:**
- [ ] `components/features/credentials/credential-detail-sheet.tsx`
- [ ] Sheet 480 px wide, vyjede zprava (`components/ui/sheet.tsx`)
- [ ] Tab `Overview`: provider banner, name, type, scope, created/last_used, last_5_ips list, "Test now" button, "Show value" toggle s 60s timer
- [ ] Tab `Used by`: list agents s checkboxes (PATCH agent_credentials inline) + "Used by MCP servers" sekce
- [ ] Tab `Audit`: timeline merged (created/rotated/used/revoked) -- z `credential_audit` API endpointu
- [ ] Tab `Settings`: rename, change scope, change account_label, "Rotate..." button (otevre Rotation dialog)
- [ ] AlertDialog na destructive actions (no window.confirm)

#### CRE-XXX-2.4 -- Add Credential wizard 4 steps (Crew-style)
**Priority:** P0
**Estimate:** L
**Acceptance:**
- [ ] `components/features/credentials/add-credential-wizard/` slozka, soubory `add-credential-wizard.tsx` + `step-provider.tsx` + `step-auth.tsx` + `step-paste-test.tsx` + `step-identity.tsx`
- [ ] Sheet 720 px wide (ne Dialog -- vetsi prostor)
- [ ] Stepper strip s blue ring/emerald check, klikatelny zpet
- [ ] Step 1: 12+ provider tile grid 4-col, search, "Recently used" chip
- [ ] Step 2: auth method karty vyplyvajici z provider state
- [ ] Step 3: monospace input + auto-test debounced 800ms + bulk .env import textarea
- [ ] Step 4: required account_label (validation), scope karty, agent pre-assign matrix s "Suggested" highlight
- [ ] ⌘+Enter shortcut, footer hint, Skip to defaults na Step 4
- [ ] `submittingRef` antidouble-submit (kradni z Crew wizardu)
- [ ] Refresh `useCredentials` hook po success
- [ ] Stary `add-credential-dialog.tsx` smazat

#### CRE-XXX-2.5 -- Bulk multiselect + AlertDialog cleanup
**Priority:** P1
**Estimate:** M
**Acceptance:**
- [ ] Tristate checkbox v list header (none/some/all)
- [ ] Selected count + floating bottom action bar (Rotate / Move scope / Revoke / Cancel)
- [ ] Bulk Rotate dialog -- pro N credentials, batch mode
- [ ] Bulk Move scope dialog -- Workspace / Crew picker
- [ ] Bulk Revoke -- AlertDialog s pocitadlem ("Revoke 5 credentials?")
- [ ] Vsechny `window.confirm` v `app/(dashboard)/credentials/page.tsx` nahrazeny AlertDialog

#### CRE-XXX-2.6 -- Mobile sheet overlay
**Priority:** P2 -- nice-to-have, ale rychly win
**Estimate:** S
**Acceptance:**
- [ ] Pri viewport `<md`: skryje provider grouping headers, list-row stack
- [ ] Detail Sheet ma full-width na mobile
- [ ] 44 px hit target na ellipsis menu icons
- [ ] Test pres `useIsMobile` hook (existuje v `hooks/use-mobile.tsx`)

---

## EPIC 3: MCP Marketplace (FE+BE)

**Goal.** `/integrations` redesign s tab strip Connected/Marketplace, full marketplace browse, Add MCP wizard, detail Sheet s per-tool toggles.

**Owner.** Frontend (s BE pomoci na CRE-XXX-3.5).

**Depends on:** EPIC 1 (CRE-XXX-1.2, 1.3).

**Estimate.** ~3 weeks.

### Tickets

#### CRE-XXX-3.1 -- /integrations tab strip + Connected polish
**Priority:** P0
**Estimate:** S
**Acceptance:**
- [ ] `app/(dashboard)/integrations/page.tsx` ma tab strip `Connected` / `Marketplace`
- [ ] Connected tab: existujici list polish s brand logos 40 px (misto Globe/Terminal generic)
- [ ] Per-server per-tool count chip ("12/20 tools enabled") -- klik otevre detail Sheet na Tools tab
- [ ] Hot-swap reload icon na rowu -- klik volume `tools/refresh` endpoint

#### CRE-XXX-3.2 -- Marketplace tab UI: sidebar + featured + grid
**Priority:** P0
**Estimate:** L
**Acceptance:**
- [ ] `components/features/integrations/marketplace.tsx`
- [ ] Levy sidebar 180 px: kategorie s counts (z `mcp_registry` API), Verified-by sub-filter
- [ ] Top: search debounced 300ms (existing pattern z `registry-browser.tsx`)
- [ ] Filter chip row: Transport / Auth / Trust
- [ ] Featured row: 5-6 vetsich karet (`is_featured = true`)
- [ ] Card grid 3-col: 48 px logo + name + 1-line desc + transport badge + auth chip + trust chip + "Install" button
- [ ] `motion/react` na tab + grid item enter

#### CRE-XXX-3.3 -- MCP detail Sheet s Tools tab + per-tool toggles
**Priority:** P0 -- diferentiator
**Estimate:** M
**Acceptance:**
- [ ] `components/features/integrations/mcp-detail-sheet.tsx` (Sheet, ne dialog)
- [ ] Tabs: Overview / Tools / Logs / Settings
- [ ] Tools tab: list z `tools` API, per-tool checkbox toggle (PATCH endpoint), description z DB
- [ ] Warning banner pri agent ma > 40 enabled tools (Cursor pattern: "Many active tools degrade quality...")
- [ ] Logs tab: ringbuffer 50 last calls (in-memory na BE -- jednoduchy WebSocket stream nebo polling)
- [ ] Settings tab: rename, change crew, change credential, hot-swap reload, delete (AlertDialog)
- [ ] Refresh tools button volume `tools/refresh`

#### CRE-XXX-3.4 -- Add MCP wizard 4 steps
**Priority:** P0
**Estimate:** L
**Acceptance:**
- [ ] `components/features/integrations/add-mcp-wizard/` slozka
- [ ] Sheet 720 px, stepper jako u Add Credential
- [ ] Step 1: 3 source karty (Marketplace / Template / Custom)
- [ ] Step 2: dle source. Custom forma s Advanced settings disclosure (OAuth Client ID/Secret pod toggle). Vpravo zivy YAML preview (recyklovat `MissionYamlEditor` v read-only).
- [ ] Step 3: Credential Picker (existujici komponenta) NEBO inline +Add nested wizard NEBO OAuth button
- [ ] Step 4: crew picker, agent multiselect, **per-tool toggle list (default all enabled)**, Test connection button vola `mcp/test`
- [ ] Stary `template-popover.tsx` smazat (nahrazen marketplace tabem)

#### CRE-XXX-3.5 -- Trust tier badges + tooltip + filter
**Priority:** P1
**Estimate:** S
**Acceptance:**
- [ ] `components/features/integrations/trust-tier-badge.tsx`
- [ ] 3 variants (verified-anthropic blue / verified-crewship emerald / community gray)
- [ ] Tooltip s pevnym copy + link na `/docs/mcp-trust-tiers`
- [ ] Filter chip "Trust" v marketplace -- multi-select z 3 tiers
- [ ] Renderuje se na: marketplace card, detail Sheet header, connected list row
- [ ] BE: vytvori se MD doc v `.claude/context/docs/mcp-trust-tiers.md` (viewable in app via existing `/docs` route)

#### CRE-XXX-3.6 -- Brand logos napric Connected + Marketplace
**Priority:** P1
**Estimate:** S
**Acceptance:**
- [ ] `components/icons/mcp-logos.tsx` (nova) sjednoti react-icons/si + lucide fallback
- [ ] Logo lookup: registry name -> icon component (mapuje SiGithub, SiSlack, SiLinear, ...)
- [ ] Pouziti na: marketplace card, connected row, detail Sheet header, Add MCP wizard Step 1 tile grid

---

## EPIC 4: Credential lifecycle (FE+BE)

**Goal.** Rotation UX, expiration policy, iCalendar feed, proactive emails, hot-swap reload.

**Owner.** Backend + Frontend.

**Depends on:** EPIC 1 (CRE-XXX-1.5).

**Estimate.** ~2.5 weeks.

### Tickets

#### CRE-XXX-4.1 -- Rotation dialog s grace overlap UI
**Priority:** P0 -- biggest enterprise win
**Estimate:** M
**Acceptance:**
- [ ] `components/features/credentials/rotation-dialog.tsx`
- [ ] Inputs: new value (s auto-test), grace overlap radio (Immediate / 24h / Custom hours)
- [ ] "After grace expires" infobox vysvetluje co se stane
- [ ] PATCH `/api/v1/credentials/{id}/rotate` s `{value, grace_seconds}`
- [ ] Po success toast s linkem "Cancel grace overlap" (DELETE `/credential-rotations/{id}`)
- [ ] Rotation history v Settings tab detail Sheet (z `GET /credentials/{id}/rotations`)

#### CRE-XXX-4.2 -- Expiration policy: input pri Add, "Expires in Xd" badge, Needs Attention zaradit
**Priority:** P0
**Estimate:** S
**Acceptance:**
- [ ] Add Credential wizard Step 4 ma `expires_at` date picker s default `+365d`
- [ ] Edit Credential umoznuje zmenit expires_at
- [ ] List row ukazuje "Expires in 7d" amber badge pokud `expires_at < now + 30d`
- [ ] "Needs attention" tab obsahuje credentials s `expires_at < now + 30d`
- [ ] Status `Error` automaticky pokud `expires_at < now`

#### CRE-XXX-4.3 -- iCalendar feed pro expirace
**Priority:** P1
**Estimate:** M
**Acceptance:**
- [ ] `internal/api/credentials_ical.go` -- `GET /api/v1/credentials/expirations.ics?token=<calendar_token>`
- [ ] Anonymous-token-based auth (ne JWT, aby slo include do kalendare)
- [ ] User si vygeneruje token v Settings → "Calendar feed"
- [ ] Vraci VCALENDAR + VEVENTs pro vsechna `expires_at` userem ownednutych credentials
- [ ] FE: button v Settings copy URL feed
- [ ] Test: validni .ics format, parsovatelny via npm `ical.js`

#### CRE-XXX-4.4 -- Proactive expiry email at 60/30/7/1 days
**Priority:** P1
**Estimate:** M
**Acceptance:**
- [ ] Cron job kazdy den 00:00 UTC (existujici scheduler z `internal/orchestrator/`)
- [ ] Najde credentials s `expires_at` v {60, 30, 7, 1} days from now
- [ ] Posle email ownerovi (potrebuje email channel -- ADR check, mozna vytvorit basic SMTP integraci)
- [ ] Idempotency: tabulka `credential_expiry_notifications` (credential_id, days_before, sent_at) zabranuje duplicitam
- [ ] Test: 60d vzdy posila pred 30d, ne dvakrat

#### CRE-XXX-4.5 -- Hot-swap reload bez restartu
**Priority:** P2 -- Paperclip pattern, nice-to-have
**Estimate:** L
**Acceptance:**
- [ ] BE: sidecar prijma SIGHUP equivalent (HTTP `POST /reload`) -> znovu nacte mcp config bez restartu
- [ ] Orchestrator endpoint `POST /api/v1/crews/{cid}/integrations/{sid}/reload` -- iteruje pres bezici agent kontejnery v crew, posila reload signal
- [ ] FE: "Reload" button v MCP detail Sheet → Settings, taky icon na connected list row
- [ ] Toast: "Reloaded on N agents"
- [ ] Test: bezici agent po reload ma updated tools list bez restartu

#### CRE-XXX-4.6 -- Last-used signal + IP audit display
**Priority:** P0 -- table-stake
**Estimate:** S
**Acceptance:**
- [ ] Credential row ukazuje `last_used_at` relative
- [ ] Detail Sheet Overview tab ma "Last 5 IPs" sekci s timestamp
- [ ] Stale status (last_used > 90d) computed v Go layer
- [ ] FE pri renderu zaroven flaguje "First time used from this IP" warning (porovnani s last_used_ips)

---

## EPIC 5: Recipes & onboarding (FE)

**Goal.** Empty state s 3 hardcoded receptami + 1-click bulk install flow + auto-detect recommendations.

**Owner.** Frontend.

**Depends on:** EPIC 1 (CRE-XXX-1.6), EPIC 2 (Add wizard), EPIC 3 (Add MCP wizard).

**Estimate.** ~1.5 weeks.

### Tickets

#### CRE-XXX-5.1 -- Recipes empty state na /dashboard
**Priority:** P1
**Estimate:** S
**Acceptance:**
- [ ] `components/features/dashboard/recipes-cards.tsx`
- [ ] 3 hardcoded recepty z `GET /api/v1/recipes` -- velke karty s preview ("This will create: 1 crew, 1 MCP server, 2 credentials")
- [ ] Zobrazeno na `/dashboard` jen pokud workspace ma 0 crews
- [ ] Klik karta -> recipe install Sheet (viz 5.2)

#### CRE-XXX-5.2 -- Recipe install flow (3-step Sheet)
**Priority:** P1
**Estimate:** M
**Acceptance:**
- [ ] `components/features/recipes/recipe-install-sheet.tsx`
- [ ] Sheet 720 px, 3 steps
- [ ] Step 1 Preview: dry-run zobrazi co se vytvori (crew name, MCP servers, required credentials)
- [ ] Step 2 Credentials: jen ty co jeste workspace nema -- pasted formular per credential, auto-test
- [ ] Step 3 Confirm: "Install Code Review crew" primary -> POST `/api/v1/recipes/{slug}/install`
- [ ] Post-install: redirect na `/crews?crew=<new_slug>` s success toast

#### CRE-XXX-5.3 -- Auto-detect recommendation banner
**Priority:** P2
**Estimate:** M
**Acceptance:**
- [ ] BE: `internal/orchestrator/exec.go` pri 401/missing-credential exception zapisuje hint do `credential_audit` s `event_type='DETECTED'`
- [ ] FE: list ma sekci "Detected" s blue status -- credentials co agent potrebuje ale nema
- [ ] Klik na Detected row otevre Add Credential wizard prefilled na zjisteny provider/type
- [ ] Datadog parallel: "we see your crew references GitHub MCP -- install GitHub credential?"

#### CRE-XXX-5.4 -- Marketplace empty-state recipes
**Priority:** P2
**Estimate:** S
**Acceptance:**
- [ ] `/integrations` Marketplace tab pri zero connected ma sekci "Get started" se 3 recepty
- [ ] Klik vede do recipe install flow (sdileny komponent z 5.2)

#### CRE-XXX-5.5 -- Onboarding tour update
**Priority:** P2
**Estimate:** S
**Acceptance:**
- [ ] Existujici onboarding z `app/(onboarding)/` rozsireny o krok "Connect your first credential"
- [ ] Vede primo do Add Credential wizardu (Step 1 pre-selected pro Anthropic)
- [ ] Po dokoncene credential -> doporuci recipe na dashboardu

---

## Cross-epic dependencies

```
EPIC 1.1 (status taxonomy) ─┬─→ EPIC 2.2 (row component)
                            └─→ EPIC 4.2 (expiration)

EPIC 1.2 (mcp_tool_bindings) ─→ EPIC 3.3 (per-tool toggles)
                              └→ EPIC 3.4 (Add MCP Step 4)

EPIC 1.3 (mcp_registry) ─→ EPIC 3.2 (Marketplace UI)
                        └→ EPIC 3.5 (Trust tiers)

EPIC 1.4 (credential_audit) ─→ EPIC 2.3 (Detail Sheet Audit tab)
                             └→ EPIC 4.6 (last-used display)
                             └→ EPIC 5.3 (auto-detect)

EPIC 1.5 (credential_rotations) ─→ EPIC 4.1 (Rotation dialog)

EPIC 1.6 (recipes API) ─→ EPIC 5.1, 5.2
```

---

## Estimated total

| Epic | Effort | Priority Sum |
|---|---|---|
| EPIC 1 (BE foundation) | ~3 weeks | 5 P0 + 1 P1 |
| EPIC 2 (Credentials FE) | ~2.5 weeks | 5 P0 + 1 P2 |
| EPIC 3 (Marketplace) | ~3 weeks | 4 P0 + 2 P1 |
| EPIC 4 (Lifecycle) | ~2.5 weeks | 3 P0 + 2 P1 + 1 P2 |
| EPIC 5 (Recipes) | ~1.5 weeks | 2 P1 + 3 P2 |
| **Total** | **~12 weeks** | **17 P0 + 6 P1 + 3 P2** |

S 1 BE + 1 FE plne na to (paralelne) ~ 6-8 weeks calendar time. EPIC 1 musi byt prvni a je hot path.

---

## Definition of Done (per epic)

- [ ] Vsechny P0 tickets shipped
- [ ] PRD acceptance criteria z `CONNECTIONS.md` § 9 splneny pro tu sekci
- [ ] Test coverage: `go test ./...` zelene, vitest pass, e2e na klicovem flow
- [ ] Doc update: `MEMORY.md` index, `architecture.md` pokud zmena, wireframes pokud relevantni
- [ ] CodeRabbit review hotovy (rounds 1-3 max), no blocking issues
- [ ] Screenshot na PR -- pred/po pro UI epicy

---

## Out-of-MVP backlog (ulozit do future-work, neimplementovat)

- Per-environment scope (Vercel pattern Production/Preview/Development)
- JIT auth from running agent (Composio pattern, opt-in)
- Field mapping UI (Paragon pattern)
- Per-conversation tool toggles (Claude.ai pattern)
- mTLS / HSM credential types (EE tier)
- OAuth scope-matrix pro 3rd-party providers (DO-NOT-BUILD #1)
- Custom recipe authoring (zatim hardcoded v Go)
- Per-credential observability stack (latency p95, requests/min) -- ringbuffer zatim staci
- Audit log full-text search (basic filter staci pro MVP)

Vytvorit Linear issue label `connections-future` a zaradit pri prislusne diskuzi.
