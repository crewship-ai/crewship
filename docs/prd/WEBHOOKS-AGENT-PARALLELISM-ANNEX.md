# Výzkumná příloha: webhooky, paralelismus a paměť

Tato příloha uchovává audit a zdroje původního návrhu z 10. září 2026. **Rozsah a závazná rozhodnutí určuje [rozhodovací PRD](WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md).** Původní závazek širšího souběhu a povinného sandboxu nahrazuje profil 1+1 s E0; omezení zůstávají výslovně uvedená v hlavním PRD. Nálezy níže nezmizely přesunem do přílohy.

Evidence původního auditu odpovídá revizi `54058e87755c57e954dbe8269874df201319e8e8`. Dodatečně ověřené závěry ze společné revize jsou v části „Doplnění“. Uvedené testy jsou historický výsledek tohoto auditu, nikoli nové ověření implementace navrhované architektury.

## 1. Návaznost na existující návrhy

Tento dokument doplňuje [release audit](PRD-RELEASE-1-0-QUALITY-AUDIT.md), [readiness report](RELEASE-1-0-READINESS-2026-08-10.md), [Issues and Routines](PRD-ISSUES-AND-ROUTINES-2026.md) a [hybridní rutiny pro 1.0](ROUTINES-HYBRID-RELEASE-1-0-2026-09-08.md). Poslední dva již popisují kolize `AgentRunLock`, návazné zprávy a potřebu vlastnictví souborů. Existující issue sessions a jejich unikátní index se nemají nahradit novým paralelním systémem konverzací.

[Crew runtime capacity](crew-runtime-capacity.md), [agent isolation](agent-isolation-findings-2026-08-01.md), [memory on wake](agent-memory-on-wake.md) a [memory retrieval](memory-retrieval-layer.md) obsahují další návrhy a historická měření. Jejich staré závěry o stavu implementace nelze automaticky přenést na dnešní kód. Například dnešní tmux příprava už některé zápisy slučuje do jednoho exec volání.

Dlouhodobé mantinely jsou v [Memory Roadmap](../../.claude/context/prd/MEMORY-ROADMAP-2026.md), [Agent Continuity](../../.claude/context/prd/AGENT-CONTINUITY-2026.md) a [Queue Mechanism](../../.claude/context/prd/QUEUE-MECHANISM-2026.md): čitelná markdown paměť, SQLite, explicitní události, trvalý kolega a obnova práce. Návrh zachovává tyto principy. Zpřísnění přímého zápisu do sdílených souborů v paralelním režimu je vědomá změna kontraktu, popsaná v §7.

## 2. Co dnes opravdu existuje

### 2.1 Tři odlišné webhookové povrchy

| Povrch | Dnešní kontrakt | Co je důležité pro 1.0 |
|---|---|---|
| Agent: `POST /api/v1/webhooks/{crewId}/{agentId}/trigger` | Vlastní JSON `event/source/data`; `X-Signature`, případně timestamp nebo deprecated plaintext secret; spouští `RunAgent` | Není přímo kompatibilní s GitHubem; samostatná cesta mimo společný agentní zámek |
| Rutina: `POST /api/v1/webhooks/{token}` | Capability URL a povinný podpis `X-Crewship-Signature`; payload, raw a headers jako vstupy pipeline; pin verze, dedup, limit, async dispatch | Nejvhodnější základ pro produkční události; stále má mezeru mezi rezervací a trvalým spuštěním |
| Page: `POST /api/v1/page-webhooks/{token}` | Uložení dat pro panel jménem držitele tokenu | Datový vstup pro stránku; samotný název webhook neznamená probuzení agenta |

Ověřeno v [agent handleru](../../internal/api/webhook.go), [ověření podpisu](../../internal/webhook/handler.go), [pipeline handleru](../../internal/api/pipeline_webhooks.go) a [page handleru](../../internal/api/pages_webhooks_inbound.go). API registrace je v `router_orchestration.go`, `router_pipelines.go` a `router_pages_webhooks.go`.

