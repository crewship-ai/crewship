# PRD — Provider logins: jedno přihlášení, N agentů

## Provozní doplnění: Google OAuth klient (2026-09-06)

Obnovování importovaných Gemini přihlášení vyžaduje serverové proměnné
`CREWSHIP_GEMINI_OAUTH_CLIENT_ID` a `CREWSHIP_GEMINI_OAUTH_CLIENT_SECRET` a restart.
Klient musí odpovídat tomu, který vydal importovaný grant; náhodně založený nový
OAuth klient existující grant nepřevezme. Konfiguraci spravuje provozovatel mimo
Git a mimo kontejnery agentů. Není to Google klient pro přihlášení do Crewship.
Bez obou hodnot se refresher neregistruje, detail uvádí chybějící konfiguraci,
ruční refresh vrací 503 a nemění počet selhání uživatelského grantu. Import a
API-klíčová cesta zůstávají dostupné; import sám nezaručuje obnovování tokenu.
Žádné upstream klientské hodnoty se nedodávají ani automaticky nestahují.
Kontext protokolu: [Google OAuth pro instalované aplikace](https://developers.google.com/identity/protocols/oauth2/native-app#offline).

## Doplnění RBAC: upozornění v inboxu (2026-09-06)

Správa provider účtů i jejich metadata jsou pouze pro OWNER/ADMIN. Upozornění
na neobnovitelné přihlášení proto cílí na roli ADMIN (včetně OWNER), nikoli
osobně na původního zakladatele: osobní cíl by zpřístupnil účet i po snížení
jeho role. Migrace upravuje také dříve uložená systémová upozornění těchto
přihlášení, včetně historie; ostatní zprávy ani jejich stav nemění.

**Status:** analýza k odsouhlasení · **Datum:** 2026-09-06
**Navazuje na:** `CREDENTIALS-VAULT.md` (typy, mount), [historical source](https://github.com/crewship-ai/crewship/blob/1a0116796acd7c1250965a9c172b90391e20e3fb/docs/prd/PRD-CREDENTIALS-V2-2026.md)
(P2 fanout, P3 bindings), [historical source](https://github.com/crewship-ai/crewship/blob/1a0116796acd7c1250965a9c172b90391e20e3fb/docs/prd/PRD-MODEL-SCOPED-CREDENTIALS-2026.md) (model policy na
credentialu)
**Cíl v jedné větě:** člověk se do providera (Anthropic, OpenAI/Codex, Google, Cursor,
Factory, …) přihlásí **jednou**, přihlášení leží v Credentials na záložce **Providers**,
a Crewship ho **rozprostře** do libovolného počtu agentů a kontejnerů — včetně toho,
že firma se čtyřmi předplatnými jednoho providera přiděluje každé jinému agentovi —
a **sám ho udržuje platné**.

Všechna tvrzení o Codexu níže jsou naměřená proti `codex-cli 0.153.2` na
crewship-dev 2026-09-06; tvrzení o kódu jsou ověřená na `a0d9d6f3` s `file:line`.

---

## 0. Rozhodnutí v kostce

1. **Provider login je vlastní pojem, ne další secret.** Secret je hodnota, kterou
   agent *použije* (DB heslo, GitHub PAT). Provider login je *seat*, kterým agent
   *platí za model* — má vlastníka, plán, kvótu, expiraci a obnovu. Dostane vlastní
   záložku **Providers** v Credentials a vlastní kartu **Provider login** v dialogu
   *Add a credential*. Datově zůstává v tabulce `credentials` (nový `type`
   + `credential_fields`), takže bindings, RBAC, audit, Keeper a revoke platí beze
   změny.
2. **Broker, ne brána.** Server vlastní přihlášení a obnovuje ho; kontejner dostane
   *odvozeninu* (env proměnnou nebo vyrenderovaný soubor) bez čehokoli, co by uměl
   rotovat. To je model, ke kterému nezávisle došly oba prozkoumané agentní runtimy (§4). Jednotná
   OpenAI-kompatibilní brána typu CLI-to-API proxy (§4.1) se **nestaví** jako hlavní cesta —
   zabila by nativní CLI (harness, sandbox, MCP), na kterých Crewship stojí.
3. **Rotující materiál nikdy neopustí server.** Refresh token je `SEALED`: nedá se
   odkrýt, nedoručuje se, neexportuje se. Do kontejneru jde jen access token
   s expirací. Ověřeno, že Codex takový `auth.json` přijme (§3.2).
4. **Fan-out = přiřazení, ne kopírování.** Čtyři Anthropic předplatná = čtyři
   provider loginy; kterému agentovi patří které, říká existující
   `credential_bindings` (WORKSPACE / CREW / AGENT + slot). Víc loginů na stejném
   scope = **pool** s prioritou, round-robinem a cooldownem — primitiva už existují
   (§2.1).
5. **Pořadí:** Codex první (je rozbitý a je jediný, který jde ověřit naživo), na něm
   vznikne abstrakce doručení, pak se na ni přepnou ostatní adaptéry (§7).

---

## 1. Proč to stojí za práci

### 1.1 Provider ≠ secret

Dnešní Credentials míchá dvě věci s různým životním cyklem:

| | Secret (GitHub PAT, DB DSN, SSH klíč) | Provider login (Claude Max, ChatGPT Plus, Cursor Pro) |
|---|---|---|
| Co to je | hodnota, kterou tool přečte | seat, kterým se platí za tokeny |
| Kdo ho vlastní | workspace | **konkrétní člověk** (ToS obou velkých providerů, §3.4) |
| Expirace | zřídka, ručně | **pravidelně** (Claude token ~1 rok, Codex 10 dní, Gemini 1 h) |
| Obnova | rotace = nová hodnota | **refresh flow** s rotujícím refresh tokenem |
| Kvóta | žádná | 5h okno + týdenní okno, per seat |
| Kolik jich firma má | jeden na službu | **několik na providera** (jeden na člověka) |
| Účtování | — | flat-rate → Paymaster `flat_rate`, žádné $ |

Jedna položka `CLAUDE_CODE_OAUTH_TOKEN` typu `ai cli` v seznamu dvanácti secretů
(viz screenshot Overview) tohle nevyjádří — nevidíš, čí to je, kdy vyprší, kolik
agentů na tom jede a jestli jsi u limitu.

### 1.2 Co je dnes rozbité (ověřeno)

| # | Vada | Kde | Důsledek |
|---|---|---|---|
| B1 | Codex se routuje přes `OPENAI_BASE_URL`, který **v Codexu neexistuje** (binárka nezná žádnou `OPENAI_*` proměnnou kromě `OPENAI_API_KEY`); `apiKeyEnvVarsForAdapter("CODEX_CLI")` vrací `nil`, takže reálný klíč do kontejneru nejde | `internal/orchestrator/exec_env.go:494`, `:1426` | Codex volá `api.openai.com` **přímo s dummy klíčem** → každý běh s API klíčem končí 401. Sidecar request nikdy nevidí. |
| B2 | Vestavěného providera `openai` nejde v Codexu přepsat (`-c model_providers.openai.base_url` → *„Built-in providers cannot be overridden"*) | — | oprava B1 musí jít přes vlastní provider id, jak to už dělá `resolveRoutedProvider` pro routované modely (`exec_env.go:847`) |
| B3 | OAuth se pozná jen podle `cred.Type == "AI_CLI_TOKEN"`, bez providera | `exec_env.go:434`, `:1273`, `:1510` | OpenAI login by se vecpal do `CLAUDE_CODE_OAUTH_TOKEN` a označil jako „Anthropic Max" |
| B4 | Subscription cesta pro Codex **není zapojená** — Codex nemá token v env, jen `$CODEX_HOME/auth.json` | `docs/guides/cli/codex.mdx:22` to říká otevřeně | s ChatGPT předplatným Codex v Crewshipu nejede vůbec |
| B5 | Label plánu natvrdo `"Anthropic Max"`; billing mód se odvozuje z jediné větve | `exec_env.go:471` | Paymaster ukazuje špatný plán pro cokoli mimo Anthropic |
| B6 | Katalog modelů: doporučený Codex model je od 2026-09-03 **GPT-6-Astra**, my nabízíme `gpt-5.5`; `pinnedNpmVersion` je `0.128.0`, reálně `0.153.2` | `config/models.json`, `cli_adapter_versions_test.go` | — |

**Upřesnění B6 po review (2026-09-07):** Astra je v katalogu volitelná,
nikoli automatický default. Dostupnost závisí na účtu, způsobu přihlášení a
rolloutu; OpenAI/Codex ponechává jako konzervativní výchozí volbu `gpt-5.5`,
aby onboarding nenutil model, ke kterému účet nemusí mít přístup. Starší
katalogové modely mohou vyžadovat API klíč. Zdroj:
[oficiální modely Codex](https://learn.chatgpt.com/docs/models).
Ověření kontraktu: `lib/__tests__/model-catalog.test.ts` a
`internal/api/onboarding_setup_crew_test.go`. Dostupnost konkrétního účtu tím
není ověřena a existujícím agentům se jejich explicitní model nepřepisuje.

### 1.3 Proč to není „zkopíruj auth.json do všech kontejnerů"

Refresh tokeny u OAuth providerů **rotují**: každá obnova vrátí nový refresh token
a starý zneplatní. Deset kontejnerů s kopií jednoho `auth.json` je deset klientů,
které obnovují nezávisle; kdo obnoví poslední, ostatním přihlášení rozbije. Pro
jednu instanci CLI se to neprojeví nikdy — proto to devět z deseti návodů radí.
Pro orchestrátor je to systémová vada, a přesně kvůli ní runtime B (§4.2) odmítá sdílet
stav s Codex CLI.

---

## 2. Ověřený současný stav

### 2.1 Co už máme a co se **použije beze změny**

| Primitivum | Kde | Role v tomhle PRD |
|---|---|---|
| `credential_bindings (scope ∈ WORKSPACE/CREW/AGENT, slot)` + unique index per scope+slot | schema (migrace P3) | **přiřazení loginu agentovi/crew/workspace** — „4 předplatná → 4 agenti" je čtyři AGENT bindings |
| `sidecar.Credential.Priority` + round-robin v nejvyšší prioritní vrstvě | `internal/sidecar/credstore.go:38-42`, `:191-290` | **pool** víc loginů na jednom scope |
| `CooldownManager` (429 → dočasně vyřadit credential) | `internal/orchestrator/failover.go` | failover uvnitř poolu |
| `credential_fields (key, value \| encrypted_value, is_secret)` + delivery `<SLOT>_<KEY>` | `internal/api/credential_fields.go`, `credential_field_delivery.go` | uložení access/refresh/id tokenu, `account_id`, plánu jako částí jednoho záznamu |
| `sensitivity = SEALED` (nikdy neodkrýt) | `credentials.sensitivity` | refresh token |
| `buildCredFileScript` + `credSecretPaths` (zápis souboru per typ, revoke = `rm -f` téhož jména) | `internal/orchestrator/exec_sidecar.go:351`, `internal/api/credential_reconcile.go:59` | **soubor v HOME agenta** (`auth.json`, `oauth_creds.json`) — nová větev v obou funkcích |
| `writeFileViaContainer(..., containerFileSecret)` — už píše `/crew/agents/<slug>/.codex/config.toml` | `internal/orchestrator/mcp_writers.go:407` | cesta i mechanismus pro `auth.json` existují |
| `credpolicy` tabulka typ → kanál, fail-safe pro neznámý typ | `internal/credpolicy/credpolicy.go:62` | nový typ dostane explicitní řádek |
| `CredentialMonitor` — periodická validace provider credentialů, status + `last_error` do DB | `internal/llmproxy/monitor.go` | **místo, kam patří refresh smyčka** (dnes jen validuje) |
| `cli_pairings` — vlastní device-code flow (RFC 8628 „in spirit") | `internal/database/migrate_consts_v86_recovery.go` | precedens pro device-code onboarding providerů |
| `SubscriptionsPanel` v Paymasteru, `CREWSHIP_BILLING_MODE=flat_rate` | `components/features/paymaster/subscriptions-panel.tsx`, `exec_env.go:471` | zobrazení flat-rate loginů — jen dostane správný label a klíčování per login |
| `llmroute` registr providerů (PathPrefix, UpstreamHost, KeyEnvVars) | `internal/llmroute/spec.go` | routování API-klíčové cesty přes sidecar |
| Brand registry s `cli: true` u 5 značek | `lib/credential-providers/registry.ts:261-283` | filtr „co je provider" pro záložku |

Závěr: chybí **pojem** (typ + záložka + karta), **doručení souboru do HOME CLI**,
**centrální refresh** a **oprava Codexu**. Nechybí přiřazování, pool, revoke, RBAC
ani audit.

### 2.2 Co je dnes vidět v UI

Screenshot Overview: `CLAUDE_CODE_OAUTH_TOKEN` je jedna položka mezi dvanácti, typ
„ai cli", tier L1, bez plánu, bez expirace, bez počtu agentů. Dialog *Add a
credential* nabízí šest tvarů (Token, Login, Key pair, SSH key, File, Certificate);
provider login se vejde jen do „Token", kde se ztratí všechno, co ho odlišuje.

---

## 3. Rešerše providerů — jak se přihlašuje které CLI

CLI adapters differ in authentication format, renewal and supported delivery.
The public binding must identify the provider/account and preserve its scope;
validate refresh against a synthetic endpoint before claiming support.
Internal account experiments are retained in private context.

## 4. Jak to řeší ostatní

Comparative product research is retained in private context. The public
contract is the provider binding, delivery, refresh and API shape below.

## 5. Cílový model

### 5.1 Pojem: `PROVIDER_LOGIN`

Nový `credentials.type = PROVIDER_LOGIN`, `provider` z AI registru. Části
v `credential_fields`:

| key | secret | poznámka |
|---|---|---|
| `access_token` | ✅ | u Claude je to celý setup-token; u Codex/Gemini krátkodobý |
| `refresh_token` | ✅, **SEALED** | jen Codex/Gemini; nikdy se nedoručuje ani neodkrývá |
| `id_token` | ✅ | jen Codex (povinné pole souboru) |
| `account_id` | ❌ | Codex `chatgpt_account_id`; Anthropic org; Google sub |
| `plan` | ❌ | `plus` / `pro` / `max` / `team` — z claimu nebo z probe |
| `expires_at` | ❌ | expirace access tokenu; řídí refresh i badge EXPIRING |
| `owner_user_id` | ❌ | kdo se přihlásil (§3.4) |
| `auth_mode` | ❌ | `subscription` \| `api_key` — API klíč **také** patří na záložku Providers (je to způsob, jak agent platí), jen s `auth_mode=api_key` a bez refreshe |

`credpolicy`: `PROVIDER_LOGIN → Delivery: per-adapter (env nebo file), KeeperGated:
false` — nový řádek, ne fallback. Stávající `API_KEY` / `AI_CLI_TOKEN` s AI providerem
se **zobrazí na záložce Providers taky** (jsou to loginy bez metadat); migrace je
dobrovolná (tlačítko „Upgrade na provider login" doplní vlastníka a plán). Nic se
nerozbije tomu, kdo nemigruje.

### 5.2 Doručení: `AuthDelivery` na adaptéru

Každý `CLIAdapter` deklaruje, jak jeho binárka čte přihlášení:

```go
type AuthDelivery struct {
    // Env: název proměnné pro tvar A ("CLAUDE_CODE_OAUTH_TOKEN", "CURSOR_API_KEY").
    Env string
    // File: cesta relativně k HOME agenta + renderer pro tvar B.
    File   string                       // ".codex/auth.json", ".gemini/oauth_creds.json"
    Render func(l ProviderLogin) []byte // vyrenderuje soubor BEZ rotujícího materiálu
    // Placeholder pro pole, která soubor vyžaduje, ale která nesmí být pravá.
    // Codex: refresh_token = "rt.crewship-managed-no-refresh".
}
```

| Adaptér | `Env` | `File` | Poznámky |
|---|---|---|---|
| `CLAUDE_CODE` | `CLAUDE_CODE_OAUTH_TOKEN` (subscription) / proxy (api_key) | — | dnešní chování, jen přes deklaraci |
| `CODEX_CLI` | — (subscription) / vlastní `model_provider` s `env_key = OPENAI_API_KEY` **na sidecar** (api_key) | `.codex/auth.json` | `CODEX_HOME=/crew/agents/<slug>/.codex` explicitně; **nikdy nenastavit dummy `CODEX_API_KEY`** v subscription módu |
| `GEMINI_CLI` | `GEMINI_API_KEY` (api_key) | `.gemini/oauth_creds.json` (subscription) | |
| `CURSOR_CLI` | `CURSOR_API_KEY` | — | bez override endpointu |
| `FACTORY_DROID` | `FACTORY_API_KEY` | — | |
| `OPENCODE` | provider env vars | `.local/share/opencode/auth.json` | |

Soubor se zapisuje stejným kanálem jako `.codex/config.toml`
(`writeFileViaContainer`, `containerFileSecret`, HOME `/crew/agents/<slug>` z
`exec_env.go:26`), s `chmod 0600`, a `credSecretPaths` dostane větev, aby revoke
soubor smazal. `last_refresh` se razí na `now` při každém renderu.

**Dva důsledky, které UI musí říct nahlas:**
1. HOME agenta je na perzistentním svazku (známé V2 z PRD-CREDENTIALS-V2) → soubor
   přežije běh. Protože neobsahuje refresh token, je to access token s expirací
   ≤ 10 dní, ne trvalé přihlášení. Přesto: přepsat při každém startu, smazat při
   revoke, a zapsat do PRD-CREDENTIALS-V2 jako další argument pro efemérní HOME.
2. Subscription provoz jde CONNECT tunelem → **model policy
   (PRD-MODEL-SCOPED-CREDENTIALS) ani metering se nevynucují**, stejně jako dnes
   u Claude OAuth. Provider login v subscription módu ukáže „model policy se
   nevynucuje" místo prázdného allowlistu.

### 5.3 Centrální refresh

Rozšířit `CredentialMonitor` (`internal/llmproxy/monitor.go`) z validace na
**refresher** s per-provider strategií:

| Provider | Strategie | Kdy |
|---|---|---|
| OpenAI/Codex | `POST auth.openai.com/oauth/token`, `grant_type=refresh_token`; uložit **nový** access + refresh + `expires_at` | ≥ 24 h před expirací (token žije 10 dní) **a** před každým startem běhu, pokud zbývá < 48 h |
| Google/Gemini | standardní Google refresh (`oauth2.googleapis.com/token`) | ≥ 10 min před expirací (žije 1 h) a před startem běhu |
| Anthropic | žádný refresh flow; **validace** + `EXPIRING` 30 dní předem + notifikace vlastníkovi | denně |
| Cursor / Factory / Copilot / Groq | validace probe | denně |

Pravidla převzatá z té proxy (§4.1) a nutná kvůli §1.3:

- **Single-flight per login** (řádkový zámek / `refresh_in_progress_until`): dva
  starty běhů ve stejnou vteřinu nesmí spustit dva refreshe — rotace by druhý
  zneplatnila.
- Backoff: 5 min po chybě, 1 min „pending". Po N chybách `status = NEEDS_RELOGIN`,
  login zmizí z poolu (ne z bindingů), vlastník dostane notifikaci s odkazem na
  re-login. Agent, který na tom loginu jede sám, dostane čitelnou chybu **před**
  startem (stejná doktrína jako §7 model-scoped PRD: vynucovat při konfiguraci, ne
  až 401 uvnitř kontejneru).
- Po úspěšném refreshi se **znovu vyrenderuje soubor** do běžících kontejnerů
  daného loginu (Codex čte `auth.json` při startu procesu; běžící `exec` doběhne
  na starém tokenu — u 10denního tokenu to nevadí, u Gemini 1 h to znamená, že
  dlouhý běh musí dostat nový soubor před expirací → render při refreshi, ne jen
  při startu).

### 5.4 Přiřazení a pool

- **Přiřazení** = `credential_bindings`. Čtyři Anthropic loginy → čtyři AGENT (nebo
  CREW) bindings se stejným slotem `CLAUDE_CODE_OAUTH_TOKEN`. Nic nového.
- **Pool** = více loginů jednoho providera na stejném scope. Výběr přes existující
  `Priority` vrstvy + round-robin (`credstore.go:191`) a `CooldownManager` na 429
  (`failover.go`). Rozdíl proti API klíčům, který je nutné dokumentovat: u
  **souborového** doručení je failover **per běh**, ne per request — Codex načte
  `auth.json` při startu; proxy cesta (API klíč) umí per request. Kvóta seatu
  (5h/týdenní okno) se čte z 429 a z hlaviček tam, kde je vidět (`usage.go:289`),
  a ukládá k loginu → Providers tab ukazuje „u limitu do 11:55".
- Pool přes seaty **různých vlastníků** je opt-in s explicitním textem (§3.4).

### 5.5 Účtování

`CREWSHIP_BILLING_MODE=flat_rate` + `CREWSHIP_SUBSCRIPTION_PLAN` z pole `plan`
(ne natvrdo „Anthropic Max"). `SubscriptionsPanel` klíčuje řádky per login, ne per
provider — čtyři seaty = čtyři řádky s vlastníkem.

### 5.6 Onboarding — jak login vznikne

| Provider | v1 | v2 |
|---|---|---|
| Anthropic | vložit výstup `claude setup-token` (dnešní) | — |
| OpenAI/Codex | **vložit `~/.codex/auth.json`** (import, jako runtimy B/C v §4) nebo API klíč; server z něj vytáhne části a **refresh token okamžitě zapečetí** | **device code v UI**: Crewship sám vede RFC 8628 flow proti veřejnému client_id (ukáže kód + URL, polluje token endpoint) — bez `codex` binárky na serveru. *K ověření:* přesný device-authorization endpoint (binárka ho má, docs ne). |
| Google/Gemini | vložit `oauth_creds.json` nebo API klíč | vlastní Google OAuth consent (client id operátora) |
| Cursor / Factory / Groq / Copilot | vložit klíč / PAT | — |

Všechny cesty používají existující `credentials:create` + `credential_fields`; nový
je jen parser vstupu (JSON → části) a okamžité `SEALED` na refresh tokenu.

### 5.7 Bezpečnost

- Refresh token: `SEALED`, nikdy v boot payloadu sidecaru, nikdy v `/secrets`,
  nikdy v reveal dialogu, nikdy v exportu backupu bez explicitního flagu.
- Do kontejneru jde jen to, co CLI potřebuje k požadavku (access token), s expirací.
- Revoke = smazat binding **a** soubor v běžících kontejnerech (rozšířený
  `credSecretPaths`) **a** — kde provider umí — `oauth/revoke`.
- Audit: kdo přihlásil, kdo přiřadil, kdy proběhl refresh, kdy failover. Vše jsou
  existující audit řádky s novými `action` hodnotami.
- Keeper: `PROVIDER_LOGIN` není Keeper-gated (stejně jako `AI_CLI_TOKEN`), protože
  CLI ho čte při startu a mediace per request není možná v CONNECT tunelu.

---

## 6. UI

### 6.1 Záložka **Providers** v Credentials

Vedle stávajícího Overview/All. Karta per login:

```text
┌ ◐ Anthropic · Claude Max ─────────────────────────── ● active ┐
│ owner  pavel@…        plan  Max 20×      expires  in 212 d   │
│ agents 3 (tech-lead, backend, qa)   crews 1   slot CLAUDE_… │
│ quota  5h window ▓▓▓▓░░ 62 %   weekly ▓▓░░░░ 31 %            │
│ last refresh —   last used 1 m ago            [Assign] [Test]│
└──────────────────────────────────────────────────────────────┘
┌ ⬡ OpenAI · ChatGPT Plus ──────────────── ⚠ at limit → 11:55 ┐
│ owner  jana@…         plan  plus         expires  in 2 d     │
│ agents 1 (codex-reviewer)              refresh  every ~9 d   │
└──────────────────────────────────────────────────────────────┘
```

Rail vlevo: **Provider** (brand), **Mode** (subscription / api key), **Status**
(active / at limit / expiring / needs re-login), **Owner**. Stat dlaždice nahoře:
*Seats*, *At limit*, *Expiring ≤ 30 d*, *Unassigned*.

Overview zůstává pro secrety; provider loginy se v něm **nepočítají** do „12 secrets"
(mají vlastní počítadlo „5 provider logins"), jinak KPI nedávají smysl.

### 6.2 Oddělené vstupy *Add secret* a *Add provider*

Další upřesnění uživatele 2026-09-06: hlavička nabízí oba vstupy současně.
**Add secret** obsahuje jen šest běžných typů tajemství; **Add provider** začíná
katalogem AI providerů níže. Legacy vstup **Connect via OAuth** se v Credentials
nezobrazuje. To neruší provider-specific device-code přihlášení ani obnovu tokenů
a neodstraňuje OAuth endpointy používané jinými integracemi.

Provider krok Access nevyžaduje ruční volbu Keeper L1–L4 ani uživatelskou expiraci;
create nepřepisuje dosavadní serverový výchozí stupeň (aktuálně L1), stejně jako
device-code flow. Jde o zjednodušení formuláře, nikoli novou ochrannou politiku.
Následně schválené upřesnění RBAC (2026-09-06): správu a metadata provider účtů
vidí pouze OWNER/ADMIN. Platí i pro starší API_KEY a AI_CLI_TOKEN záznamy modelových
providerů. Ostatním rolím agent ukazuje pouze značku přiděleného providera,
nikoli identitu účtu, vlastníka, plán či token. Běžné secrety zachovávají dosavadní
role a crew-scope. Runtime delivery se tím nemění. Serverové brány a regresní
testy: `internal/api/provider_login_policy.go`, `provider_login_policy_test.go`
a společný `credentialVisibilityFilter`.

Upřesnění uživatele 2026-09-06: nahoře přímo karty ChatGPT/OpenAI,
Claude/Anthropic, Gemini/Google, Grok/xAI a dalších providerů přijímaných API,
se značkovými ikonami. Provider není volitelná dekorace v „Brand icon".
Kliknutí otevře jeho konkrétní formulář: jen podporované metody, vstup podle
§5.6, vlastník, potom přiřazení. Kroky pro provider login se jmenují
**Provider → Connect → Access**; ostatní typy tajných údajů zůstávají dostupné.
Změna providera/metody smaže rozepsaný token. Po dokončeném device přihlášení
nelze identitu účtu zaměnit za jiného providera.

Další zpřesnění průvodce (2026-09-06): Connect již neopakuje výběr providera;
značka je v nadpisu, změna je přes Back. Výběr stejné značky zachová draft,
jiná značka smaže klíč. Název se předvyplní podle značky, ruční název se nepřepisuje.
ChatGPT předvolí device code (s importem jako alternativou), Gemini API key
(s importem osobního Google přihlášení jako alternativou), Claude setup token.
API-only provider nevyžaduje potvrzovat jedinou dostupnou metodu. Každý má
konkrétní krátký návod a odkaz na vytvoření klíče. Owner, název/štítky a technický
slot jsou upravitelné pod rozbalením. Zdroje a hranice podpory viz
[`reports/provider-onboarding-guidance-2026-09-06.md`](https://github.com/crewship-ai/crewship/blob/07bfd2360a97dbe1c9f71c03e1d9b5d5d471c993/docs/prd/reports/provider-onboarding-guidance-2026-09-06.md).

Filtr **Providers** je přímo v levé liště Overview i Providers, včetně nulových
počtů a volby „All providers". Výběr v Overview přepne do Providers. Nulový počet
nesmí skrýt celou lištu. Počty vycházejí z login metadat API, ne z názvu secretu.
Grok/Groq karta uvádí API-key cestu přes OpenCode; neznamená hotový nativní adaptér.

### 6.3 Agent

V Runtime panelu agenta: `cli_adapter` + **„Pays with"** = binding na provider
login (výběr z loginů daného providera; ukáže plán, vlastníka, stav). Změna
adaptéru na `CODEX_CLI` s Anthropic loginem → validace při uložení, ne 401 v běhu.

---

## 7. Práce — fáze

Internal work allocation, delivery phases and real-account verification
records are retained privately. Changes must preserve the public API in §10
and state provider-specific validation limits.

## 8. Otevřené otázky

1. **Migrace stávajících `AI_CLI_TOKEN`/`API_KEY`** — automaticky na
   `PROVIDER_LOGIN` (s `owner = created_by`), nebo jen zobrazit na záložce a nechat
   upgrade na kliknutí? *Návrh: zobrazit + upgrade na kliknutí; nic nemigrovat
   potichu.*
2. **Pool přes vlastníky** — povolit vůbec? *Návrh: ano, opt-in per pool, s textem
   „seaty patří různým lidem; jejich kvóty se sčítají, jejich podmínky ne".*
3. **Codex device-code endpoint** — ověřit z binárky / traffic capture před P-E.
4. **Efemérní HOME** (V2 z PRD-CREDENTIALS-V2) — tenhle PRD ho nevyžaduje, ale
   přidává důvod. Kdy?
5. **App-server / ACP runtime** — mimo rozsah; `AuthDelivery` s HOME ho nevylučuje.

---

## 9. Reference

Public references: [credentials vault](../specs/credentials-vault.md) and
[credential guide](../guides/credentials.mdx). Internal research bibliographies
are not required to use or build the product.

## 10. API kontrakt v1 (závazný pro paralelní implementaci)

Backend (P-A2/P-D) a frontend (P-B) se staví souběžně proti tomuto tvaru.
Cokoli mimo něj se dohaduje v PR, ne mlčky.

### 10.1 `login` objekt na credential rows

`GET /api/v1/credentials` a `GET /api/v1/credentials/{id}` vrací u každé řádky,
jejíž `type` je `PROVIDER_LOGIN`, nebo `AI_CLI_TOKEN` / `API_KEY` s AI
providerem (registry `cli: true` — ANTHROPIC, OPENAI, GOOGLE, CURSOR, FACTORY),
navíc pole `login`:

```json
{
  "login": {
    "mode": "subscription",              // "subscription" | "api_key"
    "provider": "OPENAI",
    "plan": "plus",                      // string | null
    "plan_label": "ChatGPT Plus",        // string | null
    "owner_user_id": "…", "owner_email": "jana@unify.cz",
    "expires_at": "2026-09-16T08:40:00Z",// access token / setup-token expiry, null = unknown/never
    "refresh": {
      "supported": true,                 // false: Anthropic setup-token, API keys
      "status": "ok",                    // "ok" | "pending" | "failed" | "needs_relogin" | "none"
      "last_at": "…", "next_at": "…", "error": null
    },
    "quota": null,                       // P-D: {"window_5h_pct":62,"window_weekly_pct":31,"resets_at":"…"} | null
    "delivery": { "kind": "file", "target": ".codex/auth.json" },   // "env" | "file"
    "pays_for": { "agents": 3, "crews": 1 }
  }
}
```

`GET /api/v1/credentials?kind=provider_login` vrací jen řádky s `login`.
Existující `AI_CLI_TOKEN`/`API_KEY` řádky dostávají `login` odvozeně (mode z
typu, plan z tokenu kde jde, refresh.supported=false) — bez migrace.

### 10.2 Typ `PROVIDER_LOGIN`

`credentials.type = PROVIDER_LOGIN`, `encrypted_value` = access token (u
Anthropic celý setup-token, u API-key módu klíč). Části v `credential_fields`:
`refresh_token` (secret, **SEALED**), `id_token` (secret), `account_id`,
`plan`, `expires_at`, `mode`. Vytvoření: `POST /api/v1/credentials` s
`type: PROVIDER_LOGIN`, `provider`, `mode`, a **`value` = to, co uživatel
vložil** (celý `auth.json`, setup-token, nebo klíč) — server rozparsuje a
rozloží do částí sám (`internal/codexauth` pro OpenAI). Orchestrátor přijímá
při doručení obě podoby (`PROVIDER_LOGIN` části i starší `AI_CLI_TOKEN` blob).

### 10.3 Nové endpointy (každý má CLI příkaz)

| Endpoint | CLI | Účel |
|---|---|---|
| `POST /api/v1/credentials/{id}/refresh` → `{login}` | `crewship credential refresh <id>` | vynutit refresh teď; 409 když už běží (single-flight) |
| `POST /api/v1/provider-logins/device` `{provider, mode?}` → `{device_id, user_code, verification_url, expires_at, interval_s}` | `crewship credential login --provider OPENAI` | začít device-code přihlášení; server polluje providera |
| `GET /api/v1/provider-logins/device/{device_id}` → `{status: pending\|complete\|expired\|denied, credential_id?}` | (totéž CLI čeká a vypíše výsledek) | stav |
| `GET /api/v1/agents/{id}` … pole `pays_with: {credential_id, name, login}` | `crewship agent get` | co agentovi platí model (odvozeno z bindingů + adaptéru) |

### 10.4 Chování refreshe

Codex: `POST https://auth.openai.com/oauth/token`, `grant_type=refresh_token`,
`client_id app_EMoamEEZ73f0CkXaXp7hrann`; nový access + refresh + `expires_at`
zpět do částí; single-flight per credential (`refresh_in_progress_until`);
backoff 5 min po chybě, 1 min pending; po 3 chybách `needs_relogin` + notifikace
vlastníkovi. Běží v `CredentialMonitor` (interval) **a** před startem běhu,
když zbývá < 48 h. Po úspěchu přepsat soubor v běžících kontejnerech daného
loginu.
