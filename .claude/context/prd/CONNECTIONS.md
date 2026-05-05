# Crewship -- Connections (CONNECTIONS.md)

**Verze:** 1.0
**Datum:** 2026-05-04
**Autor:** Pavel Srba + AI synthesis
**Status:** Draft -- planovany scope pro MVP redesign credentials + integrations + MCP marketplace
**Vztah k existujicim PRD:** rozsiruje SECURITY.md (credential vault), SIDECAR.md (credential injection), PRD.md (US-07 sprava credentials, CRED-01..07)

> **Skills out-of-scope.** Paralelni iteracni vlakno (`feat/skills-bootstrap`) zpracovava skills surface. Tento dokument se skills nezabyva.

---

## 1. Co je "Connections"?

"Connections" je sjednoceny mentalni model pro tri datove tridy ktere agentum umoznuji volat ven:

- **Credential** -- API klic, OAuth token, setup-token, secret. Sifrovany at-rest (AES-256-GCM, viz SECURITY.md).
- **MCP Server** -- Model Context Protocol server (stdio nebo HTTP transport) ktery exposuje sadu tools agentum. Vyzaduje credential.
- **Tool** -- jednotliva schopnost (`github.create_pr`, `linear.list_issues`, ...) kterou MCP server publikuje.

Aktualne mame credentials a integrations jako dve oddelene URL routes (`/credentials`, `/integrations`). Z pohledu uzivatele to jsou ale provazane vrstvy jednoho produktoveho povrchu. Tento dokument specifikuje **redesign obou stranek a jejich datove vrstvy** tak aby:

1. Vizualni jazyk odpovidal kvalite orchestrace (`/orchestration`)
2. Add/edit dialogy mely DNA Crew/Agent wizardu (stepped, dark zinc-950, blue accent, hravost, ⌘+Enter)
3. UX skaloval z 14 hardcoded MCP sablon na 200+ kuratorovany marketplace
4. Per-tool granularita umoznila bezpecne povoleni jednotlivych nastroju
5. Credential lifecycle (rotation, expiration, audit) splnoval enterprise standardy (GitLab/GitHub/Stripe)

**URL routes zustavaji oddelene** (`/credentials` + `/integrations`); "Connections" je internal mentalni model + spolecna PRD vrstva, ne sjednoceny route prefix.

---

## 2. Competitive positioning

### 2.1 Paperclip neni konkurent na teto plose

Paperclip (62k+ GitHub stars, paperclip.ing, MIT) je org-chart/governance/budget-tracking platforma. Z file-by-file review:

- `ui/src/pages/*` -- zadny `Credentials.tsx`, `Integrations.tsx`, `MCP.tsx`, `Marketplace.tsx`. Nejblizsi: `PluginManager.tsx` (paste npm name, install), `AdapterManager.tsx`, `CompanyEnvironments.tsx`.
- `server/src/secrets/*` -- 4 soubory (`external-stub-providers.ts`, `local-encrypted-provider.ts`, `provider-registry.ts`, `types.ts`) -- primitivni encrypted-KV.

**Zadny marketplace, zadny MCP koncept, zadne per-tool toggles, zadny multi-account model, zadne OAuth flows, zadny audit log scoped na credentials, zadne provider brand-logos (jen npm package names), zadne trust tiers, zadne recipes (Cliphub je announced not shipped).**

Crewship strukturalne prekonava Paperclip pres cely credentials/MCP/marketplace surface uz today. Jediny pattern z nich worth stealing: **hot-swap reload bez restartu** (na adapters/plugins). Sledovat je dale na orgchart UX a package export, ne na connections.

### 2.2 Realni benchmarky

| Konkurent | Co od nich kopirovat |
|---|---|
| **Cursor** | In-chat tool toggles + warning pri ~40 active tools |
| **Datadog** | 700+ integrations directory + 4-state status taxonomy (`Available / Detected / Connected / Error / Stale`) + auto-detect rec |
| **GitLab** | PAT lifecycle: rotation s grace overlap + last-used + last 5 IPs + iCalendar expiration feed + proactive expiry email at 60/30/7 days |
| **GitHub** | Fine-grained PAT scope picker (Resource Owner → Repo Access → Permissions ordering); Marketplace badge tooltip "Publisher domain and email verified" |
| **Stripe** | Restricted-key matrix (resource × None/Read/Write, default None) -- jen pro keys ktere vydavame my interne; one-time secret reveal copy |
| **Vercel** | Per-environment scope picker; zobrazit logo source-integration na env-var radku ktery ji provisionoval |
| **Composio** | Multi-account per provider (`allow_multiple=True`); per-tool toggle uvnitr toolkit |
| **Linear** | Terse opinionated voice -- match v UI copy |
| **Paperclip** | `supportsConfigTest === true` capability flag → conditional Test button (schema-driven UI); hot-swap reload |

