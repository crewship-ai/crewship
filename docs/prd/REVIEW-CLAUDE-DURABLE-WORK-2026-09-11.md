# Review Claude Code: durable work, paralelismus a paměť

Datum: 2026-09-11. Review checkoutu `5a4088fb`, branch `feat/durable-work-parallelism`. Navazuje na [PRD](WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md) a [implementační kontrakt](WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md). Toto je review a následné zadání, nikoli schválení release. Runtime ani schéma nebyly při review změněny.

## Závěr

Základní komponenty výrazně pokročily. Předchozí chyby držení kapacity při reconciliaci a blokovaného prefixu fronty jsou opravené. Přibylo E0, hostové memory API, durable webhook acceptance, work API/UI, metriky a crash harness. Nejdůležitější zbývající práce je sjednocení vlastníka vykonávání a uzavření skutečných integračních cest.

Samotné propojení dnešních komponent nestačí: review reprodukovalo tři chyby v přechodech stavů. Nezapínat parallel profil a nepovažovat současné Cancel API za ověřený stop runtime.

## Co Claude udělal dobře

- River zkoumal v samostatném modulu; rozhodnutí nezaneslo závislost do produkčního Go modulu. ADR rozlišuje test SQL predikátu od skutečného crash lifecycle knihovny. Pool-1 experiment dokazuje konkrétní nested-connection problém, nikoli obecnou nemožnost použít River; reject pro rozsah 1.0 je přesto obhajitelný.
- Po upozornění zopakoval měření s managed WAL a opravil výkonové závěry. Čísla nejsou univerzální strop SQLite: závisí na hostu, zatížení a velikosti transakcí. Nadále je potřeba plný acceptance/dispatch workload, ne jen fsync mikrobenchmark.
- Předchozí regresní testy mutation-checkoval. To je silnější důkaz než zelený test, který by prošel i bez kontrolovaného mechanismu.
- E0 řeší nejen tmux, ale také HOME/output/secrets a refresh adresování. `runs/` segment ve výstupech odlišuje run artefakty od běžných podadresářů.
- Memory rozlišuje explicitní legacy a guaranteed profil; hostový endpoint má skutečnou kontrolu identity a generace, nikoli prázdný Authorize callback.
- Existují child-process SIGKILL testy. Handoff správně neoznačuje SIGKILL za důkaz odolnosti vůči pádu OS.
- Handoff otevřeně přiznává neprovedený reálný T06. Část jeho tabulek je ovšem již zastaralá a odporuje novějším odstavcům.

## Nálezy a priority

### R1 — P0: queued cancel může po závodu označit živý runtime za zastavený

Důkaz: **reprodukováno testem** `TestCodexReviewCancelAfterClaim`.

[work_items.go](../../internal/api/work_items.go) Cancel nejprve načte stav mimo transakci. Pro přečtený queued/retry_wait zavolá `Transition` s cílem cancelled, bez očekávaného stavu či generace. Mezitím worker může claimnout a přejít do running. [transition.go](../../internal/work/transition.go) pak legitimně dovolí running → cancelled, protože generace 0 vypíná kontrolu a přechod je obecně povolen. API odpoví „Cancelled before it started; no runtime was involved“, přestože nic nezastavilo.

To je samostatná chyba i po budoucím zapojení jednotného dispatcheru. Oprava: jedna store operace Cancel s transakčním přečtením aktuálního stavu. Queued/retry_wait ukončí pouze pod podmínkou stejného aktuálního stavu; aktivní práci uloží durable cancel request. Oddělit uživatelský požadavek od potvrzení workeru o zastavení. `replyCancelRace` nesmí vrátit requested bez skutečně uloženého požadavku.

Akceptace: bariéra mezi původním read a claim/start; odpověď requested, runtime stále evidován jako aktivní, durable cancel viditelný po restartu; cancelled až po stop evidence. Zvlášť souběh completion/cancel a generation change při zápisu cancel eventu. `recordCancelRequest` musí načíst aktuální stav/generation uvnitř své transakce, nikoli vložit hodnoty ze starého snapshotu.