Rutinní webhooky již mají UI pro vytvoření, úpravu, zapnutí, limit a rotaci secretu. [RoutineWebhooksTab](../../components/features/routines/routine-webhooks-tab.tsx) ukazuje poslední stav a jednorázově odhaluje secret. Není správné tvrdit, že podpora webhooků vůbec neexistuje. Zároveň zde chybí ucelený průvodce GitHub integrací a historie jednotlivých doručení s retry/replay. Text prázdného stavu „Optionally protect it…“ nesouhlasí s backendem, který podpis vyžaduje.

### 2.2 Silné stránky, které zachovat

Existuje ověření HMAC, omezení velikosti čteného payloadu, rate limiting, deduplikace, šifrování uložených secretů a u rutin hashování capability tokenů. Webhooková data pro agentní prompt procházejí trust fence a webhookové běhy mají omezený egress. Rutiny mají verze, per-step výsledky, retry, waitpointy a obnovu některých přerušených běhů.

Assignments již umějí atomicky získat crew slot a čekat při obsazeném agentovi. Pipeline má `concurrency_key`, registry a dispatcher odložených běhů. [Admission controller](../../internal/admission/admission.go) už na úrovni Docker/Apple providerů omezuje starty containerů a rozestupy; na Linuxu zohledňuje host memory/PSI. Toto není chybějící funkce, přestože historický runtime PRD ještě popisoval její absenci. Paměť má durable zápis, advisory file locks, verze obsahu, indexaci, karanténu a schvalování některých společných znalostí. Potřebná práce spočívá hlavně ve sjednocení garancí a izolaci, nikoli v přepsání celé orchestrace.

### 2.3 Nálezy a závažnost

P0 znamená blokátor slíbeného chování 1.0; P1 podstatnou produktovou či provozní mezeru. Jde o prioritizaci tohoto návrhu, ne o bezpečnostní klasifikaci CVSS.