### 2.3 Verdict per planovany MVP

| Cast MVP | Verdict vs best-in-class |
|---|---|
| Sheet detail s tabs | STRONGER (vetsina pouziva full page) |
| Trust-tier badges | STRONGER (zadny AI konkurent) |
| Recipes / 1-click setup | STRONGER (Paperclip Cliphub vapor) |
| Custom curated registry | STRONGER (Cursor/Continue maji jen user-pasted JSON) |
| Mobile Sheet overlays | STRONGER (Linear/Stripe/GitHub na mobilu nepouzitelne) |
| KPI strip + tabs + grouped list | PARITY (table-stakes) |
| 4-step wizardy | PARITY |
| Marketplace sidebar + grid | PARITY (Datadog) |
| Per-tool toggles | PARITY (Cursor) |
| Bulk multiselect, AlertDialog | PARITY (table-stakes) |
| **Rotation UX** | WEAKER vs GitLab -- MUST FIX |
| **Status taxonomie 5-state** | WEAKER vs Datadog -- MUST FIX |
| **Last-used + IP signal na radku** | WEAKER vs GitLab/GitHub/Stripe -- MUST FIX |
| **Per-environment scope** | WEAKER vs Vercel -- nice-to-have, ne MVP-blocker |
| **Hot-swap reload** | WEAKER vs Paperclip -- nice-to-have |

---

## 3. Mentalni model & datova architektura

### 3.1 Definice trid

**Credential**
- Vlastnost: `provider`, `type`, `scope`, `name`, `value` (encrypted), `account_label` (now required)
- Lifecycle: `status`, `last_used_at`, `last_used_ips` (last 5), `expires_at`
- Vztahy: 0..N agentu (`agent_credentials`), 0..N MCP serveru (`mcp_credentials`)

**MCP Server**
- Transport: `stdio` | `streamable-http` (s pod-aliasy `http`/`sse`)
- Scope: per-crew (existujici)
- Vztahy: 1..N tools (nove `mcp_tool_bindings`), 0..N agentu (`agent_integrations`)

**Tool**
- Identifikace: `(mcp_server_id, tool_name)`
- Stav: `enabled` (default true), `description` (z `mcp/list-tools` response)
- **Granularita**: per-server, per-crew, per-agent toggling

### 3.2 Scope hierarchie (Workspace → Crew → Agent → Tool)

| Uroven | Existuje? | Komponenta |
|---|---|---|
| Workspace | ✅ | Credential.scope = `WORKSPACE` |
| Crew | ✅ | Credential.scope = `CREW` (s `crew_ids` array); MCP Server.crew_id |
| Agent | ✅ | `agent_credentials`, `agent_integrations` |
| Tool | ❌ NEW | `mcp_tool_bindings` (server_id, tool_name, enabled) |

### 3.3 Multi-account model (Composio pattern)

Provider muze mit N credentialu se stejnym `provider` ale ruznymi `account_label`. Konsekvence:

- `account_label` se stava **povinnym** (predtim optional v `add-credential-dialog.tsx`).
- Default value autogenerate ze provideru + last-4 chars valuee: napr. `Anthropic · ...4a2f`.
- UI seskupuje credentials pod expandable section per-provider: "Anthropic (3) ▾".

### 3.4 Status taxonomie (5-state, Datadog pattern)

| Status | Vyznam | Color | Trigger |
|---|---|---|---|
| `Available` | Provider/MCP existuje v marketplace ale neni nainstalovany | gray | (jen pro marketplace, ne na /credentials) |
| `Detected` | Agent recent run pouzil tool ktery vyzaduje neexistujici credential | blue | engine zjisti pri exec.go pri 401/missing |
| `Connected` | Funguje, posledni health-check OK | emerald | po uspesnem `mcp/test` nebo prvnim API callu |
| `Error` | Posledni pouziti vratilo 4xx/5xx, rate limit, expired | red | persisted z `last_error` + `last_checked_at` |
| `Stale` | Nepouzity > 90 dni, doporucujeme revoke | amber | computed: `now - last_used_at > 90d` |

**`Detected` je active recommendation surface** -- prevadi credentials page z pasivni listy na advisory plochu.

### 3.5 Datovy model -- delty oproti aktualnimu schematu

