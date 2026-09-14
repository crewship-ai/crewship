# Webhooky, paralelismus a paměť: rozhodnutí pro Crewship 1.0

**Release cíl: jeden chat a jeden background běh Jamieho současně v ověřeném Claude profilu; další práce čeká v trvalé frontě.** Zprávy v jedné session se zpracovávají postupně. Sériový fallback tento závazek nesplňuje.

Stav: rozhodnutý produktový a architektonický kontrakt, implementace a release ověření dosud nedokončené. Revize zahrnuje závěry diskuse z 10. září 2026. [Výzkumná příloha](WEBHOOKS-AGENT-PARALLELISM-ANNEX.md) uchovává audit, zdroje a omezení důkazů. Rozsah vydání určuje tento dokument spolu s normativním [implementačním a akceptačním kontraktem](WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md), který definuje API, stavový automat, limity, metriky, testy a rollout.

## 1. Uzavřená rozhodnutí

| Oblast | Rozhodnutí |
|---|---|
| Produkt | Jamie zůstává jedna identita. Oddělujeme session, logickou práci a jednotlivý pokus `run_id`. |
| Deployment | Jeden Go server, SQLite a existující pipeline/assignment infrastruktura. |
| Webhooky | Nové podpisové profily `github` a `standard-webhooks`; existující kontrakty přes legacy adaptéry. |
| Přijetí | Ověření → jedna transakce delivery + práce → `202` se stabilním receipt → asynchronní vykonání. |
| Fronta | Výchozí cesta rozšiřuje existující SQLite queue primitives pod jednoho vlastníka dispatch. River může nahradit implementaci jen po úspěšném [spiku](SPIKE-RIVER-SQLITE-1-0.md); Redis nepřidáváme. |
| Paralelismus | E0 je povinný release rozsah. E1 a E2 nejsou součástí 1.0. |
| Paměť | Markdown zůstává kanonický. Append má operation ID; replace vyžaduje CAS a deklarované removals. |
| Metadata | Provenance, scope, recorded time a známá validita tvrzení; neznámé časové údaje se nevymýšlejí. |

Nejde o tvrzení, že technologie jsou „nejpoužívanější“. Volíme doložené postupy pro náš provozní model; výzkumné kandidáty odlišujeme od osvědčených mechanismů.

## 2. Webhooky a trvalá práce

GitHub adaptér ověřuje nativní podpis nad raw body, čte event/action/delivery ID a filtruje před probuzením runtime. Ping ověřuje spojení bez LLM. Generic vstupy používají Standard Webhooks: podepsané ID, timestamp a payload; v 1.0 HMAC `v1`, více podpisů pro rotaci. Ed25519 není release podmínka. Zdroj: [specifikace](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md).

Deduplikace je scoped na workspace a endpoint. Retry stejné delivery vrací původní receipt; stejné ID s jiným payloadem je konflikt. Identita není odvozená pouze z podpisu, který se při novém timestampu změní. GitHub delivery hlavičky samy nejsou podepsané: replay politika proto pracuje i s ověřeným obsahem a identitou zdrojového eventu, podle možností daného typu události. Pro 1.0 také potlačuje byte-identické raw payloady v rámci endpointu a retence; přesný kontrakt je v implementační specifikaci §5.

Endpoint, target revision, payload/reference, filter decision a čekající práce se uloží atomicky. Pokud další doménový zápis nelze zahrnout do stejné transakce, potřebuje durable outbox. Runtime start není součást HTTP acceptance. DB nebo policy lookup failure nesmí způsobit „dispatch anyway“; vrací se odpovídající chyba. Nadlimitní tělo se odmítá `413`, nikoli zpracováním prefixu.

Obsazený agent znamená queued. Pouze nepřevzatá práce při plné ingress kapacitě dostane `429 + Retry-After`. `202` neznamená dokončení. Receipt vede přes autentizované, workspace-scoped API na stav, attempts a výsledek. UI rozlišuje přijetí, čekání, běh, dokončení, selhání a potřebu reconciliace.

Delivery se po selhání nemaže. Retry vytváří nový pokus stejné práce; ruční replay novou práci s `replay_of` a důvodem. Externí operace mají stabilní ID uložené před prvním voláním. Při nejasném výsledku nastává reconciliace, nikoli slepé opakování. Knihovna fronty nenahrazuje adapter-specific ochranu externích efektů.

GitHub automaticky neopakuje neúspěšné deliveries. Při nedostupném receiveru je nutná provider redelivery nebo samostatná reconciliace. Garance Crewshipu začíná úspěšným převzetím. [GitHub dokumentace](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries).

## 3. Tři samostatné garance durability