| ID | Priorita | Nález a důsledek | Evidence |
|---|---|---|---|
| W1 | P0 | Nativní GitHub podpis a událost nemají adaptér. GitHub posílá `X-Hub-Signature-256: sha256=…`, `X-GitHub-Event` a `X-GitHub-Delivery`. Agent i rutina očekávají jiné hlavičky; agent navíc jiný JSON. Pouhé vložení URL do GitHubu nestačí. | `internal/webhook/handler.go:126`, `internal/api/pipeline_webhooks.go:624`; [^1][^2] |
| W2 | P0 | Agentní webhook zahřívá container před `202`. Cold start nebo problémy provisioningu mohou překročit čas příjmu poskytovatele. | `internal/api/webhook.go`, `trigger`, kroky 3–6; GitHub.com očekává odpověď do 10 s [^1] |
| W3 | P0 | Pipeline uloží idempotency rezervaci, poté spustí goroutinu a vrátí `202`. Crash před vytvořením vlastního runu může nechat rezervaci bez obnovitelné práce; retry může deduplikovat na neexistující run. | `internal/api/pipeline_webhooks.go:787–820`; rezervace nenese celý dispatch vstup |
| W4 | P0 | Agentní cesta po chybě `CreateRun` pouze loguje a pokračuje; po chybě idempotency rezervace také pokračuje. Záznam úspěšného převzetí není tvrdou podmínkou spuštění. | `internal/api/webhook.go`, `trigger` |
| W5 | P1 | Identita události je nekonzistentní. Agent preferuje podpis před delivery ID; nový timestamp mění podpis. Pipeline explicitní klíč neprefixuje webhookem a store má workspace-level klíč. Identická těla mohou být dvě různé události, nový podpis naopak tentýž retry. | `agentWebhookIdempotencyKey`, `webhookIdempotencyKey`, `internal/pipeline/idempotency.go` |
| W6 | P0 | Pipeline po `FAILED` uvolňuje dedup klíč. Retry tak může zopakovat již úspěšné externí akce před selháním. Nejde o důkaz konkrétní duplicity, ale o nebezpečný retry kontrakt. | `internal/api/pipeline_webhooks.go:893–904`; `pipeline_webhooks_failed_forget_test.go` tento kontrakt výslovně očekává |
| W7 | P1 | Deduplikace není delivery ledger. Rutina má poslední fire a run logy, ale chybí jednotný přehled všech převzatých doručení, neúspěšných dispatchů, pokusů a replayů. | `WebhookStore.RecordFire`; `RoutineWebhooksTab` |
| W8 | P1 | `io.LimitReader` omezuje načtená data, ale samo neprokáže překročení limitu a nevynutí `413`. Agentní rate/cap chyby navíc obalový handler mapuje na obecnou `500`. | oba inbound handlery; `internal/webhook/handler.go` |
| W9 | P1 | Při chybě lookupu politiky „vyžaduj timestamp“ agentní handler přejde na optional. Jde o oslabení nastavené replay politiky při provozní chybě. | `internal/webhook/handler.go`, `requireTimestamp` |
| P1 | P0 | Chat odmítá při obsazeném agentovi ještě před persistencí zprávy. To vysvětluje nutnost ručního opakování zprávy. | `internal/chatbridge/bridge.go:735–780` |
| P2 | P0 | `TmuxSessionName` je pouze `agent-{slug}`; args, script, FIFO, exit a env mají stejné odvození. Setup nejprve zabije stejnojmennou session. Druhý běh může ukončit první. | `internal/orchestrator/orchestrator_exec_env.go:71`, `:159`, `:238` |
| P3 | P0 | Sdílený `AgentRunLock` používají chat, assignments, cron a routine runner. Agentní webhook, direct run a peer query ho podle implementace/kontraktu nepoužívají. Limit osmi webhooků není ochrana společného runtime. | `internal/chatbridge/agent_run_lock.go`; `internal/api/webhook.go` |
| P4 | P0 | Agent sdílí `/output/{slug}`, části HOME/config/secrets a stav orchestrace je klíčován `ChatID`. Pouhé přejmenování tmux session neřeší tyto konflikty. | `orchestrator_run.go:344`, `:1342–1486`; `AgentRunRequest` |
| P5 | P1 | Busy je u assignments důvod čekání, u chatů odmítnutí a u routine agent step chyba pod retry politikou. Jedna běžná situace má tři různé výsledky. | `assignments_dispatch_pump.go`; `bridge.go`; `pipeline/runner_orchestrator.go:191–227` |
| M1 | P0 | `memory.write(mode=replace)` nemá expected revision. Lock chrání zápis, ale ne změny od okamžiku, kdy model četl podklad pro nový celý dokument. | `internal/memory/tools.go`, `writeArgs`, `handleWrite`; `internal/sidecar/memory_write.go`, `MemoryWriteRequest` |
| M2 | P1 | `RecordVersion` ukládá auditní verzi; není CAS nad kanonickým souborem. `ParentSha` není podmínkou odmítnutí zastaralého zápisu. | `internal/memory/versions.go:83–140` |
| M3 | P0 pro parallel | Advisory zámky neomezí shell, který přímo přepíše dostupný soubor. Samostatné adresáře pod stejným UID nejsou izolací proti zápisu do sousedního běhu. | smlouva `FileLock`, file-first model, společné cesty runtime |

Nativní `append` **není** uveden jako ztrátový: `handleWrite` zamyká čtení, kontrolu limitu i durable write. Regresní test `TestDispatch_Write_AppendCap_NoTOCTOU` tento rozdíl dokládá. Je nutné oddělit integritu bajtů, konflikt stale replace a sémantický rozpor znalostí — každý vyžaduje jinou ochranu.

## 3. Jak to řeší současné systémy