```sql
-- Migration vXX: status taxonomy + audit signal
ALTER TABLE credentials ADD COLUMN last_used_ips TEXT;          -- JSON array, max 5 IPs
ALTER TABLE credentials ADD COLUMN expires_at TEXT;              -- ISO 8601 (UTC)
-- status uz existuje, jen rozsireni enum o 'DETECTED' a 'STALE' (computed)

-- Migration vXX+1: per-tool granularity
CREATE TABLE mcp_tool_bindings (
  id TEXT PRIMARY KEY,                   -- CUID
  mcp_server_id TEXT NOT NULL,           -- FK mcp_servers.id
  tool_name TEXT NOT NULL,               -- napr. "github.create_pr"
  description TEXT,                      -- z mcp/list-tools response
  enabled INTEGER NOT NULL DEFAULT 1,    -- bool
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(mcp_server_id, tool_name)
);
CREATE INDEX idx_mcp_tool_bindings_server ON mcp_tool_bindings(mcp_server_id);

-- Migration vXX+2: rotation lifecycle
CREATE TABLE credential_rotations (
  id TEXT PRIMARY KEY,                   -- CUID
  credential_id TEXT NOT NULL,           -- FK credentials.id
  old_value TEXT NOT NULL,               -- encrypted, kept for grace period
  new_value TEXT NOT NULL,               -- encrypted
  grace_seconds INTEGER NOT NULL,        -- 0 = immediate, 86400 = 24h, custom
  rotated_at TEXT NOT NULL DEFAULT (datetime('now')),
  expires_at TEXT NOT NULL,              -- rotated_at + grace_seconds
  rotated_by TEXT NOT NULL,              -- user_id
  status TEXT NOT NULL DEFAULT 'ACTIVE'  -- ACTIVE | EXPIRED | CANCELLED
);
CREATE INDEX idx_credential_rotations_cred ON credential_rotations(credential_id);
CREATE INDEX idx_credential_rotations_expires ON credential_rotations(expires_at);

-- Migration vXX+3: registry curation
CREATE TABLE mcp_registry (
  id TEXT PRIMARY KEY,                   -- CUID
  name TEXT NOT NULL UNIQUE,             -- "github", "linear", ...
  display_name TEXT NOT NULL,
  description TEXT NOT NULL,
  icon TEXT,                             -- ikona key (mapuje na TEMPLATE_ICONS)
  category TEXT NOT NULL,                -- productivity, dev-tools, ...
  transport TEXT NOT NULL,               -- stdio | streamable-http
  command TEXT,                          -- pro stdio
  package_name TEXT,                     -- pro npm-based stdio
  endpoint TEXT,                         -- pro http
  env_vars_json TEXT NOT NULL DEFAULT '[]',
  trust_tier TEXT NOT NULL DEFAULT 'community',  -- anthropic | crewship | community
  is_featured INTEGER NOT NULL DEFAULT 0,
  install_count INTEGER NOT NULL DEFAULT 0,      -- ne fakeujeme (DO-NOT-BUILD #4)
  upstream_source TEXT,                          -- "smithery" | "glama" | "manual"
  upstream_id TEXT,                              -- ID v upstream registry
  synced_at TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_mcp_registry_category ON mcp_registry(category);
CREATE INDEX idx_mcp_registry_featured ON mcp_registry(is_featured);
```

### 3.6 API delty

```
# Tools
GET    /api/v1/crews/{cid}/integrations/{sid}/tools                 -- list (z DB nebo live dotaz)
PATCH  /api/v1/crews/{cid}/integrations/{sid}/tools/{tool}          -- toggle enabled
POST   /api/v1/crews/{cid}/integrations/{sid}/tools/refresh         -- znovu fetch z mcp/list-tools

# Rotation
POST   /api/v1/credentials/{id}/rotate                              -- {value, grace_seconds}
GET    /api/v1/credentials/{id}/rotations                           -- history
DELETE /api/v1/credential-rotations/{id}                            -- cancel grace overlap rano

# Registry curation
GET    /api/v1/mcp-registry?category=&trust_tier=&q=&featured=      -- existuje, rozsirit o filtry
POST   /api/v1/mcp-registry/sync                                    -- admin, fetch z upstream
GET    /api/v1/mcp-registry/{id}                                    -- detail vc. tool list

# Recipes
GET    /api/v1/recipes                                              -- hardcoded, returns 3-5
POST   /api/v1/recipes/{slug}/install                               -- bulk create credentials + MCP + crew
```

---

## 4. Credentials surface (`/credentials`)

### 4.1 Shell layout