### R2 — P0: fencing neověřuje vazbu work–attempt–run

Důkaz: **reprodukováno** `TestCodexReviewCrossWorkRun` a `TestCodexReviewUnknownRunStart`.

`Transition` kontroluje pouze nenulovou generation proti work itemu. Neověří, že req.RunID patří req.WorkID a aktuálnímu aktivnímu attemptu. Dva různé work items mají běžně generation=1: run B tak může dokončit work A a následný UPDATE uzavře attempt B. `StartRunning` zase neověřuje počet zasažených řádků UPDATE runtime locatoru: i neexistující run ID převede work item do running.

Oprava: worker API vyžaduje work_id + run_id + generation a v jedné transakci ověří celou vazbu, aktuální attempt a jeho lifecycle. Žádná implicitní worker cesta generation=0. Admin/cancel/reconcile operace mají explicitně jiné rozhraní a vlastní předpoklady. Každý podmíněný UPDATE vyžaduje správný RowsAffected; neúspěch rollbackuje i event. Neřešit pouze doplněním RunID do volajícího — store musí chybu odmítnout.

Akceptace: stejné generation dvou work items, cizí/neexistující run, ukončený attempt, nulová generation, pozdní completion/heartbeat. Odmítnutí nezmění ani jeden work item, attempt či event.

### R3 — P0: acceptance a skutečné vykonávání mají rozdílného vlastníka

Důkaz: **staticky ověřeno** v [webhook.go](../../internal/api/webhook.go), potvrzeno také handoffem. AcceptDeliveryTx vytvoří queued práci, poté dispatchAccepted/runAccepted spouští původní orchestrator.RunAgent bez claimu této práce. Není zapojen produkční loop, který by spravoval Claim/Heartbeat/Transition/Recovery této cesty.

Důsledky: queued může současně znamenat běžící agent; completed agent nemusí dokončit work item; restart nepokračuje automaticky; Cancel a metriky čtou jinou skutečnost než runtime. Přidat nový pump bez odstranění přímé cesty by umožnilo dvojí spuštění stejné práce.

Oprava: přijetí smí pouze commitnout a poslat nezávazný wake-up hint. Jediný dispatcher claimuje durable work, zakládá/předává stejný run_id do agent_runs i runtime a potvrzuje lifecycle. Převést producenty podle explicitní mapy. Přechodný režim nesmí současně vlastnit tutéž práci ve dvou dispatcherech; použít feature gate/drain a integrační testy. Legacy cesty mimo převod nesmějí být počítány jako splnění společného admission.

Akceptace: HTTP webhook → receipt → claim → živý mock/runtime → completion ve stejném ledgeru; cancel; crash po acceptance před wake-up; restart; opakovaná delivery. Po zastavení/restartu vznikne nejvýše jeden vlastněný runtime a všechny acknowledged práce jsou dohledatelné.

### R4 — P0 před dispatcherem: mezera mezi runtime startem a uložením locatoru

Důkaz: **rozpor návrhu API, potřebuje integrační crash test**. Claim založí starting attempt bez locatoru; StartRunning ukládá locator až při potvrzeném startu. Recovery považuje prázdný locator za důkaz, že žádný proces nevznikl. Pád mezi skutečným startem a uložením locatoru by tento předpoklad porušil.

Oprava: před externím startem persistovat deterministický runtime locator/start intent. Recovery dohledá skutečný runtime podle předem známé identity; nepřevádět automaticky „nemám receipt“ na „nic se nestalo“. Start musí být idempotentní vůči identitě nebo explicitně reconciliovaný. Test zabije dispatchera přesně po vytvoření procesu a před StartRunning; po restartu nesmí vzniknout další proces.

### R5 — P0: transportní chyba memory POST vede k možnému druhému zápisu

