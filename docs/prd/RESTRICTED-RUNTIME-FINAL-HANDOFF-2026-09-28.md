# Předání runtime izolace hlavnímu agentovi

Stav k 2026-09-28. Issue #2709, draft PR [#2710](https://github.com/crewship-ai/crewship/pull/2710).

**Závěr: samostatný offline runtime prototyp a jeho experimentální ověření jsou
hotové. Celé Release 1.0 / A2/B PRD hotové není. Nasazení kódu na dev2
nezapnulo izolaci běžného chatu ani `crewship run`.** Nepovolovat omezené
externí klienty před uzavřením níže uvedených bran.

## Výsledek proti původnímu zadání

| Požadavek | Výsledek |
|---|---|
| Mapa současných přístupů | Zdrojová mapa mountů, HOME, agentí paměti, přímých credentials, procesů, sidecaru a sítě je v serverovém kontraktu. Nejde o tvrzení, že jsme četli tajemství živých crews. |
| Dva lidé se stejným agentem | Každý pokus má vlastní kontejner a serverem určený datový scope. Testy dvou klientů používají stejného agenta a oddělená syntetická data i účty. |
| Spustitelný prototyp | `internal/restrictedruntime`, bootstrap a `scripts/restricted-runtime-probe/run.sh`; skutečný Docker, agent UID 1001, skutečný sidecar UID 1002. |
| Negativní testy | Vlastní/cizí data, přímé env/file secrets, proc/argv, log/checkpoint, RO aliasy, symlinky a traversal, sidecar tokeny, revokace a rekonstrukce. Sdílená data jsou autorizované RO snapshoty, nikoli obecný živý sdílený adresář. |
| Životní cyklus služby | Agentí pokus se ukončuje samostatně; nezávislá testovací služba přežije stop/recovery. Existující service controller se neměnil. |
| Revokace za běhu | Obnova autority po 5 s, lease nejvýše 15 s, ukončení celého kontejneru; nejisté ukončení blokuje další přijetí do rekonciliace. Nový nezávislý dohled pokrývá SIGKILL Manageru. |
| Integrační kontrakt | Server sestavuje autoritu; runtime přijímá opaque handle, ne klientský Plan. Delegace se zužuje, obnova znovu autorizuje, širší původ dat blokuje obnovu po odebrání grantu. |
| Kompatibilita | Existující interní crews beze změny. Prototyp nemá fallback do sdíleného běhu. Produkční výběr režimu ještě musí aplikace explicitně vynutit. |

## Předchozí ověřená baseline a měření

- Implementace po opravě nezávislé expirace: `d47b73d8ac3b43fa6a7219353ce6ae99eedbb4a8`.
- Evidence: `869d3ab90`; větev `feat/restricted-runtime-dev2`.
- Dev2 běžící binárka: `e8b3572cc092783c12ae2ac1c81665d797127546`, čistý build
  2026-09-28T12:08:40Z, větev `dev2/restricted-runtime-live-20260928`.
  Tato větev zachovává původní aplikační baseline dev2; není náhradou integrační větve hlavního agenta.
- Celá Go sada: 147 úspěšných balíčků, 12 bez testů, exit 0; `go vet ./...` exit 0.
- Osm explicitně spuštěných živých testů s hostitelským `-race`: 46,495 s na PR zdroji,
  opakovaně 48,757 s na čistém nasazeném zdroji. Žádný skip se nepočítá jako důkaz.
- Dev2 CLI `whoami`, `system health` úspěšné, DB připojená; veřejný web HTTP 200.
  Tyto CLI kontroly ověřují dostupnost aplikace, nikoli end-to-end izolaci chatového běhu.
- Na nasazeném zdroji: start p50 430,287 ms / p95 458,996 ms, pět startů s teplou cache;
  paměť cgroup 2 756 608 B samotný runtime / 16 900 096 B se sidecarem a syntetickým upstreamem.
  Výpadek autority → stop 5 064,775 ms; SIGKILL Manageru → nezávislý stop 15 093,872 ms.
  Jde o měření zdravého hostitele, ne SLO; není zahrnut skutečný model/agent ani hostitelský Docker RSS.
- Původní rozpracované wireframy dev2 byly obnoveny a jejich inventář ověřen.
  Host ani Docker daemon se nerestartoval; dev1/dev3 se neměnily.

Podrobnosti a surové výstupy: [poslední ověření a nasazení](RESTRICTED-RUNTIME-CONTROLLER-EXPIRY-2026-09-28.md).
Starší sedmitestové reporty jsou historické. Jejich otevřený problém pádu
Manageru řeší osmý test `TestLiveControllerCrashExpiry`.

## Co musí následovat před splněním celého PRD