```
┌──────────────────────────────────────────────────────────────────────┐
│ Credentials                                          [+ Add Credential]│
│ Encrypted secrets, API keys, and CLI tokens for your agents          │
├──────────────────────────────────────────────────────────────────────┤
│ ┌────────┐  ┌────────┐  ┌────────┐  ┌────────┐                       │
│ │ Active │  │ Expir. │  │ Errors │  │ Linked │   <- KpiCard strip    │
│ │   12   │  │   2    │  │   0    │  │   8    │                       │
│ └────────┘  └────────┘  └────────┘  └────────┘                       │
├──────────────────────────────────────────────────────────────────────┤
│ [All]  [Needs attention 2]                  <- tab strip blue undr.  │
├──────────────────────────────────────────────────────────────────────┤
│ [Search...]  Provider▾  Scope▾  Type▾                                │
├──────────────────────────────────────────────────────────────────────┤
│ ▾ Anthropic (3)                                                      │
│   ●  ANTHROPIC_API_KEY · prod (...4a2f)  Connected · 2h ago · 4 ag.. │
│   ●  ANTHROPIC_API_KEY · dev  (...91x2)  Connected · 1d ago · 2 ag.. │
│   ●  ANTHROPIC_API_KEY · test (...zz01)  Stale · 95d ago · 0 agents  │
│ ▾ GitHub (2)                                                         │
│   ●  GH_TOKEN · personal (...abc1)  Connected · 5h ago · 3 agents    │
│   ●  GH_TOKEN · org-bot  (...d2e3)  Error · 401 Unauthorized         │
│ ▸ OpenAI (1)                                                         │
└──────────────────────────────────────────────────────────────────────┘
```

### 4.2 Row anatomy

Kazdy row obsahuje:

1. **Status pulse dot** (5 colors podle taxonomie 3.4)
2. **Provider brand-logo 24 px** (z `provider-icons.tsx` + `react-icons/si`)
3. **Name** (font-mono) `+ account_label` v sekundarnim tonu
4. **Last-4 chars valuee** v parenthesi `(...4a2f)` -- masked
5. **Status label** (`Connected` / `Error: 401 Unauthorized` / `Stale 95d` / ...)
6. **Last-used relative** (`2h ago`)
7. **Used by chip**: `4 agents` (klik otevre detail Sheet na "Used by" tabu)
8. **Hover/select checkbox** (pro bulk)
9. **Ellipsis menu**: Edit / Rotate... / Move scope / Revoke (vsechny dest. operace gated AlertDialog)

### 4.3 Detail Sheet (klik na row)

`Sheet` z `components/ui/sheet.tsx`, vyjede zprava, ~480 px wide.

Tabs:

- **Overview**: provider logo banner, name, type, scope, created_at, last_used_at, last_5_ips list, "Test now" button (volume real provider endpoint), "Show value" toggle s 60s timer.
- **Used by**: list agentu (avatar + name) s checkboxes -- inline pridani/odebrani `agent_credentials` vazby. Sekce "Used by MCP servers" ukazuje credential → server vazbu.
- **Audit**: timeline (kdo / kdy / odkud IP / jaka akce) -- merged view created/rotated/used/revoked. **Inline drawer pattern (Doppler), ne separatni stranka.**
- **Settings**: rename, change scope, change account_label, "Rotate..." button (otevre Rotation dialog -- viz 7.1), "Revoke" (AlertDialog destructive).

### 4.4 Add Credential wizard (4 steps)

Crew-wizard styling (`create-crew-dialog.tsx` reference). Komponenta = `Sheet` (ne `Dialog`, vetsi prostor pro 4 steps a tile grids). Width `sm:max-w-[720px]`.

**Step 1 -- Provider** (flip oproti aktualnimu pořadí kde se vybira typ):
- Velky provider tile grid 4-col: Anthropic / OpenAI / Google / GitHub / GitLab / Vercel / AWS / Cursor / Factory / Slack / Linear / Notion / Custom (12+ tiles)
- Kazdy tile: 64 px brand-logo, name, brand-color hover bg
- Search nahore
- Chip "Recently used" s providery z minulych 7 dni
- Vpravo: "About this provider" preview panel -- co umi, jake klice prijima, link do dokumentace
- TIP banner blue: "Vyber provider, ktery chce tvuj agent volat. Chybi ti? → Custom CLI"

**Step 2 -- Auth method** (vyplyne z Step 1):
- Anthropic: 2 karty -- `Setup token (Claude Max)` (default, doporuceno) | `Raw API key`
- GitHub: 3 karty -- `OAuth (1-click)` | `Personal access token` | `GitHub App`
- OpenAI: jen `API key` (nemaji setup-token flow)
- Slack: `OAuth (1-click)` | `Bot token`
- Karty stejne jako u Crew lineup -- border-blue na vybranem, popis pod, "recommended" badge

**Step 3 -- Paste & Test**:
- Velky monospace input s placeholder pattern (napr. `sk-ant-...`)
- **Auto-test debounced 800ms po paste** (ne manualni klik) -- volume `/api/v1/credentials/test` s `(provider, type, value)`
- Inline diagnose pri failure: "This token has the format of an old Anthropic key. Generate a new one at console.anthropic.com"
- "Show value" eye toggle, "Paste from clipboard" button
- Pro AI_CLI_TOKEN: blue TIP banner s `claude setup-token` instrukci (vetsi nez aktualni)
- **Bulk paste mode** (sekundarni link "Import from .env"): textarea, autodetect klicu jako `ANTHROPIC_API_KEY=sk-ant-...`, vytvori batch (kriticke pro onboarding)

