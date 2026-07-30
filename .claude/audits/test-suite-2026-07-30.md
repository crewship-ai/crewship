# Audit testovací sady před 1.0 — 2026-07-30

Předmět: je testovací sada architektonicky správná, pokrývá reálné use-case
scénáře, a je robustní? Ne "kolik máme procent".

Rozsah: 8 paralelních hloubkových auditů core subsystémů (běh agenta, rutiny,
issues, credentials, memory, delegace, keeper, notifikace) + nezávislý mutační
experiment + vlastní verifikace nálezů.

**Značení důkazů.** `[V]` = ověřeno v tomto auditu čtením kódu nebo spuštěním.
`[A]` = nález subagenta, kterým jsem se neprošel řádek po řádku — před prací na
něm si ho potvrď. `[?]` = otevřená otázka, ne nález.

---

## 1. Verdikt

Sada **není** slabá a **není** postavená špatně. Je postavená na jiné riziko,
než jaké produkt reálně má.

Co je vidět v číslech: Go 86,8 % statements (2 012 test souborů, ~16 000 test
funkcí), frontend 68,7 % na měřeném allow-listu, 150 Playwright testů, 19
adversariálních harness suit. To vypadá jako vyzrálá sada — a v jádru autorizace
skutečně je.

Co v číslech není vidět a rozhoduje:

1. **Nejlepší testy v repu nespouští žádný merge gate.** 19 harness suit
   ověřuje reálné use-case scénáře proti živé instanci. V CI z nich neběží ani
   jedna. Z 150 Playwright testů běží automaticky 6. `[V]`
2. **Kde je centralizovaný chokepoint, testy ho drží. Kde je konzumace
   chokepointu per-code-path, nedrží nic.** To je ta samá diagnóza jako
   chokepoint doktrína, ale posunutá o úroveň: už nejde o to, že guarantee je
   rozptýlená — jde o to, že *centralizovaná* guarantee má neověřená volací
   místa. Leader gate je učebnicový případ. `[V]`
3. **Existují tři třídy chyb, které sada strukturálně nemůže vidět:**
   in-band selhání agenta (exit 0 + `is_error`), cross-tenant referenční
   integrita na write path, a "feature je zapsaná do DB, ale nikdo ji nečte"
   (mrtvé sloupce, mrtvé soubory). Ve všech třech jsme při tomto auditu našli
   živý defekt. `[V]`

Takže: nepotřebujeme víc testů. Potřebujeme **spustit, co je napsané**, přidat
**čtyři invariantní testy na chokepointy** a zavřít **osm konkrétních defektů**.

---

## 2. Co je vážně dobré — a proč to nezlehčovat

Tohle je nadstandard, ne baseline. Kdyby se z auditu vzalo jen "co opravit",
zahodilo by se ohodnocení toho, co drží.

- **`internal/api/route_authz_invariant_test.go`** — build-time enumerace route
  tabulky. Nová mutační routa bez deklarované role **shodí build**. To je
  správně postavený chokepoint, ne per-invariant kontrola.
- **Mutační experiment: 4/4 mutanty zabity.** Viz §3 — autorizační jádro je
  reálně pinnuté, ne naoko. `[V]`
- **Credentials jsou nejlépe otestovaná vrstva v repu.** Encryption fail-closed,
  rotace vs. `PENDING_APPROVAL`, reveal audit-před-vydáním, SEALED deny,
  atribuce non-spoof, loopback invariant ignorující XFF — vše pinnuté
  adversariálními testy. `[A]`
- **`setupTestDB`** staví schéma z reálných migrací (migrated template + copy,
  `internal/api/router_test.go:66`), FK zapnuté a otestované. Raw INSERTy v
  testech tedy aspoň validují proti pravdivému schématu. `[V]`
- **`migrate_upgrade_path_populated_test.go`** — migrace proti naplněné DB.
- **Waitpointy**: decide-právě-jednou, cross-tenant reject, timeout→resume bez
  restartu, cancel kaskáda — 5 invariantů pinnutých. `[A]`
- **Keeper fail-closed** (`keeper_execute.go:444`): hardcoded DENY při chybě
  gatekeeperu, pinnuto 4 testy. Keeper ledger je append-only **na úrovni DB**
  (BEFORE UPDATE trigger, migrace v166). `[V]`
- **`.gremlins.yaml` obsahuje reálné mutační baseliny** s poctivými komentáři o
  tom, co je šum a co skutečný signál. To je vzácná disciplína.
- **Notifikace**: retry s přesným počtem pokusů, dedup přes `INSERT OR IGNORE`,
  crew audience scoped na workspace+slug s negativním testem. `[A]`

---

## 3. Mutační experiment — jediný test, co odpovídá na "jsou robustní"