Srovnání je architektonické, nikoli žebříček výkonu. Dokumentace jednotlivých produktů neprokazuje propustnost Crewshipu ani neomezenou bezpečnost jejich sdíleného stavu.

| Systém / vrstva | Doložený přístup | Co převzít | Co z něj nevyvozovat |
|---|---|---|---|
| LangSmith Deployment / Agent Server | Double texting má `enqueue` jako default, dále `reject`, `interrupt`, `rollback`. Funkce není automaticky součástí samotného open-source LangGraphu. [^4] | Explicitní politika druhé zprávy; fronta uvnitř threadu | Rollback stavu nevrátí automaticky již odeslaný email |
| LangGraph memory | Krátkodobý stav patří threadu a checkpointům; dlouhodobá paměť má vlastní namespaces napříč sessions. [^5] | Oddělit historii konverzace od společných znalostí | Společný store sám neřeší konflikty přepisů |
| Letta | Shared blocks; append operace je označena jako concurrent-safe, targeted replace může selhat při změně cíle, full rethink je last-writer-wins. Doporučuje jednoho vlastníka rozsáhlých editací. [^6] | Samostatně přidávané poznatky a jeden kurátor společných dokumentů | Stateful agent ani sdílená paměť neznamenají bezpečné libovolné přepisy |
| Anthropic Research | Samostatné kontexty pracovníků; výstupy lze ukládat jako artefakty a předávat odkazy. Článek popisuje i vyšší tokenové náklady koordinace. [^7] | Izolované úkoly, jasné výstupy a levné reference | Každý webhook nepotřebuje dalšího manažerského LLM agenta |
| Inngest | Concurrency podle klíčů a scope; obsazená kapacita znamená frontu. Čekající/sleeping kroky nespotřebovávají execution slot. Event ID má dokumentované 24h dedup okno. [^8][^9] | Oddělit limit souběhu, rychlost přijímání a dedup; účtovat aktivní práci | Deduplikace eventu není idempotence každého externího efektu |
| Temporal | Trvalá orchestrace a opakování Activities; externí operace musejí mít vhodnou idempotenci. Výpadek může nastat po efektu a před potvrzením. [^10] | Historie pokusů, stabilní identita operace, recovery | Není důvod kvůli 1.0 ihned přidávat Temporal cluster |
| n8n | Ingress a workers jsou oddělené; queue mode používá Redis a DB. Distribuovaný queue setup se SQLite není podporovaný. [^11] | Oddělení rychlého HTTP příjmu od dlouhé práce | Jeho distribuované omezení neznamená, že lokální SQLite fronta v jediném Go procesu nestačí |
| CrewAI | Dokumentace rozlišuje nativní async `akickoff`/`akickoff_for_each` a thread-based `kickoff_async` varianty. [^12] | Rozlišit způsob souběžného spouštění od organizace úkolů | Async metoda sama nedokládá ochranu sdíleného filesystemu a stale memory replace |

Z těchto zdrojů plyne doporučení: **serializovat společný konverzační stav a konfliktní mutace, paralelizovat nezávislé běhy**. Je to syntéza pro Crewship, nikoli tvrzení, že všechny produkty používají stejný interní mechanismus.

## Doplnění po kritické revizi

### Standard Webhooks

Nové obecné vstupy použijí Standard Webhooks, nikoli vlastní podpisový protokol. Podepisuje se `msg_id.timestamp.payload`; `webhook-id`, `webhook-timestamp` a `webhook-signature` oddělují stabilní identitu od podpisu konkrétního doručení. Specifikace obsahuje HMAC-SHA256 (`v1`) i Ed25519 (`v1a`) a seznam podpisů pro překrytí při rotaci. Pro 1.0 je povinný HMAC profil; asymetrická varianta je rozšíření, nikoli neověřený release závazek. Zdroj: [Standard Webhooks specification](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md).