**Step 4 -- Identity & Scope**:
- Auto-name z provideru + last-4 chars (`Anthropic · ...4a2f`) -- editovatelne
- `account_label` **required** (s validation)
- Scope toggle Workspace / Crew jako 2 velke karty (ne `<Select>`)
- Pokud Crew → multi-select crews jako chip grid pod
- Volitelne `description`, `expires_at` (default `+365d`, GitLab-style)
- **Pre-assign agents** sekce: matrix agentu z workspace s checkboxes, auto-suggest "Tyto agenty potrebuji `ANTHROPIC_API_KEY` a jeste ho nemaji" highlighted
- "Save & Test" primary; ⌘+Enter

**Footer** (vsechny kroky): hint `⌘+Enter to continue` vlevo; Cancel / Back / Continue vpravo. **Skip to defaults** na Step 4 -- vytvori workspace-scope, neassignuje agentum.

### 4.5 Bulk operations

Multi-select checkbox v hlavicce (tristate). Floating bar dole obsahuje:
- **Rotate selected** (otevre Rotation dialog v batch modu)
- **Move scope** (Workspace ⇄ Crew)
- **Revoke selected** (AlertDialog s pocitadlem)
- "Cancel" pro deselect

---

## 5. Integrations / MCP Marketplace (`/integrations`)

### 5.1 Tab strip

`/integrations` ma dve hlavni taby:
- `Connected` (default) -- soucasne UI s polished row (brand-logos, per-tool count chip)
- `Marketplace` -- novy view, browse/install

### 5.2 Connected tab -- enhancements oproti soucasnemu stavu

- Brand logo 40 px na kazdem expandable rowu (misto generic `Globe`/`Terminal` ikon)
- Status badge 5-state (sekce 3.4)
- Per-server **per-tool count chip**: `12/20 tools enabled` -- klik otevre Sheet na Tools tabu
- Hot-swap reload icon (Paperclip pattern) -- klik = re-fetch tools z `mcp/list-tools` bez restartu
- Aktualni expanded panel `ExpandedPanel` zustava, jen polish (brand logo header)

### 5.3 Marketplace tab -- novy

```
┌────────────────────────────────────────────────────────────────────────┐
│ Marketplace · 187 servers available           [+ Custom MCP server]   │
├────────────────────────────────────────────────────────────────────────┤
│  Categories         │  [Search 187 servers...]                         │
│ ───────────────     │  Transport▾  Auth▾  Trust▾                       │
│  All           187  │ ┌────────────────────────────────────────────┐  │
│  Productivity   28  │ │ FEATURED                                   │  │
│  Dev Tools      45  │ │ [GitHub] [Linear] [Slack] [Notion] [Postg.]│  │
│  Search         12  │ │ Big cards, 2-row grid, "Most installed"    │  │
│  Databases      18  │ └────────────────────────────────────────────┘  │
│  Communication  15  │  All servers                                    │
│  Cloud          22  │  ┌────────┐ ┌────────┐ ┌────────┐               │
│  AI              8  │  │ logo   │ │ logo   │ │ logo   │  3-col grid   │
│  Observability  10  │  │ Name   │ │ Name   │ │ Name   │               │
│  Self-hosted     6  │  │ desc.  │ │ desc.  │ │ desc.  │               │
│  Verified by    -   │  │ [HTTP] │ │ [stdio]│ │ [HTTP] │               │
│  ─ Anthropic    12  │  │ ✓Anthr.│ │ ✓Crew. │ │ Comm.  │               │
│  ─ Crewship     34  │  │ [Inst.]│ │ [Inst.]│ │ [Inst.]│               │
│  ─ Community   141  │  └────────┘ └────────┘ └────────┘               │
└────────────────────────────────────────────────────────────────────────┘
```

- Levy sidebar 180 px: kategorie s counts; sekundarni "Verified by" filter pod
- Top: search (debounced 300ms, full-text na name+description+category)
- Filter chips: `Transport` (any/stdio/HTTP) · `Auth` (any/OAuth/API key/None) · `Trust` (any/Anthropic/Crewship/Community)
- **Featured row** -- 5-6 vetsich karet, top of viewport
- Card grid 3-col: 48 px logo, name, 1-line description, transport badge, auth chip, trust chip, "Install" button
- Klik karta -> Sheet zprava s tabs: `Overview` / `Tools` (vsechny tools s popisy) / `Setup` (env vars, required credentials) / `Connect` (button + crew picker + agent multiselect)

### 5.4 Add MCP wizard (4 steps)

Stejny shell jako Add Credential. Width `sm:max-w-[720px]`.