`.gremlins.yaml` explicitně **zakazuje** mířit mutation testing na
`internal/api` ("NEVER point this at internal/api"). Tedy největší a
bezpečnostně nejcitlivější balík (31 803 statements, všechna autorizace) je z
mutation testingu vyloučený politikou. Ten zákaz má dobrý důvod (suite = 104 s,
tisíce mutantů = dny), ale nechává slepou skvrnu přesně tam, kde slepá skvrna
bolí nejvíc.

Ruční experiment: v odděleném worktree odstranit reálnou guarantee a pustit
**celou** `internal/api` suite. Přežití mutanta = guarantee nikdo nepinuje.

| Mutant | Co odstranil | Výsledek | Zabito čím |
|---|---|---|---|
| M1 | `canScope()` vždy `true` → scoped CLI token obejde svůj scope | **KILLED** (7) | `TestTokenScopeEnforcement_EndToEnd`, `TestRequireScopeMW_Enforcement`, `TestCanScope_WildcardAndExact`, `TestDeleteChat_ScopedTokenNonCreatorAdminForbidden` |
| M2 | `canRole`: MEMBER dostane `manage`/`delete` | **KILLED (97)** | `TestAdminFloor_MemberDeniedAdminSurface`, `TestGDPRDelete_RequiresAdmin`, +95 |
| M3 | scope gate smazán z `requireRoleScopeMW` (reotevření #864 fail-open) | **KILLED** (5) | `TestRequireScopeMW_Enforcement`, `TestTokenScopeEnforcement_EndToEnd` |
| M4 | `wsCtx`: selhání membership lookupu propadne jako MEMBER místo 403 | **KILLED** (4) | **`TestRequireWorkspace_NotMember` — a nic jiného** |

**Výsledek 4/4 zabito je dobrá zpráva a přiznávám, že koriguje můj úvodní
odhad.** Odvozoval jsem z toho, že `ctxTokenScopes` se nastavuje jen v 9 test
souborech z 2 012, že scope enforcement není pinnutý. Bylo to špatně — těch 9
stačí. Počítání grepů je hypotéza; mutace je důkaz. Proto to bylo potřeba
změřit, ne odhadnout.

**Ale M4 je nález.** Cross-workspace membership fence — pravděpodobně
nejdůležitější tenant-isolation guarantee celého produktu — drží **právě jeden
test**. Není to "netestováno", je to "single point of failure v testovací sadě":
kdokoli ten jeden test při refaktoru přepíše nebo oslabí, fence zůstane bez
dozoru a nic nezčervená. `[V]`

**Zadání:** rozšířit M4 pokrytí na tabulkovou matici (§7, T3), a přidat
`internal/api` do mutation testingu v merge-queue vrstvě s malým, cíleně
vybraným seznamem souborů (`rbac.go`, `rbac_routes.go`, `middleware.go`,
`helpers.go`) — ne celý balík. Tím se zákaz v `.gremlins.yaml` obejde legitimně,
bez dnů výpočtu.

---

## 4. Potvrzené defekty — živé chyby, ne testové mezery

Tohle nejsou "chybí test". Tohle je "produkt to dělá špatně a proto o tom
nevíme".

### D1 — Agent může selhat a run se vykáže jako `completed` `[V]`

`internal/orchestrator/orchestrator_run.go:620` nastaví `status := "completed"` a
jediné, co ho přepne na `"error"`, je `exitCode != 0`. In-band chyba z CLI
(`is_error: true` v result zprávě, `type: "error"`) jde **jen** do journal
metadat (`adapter_claude.go:304`). `sawError` (`exec_stream.go:155`) se používá
výhradně na řádku 222 v podmínce o syntéze fallback výsledku a stav runu
neovlivní nikdy.

Dopad: agent odmítne úkol nebo spadne interně, CLI vrátí exit 0, run je
`completed`, mise/rutina pokračuje na chybném výstupu a účtuje se. Postihuje
**všech 6 adaptérů** systémově.

Přímo odpovídá na otázku "jak otestujeme, že agent vrací správný výsledek":
dnes ten produkt tuhle třídu selhání neumí detekovat, takže ji nelze ani
otestovat. Nejdřív gate, pak test.

### D2 — Cross-tenant leak jména přes `assignee_id` u issues `[V]`

Write path: `assignee_id` se v `issue_handler_create.go` (INSERT na ř. 221) ani
`issue_handler_update.go:78` nevaliduje proti workspace. Přitom
`parent_issue_id` o 20 řádků výš validaci **má**, s komentářem, který přesně
tuhle třídu bugu popisuje jako opravenou.

Read path: `issue_handler.go:328-329`
```sql
WHEN m.assignee_type = 'user'  THEN (SELECT full_name FROM users  WHERE id = m.assignee_id)
WHEN m.assignee_type = 'agent' THEN (SELECT name      FROM agents WHERE id = m.assignee_id)
```
Ani jeden subquery nefiltruje workspace.

Dopad: člen workspace A nastaví `assignee_id` na ID uživatele z workspace B a
API vrátí jeho `full_name` každému, kdo to issue čte.

### D3 — `crew_id` injection při `POST /agents` `[V]`

`agents_create.go:70-84`: `CrewRoleFromDB` vrátí `""` pro non-membera workspace
té crew, kód pak explicitně (komentář na ř. 81-83) propadne na workspace roli.
Nikde se neověří, že crew patří do workspace volajícího. `req.CrewID` se dál
použije na ř. 121 (license), 132-136 (LEAD unikátnost) a 226 (INSERT). INSERT
zapíše `workspaceID` volajícího s `req.CrewID` cizího workspace.

`agents_update.go:97` tu kontrolu **má** (`AND workspace_id = ?`). Create je
proti Update nesymetrický.

### D4 — Leader gate má 4 volací místa a nula testů `[V]`

`IsLeader()` se volá v `cmd/crewship/cmd_start.go`,
`internal/api/recurring_issue_dispatcher.go`, `internal/pipeline/schedules.go:628`
a `internal/scheduler/scheduler.go`. Jediný test, který ho v celém repu zmiňuje,
je `internal/leader/lease_test.go` — a ten testuje **lease mechanismus**, ne ani
jedno z těch čtyř gate.

Smazání `if s.leaderGate != nil && !s.leaderGate.IsLeader() { return }` neshodí
nic. Dopad při dvou replikách (blue-green deploy, restart s překryvem): každá
rutina se spustí dvakrát — duplicitní agentní běhy, duplicitní notifikace,
dvojí spotřeba kreditů. To je přesně to, proti čemu byla migrace v150 postavená.

### D5 — Delegační limity jsou mrtvé sloupce `[V]`

`max_delegation_depth`, `max_parallel_delegates`, `delegation_timeout_s` mají v
celém repu **dva** výskyty: `migrate_consts_v01_init.go` (CREATE TABLE) a
`prisma/schema.prisma`. Nula čtecích míst v Go.

Operátor, který si nastaví hloubku delegace na 1, dostane falešný pocit
kontroly. Není to testová mezera — nelze psát test na neexistující enforcement.

### D6 — `/spawn` a `/assign` nemají LEAD gate `[V]`

`internal/sidecar/server.go:529` registruje `/spawn` s komentářem
"LEAD-initiated ephemeral hire". `AgentRole` se v sidecaru používá na **jediném**
místě (`server.go:351`) a rozhoduje jen o crew memory indexu. Žádný kód
neověřuje, že volající je LEAD.

**Přesné rámování** (agent to nadhodnotil): blast radius je omezený crew
`autonomy_level` — guided vyžaduje approval, strict odmítne, a obojí je pinnuté.
Chybí tedy LEAD restrikce, ne veškerá kontrola. V "open" crew ale kterýkoli
agent může najímat a delegovat.

### D7 — Backup ztrácí memory bloby `[V]`

`internal/backup/intent.go:199` a `dbdump.go:190` zahrnují tabulku
`memory_versions`. Content-addressed **blob soubory** na hostu backup nesbírá.
Po restore tedy `memory_versions` řádky ukazují na sha256 bloby, které na cíli
neexistují — historie, HITL a restore paměti jsou nefunkční, a to potichu.

### D8 — `journal_entries` není append-only, keeper ledger je `[V]`

Migrace v166 dává `BEFORE UPDATE` trigger na `keeper_request_events`
**a** na `journal_entry_priorities` — ale **ne** na `journal_entries`. Hlavní
audit journal je tedy tamper-**evident** (hash chain, v152), zatímco menší
keeper ledger je tamper-**proof**.

**Pozor, naivní fix rozbije produkt.** `journal_handler.go`,
`consolidate/compact.go` a `pipeline/store.go` nad `journal_entries` legitimně
mažou (retence, kompakce). Blanket trigger podle v166 shodí kompakci. Zadání je
**rozhodnutí politiky**, ne migrace: buď soft-delete + trigger, nebo přiznat
tamper-evident model a udělat z `VerifyChain` plánovaný gate s alertem. Viz §8.

---

## 5. Potvrzené architektonické mezery v testech

### G1 — Nejlepší testy nespouští žádný gate `[V]`

- Z 19 harness suit v `scripts/test-harness/` neběží v CI **ani jedna**. CI
  spouští `scripts/test-harness-lib-test.sh` (`ci.yml:123`) — unit test
  *knihovny* harnessu. Nehlídané tím zůstává: memory recall across sessions,
  crew-shared + cross-crew izolace, delegace/hire, notifikace po routine,
  determinismus recipe, credential escalation, keeper ingress fence, TOCTOU,
  audit integrity, orphan token reap.
- Z 150 Playwright testů běží automaticky **6** (`playwright.fresh.config.ts:23`
  → jen `onboarding-wizard.spec.ts`, nightly + manuálně). Neběží nikdy:
  `full-integration` (20), `crews-redesign` (16), `manual-crews-walkthrough`
  (15), `connectors` (13), `crews-real-workflow` (12), `edge-cases` (12),
  `crews-unification` (8), `create-crew-wizard` (7),
  `crew-privilege-controls` (6), `a11y` (4), `visual` (5).

`redteam-probe.sh` si zaslouží vlastní poznámku: **není to slabý test, není to
test**. Hlavička říká "reports what a COMPROMISED agent can reach from inside.
Emits one JSON line to stdout" — je to reporter, záměrně. Reálný nález: nikdo
ten JSON nekonzumuje jako gate, takže regrese, která odemkne cleartext secrets
v kontejneru, se zaznamená a nikdo ji neoznačí. `[V]`

### G2 — Chybí read-route a internalAuth invariant `[V]`

`route_authz_invariant_test.go` pinuje role jen na **mutačních** routách a
explicitně vyjímá `internalAuth(...)` sidecar plochu a public token/HMAC routy
("a different trust boundary, out of scope by design").

Chybí tedy dva sourozenci téhož vzoru:
- **read routes**: nová GET routa, která leakne cross-workspace data, build
  neshodí. D2 je přesně tato třída.
- **`/api/v1/internal/*`**: 55 handlerů, 55× `internalAuth(` drží dnes jen
  manuální disciplína. `[A]`

### G3 — Nikde `-race` `[V]`

`ci.yml:260` a `:302` jsou `go test ./... -count=1`, `Makefile:117` taky.
Produkt je orchestrátor s goroutinami, WS hubem, keeper dedup mutexem, lease
UPDATE, detached background workery (`setupTestDB` je má vlastním komentářem
přiznané) a 2 434× `t.Parallel()`. Několik testů v komentářích říká "run
manually under -race" — ta disciplína existuje v hlavách a není vynucená.

### G4 — Třetina sady jsou branch-reachery `[V]`

498 z 2 012 test souborů je `*_cov_test.go` / `_cov2_test.go` — 168k z 507k
řádků testů. Hlavička `core_handlers_test.go:6` to říká otevřeně: *"Coverage
targets (≥65% per file)"*. Žánr ilustruje `admin_cov_test.go:19`: zavři DB,
assertuj 500. V `_cov_` testech je 4 226 assertů na status code vs. 2 004
pohledů do těla odpovědi.

**Nuance, kterou mutační experiment přinesl:** tyhle testy nejsou bezcenné, jen
nejsou důkaz o guarantee. Autorizační jádro drží *jiné*, cílené testy — a drží
ho dobře. Takže G4 není "sada je fake", je to "coverage číslo nevypovídá o tom,
co si myslíš".

### G5 — Skip je nerozeznatelný od passu `[V]`

113× `t.Skip` v 80 souborech: 28 podmíněných na Docker, 26 na `runtime.GOOS`, 9
na `testing.Short`, 3 na chybějící binárce. macOS/arm64 matrix joby Docker
nemají → tichem hlásí zeleno. Nikde se počet skipů nepočítá.
(Ověřeno i to, co **není** pravda: žádný test neskipuje specificky v CI.)

### G6 — Frontend: 124k řádků Reactu bez měření `[V]`

Vitest v CI běží na všech 286 souborech (components tedy jako pass/fail platí),
ale coverage se měří jen na allow-listu 23,5k ze 148k řádků. Uvnitř: `stores`
99 %, `lib` 92 %, ale `components/ui` **30 %**, `lib/api` a `lib/format` **0 %**.
Gate 66/67/69/60 je zelený díky storům. Z 149 component testů používá
`userEvent`/`fireEvent` jen **82** → 67 je render-only; 41 mockuje fetch.

### G7 — Fixtury CLI driftují bez gate `[A]`

`internal/orchestrator/e2e_multi_cli_test.go` jede proti
`testdata/cli-fixtures/*.ndjson`, ne proti reálným CLI. `Makefile` target
`smoke-cli` je regeneruje a **v CI se nevolá**. `opencode.ndjson` je podle
komentáře psaný ze schématu, ne zachycený. Přesně tato třída způsobila incident
#1007 předtím, než na něj vznikl regresní test.

---

## 6. Cílová architektura CI gate

Dnes je CI jednovrstvé: všechno, co běží, běží per-PR; všechno ostatní neběží
nikdy. Cíl jsou čtyři vrstvy, kde per-PR zůstává rychlé a signál neubude.

| Vrstva | Co v ní běží | Budget | Co shodí merge |
|---|---|---|---|
| **Per-PR** | dnešní sada (markers, shell, frontend lint/tsc/build, `pnpm test:coverage`, `go vet`+build+`go test ./...`, go-lint, migration-lint, gitleaks) + **nově**: `-race` na dotčených balících, skip-budget, read-route authz invariant, internalAuth invariant | **~12 min** | cokoli červené |
| **Merge-queue** | `merge_group:` (dnes neexistuje `[V]`), plný `-race` přes `./...`, go-platforms matrix (přesun z per-PR), harness core sada proti ephemeral instanci, mutation gate na `internal/api` authz souborech + 4 dnešních balících, kritická podmnožina Playwright | ~20-30 min | regrese race, harness FAIL, mutation pod baseline |
| **Nightly** | zbylých ~15 harness suit, plný Playwright (144 testů + a11y), `make smoke-cli` (fixture refresh proti reálným CLI), plná security battery, docker smoke | bez limitu | vytvoří issue + trend, neblokuje |
| **Release gate** | plná mutation sada, package smoke matrix, cosign verifikace, migration backward-compat proti snapshotu staré prod DB, security scany jako **blocking** | bez limitu | explicitní sign-off |

**Merge queue je jednička v seznamu.** `merge_group` není v `.github/` nikde
`[V]`. Bez ní zelené PR neznamená zelený main po rebase — a při téhle commit
frekvenci je to klasický zdroj rozbitého main.

### Rychlost — kde je dnes čas

- **Největší páka, potvrzená `[V]`:** 51 test souborů volá `database.Migrate()`
  přímo, tedy pouští všech 155 migrací znovu pro každý test. Cache pattern
  `migratedTemplateOnce` existuje v **jednom** souboru
  (`internal/api/router_test.go`). Nejhorší je `internal/server` s 11 soubory — a
  `internal/server` je zároveň nejpomalejší balík v repu (164 s). Vytáhnout ten
  pattern do sdíleného test helperu a použít všude je jedna PR s největším
  poměrem zrychlení na práci.
- `internal/api` = 104 s `[V]`. Vlastní shard, aby jeden balík neurčoval timeout
  ostatním.
- `go` job má 15m budget na ~2 min reálné práce — cíl je cold build cache, ne
  pomalé testy. Persistentní cache keyed na `go.sum`.
- `-shuffle=on` — odhalí pořadí-závislé flaky testy bez dodatečného wall-clocku.
- Selektivní běh: `security.yml:52` už má `dorny/paths-filter`. Stejný vzor na
  `-race` gating a na výběr harness suit podle dotčeného subsystému.

### Flakiness politika

206× `time.Sleep` v Go testech `[V]` (typicky čekání na async worker místo
pollingu) a Playwright `retries: 2` v CI **bez reportingu**. Test, který selže
napoprvé a projde napodruhé, se mergne zeleně a nikdo se nedozví, že je
nestabilní — to maskuje reálné race podmínky v produkčním kódu.

Zadání: parsovat JSON report (Go i Playwright) na "prošlo až na N-tý pokus",
publikovat jako CI step summary + trend. Test retryující 3× za posledních N běhů
→ automaticky `flaky` tag, vyřazen z merge-blocking, dál v nightly s reportem
vlastníkovi. `CODEOWNERS` má dnes jeden blanket owner — potřebuje aspoň
per-subsystém mapování, aby quarantined test měl komu přistát.

---

## 7. Chybějící invariantní testy — čtyři, které pinují nejvíc

Tyhle čtyři jsou vybrané tak, aby každý zavíral celou **třídu** budoucích chyb,
ne jeden případ.

**T1 — `TestEveryReadRouteScopesWorkspace`**
Sourozenec `route_authz_invariant_test.go` pro GET/list routy. Enumeruje read
route tabulku, pro každou pustí request se dvěma workspacy a asertuje, že řádek
z cizího workspace neuteče. Zavírá třídu D2 a G2 natrvalo.

**T2 — `TestEveryInternalRouteRequiresInternalAuth`**
Regex/AST nad `router_internal.go` po vzoru
`TestNoLegacyAuthedMutationRegistration`: registrace s prefixem
`/api/v1/internal/` bez `internalAuth(` = build failure. Levné, kopíruje
osvědčený vzor, chrání celou budoucí sidecar plochu.

**T3 — Ingress matrix místo injektovaného contextu**
Pro každou routu z `r.mutationRoutes` + read tabulky projet přes `ServeHTTP` se
4 identitami (OWNER / ADMIN / MEMBER / cizí workspace) × 3 auth kinds (session /
CLI token bez scope / CLI token se scope), reálným tokenem přes `mintTokenFor`.
Tabulkově, jeden soubor. Nahradí test-only helper `withWorkspaceUser` reálným
ingressem a rozšíří M4 z jednoho testu na matici.

**T4 — `TestPipelineScheduler_Tick_NoopWhenNotLeader`** + sourozenci pro
zbylá 3 volací místa `IsLeader()`. Stub `Gate{isLeader:false}`, assert nulový
počet vytvořených runů. Malé unit testy, zavírají D4.

---

## 8. Otázky, na které potřebuju rozhodnutí

Tyhle nemůžu rozhodnout za tebe — každá vede k jinému zadání.

1. **D8 / `journal_entries`**: tamper-proof (soft-delete + trigger, přepsat
   kompakci) nebo tamper-evident (nechat, ale `VerifyChain` jako plánovaný gate
   s alertem)? Blanket trigger rozbije kompakci.
2. **D5 / delegační limity**: implementovat enforcement navázaný na řetěz
   `parent_lead_id` (který `resolveHireParentLead` už prochází), nebo sloupce a
   dokumentaci označit jako nevynucované a odstranit z UI?
3. **D6 / `/spawn` LEAD gate**: přidat role check, nebo přiznat, že delegovat
   smí kterýkoli agent, a opravit docstring?
4. **`temperature` / `max_tokens`**: podle auditu běhu agenta jsou to rovněž
   nečtená pole `[A]`. Implementovat, nebo odstranit z DB i UI? Test má smysl
   psát až po tomhle rozhodnutí.
5. **`learned-*.md`** `[?]`: zápis existuje (`consolidate/approve.go:122`),
   čtecí cesta v Go **není** (nula glob/ReadDir/ReadFile call sites `[V]`). Ale
   soubory leží v MemoryRoot, který je bind-mountnutý do kontejneru — je možné,
   že si je agentní CLI přečte samo z filesystému. **To rozhoduje mezi "flagship
   funkce je divadlo" a "funguje, jen jinak, než si myslíme", a je to
   testovatelné jedním testem** (schval proposal → sestav boot-prompt/prostředí
   → assert, že se pravidlo k agentovi dostane). Doporučuju ho napsat jako
   první, protože odpověď mění produktovou prioritu.

---

## 9. Plán práce

Řazeno podle "co pustí chybu do 1.0", ne podle velikosti.

### Fáze 0 — gate, ne kód (dny)

Nic z toho nevyžaduje nový test. Jen spustit, co existuje.

1. `-race` do Go jobu. **Čekej, že to hned zčervená** — to je ta informace,
   kterou chceš před 1.0, ne po něm.
2. Harness core sada (`test-memory.sh`, `test-delegation.sh`,
   `test-notifications.sh`, `test-orchestration.sh`, `test-credentials.sh`,
   `test-keeper-ingress-fence.sh`) do nightly proti dev slotu, hard fail.
3. Playwright nightly: `full-integration`, `crews-real-workflow`, `edge-cases`,
   `crew-privilege-controls`. Infrastrukturu (seed + `E2E_EMAIL`/`E2E_PASSWORD`
   + embed export) už `e2e-devcontainer.yml` má — jde o copy step.
4. `merge_group:` a merge queue.
5. Skip accounting: fail, když počet skipů překročí baseline.

### Fáze 1 — defekty (1-2 týdny)

6. **D1** (in-band error → failed run) — nejsystémovější, postihuje všech 6
   adaptérů. Gate v `orchestrator_run.go` + test s fake exec providerem.
7. **D2** (assignee cross-tenant) — validace symetrická s `parent_issue_id`
   (copy-paste vzoru) + workspace filtr v `issue_handler.go:328-329`.
8. **D3** (`crew_id` injection) — mirror `agents_update.go:97` do Create.
9. **D4** (leader gate) — T4.
10. **D7** (backup ztrácí bloby) — sbírat MemoryRoot + round-trip test.
11. **D6** (`/spawn` LEAD gate) — po rozhodnutí §8.3.

### Fáze 2 — invarianty a struktura (2-3 týdny)

12. **T1**, **T2**, **T3** — tři invariantní testy z §7.
13. `migratedTemplateOnce` do sdíleného helperu, nasadit v 51 souborech.
    Největší zrychlení na jednotku práce.
14. `internal/api` authz soubory do mutation gate v merge-queue.
15. Truncated-SSE test v `internal/llm` `[A]` — provider ukončí spojení bez
    `message_stop` a run se dnes hlásí jako úspěšný.
16. `classifyCrewRuntimeError`: 3 ze 4 chybových větví bez testu `[A]`.
17. Flakiness reporting + quarantine, `-shuffle=on`.

### Fáze 3 — po 1.0

18. Frontend: rozpustit 67 render-only component testů, `lib/api` z 0 %,
    přidávat `components/features/*` do allow-listu po slice.
19. Vizuální snapshoty buď do CI, nebo smazat.
20. Migration backward-compat gate (155 migrací bez `Down()`).
21. Rekonciliace `memory_versions` po výpadku fsnotify watcheru `[A]`.

---

## 10. Korekce dřívějších přesvědčení

Tohle je součást deliverable: několik věcí, které jsme si o téhle sadě mysleli,
už neplatí. Zapisuju je, aby se neopakovaly v dalším review.

| Dřívější přesvědčení | Realita `[V]` |
|---|---|
| "Executor wiring diverguje podle trigger path (C1/C2/C3)" | **Opraveno.** Zkonvergovalo na `executor_factory.go:93`, pinnuto reflect-sweep testem `TestNewWiredExecutor_WiresEveryDependency`. |
| "Atribuce autora issue se zahazuje" | **Opraveno a dobře pinnuté**, včetně `ctxUser` injektovaného přes `withWorkspaceUser`. |
| "Recurring issues nikdy nevystřelí" | **Firují.** Dispatcher zapojen v `cmd_start.go:964`, testován i na chybové cesty. |
| "Agent credential self-write hlásí falešný úspěch" | **Opraveno**, `escalation_falsesuccess_test.go`. |
| "Notify `crew:` nemá datový model" | **Implementováno** (`runner_notify.go:445-511`), a nejednoznačný adresát **degraduje viditelně** (warning + marker), nezahodí se tiše. Formulaci v příštím review změnit z "selže/zahodí" na "degraduje viditelně". |
| "Keeper internal surface: statický token jediná pojistka (HIGH)" | **Částečně zastaralé.** Existuje network/origin gate + trusted-proxy XFF resolver s fail-closed chováním (`internal.go:392-470`), dobře testovaný. Podmíněné na správnou konfiguraci `CREWSHIP_INTERNAL_TRUSTED_PROXIES` v `infra-crewship` — **to z tohoto repa ověřit nelze**. Co skutečně chybí: mTLS, nonce/replay ochrana, a build-time enumerace plochy (T2). |
| "Scope enforcement není pinnutý (9 test souborů z 2012)" | **Špatný odhad, korigován mutací.** M1 i M3 zabity. Grep je hypotéza, mutace důkaz. |

---

## 11. Co implementace změnila na tomhle auditu

Doplněno po první vlně oprav (PR #1531-#1543). Zapisuju to, protože dvě věci
z předchozích sekcí se ukázaly nepřesné a jedna rada byla přímo špatná.

### Rada, která byla špatná: „zapnout těch 144 e2e testů"

§9 Fáze 0 bod 3 doporučoval pustit `full-integration`, `crews-real-workflow`,
`edge-cases` a `crew-privilege-controls` v nightly. Když se celá sada pustila
proti **zdravé seedované instanci**, prošlo **24 z 78**. Aplikace byla v
pořádku — rozpadly se specy:

- `/crews` přišlo o filtry `Status:`/`Role:` → `crews-redesign` 5/11
- crew explorer se přesunul z `aside` do `main` → `crew-privilege-controls`
  **0/6**, `crew-provisioning` 0/1
- katalog konektorů zmizel za `NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS` →
  `connectors` 3/10
- `a11y.spec.ts` hlásí **živé** WCAG chyby, které si jeho vlastní hlavička
  eviduje jako opravené
- `visual.spec.ts`: všechny baseliny jsou `*-chromium-darwin.png`, takže na
  Linuxu padnou jako „snapshot missing", ne jako diff — a 3 z 5 ploch jsou na
  smazaných routách

Zapnout to natvrdo by byl šum, který někdo do týdne vypne. Správný tvar
(shipnutý v #1540): hard gate na tom, co bylo **viděno zelené**, zbytek do
`drift` bucketu, který reportuje a neshazuje, plus `coverage-guard`, který
selže, když spec není ani v jednom bucketu. Následující krok je udělat z
driftu **ratchet** — vzít per-spec počty z prvního reálného nightly a shodit,
když kterýkoli stoupne. Tím se ~140 mrtvých testů stane ochrannými hned, bez
přepisování.

Poučení obecněji: „test existuje" a „test dnes prochází" jsou dvě různé věci a
tenhle audit je na začátku nerozlišoval.

### Nález, který byl větší, než §4 D2 tvrdila

D2 popisovala nevalidovaný `assignee_id` ve dvou write path. Skutečný počet je
**sedm**: `issue_handler_create.go`, `issue_handler_update.go`,
`issue_handler_bulk.go`, `recurring_issue_handler.go`, `triage_handler.go`,
`issues_internal.go` (`InternalIssueHandler.Create`) a
`issue_handler_workflow.go` (`Review` → `request_changes`).

Poslední dvě našel **až ten invariant**, který je má hlídat — ne review. To je
nejlepší dostupný argument pro to, proč se invarianty píšou.

Ta poslední je navíc horší než ostatních šest:
```sql
SELECT id FROM agents WHERE slug = ? AND deleted_at IS NULL LIMIT 1
```
Bere **slug** z těla requestu, bez workspace i bez crew filtru. Slugy jsou
uhádnutelné (`alex`, `reviewer`), na rozdíl od CUID. A `LIMIT 1` nad
neunikátním slugem znamená, že když mají dva workspacy agenta se stejným
slugem, **legitimní** reassign ve workspace A může tiše trefit agenta z
workspace B — nedeterministické cross-tenant přiřazení bez jakéhokoli útoku.

Root cause celé třídy: `assignee_id` je **polymorfní** (podle `assignee_type`
míří do `users` nebo do `agents`), takže na něj nejde dát FK. Ve stejné
`CREATE TABLE` má `crew_id TEXT REFERENCES crews(id)` FK, `assignee_id TEXT`
ne. Databáze to principiálně zachytit nemůže, takže jediná pojistka je
aplikační validace — a v sedmi místech nebyla. Dlouhodobě správně je
`assignee_user_id` + `assignee_agent_id`, obojí s FK, ať to vynucuje DB.
To je ale migrace, tedy samostatné rozhodnutí (patří k §8).

### Chyba, kterou jsem udělal ve vlastním invariantu

Read-route invariant (#1533) v první verzi spojoval fixní čtyřřádkové okno.
Registrace v `router_*.go` jsou po jedné na řádku, takže okno přetékalo do
**další** routy — a protože ta obvykle `wsCtx` má, ungated routa se čítala
jako gated. Naměřeno: **76 z 219** rout. Odebrání `wsCtx` z `GET /api/v1/crews`
prošlo zeleně.

Nezávisle na tom narazil na tu samou chybu #1531 ve svém lookaheadu a napsal
to; teprve pak jsem si ověřil svůj. Obojí je opravené tak, že se okno zastaví
na začátku další registrace.

Poučení, které patří do každého budoucího source-guard testu v tomhle repu:
**vždycky ověř tři případy, ne jeden** — že umí zčervenat, že **ne**dělá
falešný pozitiv na legitimním víceřádkovém zápisu, a že count guard chytí
vacuous pass. U dvou nezávisle napsaných invariantů se objevila ta samá chyba,
takže to není náhoda, ale vzor.

### Co ještě implementace našla nad rámec §4

- **`internal/llm`: uříznutý SSE stream se hlásil jako úspěch u všech tří**
  providerů (`anthropic.go`, `openai.go`, a `ollama.go`, kde se navíc ztrácí i
  obsah, který už dorazil, protože `final.Content` se plní jen uvnitř
  `if chunk.Done`). `bufio.Scanner` hlásí čisté `io.EOF` jako `Err() == nil`,
  což je nerozeznatelné od normálního konce. Ověřeno probem, že zrušený
  kontext naopak dá `Err() = context.Canceled`, takže obojí lze rozlišit.
- **`payload_ref` je absolutní cesta** zapečená při zápisu
  (`internal/memory/versions.go:109`, ukládá `filepath.Join(BlobRoot, sha[:2], sha)`).
  Backup byl tedy rozbitý dvakrát — i zkopírování blobů by cross-host
  selhalo. Sha už layout určuje, takže ta cesta je redundantní; správně je
  relativní, ale to je migrace.
- **Další osiřelé stavy**: kromě `DUPLICATE` v issues je nedosažitelný i
  `DONE` v `ValidMissionTransitions` a `BLOCKED` v `ValidTaskTransitions`.
  `BLOCKED` je z nich nejzajímavější — task, který nelze označit za
  zablokovaný, je reálná produktová mezera.
- **Existující test asertoval bug jako správné chování.**
  `TestStatusPathFrom_UnreachableTarget` pinoval nedosažitelnost `DUPLICATE`.
  To je přesně to, proti čemu je pravidlo „failing test first" — test
  připnutý na špatné chování pak brání opravě.
- **`isTransientRunnerError` napájí i `retry_on` CEL predikáty**
  (`executor_retry_cel.go:130`), takže jeho chování je viditelné v autorování
  rutin, ne jen interní.