Standard nezajišťuje naši durable deduplikaci, retenci, správu důvěryhodných klíčů, správné mapování payloadu na rutinu ani fail-closed lookup politiky. GitHub zůstává samostatným nativním profilem. Stávající endpointy dostanou kompatibilní legacy adaptéry.

### SQLite a tři různé garance

[database.go](../../internal/database/database.go) nastavuje WAL, `synchronous(NORMAL)`, `busy_timeout(30000)`, `_txlock=immediate` a pool až pěti připojení. NORMAL v režimu WAL chrání konzistenci a přežije pád aplikace, ale poslední potvrzené transakce mohou po pádu OS nebo napájení zmizet. FULL přidává synchronizaci WAL při commit. Zdroj: [SQLite synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous).

Přijetí práce, následné durable přechody/operation receipts a recovery markdown+DB jsou samostatné kontrakty. FULL pouze při enqueue nechrání ztracený completion. Ani FULL na celé DB nevytvoří atomickou transakci s markdown souborem nebo vzdáleným API. Garance předpokládá storage respektující fsync.

### River versus vlastní fronta

[riversqlite](https://raw.githubusercontent.com/riverqueue/river/master/riverdriver/riversqlite/river_sqlite_driver.go) používá `database/sql`, umí obalit existující `*sql.Tx` a jeho dokumentace uvádí early testing s omezenou reálnou zkušeností. Doporučený pool s jedním připojením snižuje vlastní souběh driveru; nezastaví jiné pooly nad stejným souborem.

River je kandidát na mechanismiku fronty, nikoli hotové řešení session ordering, sidecar capability, memory fencing a idempotence externích efektů. [Zadání ověření](SPIKE-RIVER-SQLITE-1-0.md) musí před volbou knihovny ověřit transakčnost, restart, late completion a souběh se skutečným nastavením Crewship DB. Alternativa je rozšíření existujících assignments/pending_runs, nikoli automaticky třetí nezávislá fronta. Goqite je užší kandidát pro transport práce, nikoli doložená náhrada celého workflow kontraktu; v této revizi nebyl proveden jeho implementační audit.

### Autentizace: oprava nesprávné hypotézy

[codexauth.Render](../../internal/codexauth/codexauth.go) i [geminiauth.Render](../../internal/geminiauth/geminiauth.go) v doručovaném loginu nahrazují skutečný refresh token placeholderem. Hypotéza závodu dvou CLI o skutečný refresh token se touto cestou nepotvrdila.

Ověřený problém je v [syncLoginFile](../../internal/orchestrator/auth_delivery.go): zapisuje nebo odstraňuje login v HOME podle agent slug. Druhý běh proto může změnit soubor prvního. Dopad na již běžící CLI závisí na tom, kdy znovu čte soubor; nelze z komentáře o startup fallbacku odvodit, že každý běh nutně utratí celý pokus za 401. `last_refresh` ovlivňuje rozhodování CLI, ale není důkazem sdílení skutečného refresh tokenu.

Claude nemá file-delivered login a je vhodný první pilot. Nadále sdílí jiné cesty a runtime stav, které musí E0 oddělit. Způsobilost profilu se testuje na celém životním cyklu, včetně přípravy a cleanup, nikoli pouze podle názvu adaptéru.

### E0, E1 a E2

E0 znamená vlastní run ID, tmux/socket naming, args/env/FIFO/exit, HOME, workdir, outputs, checkpointy a cílení cancel/attach/reap. Zachovává UID a crew lifecycle. Pod společným UID nejde o vynucenou izolaci: shell může změnit sousední cesty i obejít memory tool.

E1 zvažuje odlišné principals a kernelové vynucení přístupu. Rozsah zahrnuje image/development features, terminal exec, chown/mounty, secrets, tmux a sidecar autorizaci. `0700` s odlišným UID je skutečné DAC omezení, ale sdílená síť a služby vyžadují další analýzu. Overlay mount není automatickým důsledkem UID a musí mít bezpečný mechanismus přípravy. [Technické zadání](SPIKE-RUN-ISOLATION-E1.md) neurčuje předem vítěznou variantu ani odhad v dnech.

E2 přidává lifecycle samostatného run sandboxu vedle crew služeb. Modely OpenHands, Docker Sandboxes, E2B, Modal a Daytona jsou kandidáti pro další srovnání; jejich konkrétní GA data, výkonnostní čísla a závěr „odvětví se sjednotilo“ nejsou touto revizí ověřeny a nepoužívají se jako release evidence. Samostatný container také není synonymum samostatného kernelu. Pro 1.0 je rozhodující ověřené chování Crewship E0, nikoli marketingové srovnání sandboxů.

### Paměť a směr 1.1

CAS chrání replace proti zastaralému podkladu. Deklarované removals chrání proti nedeklarovanému odstranění bajtových úseků; nepoznají pravdivost ani to, že model vědomě deklaroval chybnou změnu. Při odmítnutí se nic automaticky neobnovuje. Ochrana explicitně označených záznamů je samostatná policy.

Bi-temporální metadata rozlišují platnost tvrzení a čas jeho zaznamenání. Neznámá platnost zůstává neznámá. Rozpor, oprava a odvolání si ponechají provenance a scope. Přidání metadat nevyžaduje přechod na graph/vector databázi.

[StateFuse](https://arxiv.org/html/2607.05844v1) je výzkumný kandidát na kontrakt immutable operací, explicitních konfliktů a correction handles, ne univerzálně lepší memory engine. Correction-handle ablace má 13 úloh; kontrolovaný agent loop 50 úloh. Výsledek 100 % versus 60 % po verifikaci není unikátní vítězství StateFuse: stejného výsledku dosahují také jiné conflict-preserving baseline. Na 282 otázkách oficiálního výřezu benchmarku se silné varianty shodují v accuracy. Jde o podporu viditelných konfliktů a opravitelných tvrzení, nikoli důkaz jediné správné architektury.

Pro 1.1 vyhodnotit append-only operations jako zdroj pravdy a markdown jako projekci. Ruční editace potřebuje explicitní převod na operace s provenance human; log ji sám bezpečně neinterpretuje. Nadále potřebujeme autorizaci retrakcí, stabilní identitu operací a pravidla konfliktů. Mem0/Zep/Graphiti/Cognee nejsou v této revizi výkonově porovnány; žádné převzaté leaderboard číslo není podkladem rozhodnutí pro 1.0.

## Stav ověření

### Stav ověření podkladu

Staticky byly prověřeny relevantní handlery, runtime naming, agentní zámek, queue primitives, provider admission, nativní memory write a UI webhooků. Ověření na dev2 dne 10. září 2026:

| Kontrola | Výsledek |
|---|---|
| `go vet ./...` | PASS, exit 0 |
| `go test ./... -count=1` | FAIL, exit 1: `internal/api` a `internal/database` ukončeny timeoutem 10 minut na balíček |
| `internal/webhook`, `internal/chatbridge` v tomto běhu | PASS, přibližně 0,004 s a 0,321 s |
| `internal/orchestrator`, `internal/pipeline` v tomto běhu | PASS, přibližně 37,9 s a 16,7 s |
| `internal/memory` v tomto běhu | PASS, přibližně 103,1 s |
| Lokální odkazy a formát dokumentu | Ověřena existence všech odkazovaných lokálních cest |

Test log neobsahoval záznam `--- FAIL:` před timeouty. To neopravňuje označit nedokončené balíčky za zelené; z tohoto běhu nelze rozlišit pouze pomalý průchod od dalšího problému. API webhook testy proto nelze jako celek považovat za dokončené. Protokol příkazů je v `/tmp/crewship-2-webhooks-parallelism-go-test.log` a `/tmp/crewship-2-webhooks-parallelism-go-vet.log` na analyzované instanci; tyto dočasné cesty nejsou přenosný release artefakt.

Živý GitHub delivery, crash drill ani benchmark více Jamie runtime nebyly v tomto návrhu provedeny. Závěry oddělují doložený kód, výsledky existujících testů a navrhované garance. Výstup této práce je dokument; runtime ani databázové schéma se tímto návrhem nemění.

## Zdroje

Lokální odkazy v textu odkazují na analyzovaný checkout; čísla řádků jsou orientační pro uvedenou revizi. Externí dokumentace byla ověřena 10. září 2026; pokud zdroj neuvádí datum vydání, neodvozujeme ho z data indexace. Číslované reference označují konkrétní zdroje pro předcházející tvrzení.

[^1]: GitHub. [Best practices for using webhooks](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks). GitHub.com, průběžná dokumentace: 10s response, event/action a redelivery ID.
[^2]: GitHub. [Validating webhook deliveries](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries). Průběžná dokumentace: raw body, HMAC-SHA256, podpisová hlavička.
[^3]: GitHub. [Handling failed webhook deliveries](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries). Průběžná dokumentace: neexistence automatické redelivery a možnosti obnovy.
[^4]: LangChain. [Double texting](https://docs.langchain.com/langsmith/double-texting). LangSmith Deployment / Agent Server; odlišení od open-source frameworku.
[^5]: LangChain. [Memory overview](https://docs.langchain.com/oss/python/concepts/memory). Thread state, namespaces, hot-path a background memory.
[^6]: Letta. [Shared memory](https://docs.letta.com/v1-sdk/memory/shared-memory). Zvláště oddíly Read-only blocks a Concurrency; insert/replace/rethink a vlastník velkých změn.
[^7]: Anthropic. [How we built our multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system). Engineering, 2025; oddělené kontexty, náklady koordinace a ukládání artefaktů.
[^8]: Inngest. [Concurrency management](https://www.inngest.com/docs/guides/concurrency). Aktivní kroky, keys, scopes a čekající práce.
[^9]: Inngest. [Handling idempotency](https://www.inngest.com/docs/guides/handling-idempotency). Stabilní event ID a časově omezené dedup okno.
[^10]: Temporal. [What is idempotency? And why it matters for durable systems](https://temporal.io/blog/idempotency-and-durable-execution). Retry Activities a identita externích operací.
[^11]: n8n. [Enable queue mode](https://docs.n8n.io/deploy/host-n8n/configure-n8n/scaling/enable-queue-mode). Workers, Redis, persistent DB a omezení distribuovaného SQLite setupu.
[^12]: CrewAI. [Crews — Kicking Off a Crew](https://docs.crewai.com/v1.15.21/en/concepts/crews). Dokumentace verze 1.15.21, rozlišení native async a thread-based execution.

## Implementační rozhodnutí po doplnění kontraktu

Normativní [implementační specifikace](WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md) doplňuje datový a HTTP kontrakt, společného vlastníka fronty, retenci, výchozí kapacitu, baseline protokol a testy T01–T14. Výchozí volba zůstává Go + SQLite s rozšířením současných primitives; River je podmíněná implementační alternativa, nikoli nový produktový požadavek. [goqite](https://github.com/maragudk/goqite) nabízí i transakční metody, ale samotná minimální queue neodstraňuje nutnost doménového admission/recovery kontraktu. Redis nepřidáváme kvůli další službě a oddělenému commit prostoru; nejde o tvrzení, že Redis nelze provozovat spolehlivě.

Pro posouzení Riveru se kontroluje také rozsah OSS/Pro: [produktová dokumentace](https://riverqueue.com/) označuje některé pokročilé funkce jako Pro. Schopnosti ověřovat na připnuté verzi. Metriky ve specifikaci jsou návrhové cíle, nikoli převzaté benchmarky nebo již naměřené výsledky Crewshipu.