**Step 1 -- Source**: 3 karty
- `Browse Marketplace` (default, vede do marketplace tab inline -- in-wizard)
- `From template` (14 curated, brand logos)
- `Custom server` (paste URL nebo command)

**Step 2 -- Configure**: dle source
- Marketplace/Template: auto-vyplnene `command`/`url`/`env`, uzivatel jen edituje
- Custom: standard form (name, transport, command/args nebo url, env JSON)
- Vpravo zivy YAML preview (recyklovat orchestration `MissionYamlEditor` v read-only modu)
- **Advanced settings** disclosure (Claude.ai pattern) -- skryva OAuth Client ID/Secret pod toggle "I have my own OAuth app"

**Step 3 -- Auth**:
- Pokud server vyzaduje credentials -> Credential Picker (existujici komponenta) NEBO inline "+ Add credential" (nested wizard) NEBO OAuth "Connect with X" button
- Pokud OAuth -> button zahaji flow primo, status "Waiting..." pak ✓ po returnu
- Pokud zadny auth -> skip karta

**Step 4 -- Assign & Test**:
- Crew picker (kde server pobezi)
- Agent multiselect (kteri maji k tools pristup)
- **Per-tool toggle list** (default all enabled, expandable)
- "Test connection" button -- vola `mcp/test`, vypise tools ktere server publikuje
- Po success: "✓ Add MCP server"

### 5.5 MCP Server detail Sheet (klik na connected row nebo install ze marketplace)

Tabs:
- **Overview**: status, transport, last_test, agent_binding_count, **per-tool count** (12/20 enabled), brand logo banner
- **Tools** *(MVP klicovy diferenciator!)*: seznam vsech tools (nazev, popis, "enable" toggle per tool, requested permissions). Pri > 40 tools enabled na agenta -> warning banner (Cursor pattern: "Cursor will send tool definitions to the model; many active tools degrade quality").
- **Logs**: posledních 50 tool calls (agent → tool → success/error → duration). Pro MVP ringbuffer v pameti, ne perzistovane.
- **Settings**: rename, change crew, change credential, hot-swap reload, delete

### 5.6 Trust tier badges

3 tiers, vzdy s tooltipem (kradni copy z GitHub Marketplace badge):

| Tier | Tooltip copy | Kdo dela verifikaci |
|---|---|---|
| `verified-anthropic` | "Verified by Anthropic — Server is in the official Anthropic MCP Registry" | Anthropic upstream |
| `verified-crewship` | "Verified by Crewship — Reviewed for compatibility and security by the crewship team" | Crewship (manualni curate) |
| `community` | "Community — Not verified by Anthropic or Crewship; review the source before installing" | nikdo |

Tooltip MUSI obsahovat link na kriteria (`/docs/mcp-trust-tiers`). Bez toho je badge placebo (Make anti-pattern).

---

## 6. Recipes (1-click setup)

`/dashboard` empty state, taky karta v `/integrations` Marketplace tab.

3 hardcoded recepty pro MVP:

1. **Code review crew** = Anthropic API key + GitHub OAuth + GitHub MCP server (configured pro `pull_requests:read`, `issues:write`)
2. **Triage crew** = OpenAI API key + Linear OAuth + Linear MCP server
3. **Research crew** = Anthropic API key + Brave Search MCP server (no auth)

Klik na recipe -> mini-wizard (1 Sheet, 3 steps):
1. Show what will be created (dry-run preview)
2. Collect credentials (only those not yet present in workspace)
3. Confirm → bulk create (atomic transakce -- vsechno nebo nic)

Recepty jsou hardcoded v Go (ne DB tabulka pro MVP) -- viz API delty (`GET /api/v1/recipes`).

---

## 7. Credential lifecycle

### 7.1 Rotation s grace overlap (STRONGER nez GitLab)

GitLab pattern: "Rotate creates a new token with identical permissions; the original becomes inactive immediately."

**Crewship pattern:** Rotate creates new token with identical permissions; **the original stays active for a configurable grace window (default 24h)** so dependent crews don't break mid-run.

Rotation dialog (otevirany z ellipsis menu na credential row, taky z detail Sheet → Settings → Rotate...):

```
Rotate ANTHROPIC_API_KEY · prod

  New value: [paste new token........................................]
             [Auto-test after paste]   ✓ Valid

  Grace overlap: ( ) Immediate (original deactivated now)
                 (•) 24 hours    (recommended)
                 ( ) Custom: [12] hours

  After grace expires:
   - Old value will be permanently deleted
   - All dependents will automatically use new value
   - Audit log entry will record the rotation

  [Cancel]                                          [Rotate]
```