1. **Aplikační autorita a integrace (#2711):** produkční `Authority` nad společnými
   granty a durable attempt/generation, `Catalog` nad vlastněným úložištěm;
   jednotné zapojení chat/CLI/rutina/webhook/fronta/retry/delegace. Autorizovat před
   sestavením promptu, paměti i zveřejněním výstupů. Nestačí změnit container ID
   v legacy cestě, která už sestavila širší crew kontext.
2. **Připojený runtime:** současný profil má `network=none`. Produkční broker musí
   vzniknout před uvolněním agentího procesu, dostat jen oprávněné operace a
   přístup k povoleným upstreamům. Ověřit přímé obcházení proxy, IPv4/IPv6/DNS,
   interní služby a host API s dosažitelnými pozitivními kontrolami.
3. **Úložiště:** vynutit persistentní diskové kvóty, verzovat resource labels a
   provenance a doplnit produkční provisioning. Tmpfs a RAM limity diskovou kvótu
   nenahrazují. Host bind mounty a obecný import adresářů zůstávají nepodporované.
4. **A2/B end-to-end:** skuteční dva uživatelé stejného agenta přes HTTP a WS/SSE,
   history/search/counts, Files, logy, artefakty, memory recall/consolidation,
   obnovu a odvolání streamu. Souborové testy nemohou dokázat správný výběr dat serverem.
5. **Credentials a provoz:** doplnit skutečné provider adaptéry včetně refresh,
   OAuth/SSH/certifikátů podle podporovaného rozsahu a ověřit host/Docker reboot,
   zálohy a případný failover. Již přečtené tajemství nelze vzít zpět; kde je
   vyžadováno externí zneplatnění, zajistit krátkou platnost nebo revokaci u providera.
6. **Rollout a review:** sdílený režim pouze pro explicitně důvěryhodné crews;
   chybějící autorita/unsupported profil musí odmítnout omezený běh bez fallbacku.
   Získat skutečné review implementace a aktuální zelené CI před sloučením.
   CodeRabbit skutečně provedl review commitu `d47b73d8a`; jeho pět připomínek
   bylo zkontrolováno a opraveno v `9e84ecda1`. Novější head zatím znovu
   zkontrolovaný není; green rate-limit notice není schválení.

Kontejnery sdílejí kernel a Docker administrátor je důvěryhodný. Není ověřena
odolnost proti kernel escape, pozastavení hostitele ani univerzální pevná lhůta
fyzického ukončení při poruše daemonu. Literal secret scrubber nezachytí kódované
kopie; důvěrnost výstupů musí zajišťovat také jejich audience.

## Doporučení hlavnímu agentovi

Převzít kontrakt a diff jako ověřenou runtime stavebnici, nikoli zapnout klientský
režim. Nejprve rozhodnout vlastníka #2711 a společného serverového adaptéru,
poté navázat připojený profil a storage kvóty, nakonec společnou A2/B matici.
Původní rozdělení práce ponechalo grant model, API oprávnění a Settings hlavnímu
agentovi; tato práce do nich nezavedla konkurenční implementaci.

V integračním kódu vždy spouštět agentí příkazy explicitně jako UID 1001:
`Config.User=1002` patří chráněnému init dohledu, nikoli uživatelskému shellu.
Na startu provést `Reconcile`; retry je nový attempt a nové řešení credentials.
Viz [serverový kontrakt](RESTRICTED-RUNTIME-SERVER-CONTRACT-2026-09-28.md)
a [API balíčku](../../internal/restrictedruntime/README.md).

## Finální opravy review a nové ověření

Commit `9e84ecda1` explicitně vyžaduje privátní cgroup namespace a runc,
odmítá dodatečné device rules/sysctls, rozlišuje potvrzenou absenci po
odmítnutém startu od nejistého stavu Dockeru, odstraňuje ukončené kontejnery
a při dalším přijetí uvolňuje ukončené sessions z paměti. Durable attempt
záznamy zůstávají pro fencing/audit; jejich dlouhodobá retence patří integraci.
Opraven byl i souběh při čtení readiness souboru v crash testu a sjednoceny
odkazy na vlastníka aplikační autority (#2711).

Regresní testy navíc odmítají rozšířenou daemon konfiguraci, ověřují odstranění
kontejneru a přijetí dalšího klienta po zamítnutém mountu.

Finální čistý PR zdroj `9e84ecda1`: všech osm živých testů prošlo s `-race`
za 51,329 s; stop po pádu Manageru 14 972,722 ms. Cílené unit/race testy
a `go vet ./...` prošly.

Dev2 po opravách: `ab11a606631b117d918cd7f4e3c0e2de552c8f0d`, čistý build
2026-09-28T12:46:06Z ověřený přes `/proc/<MainPID>/exe version`. Osm živých
testů na tomto nasazeném zdroji prošlo za 57,354 s, SIGKILL → stop
14 966,475 ms. CLI `whoami` a `system health` exit 0, veřejný web HTTP 200.
WIP wireframy byly obnoveny a inventář zůstal shodný.

Finální surové záznamy: [PR live testy](reports/restricted-runtime-final-review-live-2026-09-28.txt),
[dev2 live testy](reports/restricted-runtime-final-review-deployed-2026-09-28.txt),
[dev2 CLI](reports/restricted-runtime-final-review-cli-2026-09-28.txt).

Opakovaná celorepozitářová Go sada po opravách: **147 balíčků prošlo**,
12 balíčků bez testů, exit 0; [surový výstup](reports/restricted-runtime-final-review-go-2026-09-28.txt).
Sada začala před posledním doplněním diagnostiky výsledku Docker kill; finální
commit navíc samostatně prošel unit/race, vet a oběma živými běhy uvedenými výše.
`agents-invariants` potvrdil všechny čtyři kontrolované invarianty.
