# Routines — integrační uzavírání PR (22. září 2026)

## Verdikt a rozsah tohoto záznamu

Stav k 14:00 UTC: tři PR jsou sloučená; společný kandidát Routines je ověřený
na veřejném DEV1. **Integrační PR ještě není sloučené a PRD není přijaté.**
Chybí nezávislé schválení a finální CI posledního headu; §11 nemá žádný doložený
průchod reprezentativního uživatele. Pozdější stav merge určuje příslušné PR,
nikoli tato časově vymezená zpráva. Procenta dokončení nejsou měřená.

## Co je sloučené

| PR | Výsledek | Commit v main |
| --- | --- | --- |
| [#2641](https://github.com/crewship-ai/crewship/pull/2641) | Povinné HTTP credentials selžou uzavřeně před odchozím požadavkem; chyba credential probe blokuje start | `ff0c8b95601643e7141a1e3d7ca06d34c764a149` |
| [#2635](https://github.com/crewship-ai/crewship/pull/2635) | Aktualizace připnutých CI akcí | `6ceddb52dbaab2048075a7638f2a03531f0f49f6` |
| [#2632](https://github.com/crewship-ai/crewship/pull/2632) | Deklarované Go závislosti; SQLite 1.59 používá stejný SQLite engine a odpovídající libc | `ef36bbfe166084da9ac60196cd2336d5603871b7` |

Všechny tři měly celé zelené CI a skutečné review přesného headu: CodeRabbit
u #2641, nezávislé manuální review původních botích změn u #2635/#2632.
Nebyl použit přepínač `--admin`. Další merge používají squash podle aktivního
pravidla lineární historie.

## Společné Routines PR #2631

[#2631](https://github.com/crewship-ai/crewship/pull/2631) zahrnuje:

- Stránkování katalogů, dávkové lookupy plánů/kalendáře a indexy feedů.
- Work a Deliveries v Activity, přesměrování starého `/work` (#2638/#2636).
- Autorování typovaných otázek a pojmenovaných rozhodnutí existujících approval
  kroků v Edit, přes draft a explicitní publish (#2640/#2637). Nejde o obecný
  vizuální editor grafu nebo vytváření libovolného nového kroku.
- Ochranu neuloženého textu při reloadu a navigaci, čisté otevření uloženého
  draftu a zachování serverového draftu při zahození (#2644).
- Run/Plan podle skutečných rolí a capabilities: delegovaný Run i pro VIEWER,
  samostatné vytváření recurrence, MANAGER+ pro zrušení odkladu a ADMIN+ pro
  změnu existujícího plánu (#2645).
- Obnovený Test v aktivním detailu kroku: kontrolu zobrazeného publikovaného
  receptu, import zachycených dat a výpočet vzorku bez obnovení zdrojového běhu.
  Podporované jsou stávající top-level transform/agent/HTTP/script kontrakty;
  externí kroky vyžadují explicitní náhradu výstupu. Sbalené transformace a
  výběr z mapy vedou ke stejnému Testu. Mobilní pole pro zdrojový run se už
  nesmrskne vedle tlačítka (#2647).
- Pravdivé výsledky odpojených agentích procesů z #2628/#2626: proces bez
  potvrzeného ukončení není úspěšný, kapacita a dohled se nezahazují; po
  současném dokončení dvou držených běhů se agent vrátí online právě jednou.
  Tyto pojistky jsou procesové, ne trvalá obnova přes restart serveru.

Zdrojová PR #2638/#2640/#2628 zůstávají otevřená, dokud se integrace skutečně
nesloučí. Po squash merge budou uzavřena jako začleněná s odkazem na výsledný
commit; původní hlavy nelze označit za předky squash commitu. Větev #2628 musí
zůstat dostupná pro cizí navazující draft #2646.

## Skutečně nasazený a ověřený DEV1

**Nesloučený testovací kandidát `6981d152738a98c05bac14b960b1acd457100e23`**, build
`2026-09-22T13:57:54Z`. Autentizované API, frontendový marker a tři veřejně
stažené JS assety souhlasí. PID 2281811 a `/tmp/crewship-1-dev` mají stejný
SHA-256 `ae788645b4c4e6e67a33140cacb972e847fb89401f9e2ccdc8c8875fd5c61585`.
`dirty=true` je ponecháno; všech 17 původních WIP souborů je byte-identických
oproti původnímu SHA-256 baseline. DEV2/DEV3 nebyly měněny.
[Veřejný protokol nasazení a přejímky](https://github.com/crewship-ai/crewship/pull/2631#issuecomment-5777896347).

Šest veřejných browserových sad prošlo s exit 0 a bez JS chyb (23 skupin
kontrol). Nejde o 23 reprezentativních uživatelů:

| Oblast | Čerstvý důkaz |
| --- | --- |
| Activity/Work | Redirect, taby, klávesnice, historie, reload a 390 px bez přetečení |
| R8 | Autorování → draft → publish → číselná odpověď 42.5 → dokončení; `run_cmucqprdx003065b8ba76` |
| Draft/navigace | Čistý draft bez varování; odmítnutý reload/Back/Forward zachová text i původní cíl Vpřed; zahození zachová draft a vrátí fokus |
| Role v UI | Pět výchozích/delegovaných kombinací pro Run a Plan |
| R6 | Routines `run_cmucqpo8n0007493ff453` a Inbox `run_cmucqpr24002d83557bec` zachovaly 0/false; souběh na `run_cmucqpswc00372dc89528` vrátil 409/200 a uložil jedinou vítěznou odpověď 202 |
| R4/R9 | Kontrola publikovaného receptu, import `run_cmucqpowz000c0dea4104`, výpočet 42 bez nového běhu; mobilní pole 246.89 px a dokončený import; HTTP vyžaduje náhradu |
| R10 | Dokončený export v1/v2 s výstupy 42/84; `run_cmucqpsda00331454d123`, `run_cmucqpso2003497829334` |

Znovu prošlo všech 60 vybraných API kontrol rolí/capabilities na stejném
nasazení, včetně grant/revoke a odmítnutého vlastního povýšení. Nejde o úplnou
matici všech endpointů a tokenů. Vlastní rutiny/plány smazané 204, vyhrazené
workspace 200; auditní historie zachována. Kandidátní deploy není merge.

## Automatické, negativní a výkonové důkazy

- Celý čistý Go strom s posledními Go závislostmi: 146 testovaných balíků,
  10 bez testů, `go test ./... -count=1 -timeout=45m` a následné `go vet ./...`
  exit 0. API 1106,453 s, database 1224,516 s. Go zdroje a dependency soubory
  testovaného `393b1e639` jsou shodné s nasazeným `6981d1527`; poslední rozdíl
  je pouze responsive CSS.
- Routines frontend: 382 testů / 54 souborů, čisté testové typy, lint 0 chyb
  (30 stávajících varování), produkční build. Mobilní změna navíc 45 cílených
  testů a skutečný browserový negativní/pozitivní průchod.
- Regrese selhaly před opravou: ztracený text při reloadu, pět nesouladů
  oprávnění, nedostupný Test a příliš úzké mobilní pole; souběžné zavření
  detached holdů selhalo bez závěrečného návratu online. Původní opravy R8,
  přesměrování a credential guard také mají zaznamenaný negativní důkaz.
- Předchozí plné Go běhy a nové browserové průchody nenahrazují finální CI
  posledního headu. Skipped/neutral ani rate-limited green neznamená provedenou
  kontrolu. Zaznamenané CI annotations byly warnings, ne nový potvrzený nález.
- Výkon z řízeného měření: handler 300 plánů přibližně 15 → 3 ms medián;
  historie 100 kroků, 1 000 uložených pokusů a 10 000 journal událostí bez
  duplicit/chybějících ID. Jde o omezené měření, ne o 1 000 skutečných retry,
  crash test nebo certifikaci výkonu celého produktu. Podrobnosti v
  [auditu](routines-security-performance-audit-2026-09-20.md).

Opravy testovacích postupů jsou zachované odděleně: očekávání MEMBER draft
read, explicitní workspace při souběhu, required-label selektor, nepodporovaná
aritmetika v porovnávací fixture a cleanup lokálního proxy serveru. Tyto
případy nebyly vydávány za produktové chyby. Opravené celé průchody prošly.

## Závislosti a další PR

- #2633 nyní spojuje 14 frontendových/tooling aktualizací s Node typy #2634.
  Head `561f1d99f`: frozen install, přesně 15 deklarovaných přímých změn bez
  drift override, 9 213 testů / 772 souborů, typy, lint a build prošly.
  Zbývá finální CI a nezávislé schválení. #2634 uzavřít až po začlenění.
- Prospektivní kombinace #2631 + #2633 má izolovanou větev, finální head
  `edf7770cd`. Celá frontendová sada 9 246 / 775 prošla před poslední CSS
  úpravou; finální typy, build a browser Activity/R8/navigace/R9/R10 i mobilní
  import prošly po ní. Čerstvý checkout nejprve postrádal generované Prisma
  typy; `pnpm exec prisma generate` doplnil předpoklad, bez migrace.
  **Frontendové závislosti #2633 nejsou nasazené na DEV1.**
- #2619/#2622 jsou aktivní oddělené providerové změny crewship_3; #2646 je
  draft sdíleného admission crewship_2. Jejich kód ani instance tato relace
  nemění a jejich dokončení nepředstírá.
- #2630 je draft pilotu Jev; skutečná inference není doložená. Není součástí
  původního Routines PRD.

## Zbývající brány

1. Dokončit CI a skutečné nezávislé review finálního #2631 a #2633. CodeRabbit
   při kontrole v 13:36 opět hlásil limit; jedna žádost pro #2631 je zařazená
   přibližně na 14:35 UTC. GitHub vyžaduje schválení posledního reviewable push
   jiným účtem; vlastní review změn tuto podmínku nenahrazuje.
2. Po skutečném squash merge uzavřít začleněná zdrojová PR a uvolnit jejich
   claims. Nepovažovat testovací deploy za uzavření PR.
3. **§11 NOT VERIFIED**: nula doložených průchodů pěti reprezentativních lidí.
   Každou z pěti úloh musí bez nápovědy zvládnout alespoň čtyři. Připravený
   [recorder](../wireframes/routines-acceptance-recorder.html) výsledek pouze
   zaznamená; automatizace a interní walkthrough jej nenahrazují.
4. Tvrdý pád serveru, živý nejistý externí zápis, autorizace mimo vybranou
   matici a kompletní zátěžový benchmark nejsou tímto čerstvě plně prokázané.
   Dřívější/serverové důkazy jsou zachované s omezeným rozsahem.

Čerstvá evidence a opakovatelné skripty:
`/srv/crewship/backups/crewship_1/routines-public-final-20260922/`.
Kombinované testy, negativní důkazy a test s frontendovými závislostmi:
`/srv/crewship/backups/crewship_1/routines-combined-20260922/`.
Předchozí nasazení `6a236506f` má vlastní nezměněný adresář
`routines-public-closeout-20260922/`; není označené za aktuální deploy.

## Doplnění po nezávislém review (22. září, 14:36 UTC)

CodeRabbit skutečně dokončil review headu `21f3433df` (78 souborů) a vrátil
šest nálezů. Nejde již o rate-limited zelený status. Regrese potvrdily:

- Potvrzeně zastavený detached proces vracel stále nonterminal sentinel.
  Samostatná terminální chyba teď vede do existujícího failure/cancel zpracování.
  Částečná odpověď a usage zůstávají zachované. Webhook dispatcher ponechává
  výsledek externích účinků `unclear`, bez automatického retry; potvrzený konec
  procesu nedokazuje úspěch ani neprovedení externího zápisu.
- Neověřený konec procesu neukončoval chatový stream. `done` nyní uzavírá pouze
  tento stream s důvodem `detached_still_running`; běh zůstává neterminální.
- Index plánů nepokrýval ID tie-breaker. Test skutečného filtru a řazení před
  opravou doložil `USE TEMP B-TREE`. Doplněna nová migrace, původní již nasazená
  migrace se nemění.

Upřesněn odkaz issue #2639 / PR #2641, EXPLAIN používá kontext testu a chyby
seznamu plánů mají kontext operace. Doporučení změnit výchozí seznam na 500
bez migrace klientů není přijato: CLI list/doctor/digest dosud nečtou paging
hlavičku a tiše by ztratily další plány. Explicitní stránkování UI zůstává
omezené. Nepoužitý context parametr se do čistých parser/header helperů nepřidává.

Během review se main posunul na `2317c32ee` (providerová integrace #2622).
Nový základ je lokálně začleněný; jediný textový konflikt byl CHANGELOG a oba
záznamy jsou zachované. Probíhá nové ověření kombinace; předchozí testy ani
DEV1 `6981d1527` nejsou důkazem nasazení těchto nových změn. PR #2633 má také
nový základ (head `42e22ce75`); jeho předchozí head `561f1d99f` měl všechna CI
zelená, ale nikoli nezávislé schválení. Ruční review bylo vyžádáno a zatím
nepřišla odpověď. Zdrojová PR zůstávají otevřená do skutečného začlenění.