Backend (`POST /api/v1/credentials/{id}/rotate`):
1. Vytvori `credential_rotations` row se starou+novou hodnotou (obojí encrypted)
2. Update `credentials.value` na novou hodnotu
3. Cron job kazdou hodinu kontroluje `credential_rotations.expires_at <= now` a maze old_value
4. Pokud uzivatel chce zrušit overlap rano: `DELETE /api/v1/credential-rotations/{id}` -- zmaze old_value okamzite

Sidecar (viz SIDECAR.md) musi umet handle "fallback to old value on 401" behem grace okna -- zkusi nejdriv `value`, pri 401 + active rotation zkusi `old_value` z `credential_rotations`.

### 7.2 Expiration policy

- `expires_at` se nastavuje pri Add Credential (default +365d, prepisat lze)
- Status taxonomie -> `Error` automaticky kdyz `expires_at < now`
- Filtry "Needs attention" tab obsahuje credentials s `expires_at < now + 30d`

### 7.3 iCalendar feed (GitLab pattern)

`GET /api/v1/credentials/expirations.ics?token=<user_calendar_token>`

Vraci iCal feed kde jsou eventy pro vsechny `expires_at` userem owned credentialů. Uzivatel si feed pripoji do Google Calendar/Outlook -> dostane warning v kalendari pred expiraci. **Read-only, anonymous-token-based** (ne JWT, aby slo include do kalendare).

### 7.4 Proactive expiry email (GitLab gold standard)

Cron job kazdy den v `00:00 UTC`:
- Najde credentials s `expires_at` v {60, 30, 7, 1} dni from now
- Posle email creditovi ownerovi (vyžaduje implementaci email channel -- viz mailer ADR)

### 7.5 Hot-swap reload (Paperclip pattern, lifecycle-adjacent)

MCP server config zmena dnes vyžaduje restart kontejneru. Po implementaci:
- "Reload" button na MCP detail Sheet → Settings
- Backend posle SIGHUP equivalent stale agent kontejnery v dotcene crew, sidecar znovu nacte mcp config bez restartu

---

## 8. Anti-patterns (DO-NOT-BUILD)

Nasledujici features konkurence ma, ale crewship je **vedome nestavi** v MVP:

1. **Žádný scope-matrix UI pro OAuth providery které neovládáme** (Anthropic, OpenAI, Slack). Jejich tokeny mají opaque scope strings; replikovat Stripe matrix UI je theatre. Matrix UI **jen pro crewship-issued internal API keys** (na admin/api-keys, mimo scope tohoto PRD).

2. **Žádný npm-paste installer marketplace** (Paperclip Plugin Manager). Curated registry s Smithery sync je správná úroveň friction.

3. **Žádné mTLS / certificate-based / HSM credential types v MVP.** Doppler/Infisical/Vault tu kategorii vlastní; defer do EE tier (CRE-88..98).

4. **Žádné install counts pokud nejsou real.** Sebe-hostovaná DB nemůže srovnat instaly napříč instalacemi. Misto toho `is_featured` flag (kurátor, ne data) -- Featured by Crewship.

5. **Žádný JIT auth jako default** (Composio anti-pattern pro self-hosted ops). Pre-provisioning model je správný; JIT auth jen jako workspace setting opt-in pro Phase 2.

6. **Žádné review/rating systémy v marketplace** (Zapier anti-pattern). Moderation surface, brigading, spam.

7. **Žádný window.confirm() na destructive ops.** AlertDialog vsude.

8. **Žádné nested tabs uvnitř tabs** (Doppler anti-pattern). Audit + History merged timeline, ne dva nested taby.

9. **Žádné per-conversation toggles bez sticky default** (Claude.ai anti-pattern). Každý toggle musí mit persistent storage.

10. **Žádný modal-stack** (Zapier anti-pattern). Wizard kroky uvnitř jednoho Sheet, OAuth redirect = full-page (ne nested modal).

11. **Žádná path-based URL navigace** (Vault anti-pattern). Credentials jsou flat, ne `/credentials/workspace/anthropic/key1`.

12. **Žádné "My credentials" / "All credentials" duální taby** (n8n anti-pattern). Jeden list, scope = filter chip.

---

## 9. Acceptance criteria per surface

### 9.1 Credentials page (`/credentials`)

- [ ] KPI strip se 4 kartami (Active / Expiring 30d / Errors / Linked agents) -- animated nums, real values
- [ ] 2-tab strip (`All` / `Needs attention` s badge count)
- [ ] Search input + 3 filter chips (Provider/Scope/Type)
- [ ] Provider-grouped collapsible list (default expanded if < 5 groups)
- [ ] Row layout: pulse dot · brand-logo 24 px · name + label · masked last-4 · status · last-used relative · used-by chip · ellipsis menu
- [ ] Status pulse dot ma 5 colors (Available/Detected/Connected/Error/Stale)
- [ ] Click row -> Sheet zprava s 4 tabs (Overview / Used by / Audit / Settings)
- [ ] Bulk multiselect s floating action bar (Rotate / Move scope / Revoke)
- [ ] Žádný `window.confirm` -- vse cez AlertDialog
- [ ] Mobile: tabulka schovana, list-row varianta s overlay Sheet