Dnešní [database.go](../../internal/database/database.go) používá WAL + `synchronous(NORMAL)`: commit přežije pád aplikace, ale může zmizet po pádu OS či napájení. [SQLite kontrakt](https://www.sqlite.org/pragma.html#pragma_synchronous).

| Garance | Povinný mechanismus |
|---|---|
| Přijatá práce nezmizí | Atomické delivery + enqueue, commit v režimu FULL před `202`, storage respektující fsync. |
| Dokončení a evidence operací nezmizí | Stejná durable politika pro autoritativní transitions a operation receipts; restart/recovery se nesmí opírat o slabší potvrzení. |
| Markdown a DB se po pádu sjednotí | Durable intent → zamčená kontrola revize → fsync/rename → durable potvrzení a audit; recovery každé mezery. |

Výchozí implementační volba je FULL na všech připojeních hlavní DB. Oddělený pool je přípustná optimalizace až po měření a prokázání, že všechny autoritativní zápisy používají správnou connection. Pragma je per-connection; náhodné přepínání uvnitř sdíleného poolu není přijatelné.

Recovery markdownu porovná skutečný obsah s původním a cílovým hashem intentu. Potvrdí aplikovaný zápis, bezpečně dokončí nepoužitý intent nebo označí konflikt. Další mutace téhož klíče nesmějí recovery předběhnout. FULL nevytváří společný commit DB a filesystemu ani garanci právě jednoho efektu ve vzdálené službě.

## 4. E0: povinná oddělená identita běhů

Každý běh má vlastní `run_id`, tmux session a související socket/signal naming, args/env/FIFO/exit, writable HOME/config, login file, workdir, output namespace a log stream. Session checkpoint zůstává přiřazen své session a zapisuje jej jediný aktivní tah. Cancel, attach a reap cílí konkrétní run; ukončení B nesmí změnit A.

Adaptér je způsobilý k paralelnímu profilu, pokud příprava, běh ani cleanup jeho autentizace nezapisují ani nemažou soubor sdílený s jiným aktivním během téhož agenta. File-delivered login používá per-run HOME. Claude je první ověřovaný profil; ostatní zůstávají sériové do splnění stejných kritérií. Codex/Gemini již dostávají placeholder refresh token: problém je společný mutable HOME, nikoli doložený závod CLI o skutečný refresh token.

E0 nemění UID 1001/1002, image ani crew container lifecycle. Bez plánované změny sidecar lifecycle musí test prokázat, že start B nezmění policy, credential attribution nebo memory identitu A; pokud to nelze splnit, jde o release blocker, nikoli odložený detail.

**Hranice profilu:** běhy sdílejí doménu důvěry, principal a container. Per-run cesty nejsou kernelové vynucení izolace. Shell může otevřít sousední cestu nebo obejít memory službu. Tento profil neslibuje sandboxovou ochranu před škodlivým či nedbalým zápisem. Omezení musí být uvedeno i u zapnutí profilu.

E1 zvažuje oddělené principals a vynucení přístupu; jeho rozsah přes image, terminal, mounty a sidecar určí [isolation spike](SPIKE-RUN-ISOLATION-E1.md). E2 přidává lifecycle samostatných run sandboxů. Obě fáze jsou mimo release 1.0.

## 5. Paměť a soubory

Transcript patří session, scratch běhu, artefakt má vlastníka a run ID. Sdílené znalosti mají scope; soukromý chat nesmí automaticky učit celý workspace. Oprávnění se ověřují při čtení, zápisu i pozdějším dispatchi.

Všechny podporované memory write cesty používají společný mutation kontrakt. Append se deduplikuje podle operation ID; čtení aktuálního souboru, cap a aplikace změny jsou pod stejným zámkem. Replace nese `expected_revision` a deklaraci `removals` vůči této revizi. Deklarace používá přesné odstraněné úseky s pozicí v podkladu nebo jiným jednoznačným identifikátorem; přesný formát, deterministický diff a normalizaci konců řádků určuje implementační specifikace §8.

Před zápisem se pod zámkem ověří revize a přesná shoda deklarovaných odstranění se skutečným diffem. Zastaralý podklad vrací `memory_conflict`, nedeklarované odstranění `undeclared_removal`. Opakovaná operation se stejným obsahem vrací původní výsledek, s jiným obsahem chybu. Nic se automaticky neobnovuje. Guard nepozná sémantickou chybu, kterou model zároveň deklaroval.

Bi-temporální metadata rozlišují `recorded_at` a známé `valid_from/valid_to`; změny, zdroj a odvolání se dohledají. Nejde o závazek přestavět celou paměť na graf. Ruční editace má provenance human a pro garantovaný zápis prochází verzovaným importem/editorem nebo řízenou offline editací. Přímý online shell/host write je v E0 mimo garanci CAS; drift musí být viditelný a nesmí být prezentován jako bezpečně sloučený.

Konsolidace vytváří návrh mimo lock a aplikuje jej přes CAS. Index je obnovitelná projekce; potvrzený zápis musí být přímo čitelný i při zpoždění indexu. Op-log jako zdroj pravdy a markdown jako projekce jsou výzkumný směr 1.1, nikoli vydávaná vlastnost.

## 6. Kapacita, plán a mapování nálezů

Všichni producenti používají společný admission kontrakt. Chat má rezervovanou kapacitu, background omezený podíl a aging; další zprávy mají durable mailbox a pořadí. Kapacita platí napříč zdroji, s limity server/workspace/crew/agent/provider a resource. Zachovat existující provider admission pro starty containerů. Zaparkované čekání ani parent čekající na dítě nesmějí vyčerpat execution sloty potřebné k pokračování.

| Původní nálezy | Řešení pro 1.0 / zbývající hranice |
|---|---|
| W1, W5, W8, W9 | Podpisové adaptéry, scoped delivery ledger, limit těla, fail-closed policy. |
| W2–W4, W6–W7 | Atomic acceptance, durable transitions, safe retry/reconciliation a delivery UI. |
| P1, P3, P5 | Durable chat mailbox a společný admission pro všechny vstupy. |
| P2 | E0: run-specific tmux, scratch a cancel/attach/reap. |
| P4 | E0: HOME/output/config/state attribution; nezabrání přímému přístupu sousedního procesu. |
| M1–M2 | CAS, operation IDs, removals a memory recovery; auditní verze sama nestačí. |
| M3 | E0 neřeší vynucené zabránění shell bypassu. Výslovné omezení; E1/E2 navazují. |

Pořadí: rozhodnout River a změřit FULL → acceptance/recovery → společný admission a mailbox → E0 a memory mutation kontrakt → GitHub/Standard Webhooks a delivery UI → release drill. Vývoj částí může probíhat souběžně; žádná nesmí obejít společné release podmínky.

Číselné výchozí limity, dosud nezměřené baseline, release metriky a testy T01–T14 jsou v implementační specifikaci §6 a §10–12. Nejde o již dosažený výkon.

Odpovědnost za výsledek technických ověření přidělí implementační issue. Termín River rozhodnutí je před implementací fronty; termín sidecar/E0 rozhodnutí před zapnutím parallel flagu. E1 rozhodnutí nemění povinný rozsah E0 ani automaticky neblokuje omezený profil 1.0.

## 7. Důkaz splnění a co 1.0 nedělá

Release musí mít záznam těchto end-to-end výsledků na deklarovaném adapteru a hardwaru:

1. Native GitHub event a Standard Webhooks retry vytvoří jednu práci; ping nevzbudí LLM. Neplatný podpis, policy lookup failure a příliš velké tělo nic nespustí.
2. Crash před/po acceptance commit a před odpovědí zachová správnou identitu práce; restart obnoví queued práci. Zvlášť ověřit storage konfiguraci pro power-loss garanci.
3. Jeden Jamie současně vede chat a dokončuje background práci; další zprávy se neztratí. Streams, HOME, artefakty i credentials se nepřepíšou. Cancel B nezastaví A.
4. File auth write/remove a sidecar start druhého běhu nezmění první. Testovat přípravu i cleanup, nejen hlavní exec.
5. Dva replace z jedné revize nezpůsobí lost update; neohlášené removals se odmítnou. Append retry nevytvoří duplicity; crash mezi intent/rename/confirm je obnovitelný.
6. Lost completion a nejasný externí efekt nevedou ke slepému opakování. Stale worker nemůže potvrdit operaci přes podporované API; E0 shell bypass zůstává označeným omezením.
7. Vyčerpání kapacity neztratí accepted práci; chat i background dostávají kapacitu. Odebrání oprávnění během čekání se projeví při dispatchi.

1.0 nedělá multi-host execution, vynucenou izolaci běhů E1/E2, univerzální parallel pro všechny CLI, automatické sémantické slučování paměti ani přesně-jednou externí efekty bez podpory cílové služby. Nezavádí Temporal, CRDT nebo novou vector DB.

**Stav ověření:** zatím dokumentace a audit kódu. Původní `go vet` prošel, úplný `go test` skončil timeoutem API/database; podrobnosti a prošlé balíčky jsou v příloze. Technická ověření River/E1 ani uvedený release drill nebyly provedeny. Aktualizace dokumentů není implementace ani potvrzení provozní připravenosti.