Důkaz: **statická analýza** [memory_write.go](../../internal/sidecar/memory_write.go), mutateOnHost a jeho volající. Chyba memoryHostRequest se označí jako degradeReason. Pokud RequireGuaranteed není zapnuté, handler provede lokální legacy mutation. Transportní chyba však může nastat po úspěšném commitu hostu a před doručením odpovědi. Append pak může proběhnout dvakrát, legacy zápis obejde hostový ledger a rozbije jeho hash/revision návaznost.

Oprava: po odeslání host mutation nepřecházet při nejasném výsledku na druhého writeru. Vrátit retryable/unknown a opakovat hostovou operaci se stejným operation_id, případně dohledat její stav. Legacy fallback povolit jen podle explicitní kompatibilní policy před pokusem o guaranteed write a při prokázaném nepřijetí. Pro parallel profil musí required-guaranteed vynucovat server/runtime konfigurace, nikoli volitelná prosba modelu.

Akceptace: host append commitne a transport zahodí odpověď; sidecar nic lokálně nepřidá; retry stejného operation_id vrátí původní výsledek. Zvlášť connection-refused před odesláním a timeout po odeslání. Ani jedno se nesmí automaticky považovat za důkaz nepřijetí.

### R6 — P1/release blocker: agentní memory cesta stále není guaranteed

Důkaz: **staticky ověřeno**, hostMutationFencing bere run/generation pouze z requestu; komentář i handoff uvádějí chybějícího producenta. MCP writer zůstává legacy.

Oprava: per-attempt konfigurace/token → sidecar/MCP → host mutation se stabilním operation_id, run_id a generation. Identitu runu ověřit vůči autentizované capability, ne pouze vůči libovolnému jinému živému běhu stejného agenta z body. Run ID může být předán per-exec environmentem nebo per-run configem; zakázaný je boot-frozen container env. Handoff tyto dvě možnosti nemá směšovat.

Akceptace: agent-facing read/append/replace → host ledger → revision/idempotency; žádný legacy fallback pro parallel profile; run A token + body run B odmítnut; stale run odmítnut. Pokrýt první vytvoření klíče a přenos read revision, nikoli vyžadovat improvizovaný prázdný append od modelu bez zdokumentovaného bootstrap protokolu.

### R7 — P1: revokace run tokenu nepřežije restart sidecaru

Důkaz: **statická analýza** [run_registry.go](../../internal/sidecar/run_registry.go) a [internaltoken.go](../../internal/auth/internaltoken/internaltoken.go). Registry je in-memory, unknown run vrací current=true; crew run key je deterministicky odvozený a token nemá expiraci. Nový registry tedy nezná dřívější ended run a samotný kryptograficky platný token ho neodmítne. Nejde o tvrzení, že hostové guaranteed memory API tento token automaticky přijme; má další DB kontrolu. Pro ostatní sidecar operace však lokální revokace sama nestačí.

Oprava: při restartu/unknown run ověřit aktivní attempt u autoritativního vlastníka nebo bezpečně obnovit registr a revokace před obsluhou. Popsat chování při nedostupném hostu a dobu platnosti cache. Expirace sama nenahrazuje okamžitou revokaci; rotace celého crew klíče nesmí bez koordinace odpojit jiné živé běhy.

Akceptace: end A → restart sidecaru se stejnou crew konfigurací → token A odmítnut; B nadále funguje; ztracený end notification a nedostupný host mají explicitní bezpečné chování.

### R8 — P1: dedup a admission při přetížení neodpovídají kontraktu

Důkaz: **staticky ověřeno** v receiptOrRefusal. Lookup používá pouze source ID, stejné ID s jiným tělem tak při throttlingu dostává duplicate místo konfliktu. Zároveň před acceptance zůstává in-flight gate agenta a jeho obsazenost vrací ingress-full, ačkoli PRD odlišuje plnou vstupní kapacitu od obsazeného runtime.

Oprava: podpis/policy ověřit, duplicate porovnat s hashem a scoped identity i v odmítací větvi. DB lookup failure je unavailable, nikoli tvrzení o kapacitě. In-flight omezení přenést do společného admission; validní nová práce při obsazeném agentovi čeká, pokud zbývá ingress kapacita.