### 9.2 Add Credential wizard

- [ ] 4 kroky (Provider → Auth → Paste & Test → Identity & Scope)
- [ ] Stepper strip s blue ring na current, emerald check na done, klikatelny zpet
- [ ] Step 1: 12+ provider tile grid s brand logos 64 px, search, "Recently used" chip
- [ ] Step 2: auth method karty vyplyvajici z provideru
- [ ] Step 3: monospace input + auto-test debounced 800ms + bulk .env import
- [ ] Step 4: required account_label, scope cards (Workspace/Crew), agent pre-assign matrix
- [ ] ⌘+Enter shortcut na vsech krocich, footer hint
- [ ] Skip to defaults na Step 4
- [ ] `submittingRef` antidouble-submit pattern (z Crew wizardu)

### 9.3 Integrations / Marketplace (`/integrations`)

- [ ] Tab strip `Connected` / `Marketplace`
- [ ] Connected: brand logos 40 px, per-tool count chip, hot-swap reload icon
- [ ] Marketplace: levy sidebar 180 px s kategoriemi + counts, Verified-by sub-filter
- [ ] Marketplace: search debounced 300ms, 3 filter chips (Transport/Auth/Trust)
- [ ] Marketplace: featured row 5-6 vetsich karet
- [ ] Marketplace: card grid 3-col s logo 48 px + transport/auth/trust badges + Install button
- [ ] Klik karta -> Sheet s tabs Overview / Tools / Setup / Connect

### 9.4 Add MCP wizard

- [ ] 4 kroky (Source → Configure → Auth → Assign & Test)
- [ ] Step 1: 3 source karty (Marketplace / Template / Custom)
- [ ] Step 2: live YAML preview, Advanced settings disclosure
- [ ] Step 3: credential picker + inline +Add nested wizard + OAuth button
- [ ] Step 4: per-tool toggle list, Test connection vola mcp/list-tools

### 9.5 MCP detail Sheet

- [ ] Tabs: Overview / Tools / Logs / Settings
- [ ] Tools tab: per-tool toggle s description z mcp/list-tools
- [ ] Warning banner pri > 40 enabled tools per agent (Cursor pattern)
- [ ] Logs tab: ringbuffer 50 last calls (in-memory ok pro MVP)

### 9.6 Trust tiers

- [ ] 3 tiers (verified-anthropic / verified-crewship / community) jako badge
- [ ] Tooltip s linkem na `/docs/mcp-trust-tiers`
- [ ] Filterovatelne v marketplace

### 9.7 Recipes

- [ ] 3 hardcoded recepty na `/dashboard` empty state
- [ ] 1-click bulk install flow s dry-run preview
- [ ] Atomic create (vsechno nebo nic)

### 9.8 Lifecycle

- [ ] Status taxonomie 5-state, computed pro Stale (last_used > 90d)
- [ ] Rotation s grace overlap (Immediate / 24h / Custom hours)
- [ ] iCalendar feed `expirations.ics` s anonymous-token auth
- [ ] Proactive expiry email at 60/30/7/1 days
- [ ] Last-used timestamp + last_5_ips na kazdem rowu
- [ ] Hot-swap reload bez restartu kontejneru

---

## 10. Out of scope (Phase 2+)

- **Per-environment scope** (Vercel pattern Production/Preview/Development) -- nice-to-have, neblockuje MVP
- **JIT auth from running agent** (Composio pattern) -- workspace setting opt-in, Phase 2
- **Field mapping UI** (Paragon pattern) -- YAGNI dokud customeri neprijdou s dotazem
- **Per-conversation tool toggles** (Claude.ai pattern) -- jen pokud sticky default
- **mTLS / HSM credential types** -- EE tier
- **OAuth scope-matrix pro 3rd-party providers** -- viz DO-NOT-BUILD #1
- **Skills surface** -- paralelni iterace `feat/skills-bootstrap`

---

## 11. Reference

- Predchozi rešerše -- viz git history this PR
- Memory: `project_credentials_integrations_strategy.md` (full competitive verdict)
- SECURITY.md -- credential vault threat model
- SIDECAR.md -- credential injection flow (sidecar bude potrebovat update pro grace overlap fallback)
- DESIGN.md -- design tokens, color palette
- PRD.md US-07 + CRED-01..07 -- pristup ke credentials od Phase 1
- Crew wizard reference: `components/features/crews/create-crew-dialog.tsx` + `create-crew/step-*.tsx`
