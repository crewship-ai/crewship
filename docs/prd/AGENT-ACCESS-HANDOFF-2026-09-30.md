# Předání dalšímu modelu: Agent Access / Runtime Release 1.0

Stav ověřený **30. 9. 2026 07:49 UTC**. Dokument předává implementaci,
zdrojové soubory, důkazy, otevřené problémy a pořadí další práce. Není prohlášením
splněného PRD. Jde o Agent Access / Runtime, **nikoli UX Routines**.

## Aktualizace nástupce — 30. 9. 2026

Níže je historické předání ze 07:49 UTC. Nástupce převzal claim #2711
a opravil timestamp lint v `provider_linux_test.go` pomocí
`tsformat.Format(expires)`; test stále porovnává přesnou deadline včetně nanos.
Po této změně prošly celý `go test ./... -count=1 -timeout=30m -p=4`, celý
`go vet ./...`, timestamp lint, migration lint a agents invariants.
CI a review nového headu je nutné ověřit před merge, tato aktualizace je
nenahrazuje.

Nezávislá předběžná přejímka nástupce zopakovala 15/15 živých Docker testů
s race na HEAD `3acdaa260`, před opravou testovacího timestampu. Pracovní strom
tehdy obsahoval pouze tento necommitovaný dokument; produkční kód odpovídal
HEAD. Původní live report má nesrovnalost mezi deklarovaným čistým zdrojem
`7c1cad17` a vloženým logem `317d8257`/dirty. Není proto sám o sobě důkazem
čisté identity finálního harnessu. Nové lokální raw logy:
`/tmp/crewship-1-access-audit-live-20260930.log` a
`/tmp/crewship-1-pr2723-go-test.log` (dočasné, nejsou tracked artifacts).

Uživatel nyní autorizoval pokračování celého PRD a paralelní implementaci.
Ti pracují v oddělených worktrees na scoped kontextu, durable rezervacích
nákladů a produkčním restricted dispatchi. Žádná z těchto nových dodávek
zatím neznamená dokončení Release 1.0; vyžadují integraci a přejímku.

## 1. Co uživatel požaduje a co znamená hotovo

Uživatel chce dokončit celé PRD co nejefektivněji, včetně implementace, testů,
review, merge a skutečné přejímky na **Development 1**. Autorizoval pokračování,
opravování nalezených souvisejících chyb i nasazování na dev1. Chce skutečné
ověření přes API/CLI a Linux/Docker, nikoli závěry pouze z unit testů.

**Release 1.0 není hotový ani přijatý.** Restricted členství lze nastavit přes
API/CLI, ale běžný omezený chat, `crewship run` a rutiny ještě nejsou zapojené do
izolovaného runtime. Nelze slíbit, že klient jednoho agenta bezpečně používá celý
produkt. Odmítnutí dosud neintegrované cesty není pozitivní akceptace její funkce.
Počet PR ani počet prošlých testů není procentem dokončení. Přesné procento
neuvádět; rozhodují dosud otevřené průřezové akceptační brány.

## 2. Bezprostřední stav: nejprve opravit a uzavřít #2723

