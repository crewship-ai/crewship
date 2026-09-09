# Závěrečná zpráva — Codex a provider logins v Crewshipu (2026-09-06)

Zpráva o jedné pracovní session (cca 08:30–11:25 UTC, crewship-dev, klon
`crewship_3`, model Opus 5 do 09:40 a Fable 5.1 poté). Je psaná tak, aby se
každé tvrzení dalo ověřit: u každého je uvedeno **jak** bylo zjištěno
(naměřeno / přečteno z kódu / převzato z dokumentace / odvozeno) a kde leží
důkaz (commit, soubor:řádek, log). Části, které jsem **neověřil** nebo se
**nepovedly**, jsou v §6 a §7 — schválně odděleně, aby se nedaly přehlédnout.

---

## 1. Zadání a jak se vyvíjelo

1. *„Zprovoznit Codex v Crewshipu, vyzkoušet na dev3, zjistit, zda funguje
   jako Anthropic — že mu stačí token."*
2. Po prvních zjištěních rozšířeno na *„jednotnou přihlašovací bránu napříč
   všemi AI providery, jedno přihlášení rozprostřené na všechny agenty,
   snadný refresh"* → rešerše + PRD.
3. Poté *„záložka Providers v Credentials, více předplatných jednoho
   providera per agent"* → wireframe.
4. *„Začni na tom pracovat"* → implementace fáze P-A + živý test.
5. *„Nevidím to na dev3"* → nalezení příčiny (mrtvá komponenta), sedmá karta
   ve wizardu.
6. *„Udělej vše podle PRD a wireframu paralelními agenty, otestuj"* → tři
   paralelní agenti, integrace frontendu, nasazení.
7. *„Ukončuj práci, převezme to konkurence"* → handoff.

---

## 2. Rešerše — co jsem zjistil a jak

### 2.1 Codex CLI (naměřeno proti `codex-cli 0.153.2` na crewship-dev)