Akceptace: throttled same-ID/same-body → původní receipt; different-body →409; lookup failure →503; busy agent + volná ingress kapacita →202 queued bez dalšího runtime.

## Pořadí realizace a vhodné paralelní větve

1. **Koordinátor nejprve uzavře store rozhraní:** R1/R2 a locator/start intent R4. Sepsat očekávané atomické operace a jejich preconditions. Převzít níže přiložené failing testy, opravit a rozšířit o API bariéry.
2. **Větev memory:** R5 okamžitě, poté R6. Nezávislá od claim SQL, ale přesný run identity kontrakt převzít od koordinátora. Oba agent-facing vstupy mají stejné garance.
3. **Větev sidecar:** R7 a run capability → request binding. Koordinovat s memory vlastníkem; oba nesmí současně přepisovat memory_write.go bez rozdělení práce.
4. **Koordinátor/dispatcher větev:** R3 s již opraveným store. Zpočátku jedna vertikální webhook cesta přes mock runtime; pak zapojit chat mailbox a další producenty. Žádný dvojí vlastník práce během migrace.
5. **Webhook/API větev:** R8 a integrační cancel test nad skutečným dispatcherem; respektovat společné store operace místo vlastní SQL varianty lifecycle.
6. **Integrační gate:** skutečný Claude T06/T07, queued třetí úloha, reálné memory tools, cancel B při pokračujícím A, restart a ztracené acknowledgements. Až poté profil povolit.
7. **Release uzavření:** dispatch-time permissions/budget, external operation unknown receipts, retence/recovery sweeper, UI reconnect/stavy, zátěž, T14 a upgrade/drain drill. Tyto dosud chybějící části nezanikají tím, že zde mají konkrétní regresní chyby přednost.

Paralelní agenty lze použít při následné implementaci, ale sdílené rozhraní a soubory musí mít jednoho vlastníka. Neprovádět další obecnou rešerši front ani redesign E1/E2. Nález vyžadující úpravu kontraktu doložit konkrétním testem. Základní bezpečnostní invariant se nesmí oslabit pouze proto, že jeho produkční propojení ještě neexistuje.

## Důkazy a reprodukce

Před tímto hlubším průchodem na stejném checkoutu prošly:

- `go test ./internal/work ./internal/webhook/profiles ./internal/memory/memdiff -count=1`
- `go test ./internal/api -run 'TestAgentWebhookAcceptance_|TestInternalMemoryMutation_|TestInternalMemoryCanonicalRead_' -count=1`
- `go test ./internal/orchestrator -run 'TestT07_|TestLiveRunHomes_' -count=1`

V tomto review spuštěné `go test ./internal/work -run TestCodexReview -count=1` skončilo **FAIL** se třemi očekávanými regresními nálezy. Zdroj testů je uchován v [reports/codex-review-work-regressions-2026-09-11.go.txt](reports/codex-review-work-regressions-2026-09-11.go.txt). Dočasný test byl následně odstraněn z aktivního balíčku, aby review neměnilo jeho běžný testovací průchod.

Pro reprodukci z repo root zkopírovat soubor do dosud neexistujícího `internal/work/codex_review_probe_test.go` a spustit výše uvedený filtr. Při opravě z něj vytvořit trvalé regresní testy podle místních konvencí. Testy používají existující migrated SQLite fixture, mock nejsou; nepouštějí Docker, LLM ani externí efekty.

Pozorované výsledky:

```text
FAIL TestCodexReviewCancelAfterClaim:
  stale queued cancel marks a running item cancelled without stopping runtime
FAIL TestCodexReviewCrossWorkRun:
  run B can complete work A and close B attempt
FAIL TestCodexReviewUnknownRunStart:
  item becomes running with no matching runtime attempt
```

Ostatní zde uvedené nálezy mají označení statická analýza nebo požadovaný crash experiment: nejsou vydávány za již reprodukované E2E selhání. Kompletní Go suite, frontend suite ani reálný Claude/Docker souběh nebyly při tomto review spuštěny. Žádný merge ani deployment neproběhl.