- Repozitář: `/srv/crewship/crewship_1`.
- Větev: `feat/restricted-provider-binding-2711`.
- Otevřené [PR #2723](https://github.com/crewship-ai/crewship/pull/2723).
- Head: `3acdaa2608ce6723b543d43f65a56e59ef1d52dd`.
- Poslední produkční změna: `7c1cad17d6b5328a2040ed8d86ffd58ff3d1c7fa`.
  Následující commit `3acdaa260` mění jen dokumentaci/důkazy.
- PR není merged, rozhodnutí `APPROVED`, merge `BLOCKED` kvůli nedokončenému/neúspěšnému CI.
- **Nový zjištěný blocker při předání: vzdálený Go Lint selhal.**
  Lokální Go testy a vet byly zelené; to tento lint nenahrazuje.
- CI run: [36684787001](https://github.com/crewship-ai/crewship/actions/runs/36684787001).
  Selhal [Go Lint job 109788105191](https://github.com/crewship-ai/crewship/actions/runs/36684787001/job/109788105191).
- Přesná příčina: `internal/restricteddispatch/provider_linux_test.go:187`
  zapisuje `expires.Format(time.RFC3339Nano)` přes `ExecContext`.
  `lint-tsformat` hlásí tři proximity nálezy stejné řádky. Samotný golangci-lint
  předtím v jobu vypsal `0 issues.`
- Doporučená oprava: použít standardní `tsformat.Format(expires)` a import
  `internal/tsformat`; ověřit zachování přesné deadline v regresi. Pokud by byl
  RFC3339Nano záměr testu, existuje odůvodněná výjimka `tsformat:allow`, ale není
  důvod obcházet konvenci bez konkrétní potřeby. **Oprava ještě nebyla provedena.**
- Spustit `go run ./scripts/lint-tsformat origin/main` a relevantní test,
  požadované kontroly po změně; potom push a kontrolovat nový finální head.
- **Aktualizace během psaní předání, 07:49 UTC:** CodeRabbit dokončil review
  finálního headu `3acdaa260` a schválil jej, review ID `5363096947`, bez actionable
  komentářů. `scripts/review-status.sh 2723` potvrzuje skutečné review podle
  walkthrough i schválení (prázdné tělo review samo by nestačilo).
  Předchozí zpráva této session o čekání na review je tím překonaná. Po opravě
  lintu bude nutné ověřit review nového headu; schválení starého není automatický
  důkaz revize nového diffu.
- Ostatní dlouhé Go/race/frontend joby ještě běžely. Security Result, frontend
  build, migrace, Shell, onboarding a Playwright subset již prošly. Stav znovu
  načíst; zde není tvrzení o finálním úspěchu celé vzdálené sady.

Při začátku zápisu tohoto předání byl pracovní strom čistý. **Tento předávací
soubor je záměrně místní necommitovaný soubor**: nový model jej má zachovat a
může jej přidat spolu s opravou lintu. Nebyl kvůli němu znovu spuštěn celý CI
pipeline a nebyl měněn produkční kód. Žádný další implementační WIP nezůstává.

Claim #2711 byl uvolněn:
[release komentář](https://github.com/crewship-ai/crewship/issues/2711#issuecomment-5906558290).
Nástupce musí zkontrolovat a převzít claim před prvním commitem. Aktivní cizí
claim nepřebírat. Všichni používají stejný GitHub účet, assignee není zámek.

## 3. Kde začít číst

1. [Výchozí PRD a rozhodnutí](RESEARCH-AGENT-ACCESS-RUNTIME-HEARTBEAT-2026-09-27.md).
2. [Implementační protokol](AGENT-ACCESS-RELEASE-1-IMPLEMENTATION-2026-09-27.md).
3. [A2/B akceptační matice po vstupech](AGENT-ACCESS-A2-B-TEST-MATRIX-2026-09-27.md).
4. [Průběžná implementace této session](AGENT-ACCESS-CONTINUATION-2026-09-29.md).
5. [Serverový kontrakt izolovaného runtime](RESTRICTED-RUNTIME-SERVER-CONTRACT-2026-09-28.md)
   a [původní runtime handoff](RESTRICTED-RUNTIME-FINAL-HANDOFF-2026-09-28.md).
6. [Správa grantů API/CLI](AGENT-ACCESS-POLICY-API-2026-09-29.md),
   [HTTP broker](RESTRICTED-HTTP-BROKER-CONTRACT-2026-09-28.md),
   [textový Responses adaptér](RESTRICTED-RESPONSES-ADAPTER-2026-09-29.md).
7. Zastřešující [#2703](https://github.com/crewship-ai/crewship/issues/2703),
   integrační [#2711](https://github.com/crewship-ai/crewship/issues/2711).

Staré věty o draftu či billing blokaci jsou historické. GitHub CI nyní běží;
aktuální lint chyba není billing problém. Podrobnosti bezpečnostních nálezů
nepublikovat bez rozmyslu: repozitář je veřejný.

## 4. Implementace dodaná v navazující práci této session

| PR | Stav a merge commit | Konkrétní výsledek |
|---|---|---|
| #2717 | merged `7bbb09832c42dff0fcb09f5e4600a31b8f24e49e` | Durable serverová autorita, přesné agent/project granty, konzervativní brány restricted cest, lidský původ před kontextem, kontrola doručování/revokace. |
| #2720 | merged `51a931f423830bdb3ca6403b2fe27740e7bb8758` | `access.Store` → izolovaný runtime přes aplikační adaptér; omezený SSE broker v2. |
| #2721 | merged `272b43f58793872c6db9241f715cf33fd8a0cdf1` | Verzovaná správa členských grantů přes API/CLI, konflikty 409, revokace, dvě lidské identity. |
| #2722 | merged `50a9ea900066144581e9ddfa6ad506cfca47ad6d`, 30. 9. 07:15 UTC | Hostová politika pro stateless textové Responses; přesný model a tokenový strop, uzavřené schéma, SDK alias `/v1/responses`. |
| #2723 | **OPEN**, head `3acdaa260` | Neměnná vazba aplikačního pokusu na provider credential/grant/model, monotónní revokace a zákaz rozšíření credentials při delegaci. |

PR #2722 mělo skutečné schválení CodeRabbit na finálním headu `5eacc19ba` a zelené
CI. ARM64 job vyžadoval jeden rerun kvůli 10s timeoutu existujícího Pages
collector testu; kód ani timeout se kvůli tomu neměnil. U #2723 je příčina
selhání známá a vyžaduje opravu; nepřenášet na ni vysvětlení z #2722.

Původní izolovaný runtime #2710 a broker #2715 dodal dříve souběžný agent na
dev2. Jeho původní důkazy nejsou mým opakováním těch testů. Následný rozšířený
harness s aplikační autoritou a provider vazbou jsem skutečně spustil na dev1.

## 5. Stav proti celému PRD

| Oblast | Dodané / doložené | Co ještě brání uzavření |
|---|---|---|
| A1, crew soubory | Směrové none/read/read_write, verze/409, traversal a requester opravy, API/CLI/Settings. | Souborové API samo neizoluje shell ve sdíleném crew kontejneru; rozsah shellu řeší restricted runtime. |
| A2, člověk–agent–projekt–operace | `access.Store`, persistentní granty, revize, scoped pokusy a rodiče; management API/CLI; neintegrované cesty denied. | Kompletní pozitivní provoz přes Files, artefakty, paměť, journal, logy/SSE, frontu, retry, delegaci a výstupy. Settings UI pro tuto politiku. |
| B1, odebrání při rutině | Recheck členství před dalším dispatch, nested/resume; fail closed při DB chybě. | Není to obecná jemná autorita ani okamžité zabití běžícího sdíleného procesu; zapojit restricted pokusy. |
| B2, ruční rutina | Serverový původ routine.run/routine.batch, aktuální role/capability při spuštění; replay aktuálním operátorem. | Přenos konkrétního lidského resource scope přes skutečnou frontu, obnovu a vnoření. |
| B3, Pages akce | Serverová identita akce/panelu/rutiny a fingerprint; změna nebo odstranění akce zastaví další krok. | Provázání s restricted runtime a úplným resource scope. |
| B4, čtení jednoho chatu | Odvolatelný krátký token, textová projekce, CLI a čtečka; audience sjednocené v #2716. | Není to obecná izolace dat klienta; původní živý pozitivní browser přepis byl prázdný, projekce obsahu automaticky testovaná. |
| C, spravované služby | Desired running/stopped, lease controller, obnova kontejneru a zachování dat/Stop na dev1. | Úplné kvóty, doložený reboot hosta/Dockeru a obnova; nerebootovat sdílený host kvůli testu dev1. |
| D, credentials | Agent/lease granty v sidecaru, deny-all DTO; runtime a broker; nově #2723 explicitní API-key binding a revokace/regrant. | Skutečné login/native CLI/provider adaptéry, refresh/account pravidla, přímé credentials a provozní přejímka. Sdílený UID nechrání env/soubory. |
| E, vstupy a IPC | Opravy cross-crew vstupů, publikum chatů, lidský admission, deny ceiling a stream rechecks. | Jedna autorita v celém skutečném řetězci chat → fronta → delegace → runtime → výstup, včetně služeb a všech producentů. |
| F, levný preflight | assigned_issues/has_work bez kontejneru/modelu; prázdný dev1 běh 61 ms/$0 a fixture benchmark. | Není rezervace práce ani celý heartbeat benchmark; deduplikaci svázat s durable autoritou. |

## 6. Kontrakt a navigace ve zdrojácích

### Serverová autorita

- `internal/access/store.go`, `attempt.go`: režim trusted/restricted, stabilní
  resource ID, konkrétní operace, revize členství a policy. Scope odděluje člověka,
  workspace, chat, členství/revizi a generaci/revizi chatu.
- `Admit` musí předcházet sestavení promptu a čtení paměti. Vrací opaque handle;
  v DB je jen jeho digest. `Resolve` znovu ověřuje všechny předky, členství,
  granty a chat. Dítě práva pouze zužuje. Retry je nový admission.
- Odebrání a přidání člena/grantu neobnoví starý pokus. Neodvozovat restricted
  režim automaticky z role VIEWER a nepovolovat alternativní cestu změnou role.
- Public task JSON nesmí vytvořit autoritu, rodičovský handle ani trusted builder.

### Aplikační runtime a provider binding (#2723)

- `internal/restricteddispatch/authority_linux.go`: `Prepare` uloží neměnný
  command pro admitted attempt; poslední resolve před uložením, neúspěšná
  příprava revokuje pokus. `Volume` stále odmítá všechny persistentní mounty.
- `provider_linux.go`: `PrepareResponses` připne provider před trusted builderem.
  Přijímá právě jeden explicitní `agent_credentials` grant s `OPENAI_API_KEY`.
  Agent i credential musí patřit správnému workspace; provider OPENAI, API_KEY,
  ACTIVE, security level 1/2, platné expirace. Není workspace/crew fallback,
  login refresh, výběr z poolu ani Keeper level 3/4.
- SHA256 revize zahrnuje ID a ciphertext; binding ukládá credential ID, grant ID,
  revizi, model a max_output_tokens 1–32768. Dítě musí mít stejný klíč/revizi/model
  a nesmí zvýšit tokenový strop. Jiné credentials cílového agenta se odmítnou.
- `BrokerSecret` dešifruje jen hostovému relayi, kontroluje binding a ještě jednou
  aktuální autoritu. Přijímá jen bearer syntaxi, odmítá JSON/whitespace/BOM.
  `Secrets` vrací prázdnou mapu: skutečný provider key není v procesu agenta.
- `Account` v tomto adaptéru znamená ID připnutého klíče, nikoli ověřené ID
  vendor účtu. Žádné caller account/project hlavičky se nepředávají.
- Migrace `internal/database/migrations/20260930070905_restricted_provider_bindings.sql`:
  immutable binding, zákaz přidání po připraveném launch, revokace při odstranění
  bindingu; triggery credentials/grant/agent změn. ID klíče/grantu jsou tombstones
  bez FK, aby jejich odstranění nezměnilo síťový pokus na offline režim.
- Mutace stavu a revokace pokusu jsou ve stejné DB transakci. Revoke/regrant mezi
  dvěma watchdog pollingy neobnoví proces ani oprávnění jeho potomků.
- `internal/backup/intent.go`: bindingy jsou runtime data, z workspace bundle
  vyloučené. Obnovený workspace nesmí zdědit starý execution verdict.
- `provider_linux_test.go`, `provider_live_test.go`: pozitivní/negativní případy,
  dva lidé stejného agenta, parent binding, revokace/regrant a skutečné kontejnery.

### Broker a omezení modelového provozu

- `internal/restrictedruntime/broker_responses.go`: stateless text, přesný endpoint
  `POST https://api.openai.com/v1/responses`, server model, tokenový strop,
  `store:false`, `stream:true`, uzavřené JSON schéma a kontrola duplicitních klíčů.
- Zakázané jsou resource reference, tools, previous_response_id, uploads, remote
  conversation, encrypted reasoning a další neklasifikované vstupy. Alias
  `/v1/responses` i operation-ID cesta mají tutéž hostovou kontrolu.
- Broker v2 omezuje request/response, délku streamu a kontroluje revokaci po
  framech; přímý síťový přístup agenta je zakázaný. Nejde o obecný internet.
- Per-request token limit **není** kumulativní rozpočet ani kompletní accounting.
- `internal/restrictedruntime` izoluje pokusy, UID1001 agenta a UID1002 broker,
  bez Docker socketu, s readonly root, omezenými tmpfs/cgroups, watchdogem a
  fail-closed chováním bez fallbacku do sdíleného crew.

## 7. Důkazy a přesný stav dev1

Dev1 API 8081, web 3011, veřejně `https://crewship-dev1.unifylab.cz`.
Běží čistý **`7c1cad17d6b5328a2040ed8d86ffd58ff3d1c7fa`**, build
`2026-09-30T07:29:10Z`, Go 1.27.1, linux/amd64. PID při předání 266663
(nespoléhat na něj později). Identita ověřená přes `/proc/<pid>/exe version`.
Web marker je `7c1cad17`. `dev.sh status` správně hlásí rozdíl proti HEAD
`3acdaa260`, který ale obsahuje pouze pozdější dokumentaci; není to neotestovaný
produkční diff. Kvůli samotnému předání nebyl znovu proveden reload.

- [Full Go/vet/race](reports/restricted-provider-binding-go-2026-09-30.txt):
  finální celý průchod 150 balíčků, exit 0; targeted race 124.359 s; full vet.
- [Živý Docker harness](reports/restricted-provider-binding-live-2026-09-30.txt):
  15/15 s race, vlastní syntetické DB a credentials, dvě izolované lidské identity,
  Linux oprávnění a absence klíče v env/souborech, ukončení po provider revokaci,
  fresh admission po vrácení grantu. Žádný skutečný placený model nebyl volán.
- [Mutace](reports/restricted-provider-binding-mutation-2026-09-30.txt): odstranění
  kontroly klíče/revize rodiče v Go overlay způsobilo skutečné selhání regrese.
- [Dev1](reports/restricted-provider-binding-dev1-2026-09-30.txt): čistá binárka/web,
  health/readiness 200, backend environ UID1000/mode0400, nová tabulka + 10 triggerů,
  autentizované CLI, dva klienti stejného agenta, revoke H1 bez vlivu na H2 a 409.
- API/CLI smoke na živé DB testoval management policy. Samotný nový provider
  binding testoval harness proti vlastním migrovaným fixture DB a Dockeru.
  **Není to end-to-end skutečného produkčního chatu s placeným providerem.**

Dřívější chyby ve vývoji: source guard credential loaderu odhalil chybějící
klasifikaci; přidána klasifikace i odmítnutí structured secrets. Další Go průchod
se překryl s reloadem a regenerací `web/out`, proto chyběly embedded manifesty.
Finální celý průchod proběhl až po dokončení čistého deploymentu a prošel.
Nový vzdálený tsformat lint z oddílu 2 zůstává neopravený.

## 8. Konkrétní pořadí pokračování

### Krok 0 — dokončit rozpracované odevzdání

Převzít claim, opravit tsformat, ověřit finální změnu a CI/review #2723. Nespouštět
opakovaně celé CI na nezměněném kódu kvůli průběžným poznámkám. Před merge ověřit
aktuální head a skutečné CodeRabbit review, ne jen zelenou kontrolu. Automatický
merge repozitář neměl povolený; ruční merge až po splnění všech bran. #2711
nezavírat — dříve jej automatické closing reference uzavřelo předčasně.

### Krok 1 — scoped prompt, historie a paměť

Napojit autoritu na skutečný serverový builder a zdroje kontextu. Výběr má stát na
serverovém principal/chat/resource scope, ne pouze agent_id nebo crew_id.
Zkoumat `internal/orchestrator/orchestrator_run.go:assembleSystemPrompt`,
`internal/conversation/store.go` a `internal/episodic/recall.go`.
Současný recaller dostává workspace/crew/agent/role, nikoli celý lidský scope.
Conversation JSONL se čte podle session ID; samotný správný název souboru není
kontrola aktuálního publika ani provenience.

Přejímka: H1/H2 stejného agenta s odlišnými canary texty; žádný cizí text ani
metadata v historii, promptu, recall, summary, konsolidaci, exportu či verzích,
také po restartu. Revokace před builderem i během přípravy nesmí vyvolat model
nebo uložit použitelný launch. Sdílené crew/persona/skill kontexty explicitně
klasifikovat. Není přijatelné jen filtrovat UI nad širokým promptem.

### Krok 2 — kumulativní účtování a rezervace před provider requestem

`internal/paymaster/budgets.go` a `middleware.go` výslovně dokumentují mezeru:
Enforce počítá jen již zapsaný spend, rezervace přes dobu skutečného LLM volání
neexistuje. Mutex při Check nebrání souběžnému přečerpání. Restricted broker
potřebuje durable, atomickou předběžnou rezervaci a následné vyúčtování svázané
s pokusem, principal/workspace, klíčem a modelem. Stanovit konzervativní chování
při chybě, odpojení streamu a pádu/restartu; unknown usage nesmí znamenat zdarma.
Děti, retries ani více broker requests nesmějí obnovovat celý dostupný rozpočet.
Testovat dvě souběžná volání a obnovu, nikoli pouze sekvenční limit.

### Krok 3 — skutečný dispatch a výstup

Zapojit běžný chat a CLI do admitted runtime až po krocích výše, následně skutečné
rutiny, Pages/Issues, frontu, retry a delegaci. Zachovat RBAC a původ akce jako
další podmínky, resource ceiling je nenahrazuje. Nenahrazovat chybějící autoritu
trusted/shared execution. Zavést publikum pro běh, výstup, log, SSE, journal,
artefakt a stažení; kontrola při přijetí nestačí, ověřovat při doručování.
Přejímka musí projít reálnými vstupy aplikace pro oba klienty, pozitivně i
negativně, včetně revokace za frontou a během streamu, restartu a nested běhu.

### Krok 4 — skutečné provider/login/native CLI adaptéry

Textový Responses adaptér úmyslně nepodporuje Codex tool loop ani subscription
login. Dřívější zjištění na dev1: běžní agenti používali CODEX_CLI/OPENAI a
PROVIDER_LOGIN. Znovu ověřit aktuální konfiguraci bez vypisování tajemství;
nový API-key adaptér sám takové agenty nerozběhne.

Syntetický předchozí probe: Codex 0.157.0 v image `crewship-cache:b6df4c50983f`
(host tehdy 0.159.0) uměl custom env_key provider a POST `/v1/responses` proti
mock SSE. ChatGPT login navíc volal models a automatický MCP/backend; změna
base URL sama nestačí. Neopisovat verze jako trvale aktuální kontrakt.
Potřeba: ověřené account/refresh binding, omezené nativní body a tool protokol,
provenience opaque reasoning/state, žádné plošné kopírování auth souborů a
žádné povolení obecných hostů jako zkratka. Ověřit kompatibilitu vnitřního CLI
sandboxu; tichý přechod do danger-full-access není akceptační řešení.

### Krok 5 — storage a provozní hranice

Dokončit katalog stabilních ID, zdrojovou provenienci, scope memory/checkpoint/
output a skutečně vynucené persistentní diskové kvóty. `Authority.Volume` zatím
správně vše odmítá; tuto bránu neodstranit pouhým zpřístupněním host path.
Prověřit traversal/symlink/writable alias, stejný agent se dvěma klienty,
zálohu/obnovu a revokaci přímých credentials. Doplnit kvóty spravovaných služeb.
Host/Docker reboot testovat na vyhrazeném izolovaném prostředku tak, aby test
dev1 nerestartoval ostatní instance; dosud takový důkaz nemáme.

### Krok 6 — finální akceptace celého Release 1.0

Vyplnit každý řádek A2/B matice důkazem konkrétního buildu: pozitivní i negativní
cesta, H1/H2 stejného agenta, aktuální policy, revokace, restart, Linux hranice.
Doplnit provozní body C/D/F a UI správu. Červený nebo pouze denied pozitivní
případ znamená otevřenou bránu. Teprve po úplné přejímce, CI a nezávislém review
aktualizovat PRD jako splněné a uzavírat #2711/#2703 v jejich skutečném rozsahu.

## 9. Praktická pravidla a příkazy pro nástupce

Číst `AGENTS.md`, `CODEX.md`, `CONTRIBUTING.md`. Pouze dev1; nesahat do dev2/3,
stage ani jejich procesů. Živou DB měnit přes CLI/API; přímé SQL jen read-only.
SQL zápisy ve vlastních testovacích DB jsou v pořádku. Neprovádět demo seed/reset.

```bash
cd /srv/crewship/crewship_1
pwd
git status --short
./dev.sh status
scripts/claim-issue.sh 2711 --check
scripts/claim-issue.sh 2711
gh pr view 2723 --json state,headRefOid,reviewDecision,mergeStateStatus
gh pr checks 2723
scripts/review-status.sh 2723 --checks
```

Pokud review opravdu chybí, použít cílený `scripts/review-status.sh --retrigger 2723`
podle pravidel/backoff; nespouštět `--all`. Dokončené review musí pokrývat finální
head po opravě. Při merge lze použít `gh pr merge 2723 --squash --match-head-commit
<ověřený-head>`; nenahrazovat review silovým merge.

Nasazení pouze `sudo systemctl reload crewship-ws@1`. **Nespouštět reload souběžně
s Go build/testy**, protože přegeneruje embedded `web/out`; také během buildu
necommitovat, jinak ztratí důkaz čisté identity. Po deploy ověřit skutečnou
binárku, marker webu, health/readiness, autentizované CLI a relevantní živé testy.
CLI instance: `/tmp/crewship-1-dev --server http://localhost:8081`.
Neodstraňovat tracked `web/out/.placeholder.html`, nevyrábět ručně HTML stub.

Celý Go průchod na tomto hostu používat s **vlastním exec-enabled tmpfs** pro
TMPDIR i GOTMPDIR. Běžný disk v této session vedl k extrémně pomalým DB testům.
Příklad (nový vlastní adresář, žádné sdílené mazání):

```bash
binding_test_dir=$(mktemp -d /tmp/crewship-1-tests.XXXXXX)
sudo mount -t tmpfs -o "size=4G,mode=0700,uid=$(id -u),gid=$(id -g),nosuid,nodev,exec" tmpfs "$binding_test_dir"
TMPDIR="$binding_test_dir" GOTMPDIR="$binding_test_dir" go test ./... -count=1 -timeout=30m -p=4
TMPDIR="$binding_test_dir" GOTMPDIR="$binding_test_dir" go test -race ./internal/restricteddispatch -count=1
go vet ./...
go run ./scripts/lint-tsformat origin/main
go run ./scripts/lint-migrations
go run ./scripts/agents-invariants
TMPDIR="$binding_test_dir" GOTMPDIR="$binding_test_dir" scripts/restricted-runtime-probe/run.sh -race
# Až skončí všechny procesy používající tento konkrétní mount:
sudo umount "$binding_test_dir"
rmdir "$binding_test_dir"
```

Předchozí vlastní tmpfs `/tmp/crewship-1-binding-test.wsUNai` je již odpojený a
adresář odstraněný. Soubor `/tmp/crewship-1-binding-test-dir` může obsahovat starou
cestu; nepoužívat ji bez nové přípravy. Žádné testy z této session už neběží.
Precommit lintu API může trvat několik minut. Migration lint počet „0 added“
se vztahuje na legacy Go registry; nové SQL soubory kontroluje samostatně.
Frontend při změně UI ověřit lint/build/testy; pnpm, nikoli npm/yarn.

Pomocné lokální skripty (nejsou garantované po vyčištění /tmp):
`/tmp/crewship-1-policy-live.py` vytváří vlastní syntetický workspace/dva klienty,
prověřuje policy přes CLI/API a workspace uklidí; syntetické účty zůstanou bez
členství. `/tmp/crewship-1-codex-provider-success.py` je starší syntetický CLI probe.
Před opakováním je přečíst; jejich existence nenahrazuje tracked testy a důkazy.
Aktuální lint log: `/tmp/crewship-1-binding-lint-handoff.log`.

Uživatel nyní výslovně předává práci jinému modelu. Toto kolo pouze sepsalo a
ověřilo předání; **neopravovalo lint, nemergovalo #2723 a znovu nenasazovalo**.