| Zjištění | Jak ověřeno |
|---|---|
| Codex **nemá** env proměnnou pro subscription login; jediný nosič je `$CODEX_HOME/auth.json` | `codex login --help`, `strings -a` binárky (`CODEX_HOME`, `CODEX_API_KEY`, `CODEX_ACCESS_TOKEN`; žádná jiná), pokus s `codex login status` v prázdném `CODEX_HOME` |
| `auth.json` vyžaduje všechna čtyři pole `tokens.{id_token,access_token,refresh_token,account_id}`; bez `id_token` i bez `refresh_token` odmítne („missing field") | pokusy s ořezaným souborem v izolovaném `CODEX_HOME` |
| **`refresh_token` může být placeholder** — s `rt.crewship-managed-no-refresh` hlásí `Logged in using ChatGPT` a autentizuje | `codex exec` došel k chybě kvóty účtu (tj. za autentizaci) |
| `CODEX_API_KEY` **přebije** `auth.json`; `OPENAI_API_KEY` **nepřebije** | dva `codex exec` běhy s dummy klíčem: první skončil 401 `invalid_api_key` (klíč vyhrál), druhý chybou kvóty (login vyhrál) |
| `OPENAI_BASE_URL` binárka nezná; request jde na `api.openai.com/v1/responses` i s base URL na mrtvý port | `strings` + běh s `OPENAI_BASE_URL=http://127.0.0.1:9119/...` → 401 z `api.openai.com` |
| Vestavěného providera `openai` nejde přepsat (`-c model_providers.openai.base_url`) | chyba *„Built-in providers cannot be overridden"* |
| `codex login --with-access-token` je pro enterprise *agent-identity JWT*, ne ChatGPT | ChatGPT access token odmítnut: *„agent identity JWT payload is not valid JSON"* |
| ChatGPT access token žije 240 h, `id_token` 1 h (Codexu nevadí); plán v claimu `https://api.openai.com/auth.chatgpt_plan_type` | dekódování JWT z `~/.codex/auth.json` (jen claimy, bez podpisu) |
| `codex exec` mimo git repo končí exit 1 bez `--skip-git-repo-check` | až první živý běh na dev3 (viz §4.2) |

### 2.2 Ostatní CLI a providery (převzato z dokumentace, neměřeno)

Claude Code (`claude setup-token` → env), Gemini CLI (OAuth soubor
`~/.gemini/oauth_creds.json` nebo `GEMINI_API_KEY`), Cursor (`CURSOR_API_KEY`),
Factory (`FACTORY_API_KEY`), OpenCode (`~/.local/share/opencode/auth.json`),
Copilot CLI (`COPILOT_GITHUB_TOKEN`/`GH_TOKEN`), Grok Build, Groq Code CLI;
Perplexity `pplx` **není** coding agent (search CLI). Závěr: existují jen tři
tvary doručení — env proměnná, soubor v HOME daného CLI, jen interaktivně.
Zdroje jsou v `docs/prd/provider-logins.md` §9.

### 2.3 Konkurence (převzato z jejich dokumentace)

Dva prozkoumané agentní runtimy drží OAuth ve vlastním store a záměrně ho
**nesdílejí** s CLI kvůli race při rotaci refresh tokenů; jeden z nich zrušil
`codex-cli` backend ve prospěch Codex app-serveru. CLI-to-API proxy projekt
řeší multi-účet round-robinem, single-flight refreshem (5 s tick, backoffy 5 min
/ 1 min) a cooldownem na 429. Orchestrátory typu Vibe Kanban / Conductor
dědí přihlášení hostitele (jedna instance). Nikdo nemodeluje více seatů
jednoho providera přiřazených agentům.

### 2.4 Podmínky providerů

Anthropic Team/Enterprise: seat per osoba (citace v PRD §3.4, z help centra
— staženo). OpenAI Business: sdílení účtu zakázáno (**pouze shrnutí z
vyhledávání; článek help centra vrátil 403, znění doslovně neověřeno**).

---

## 3. Chyby nalezené v Crewshipu

| # | Chyba | Kde | Jak zjištěno | Stav |
|---|---|---|---|---|
| B1 | Codex API-klíčová cesta nikdy nedosáhla sidecaru: `OPENAI_BASE_URL` Codex nečte, `apiKeyEnvVarsForAdapter("CODEX_CLI")` vrací nil → dummy klíč šel přímo na `api.openai.com` → 401 | `internal/orchestrator/exec_env.go:494`, `:1426` | kód + měření §2.1 | opraveno `cadfcdc2` |
| B2 | vestavěný provider nejde přepsat → oprava musí jít přes vlastní `model_provider` | — | měření | opraveno `cadfcdc2` |
| B3 | OAuth detekce jen podle `Type == "AI_CLI_TOKEN"` — OpenAI login by skončil v `CLAUDE_CODE_OAUTH_TOKEN` s labelem „Anthropic Max" | `exec_env.go:434`, `:1273`, `:1510` | kód | opraveno `cadfcdc2` |
| B4 | subscription cesta pro Codex nezapojená | `docs/guides/cli/codex.mdx:22` | docs + kód | implementováno `cadfcdc2` |
| B5 | `CREWSHIP_SUBSCRIPTION_PLAN` natvrdo „Anthropic Max" | `exec_env.go:471` | kód | opraveno pro Codex `cadfcdc2` |
| B6 | katalog modelů zastaralý (`gpt-5.5` default; GPT-6-Astra doporučený od 2026-09-03), pin `0.128.0` vs `0.153.2` | `config/models.json`, `cli_adapter_versions_test.go` | web + `codex --version` | pin opraven; **katalog neopraven** (§7) |
| B7 | `codex exec` bez `--skip-git-repo-check` končí exit 1 | `adapter_codex.go` | živý běh dev3 | opraveno `eef84e4f` |
| B8 | CLI `--value-stdin` četlo jen **první řádek** stdin (create/update/rotate) — víceřádkový `auth.json` i PEM klíč by dorazil ořezaný | `cmd/crewship/cmd_credential_mutate.go:261,528,674` | živý běh (400 „unexpected end of JSON input") | opraveno `eef84e4f`, limit 64 KiB `1679b6bb` |
| B9 | probe klíče posílal ChatGPT JWT na `api.openai.com` a hlásil „Invalid API key" | `cmd_credential_mutate.go` | živý běh | opraveno `eef84e4f`, update-cesta `1679b6bb` |
| B10 | **Wizard neuměl vytvořit `AI_CLI_TOKEN` ani `API_KEY`** — „Token" ukládá `CLI_TOKEN` (soubor v `/secrets`, který žádné model CLI nečte); `add-credential-dialog.tsx` je mrtvý kód | `lib/credentials/item-types.ts`, `app/(dashboard)/credentials/page.tsx` | uživatel nahlásil „vidím starou verzi" → grep bundlu | opraveno `f89e0802` (sedmá karta Provider login) |
| B11 | selhání zápisu/smazání `auth.json` v dávkovém preflightu run neshodilo (nález CodeRabbit, CWE-284) | `orchestrator_run.go` | review | opraveno `1679b6bb` + `a249ad43` |
| B12 | `TestBuildCLICommand` zčervenal po B7 (tři očekávané argv) — do CI to odešlo červené | `failover_test.go` | hlásil agent | opraveno `269b0b7b` |
| — | pozorování: `parser_codex` ukázal `turn.failed` jako text event + prázdný `error` event | `parser_codex.go` | živý běh | **neopraveno** |

---

## 4. Co jsem udělal

### 4.1 Analýza a design (artefakty)

- **PRD** `docs/prd/provider-logins.md` (v repu, commit `cadfcdc2`; §10 API
  kontrakt `1d3f33f9`) — rozhodnutí „broker, ne brána", typ `PROVIDER_LOGIN`,
  `AuthDelivery` per adaptér, centrální refresh, přiřazení přes existující
  `credential_bindings`, fáze P-A…P-F. Publikováno i jako stránka:
  https://claude.ai/code/artifact/4fdfb3f8-f6ef-4834-a909-06a55b9b2202
- **Wireframe** (5 obrazovek):
  https://claude.ai/code/artifact/d201227f-218f-420e-affd-81e8b42958d9
- Issue **#2428**, PR **#2430** (`feat/codex-provider-login`).

### 4.2 Implementace na `feat/codex-provider-login` (13 commitů, 50 souborů, +6113/−204)

| Commit | Obsah |
|---|---|
| `cadfcdc2` | `internal/codexauth` (parser/renderer `auth.json`, placeholder refresh, plán z JWT); `codexDefaultRoute` + `routedProvider.ProxyBaseURL()` (API klíč přes sidecar `/openai/v1`); `credentialOAuthKind` (Anthropic vs OpenAI); `syncCodexAuthFile` (zápis/mazání v HOME agenta, 0600); `CODEX_HOME`; billing `flat_rate` + plán; login mimo sidecar CredStore; revoke path; ochrana v Files API; validace při vytvoření; docs, CHANGELOG; testy |
| `eef84e4f` | B7, B8, B9; docs `assign --env-var-name` |
| `f89e0802` | sedmá karta **Provider login** ve wizardu (Subscription → `AI_CLI_TOKEN`, API key → `API_KEY`), `providerLoginPresentation`; revert mrtvého dialogu; testy |
| `1d3f33f9` | PRD §10 API kontrakt |
| `1679b6bb` | 7 nálezů CodeRabbitu (post-flush check, 64 KiB stdin, probe v update, subtesty, PRD bez jmen cizích produktů, oprava tabulky, `text` fence) |
| `e20b9447`…`cc715186` (agent, merge `ecdccee0`) | Providers tab, detail seatu, Assign dialog, wizard „Sign in with a code" + Owner, agent „Pays with", docs |
| `269b0b7b`, `a249ad43` | B12; post-flush check jen při selhaném flushi |

### 4.3 Paralelní agenti (větve pushnuté, **nesloučené**)

- `feat/provider-login-backend` (`f524fc8e` + WIP `6e473736`): typ
  `PROVIDER_LOGIN` s částmi (`internal/providerlogin`), objekt `login`,
  `?kind=provider_login`, `pays_with`, centrální refresh se single-flight a
  `needs_relogin`, `crewship credential refresh`, Paymaster `logins[]`.
- `feat/provider-login-device` (`12e94fb7`, `44996dc9`): `AuthDelivery` per
  adaptér, Gemini `oauth_creds.json` (`internal/geminiauth`), device-code
  přihlášení server-side (`crewship credential login`).
- `feat/providers-tab` — sloučeno.

---

## 5. Jak bylo co ověřeno

### 5.1 Testy a gates (lokálně, na finálním stavu větve)

| Příkaz | Výsledek |
|---|---|
| `go test ./internal/orchestrator/ -count=1 -timeout 900s` | ok (po `a249ad43`) |
| `go test ./internal/codexauth/ ./internal/server/` | ok |
| `go test ./internal/api/ -run 'Credential\|ProxyFiles\|Onboarding' -timeout 900s` | ok (**celý balík `internal/api` nepuštěn**, viz §6) |
| `go test ./cmd/crewship/ -run 'ReadValueStdin\|Credential'` | ok |
| `pnpm exec vitest run components/features/credentials components/features/crews lib/credentials` | 83 souborů, 1161 testů ok (po merge frontendu) |
| `pnpm exec eslint` na dotčených souborech, `pnpm exec tsc --noEmit` | čisté |
| `go run ./scripts/docs-inventory -strict`, `go run ./scripts/docs-surface-check` | čisté (před merge agentových větví) |
| CI na PR #2430 | na konci session **běží** pro `a249ad43`; předchozí běhy měly `TestBuildCLICommand` červený |

### 5.2 Živý test na dev3 (doložitelné)

- Nasazeno: `./dev.sh status` → `feat/codex-provider-login @ …`, `web/out` build 10:11 → 10:25 → 11:03.
- DB seed (`./dev.sh seed`), CLI profil `dev3`, credential „ChatGPT Plus (live
  test)" (`AI_CLI_TOKEN`/`OPENAI`, **reálný `~/.codex/auth.json` uživatele**),
  agent `codex-reviewer` (CODEX_CLI, crew `engineering`), přiřazeno.
- `crewship run codex-reviewer "Reply with exactly: CREWSHIP_CODEX_OK"`:
  1. běh (10:06): exit 1 *„Not inside a trusted directory and --skip-git-repo-check was not specified"* → B7.
  2. běh (10:07, po opravě): log agenta
     `/tmp/crewship-3-logs/crews/<crew>/agents/codex-reviewer/current.jsonl`
     obsahuje `thread.started` a `turn.failed: "You've hit your usage limit …
     try again at Sep 7th, 2026 11:55 AM"` → autentizace **proběhla**
     (chyba je kvóta účtu, přichází až po ní).
- CLI: vytvoření z víceřádkového souboru přes `--value-stdin` OK (po B8);
  holý token odmítnut 400 s čitelnou hláškou; zbytkový test credential smazán.
- Frontend: přítomnost nové dlaždice a záložky ověřena grepem v chunku, který
  veřejná URL `crewship-dev3.unifylab.cz` reálně servíruje.

### 5.3 Co ověřeno **nebylo**

- API-klíčová cesta (Codex → sidecar → `api.openai.com`) — jen unit testy na
  env a příkaz; na boxu není OpenAI API klíč.
- Zelený konec Codex běhu — kvóta účtu do 2026-09-07 11:55.
- Device-code flow — krok v prohlížeči nikdo nespustil; těla odpovědí
  OpenAI jsou odvozená z klientských struktur (dle agenta).
- Gemini `oauth_creds.json` — tvar ze zdrojáků gemini-cli, ne z běžící binárky.
- Záložka Providers s daty — backend (`login` objekt) není sloučený, záložka
  na dev3 je prázdná.

---

## 6. Co se nepovedlo a chyby v mém postupu

1. **Upravil jsem mrtvou komponentu** (`add-credential-dialog.tsx`) a
   nasadil ji jako „UI změnu"; uživatel správně viděl starou verzi. Příčina:
   grep na `AI_CLI_TOKEN` našel legacy soubor a nic neselhalo. Napraveno
   revertem a sedmou kartou ve wizardu; ověření bundlu jsem od té chvíle
   dělal grepem servírovaného chunku. Zapsáno do paměti projektu.
2. **Poslal jsem do CI červený test** (B12): po přidání
   `--skip-git-repo-check` jsem pustil jen `-run 'Codex|AdapterArgv'`, ne celý
   balík. Všiml si toho až agent.
3. **Post-flush check byl napsán špatně** (`1679b6bb`): `stepFailed` bez
   gate na `flushErr != nil` shodil `RunAgent*` testy s nil chybou; navíc
   jsem commit `269b0b7b` pushnul dřív, než jsem si všiml, protože
   `go test … | grep | head` v řetězci `&&` maskuje exit code. Opraveno
   `a249ad43`.
4. **Celý balík `internal/api` jsem lokálně nepustil** (>12 min pod zátěží;
   spoléhám na CI).
5. Dva neúspěšné pokusy o skriptované editace (Python syntax error kvůli
   uvozovce v českém textu; regex, který nenašel `it.each` tabulku) — bez
   dopadu na výsledek, ale stály čas.
6. **`credential assign` vyžaduje `--env-var-name`** — první živý běh proběhl
   *bez* přiřazeného loginu, což jsem si uvědomil až z výstupu; kdybych chybu
   B7 nedostal, test by mě mohl mýlit.
7. **Paymaster per seat, GPT-6-Astra v katalogu, `parser_codex`** — nedokončeno
   (§7). Katalog jsem odložil kvůli 12 pinnutým testům.
8. Použil jsem **reálný ChatGPT token uživatele** na dev3 (jeho vlastní, na
   jeho infrastruktuře; smazal jsem jen pomocný druhý credential — první
   „ChatGPT Plus (live test)" tam **zůstává** přiřazený agentovi, ať si ho
   uživatel ponechá nebo smaže).
9. V raných verzích docs (`codex.mdx`) jsem napsal, že dialog *Add secret*
   přijme soubor pod „Token → AI CLI token" — bylo to nepravdivé (B10);
   opraveno v `f89e0802`.
10. Odhad „Codex funguje jako Anthropic, stačí token" jsem musel po měření
    otočit; první rešerše `--with-access-token` vypadala jako ta cesta a
    nebyla.

---

## 7. Otevřené body pro nástupce (pořadí)

1. Sloučit `feat/provider-login-backend` (čekat konflikt v `failover_test.go`
   — B12 opravily nezávisle tři větve), doplnit CHANGELOG záznam, `<Warning>`
   v `codex.mdx` („central refresh is the next increment" už neplatí),
   řádek v `.claude/context/prd/CREDENTIALS-VAULT.md`; projet
   `go test ./internal/api -timeout 1500s`, `docs-inventory -strict`,
   `docs-surface-check`.
2. Sloučit `feat/provider-login-device`; odchylky od §10.3 (start `201`,
   status nese `user_code`/`verification_url`/`expires_at`/`error`, cizí id
   `404`) buď přijmout do PRD, nebo srovnat s frontendem.
3. Nasadit dev3, ověřit záložku Providers s daty, `Pays with`, `Refresh now`.
4. Device-code přihlášení naživo (jednou potvrdit kód v prohlížeči).
5. Po 2026-09-07 11:55 zelený běh `codex-reviewer`.
6. GPT-6-Astra do katalogu (`gpt-6-astra`, $10/$50 za M, cached $1 — podle
   web rešerše, neověřeno v OpenAI ceníku) + `internal/paymaster/pricing.go`.
7. Paymaster per seat (potřebuje `credential_id` v ledgeru), `parser_codex`
   `turn.failed`, pooly/kvóty (P-D), Copilot/Grok/Groq (P-F).
8. Nikdy nemergovat na červené CI; CodeRabbit rate-limit poznámka v paměti.

---

## 8. Odkazy a umístění

- PR: https://github.com/crewship-ai/crewship/pull/2430 (handoff komentáře 2×)
- Issue: https://github.com/crewship-ai/crewship/issues/2428 (claim uvolněn)
- Větve na originu: `feat/codex-provider-login`, `feat/providers-tab`,
  `feat/provider-login-backend`, `feat/provider-login-device`
- Worktree agentů: `/srv/crewship/crewship_3/.claude/worktrees/agent-{ad23977923ee0fefb,aedb59d3bb88d4400,af04a356dbdc8e73b}`
- Paměť projektu (`~/.claude/projects/-srv-crewship-crewship-3/memory/`):
  `codex-auth-is-a-file-not-a-token.md`,
  `credentials-page-renders-the-wizard-not-the-dialog.md`,
  `provider-logins-handoff-2026-09-06.md`
- dev3: běží `feat/codex-provider-login @ a249ad43`? **Ne** — poslední
  nasazení bylo `ecdccee0` (11:03); commity `269b0b7b` a `a249ad43` mění jen
  testy a jeden gate v orchestrátoru a **nasazeny nebyly**.
