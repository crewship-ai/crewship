# Routines — stav po sloučení a přejímka (22. září 2026)

## Verdikt

**Stav k 15:25 UTC:** hlavní integrace #2631 je sloučená a skutečný commit
z main běží na DEV1. Čtyři PR této relace jsou sloučené, tři zdrojové PR
uzavřené jako začleněné. **PRD ani Release 1.0 nejsou uživatelsky přijaté:**
§11 má nula doložených průchodů reprezentativních uživatelů. Procenta
hotovosti nejsou měřená a nepoužívají se.

[Závěrečný veřejný protokol po merge](https://github.com/crewship-ai/crewship/pull/2631#issuecomment-5779190090)
obsahuje aktuální identitu nasazení, testy a úklid. Starší protokoly zachovávají
svůj původní čas; nepředstavují nové přeměření.

## Co je sloučené a uzavřené

| PR | Výsledek | Commit v main |
| --- | --- | --- |
| [#2641](https://github.com/crewship-ai/crewship/pull/2641) | Povinné HTTP credentials a probe při chybě blokují spuštění | `ff0c8b956` |
| [#2635](https://github.com/crewship-ai/crewship/pull/2635) | Aktualizace připnutých CI akcí | `6ceddb52d` |
| [#2632](https://github.com/crewship-ai/crewship/pull/2632) | Deklarované Go závislosti | `ef36bbfe1` |
| [#2631](https://github.com/crewship-ai/crewship/pull/2631) | Společná integrace Routines, Activity a pravdivých běhových stavů | `8086c964c` |

Všechny měly zelené finální CI a skutečné review. Nebyl použit `--admin`.
#2638 (Activity), #2640 (R8) a #2628 (detached exec) jsou uzavřené jako
začleněné do #2631. Jejich zdrojové hlavy jsou obsažené v testovaném integračním
headu; netvrdí se jejich ancestry vůči squash commitu. Strom squash merge
`8086c964cbeac84d77e677c03d3511b4063e5103` je byte-identický se schváleným
`156f518970d8defb9566be164a56f717ac389fd0`. Větev #2628 zůstává pro cizí #2646.

Providerovou integraci #2622 sloučil její vlastník; tato relace ji zahrnula
z main a ověřila společný strom. Nepřipisuje si její implementaci ani DEV3.

## Co lze vidět a používat

- Work a Deliveries jsou pod Activity; staré `/work` se přesměruje.
- Edit chrání neuložený text při reloadu, odkazu a Back/Forward, neobtěžuje
  u čistého uloženého draftu a při zahození zachová serverový draft.
- Decisions v Edit upravuje typované otázky a pojmenované akce existujících
  approval kroků, také v nested/hook definicích. Není to obecný editor grafu.
- Run a Plan odpovídají rolím a capabilities. Delegovaný Run může mít i VIEWER;
  recurrence má vlastní grant, zrušení odkladu MANAGER+, změna plánu ADMIN+.
- Test v aktivním detailu kroku kontroluje publikovaný recept, importuje
  zachycená data a počítá vzorek bez obnovení původního běhu. Podporuje existující
  top-level transform/agent/HTTP/script kontrakty. Externí kroky vyžadují
  explicitní náhradu; mapový výběr i sbalené transformace vedou ke stejnému Testu.
- New routine → Copy vytváří nespustitelný draft; přejmenování jej nepublikuje.
  Teprve explicitní Publish vytvoří v1. Dva editory dostanou konflikt 409
  bez ztráty jejich textu.
- Katalogy stránkují, plány/kalendář používají dávkové lookupy a feed indexy.
- Proces s neověřeným koncem není označený za úspěšný a drží kapacitu/dohled.
  Potvrzené zastavení má vlastní terminální chybu. Částečná odpověď i usage
  zůstávají; ukončení chatového streamu není vydáváno za dokončení běhu.

## Nasazený DEV1

Ověřeno 15:22:54 UTC: **main `8086c964cbeac84d77e677c03d3511b4063e5103`**,
build `2026-09-22T15:21:44Z`, schema `20260922143140`, PID 2494773.
API, frontendový marker, tři veřejné JS assety a běžící binárka souhlasí.
SHA-256 procesu i `/tmp/crewship-1-dev`:
`8182d4cde936978adb456b72da515da64f565ea0e158cb226f8342a114933c41`.

`dirty=true` zůstává výslovně uvedené: všech 17 původních WIP souborů má
původní SHA-256. Přechod na main použil dočasný stash, obnovu a kontrolu všech
hashů; vlastní dočasný stash byl poté odstraněn. DEV2/DEV3 nebyly měněny.

## Testy a síla důkazů

- Čistý finální Go strom: **146 testovaných balíků + 10 bez testů**, celý
  `go test ./... -count=1 -timeout=45m` a `go vet ./...` exit 0. API 996,624 s,
  database 1124,476 s. Testovaný strom odpovídá squash merge.
- Frontend: **9 256 testů / 775 souborů**, testové typy, lint bez chyb
  (stávající varování ponechána) a produkční build. Celé dotčené balíky
  orchestrator/chatbridge/pipeline/dispatch prošly také s Race.
- Finální GitHub CI včetně platformních sad, Shuffle a všech Race sad prošlo;
  bezpečnostní a CodeQL brány také. Prohlédnuté annotations obsahovaly
  upozornění na CI toolchain a existující lint, nikoli nový potvrzený nález.
- Před merge prošlo šest veřejných browserových sad (23 skupin kontrol):
  Activity/history/keyboard/mobile, R8 author→draft→publish→answer,
  navigace/fokus, pět rolí v UI, R6 oběma skutečnými formuláři včetně souběhu
  (jedna odpověď 200, druhá 409), R4/R9 a dokončený export R10.
- Navíc prošly dva skutečné editory a mobilní Copy→rename→Publish a celé
  autorování/odpověď R8 při 390 px. Nejde o reprezentativní uživatelské testy.
- Všech **60 vybraných živých API kontrol** rolí/capabilities prošlo včetně
  grant/revoke a odmítnutého vlastního povýšení. Není to matice všech endpointů.
- Po nasazení skutečného main prošly znovu tři browserové sady (11 skupin):
  R8, R4/R9/R10 a dva editory/Copy/Publish. Bez JS chyb. Vlastní rutiny/plány
  smazané 204, vyhrazené workspace 200; auditní historie zachována.

Negativní důkazy pokrývají ztrátu textu, nesoulad oprávnění, chybějící Test,
mobilní pole, souběžné uzavření holdů a poslední review opravy. Výkonové měření
handleru 300 plánů dalo přibližně 15 → 3 ms medián. Syntetická historie
100 kroků / 1 000 uložených pokusů / 10 000 událostí se načetla bez duplicit
nebo chybějících ID. Nejde o 1 000 skutečných retry ani kompletní benchmark.
Podrobnosti jsou v [auditu](routines-security-performance-audit-2026-09-20.md).

## Nezávislé review a následné opravy

CodeRabbit provedl plné review 78 vybraných souborů na `21f3433df`. Vrátil
šest nálezů: pět opraveno, jeden po doložení kompatibility stažen. Na
`156f51897` reviewer zkontroloval opravy ve všech šesti vláknech, uzavřel je
a v 14:43:32 UTC schválil přesný head. Automatický další plný průchod byl
rate-limited; netvrdí se druhé plné review 78 souborů.

- Potvrzeně zastavený proces už nevrací nonterminal sentinel. Webhook ponechává
  externí účinky jako `unclear`, bez automatického retry: smrt procesu
  neprokazuje, zda externí zápis proběhl. Zrušení zůstává CANCELLED a odpověď
  se uloží jednou.
- Neověřený konec nyní uzavírá chatový stream s konkrétním důvodem, nikoli běh.
- EXPLAIN reprodukoval dočasné řazení při ID tie-breakeru. Nová migrace doplnila
  index; již nasazená původní migrace se neměnila.
- Kontext EXPLAIN, chyby seznamu a odkazy issue/PR jsou opravené. Nepoužitý
  context parametr se do čistého parseru/header helperu nepřidává.
- Výchozí neomezený seznam plánů bez `limit` zůstává kvůli CLI/doctor/digest
  klientům, kteří nečtou paging hlavičku. Tichý limit 500 by je ořízl;
  reviewer návrh stáhl. UI používá omezené stránky. Globální limit vyžaduje
  koordinovanou změnu klientů a API kontraktu.

Širší Race sada odhalila test, který po 150 ms rušil běh ještě před zápisem
jeho řádku. Nyní ruší při vstupu do kroku; deset Race opakování i celá sada
prošly. Počáteční chybné harness selektory, envelope, nepodporovaná aritmetika,
proxy cleanup, nesprávný název tsconfig a init skript na about:blank byly
opravené a jsou odděleně zachované v logu. Nejsou vydávány za produktové chyby.

## Otevřené práce a hranice

1. **§11 NOT VERIFIED**: žádný doložený průchod pěti reprezentativních lidí.
   Každou z pěti úloh musí bez nápovědy zvládnout alespoň čtyři. Připravený
   [recorder](../wireframes/routines-acceptance-recorder.html) automatickou
   nebo interní validaci za lidskou přejímku nevydává.
2. Tvrdý pád serveru, živý nejistý externí zápis, úplná autorizační matice a
   benchmark celého produktu nejsou tímto nově kompletně prokázané.
3. Detached dohled je procesový. Trvalá obnova a společné admission jsou
   oddělený draft #2646; #2648 opravuje plánovač, ale nedokončuje durable I7.
   Nezávislé omezené review #2648 zde doložilo scheduler Race/vet a mutaci
   (bez guardu 13 Exec); nic nebylo nasazeno na DEV2. #2630 je oddělený draft.
4. **Frontendové závislosti nejsou sloučené ani nasazené.** Ruční integrační
   PR [#2649](https://github.com/crewship-ai/crewship/pull/2649) zahrnuje #2633
   a #2634, přesně 15 deklarovaných změn a 179 ostatních přímých závislostí
   beze změny. Nový PR umožňuje skutečné review po ignorování původního
   Dependabot autora, bez změny review konfigurace nebo ochrany větví.
   Celý strom odpovídá testované kombinaci `5a80cdf3b`: 9 256 testů, typy,
   frozen install/drift guard a build. Finální CI/review #2649 je nutné znovu;
   zelený předchozí head #2633 není jeho náhradou. Lockfile je automatickým
   review filtrem vyloučen, což není vydáváno za jeho strojové review.

## Evidence

- Skutečný main: `/srv/crewship/backups/crewship_1/routines-main-final-20260922/`.
- Kandidát po review: `routines-public-reviewed-20260922/` pod stejným rootem.
- Kombinované testy, regrese a dependency kombinace: `routines-combined-20260922/`.
- Předchozí kandidáti `6981d1527` a `6a236506f` mají vlastní nezměněné adresáře;
  nejsou označeni za aktuální deploy.
