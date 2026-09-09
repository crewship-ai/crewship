# Routines: jedna srozumitelná pracovní plocha od receptu k výsledku

2026-09-08 · nový návrh k rozhodnutí, nikoli schválená implementace.

[Otevřít přehled pěti směrů a tři interaktivní wireframy](https://crewship-dev1.unifylab.cz/design/crewship-1-0/routines-directions-20260908/index.html).

> **Aktualizace doporučení:** nová klientská interpretace a priority jsou v oddílu 16. Původní varianty A/B/C zůstávají srovnávacími exploracemi, nikoli doporučeným finálním řešením.

## 1. Co tento audit skutečně pokrývá

Prošel jsem na dev1 `feed-change-report` (název GitHub incident brief), jeho
definici, aktuální editaci a dva existující běhy. Čtení API a prohlížení UI
nespustilo další rutinu ani nezměnilo její zadání. Produkční referencí je
aplikace `9029ac92c`; dokumentační HEAD před auditem byl `db8ece1bc`.
Cizí rozpracované změny v hlavním klonu nejsou důkaz nasazené funkce a zůstávají
zachované. Starší PRD porovnávám s aktuálním kódem: například povinná kontrola
výsledku už umí selhat při nedostupném hodnotiteli, takže starý nález fail-open
nelze mechanicky opakovat jako současný stav.

Nové HTML jsou samostatné, interaktivní návrhy s výslovně ukázkovými daty.
Nekomunikují s API, modely ani credentials; neukládají skutečné rutiny a nic
nespouštějí. Čísla verzí a běhů ve wireframech jsou ilustrační. Dvě velikosti
receptu slouží ke srovnání layoutů, nikoli jako nové produkční šablony.

## 2. Hlavní závěr

Předchozí úprava sjednotila popup. Nesjednotila však samotný pracovní prostor.
Definice, editace a běh stále používají odlišnou organizaci informací. Uživatel
se musí znovu orientovat právě ve chvíli, kdy spustil práci a chce ji sledovat.

Routines mají být **čitelný recept, jehož konkrétní provedení lze sledovat na
stejném místě**. Pro klienta nejsou hlavním produktem DSL, trace ani galerie
technických karet. Základní otázky jsou:

1. Co rutina udělá a co dostanu?
2. Co potřebuje ode mě a z připojených systémů?
3. Jakými kroky k výsledku dojde a kdo je provede?
4. Co se právě děje, co selhalo nebo na koho se čeká?
5. Kde je výsledek, jak byl ověřen a co mám udělat dál?

## 3. Konkrétní zjištění na feed-change-report

| Zjištění | Důkaz | Dopad na klienta | Navrhovaná změna |
| --- | --- | --- | --- |
| Definice jsou pouze dva kroky, ale hlavní prostor zabírá velký graf a sada postranních karet. | `fetch` HTTP → `report` agent Riley, jeden volitelný vstup URL, výstup report. | Jednoduchý postup vypadá komplikovaněji, než je. | Dva čitelné kroky s názvem, účelem a předávanými daty; detail na vyžádání. |
| Po přechodu do běhu přibude další hlavička, akce a druhá navigace. Graf se posune níž. | Definice: karta + Definition/History/Versions/Plan. Běh: totéž plus Run heading, Run again, error banner, Progress/Outputs/Activity/Inputs. | Ztráta pozice a pocit jiné aplikace, přestože existují sdílené komponenty. | Jeden rámec, stabilní pás kontextu a stejná reprezentace kroků. |
| Existuje report, kroky jsou zelené, celkový výsledek je červený. | Oba načtené běhy: `status=completed`, `outcome=FAILED`, `error_message="no outcome reported"`. | Klient neví, zda report existuje, zda je špatně, nebo zda selhal systém. | „Report vznikl. Chybí potvrzení dokončení.“ Oddělit vykonání kroků a ověřený výsledek, zachovat skutečný FAILED outcome. |
| Report se zobrazuje jako surový Markdown v úzkém panelu. | Viditelné `#`, `**` a textová tabulka v `<pre>`. | Výstup není prezentovaný jako hotová práce; klient čte technický obal. | Bezpečný Markdown renderer, normální tabulka, dostatečně široký náhled; raw response do detailu. |
| Ruční zadání obchází implicitní kontext popisu. | Popis říká „only when an incident exists“, ale oba kroky samotné nemají `if`. Agentní prompt předpokládá existenci incidentu. | Klient může očekávat podmínku, kterou tento ruční průchod nevynucuje. | Odlišit pravidlo automatického vyvolání od podmínek v receptu; přehled manuálního startu ukáže, co se skutečně spustí. |
| Editace je navigačně drahá. | Edit → Steps → report → Validate → Validate & Save. To je pět navigačních/akčních kliknutí, bez kliknutí do pole a psaní. | I změna jedné instrukce působí jako založení celé rutiny. | Edit → vybraný krok → Save version; z detailu kroku případně přímé Edit step. Cílový počet je návrh, nikoli změřená úspora u klientů. |
| Vizuální tvorba postupu není dokončená. | Prázdný Steps: „No steps yet. Add your recipe in Code.“ Existující inspektor umí jen vybraná pole. | Člověk bez DSL nemůže sám sestavit celý recept. | Katalog skutečně podporovaných kroků, přidání/vložení/bezpečné odstranění a napojení dat. |

Příčinu chybějícího outcome nelze vyřešit přebarvením badge. `DeriveOutcome`
záměrně nepřijímá čisté technické dokončení agentního běhu bez rozpoznaného
HANDOFF outcome jako úspěch. Je potřeba dohledat, zda se potvrzení ztratilo
v instrukcích, adaptéru nebo interpretaci odpovědi. Samotná existence reportu
neopravňuje UI prohlásit práci za ověřenou.

## 4. Co je již dobrý základ

- Levý katalog s hledáním, filtry, ikonami a barvami zachovat ve všech stavech.
- Definice běhu je historický snapshot. Nenačítat aktuální recept jako náhradu
  za nedostupnou historii a neoznačovat aktuální verzi jako vykonanou.
- Kroky mají záznamy jednotlivých pokusů, cesty vnoření a zaznamenané výstupy.
- Typované otázky, připravené odpovědi, vlastní odpověď, výchozí hodnoty a validace
  již existují; není nutné budovat další paralelní formulářový systém.
- Kalendář a lidské ovládání opakování již mají použitelný základ. V redesignu
  zůstává den / tři dny / týden / měsíc / rok, ikony a plánování kliknutím.
- Inbox, Activity a navázané Issues již mají konkrétní identity provedení a
  rozhodnutí. Nové UI má tyto vztahy zpřístupnit, nikoli vytvořit druhý audit.
- YAML/JSON ponechat jako pokročilý a úplný způsob zápisu. Skrytá nebo dosud
  nepodporovaná pole nesmějí při vizuální editaci zmizet.

## 5. Co engine umí a co chybí v editoru

Reálný dispatcher zná jedenáct typů. Větvení přes `if` a závislosti přes `needs`
nejsou důvod zobrazit neexistující vykonatelný typ „branch“.

| Schopnost | Současný backend | Co má umět klientský editor |
| --- | --- | --- |
| Agentní práce `agent_run` | Skutečný agentní runner, nástroje a kontejner podle konfigurace. | Agent s avatarem, instrukce, zdroje, očekávaný výsledek a kontrola. |
| HTTP `http` | Požadavek, povolené cíle, timeout a odpověď. | Služba/URL, metoda, napojený účet, lidské chyby. |
| Skript `script` | Soubor v prostoru týmu, interpret a argumenty; závisí na kontejneru. | Výběr souboru, dostupnost interpretu/závislostí, argumenty a data. Ne pouze ruční cesta. |
| Inline výpočet `code` | Zapojené runtimy jsou `expr` a `cel`. Python/Go/Bash jsou zde rezervované a nezapojené. | Nabízet dostupné varianty. Python vysvětlit přes připravený skript/agentní nástroj, ne fingovaný inline sandbox. |
| Transformace `transform` | Přenos a transformace dat. | Výběr zdrojového pole, cílové mapování, ukázka dat a typová kontrola. |
| Čekání `wait` | Rozhodnutí, datum nebo událost. | Rozlišit „čekáme na člověka“, „na událost“ a „do termínu“; kdo a co má udělat. |
| Opakování `foreach` | Vnořené zpracování položek; čekání uvnitř body není podporované. | Sbalitelná skupina, počet položek a výjimky. Neprodávat trvalou lidskou frontu pro každou položku, dokud není implementovaná. |
| Jiná rutina `call_pipeline` | Složení pracovních postupů. | Výběr receptu, mapování vstupů, vazba na konkrétní podběh. |
| Dotaz `query` | Podporované dotazy nad daty workspace. | Výběr podporovaného zdroje, parametrů a náhled struktury výsledku. |
| Oznámení `notify` | Podporované oznamovací kanály. | Příjemce/cíl, zpráva a potvrzení doručení, pokud jej konektor poskytuje. |
| Crewship akce `crewship` | Registrované interní akce. | Výběr skutečné akce a jejích parametrů podle oprávnění. |

Recept kombinuje mechanickou práci a úsudek agenta. Silný autor může připravit
lepší instrukce a kontroly; nelze však slíbit totožný obsah při libovolném modelu,
změněných externích datech a jiném prostředí. Klientsky srozumitelný kontrakt je
**stejný požadovaný výsledek, pravidla a doložené kontroly**, nikoli předstíraný
bitově identický výstup všech modelů.

## 6. Pět směrů uspořádání

Všechny zachovávají vizuální jazyk Crewship: šedý/tmavý podklad, stávající
akcenty, ikonky a levý katalog. Liší se organizací práce, ne barevným skinem.

| Směr | Detail / běh | Editace | Silná stránka | Cena a riziko |
| --- | --- | --- | --- | --- |
| **1. Pracovní list** | Čitelný vertikální recept. Běh doplní stejné kroky o skutečné stavy a důkazy. | Úprava ve stejném prostoru; přidání kroku, datové vazby a Save version. | Nejrychlejší porozumění běžnému klientovi. Doporučený základ. | Paralelní a velmi rozsáhlý DAG potřebuje skupiny nebo mapu. |
| **2. Mapa toku** | Jeden graf v režimu recept / běh, stejné pozice a vizuální uzly. | Node inspector, knihovna kroků a vazby; zdroj pro pokročilé. | Nejpřesnější přehled větvení, závislostí a podběhů. | Pro dvě akce zbytečná plocha; ruční kreslení hran může být pro klienta obtížné. |
| **3. Pracovní stůl** | Seznam kroků, pracovní detail, související kontext. Výsledek se otevírá jako dokument. | Formulář vybraného kroku je hlavní obsah, nikoli malý popup. | Efektivní pro autora a delší recepty; minimum přepínání sekcí. | Na menších displejích musí být jeden panel; klient může ztratit celkovou mapu. |
| **4. Časová osa zakázky** | Fáze „Převzato → Zpracování → Kontrola → Předáno“, s výsledky a událostmi. | Nastavení fází a jejich kroků. | Výborné pro obsluhu velkého počtu běhů a výjimek. | Hůře reprezentuje paralelní závislosti; generické fáze mohou lhát o skutečném receptu. |
| **5. Brief a AI spoluautor** | Krátký popis cíle a čitelný návrh receptu, vedle konverzace. | Agent připravuje draft, člověk přijímá konkrétní změny. | Nejmenší bariéra při prvním sestavení a změnách přirozeným jazykem. | Chat nesmí být jediný způsob editace, pravda o průběhu ani implicitní souhlas se spuštěním. |

Moje doporučení: **1 jako výchozí kostra**, možnost přepnout reprezentaci téhož
receptu do mapy 2, když ji potřebuje. Směr 3 je alternativní kandidát pro produkt,
nikoli povinná třetí obrazovka. AI spoluautora lze přidat jako způsob přípravy
stejného draftu, ne další oddělený editor. Časovou osu použít pro Activity.

## 7. Tři wireframy a jak je hodnotit

- [A — Pracovní list](https://crewship-dev1.unifylab.cz/design/crewship-1-0/routines-directions-20260908/a-recipe.html)
- [B — Mapa toku](https://crewship-dev1.unifylab.cz/design/crewship-1-0/routines-directions-20260908/b-flow.html)
- [C — Pracovní stůl](https://crewship-dev1.unifylab.cz/design/crewship-1-0/routines-directions-20260908/c-workbench.html)

Každý má přímé odkazy `#edit` a `#run`, dvě velikosti receptu, přepínání stavů,
náhled výsledku, lokální editaci, knihovnu kroků a zdrojů, historii/verze,
ukázkové schválení, stop a kalendář. Vytvoření přes AI je výslovně lokální
simulace. Kalendář je pomocný model plánování, ne náhrada existujícího plného
kalendáře; demonstrační měsíc je září 2026. Některé navazující plochy jsou
kontextové náhledy, ne implementované produktové editory.

Pro porovnání udělat stejných šest úloh: vysvětlit recept bez rozkliknutí,
změnit instrukci, vložit skript, napojit předchozí výsledek, spustit a najít
výstup, pochopit neověřený výsledek nebo čekání. Sledujeme počet přechodů,
návraty, ztrátu orientace a schopnost správně popsat stav. Nejen počet kliků.
Hodnocení variant je odborný návrh; zatím nejde o uživatelský výzkum s klienty.

## 8. Doporučený průchod od A do Z

**Pochopení.** Hlavička: ikona, název, jedna věta o účelu, tým, verze. Pod ní
„Kdy začne / Co dodám / Co dostanu“. Dvě hlavní akce Edit recipe a Start routine.
Rozpočet, hashe a technická oprávnění nepřebírají hlavní plochu.

**Tvorba.** New routine nabídne přímé založení draftu s názvem a cílem. AI,
šablona a ruční tvorba se setkají na stejné pracovní ploše. Není nutné absolvovat
čtyři sekce, aby člověk uviděl prázdný recept. Starter s neexistujícím agent slug
nesmí působit jako hotový a připravený recept.

**Úprava.** Edit zapne draft nad stejnými kroky. Jeden vybraný inspektor,
žádné paralelní modální editory. Viditelná vazba „používá → vytváří“. Výběr
proměnné nabízí typy a jen dostupné zdroje, nikoli volný text `${…}` jako jediný
způsob. Odstranění nesmí potichu přerušit závislosti. Vizuální pořadí není totéž
co vykonávací závislost; přetažení to musí jasně rozlišit.

**Uložení.** Save version provede definovanou statickou validaci a publikuje
novou verzi. Chyba se váže k poli/kroku s akcí opravy. „Definition valid“ není
„Run succeeded“. Drahé nebo externí testování je samostatná akce. Návrh trvalého
draftu a ochrany před souběžnou editací je uveden níže, ne už existující záruka.

**Start.** Bez vstupů zachovat přímé spuštění. Pokud jsou potřeba odpovědi,
jeden krátký startovní formulář s připravenými hodnotami a viditelným týmem/verzí.
Chybějící připojení je problém konkrétní connection, nikoli anonymní pole „key“.
Ověřené předpoklady, pouhá konfigurace a nedostupná kontrola mají odlišné stavy.

**Běh.** API vrátí identitu provedení a URL ji obsahuje pro sdílení a návrat.
Zůstává stejný shell a reprezentace kroků. Konkrétní běh se jmenuje „Run“,
i když se zároveň ukládá do historie. History/Runs je výběr provedení, nikoli
nový design runtime. Historická verze musí být zřetelná.

**Čekání a problémy.** Místo obecného „waiting“ ukázat příčinu, vlastníka,
předložený výsledek a další možnou akci. Stejné rozhodnutí jako Inbox. Vrácení
k opravě automaticky nevymýšlí další workflow: musí existovat větev v receptu
nebo navázaný kontrakt Issues. Stop zachovává hotovou práci a její důkazy.

**Výsledek.** Výsledek je čitelný dokument, strukturovaná tabulka nebo soubory
s původem a stavem dostupnosti. Vedle něj konkrétní výsledek kontrol. Raw JSON,
logy, časování a tokeny do rozbalitelného detailu. Nezaměňovat zprávu agenta,
deklarovaný výstup a skutečně dostupný artefakt.

## 9. Stavy, které musí stejný layout zvládnout

| Situace | Co má být hlavní sdělení | Co se nesmí stát |
| --- | --- | --- |
| Nový/prázdný draft | Vyberte první krok. | Odkázat pouze do Code. |
| Validace neprošla | Konkrétní pole/krok a způsob opravy. | Dlouhý obecný JSON jako jediná zpětná vazba. |
| Nedostupné credentials / agent / soubor | Co chybí a kde to opravit; co bylo opravdu ověřeno. | Zobrazit zelenou připravenost z pouhé deklarace. |
| Queued | Proč čeká na kapacitu, pokud to víme. | Ukázat agenta jako aktivně pracujícího. |
| Running | Aktuální krok, dokončené kroky, živé důkazy. | Smyšlená procenta úspěšnosti nebo doby dokončení. |
| Human / datetime / event wait | Kdo má rozhodnout, na jakou událost nebo do kdy se čeká. | Vše označit „Waiting for a decision“. |
| Skipped / podmínka neplatí | Tato větev se nespouští a proč, je-li důvod zaznamenán. | Zaměnit přeskočený krok za hotový či chybný. |
| Retry / přerušení | Pokus N, důvod, vazba na předchozí důkazy. | Vydávat nový běh za pokračování stejného externího účinku. |
| NO_CHANGE | Recept výslovně hlásí, že nebyla potřeba změna. | Vyvozovat NO_CHANGE z libovolného `false`, prázdného pole nebo nulového počtu. |
| PARTIAL / WORK_CREATED | Částečné dokončení nebo vznik navazující práce, s jejím odkazem. | Zploštit každý technicky completed běh na kompletně hotový klientský výsledek. |
| Completed + chybějící outcome | Report může existovat, ale chybí potvrzení výsledku. | Označit samotné ukončení agenta za úspěch. |
| Failed + částečné výstupy | Co selhalo a co se zachovalo. | Schovat hotové soubory za chybový stav. |
| Stopped | Co bylo zastaveno a co už proběhlo. | Slibovat vrácení provedených externích akcí. |
| Historie/archiv nedostupný | Zachované důkazy a explicitně chybějící část historie. | Podsunout aktuální definici jako historickou. |

## 10. Co chybí: UI, kontrakt, databáze

**Nejdřív společná projekce, ne další redesign komponent vedle sebe.** Jedna
vrstva vytvoří seznam uzlů, skupin, závislostí a popisků z konkrétní definice.
Tentýž model vykreslí Recipe, Edit i Run. Stav běhu přichází odděleně přes
`run_id + execution_path + attempt`; nepřepisuje definici. Activity může přidat
technický detail, ale neurčuje odlišnou hlavní navigaci Routines.

**Metadata kroků.** Navrhnout validované, volitelné zobrazovací jméno a krátký
účel, případně skupinu. Stabilní `step.id` zachovat pro reference. Současné
humanizování `read_answers` není náhrada autorského vysvětlení. Neznámá metadata
musí přežít round-trip; migrace nesmí přepisovat historické DSL.

**Datové vazby a výstupy.** Autor potřebuje katalog výstupů, typ, původ a vazbu
na krok/artefakt. Připravené odpovědi už existují; vizuální mapování jejich hodnot
do kroků je další schopnost. Výsledek musí spojovat report, kontrolu a dostupný
soubor, nikoli odvozovat úspěch z libovolného textu. Bezpečné renderery: Markdown
bez nebezpečného HTML, typované tabulky, náhled podporovaných souborů.

**Readiness a důvody.** Potřebujeme strukturovaný důvod problému a doporučenou
akci (`step`, `field`, `connection`, `reason_code`, `repair_target`). Srozumitelné
hlášení „completion confirmation missing“ nesmí vznikat jen heuristikou nad
anglickým error stringem. Kontrola připojení nesmí být falešná zelená kontrolka.

**Draft a souběžná editace.** Pro samotné sjednocení layoutu nová tabulka není
nutná. Pro robustní pokračování po zavření a AI spoluautorství dává smysl
samostatný draft: identita, rutina, základní verze, autor, definice, revize,
updated_at. Publikace s `expected_head_version`/revizí má konflikt vrátit,
ne tiše překrýt změnu druhého člověka. Jde o návrh kontraktu, nikoli potvrzení,
že současný save endpoint tento mechanismus již poskytuje.

**Run journal zachovat.** Nezakládat další history tabulku pro nový design.
Využít `pipeline_runs`, `pipeline_step_executions`, existující verze, artefakty,
waitpointy a journal. Každý renderer výsledku ukazuje evidenci téhož provedení.
Případné nové sloupce navrhovat podle skutečně chybějících dat, ne podle karty UI.

## 11. Napojení na zbytek Crewship

- **Activity:** jeden run a jeho kroky/pokusy; události nesmí být rekonstruované
  z posledního textu agenta. Zachovat pořadí a vazbu na akci.
- **Inbox:** tentýž waitpoint a verdikt, včetně zamítnutí zastaralého rozhodnutí.
- **Credentials:** tým/účet a skutečný rozsah použití; do startovního formuláře
  automaticky netahat secret values. Autorství není oprávnění k použití účtu.
- **Issues:** navázané provedení se po rutině vrací k Lead review a případnému
  klientskému přijetí. Tlačítko schválení rutiny nesmí samo dokončit Issue.
- **Chat:** slash vstupy a AI autor používají stejný formulářový/definiční
  kontrakt. Návrh od AI je draft, nikoli automaticky schválené spuštění.
- **Files:** ukázat deklarované a skutečně dostupné artefakty, jejich původ
  a oprávnění. Skript se vybírá z prostoru týmu s dostupným interpretem.

## 12. Doporučené pořadí implementace

1. **P0 — společný detail a běh.** Jedna kostra, stejné identity uzlů a kontext, společný
   inspektor, čitelný výsledek, vysvětlení technického stavu a outcome.
2. **P0 — dokončit ruční tvorbu.** Add step, úplná editace klíčových typů,
   datové vazby, bezpečné odstranění a Save version bez vynuceného průvodce.
   Katalog nesmí nabízet nepodporované runtime schopnosti jako funkční.
3. **P0 — výsledek a ověření.** Dohledat chybějící completion kontrakt referenční
   rutiny; povinné kontroly musí rozhodovat o připravenosti výsledku.
4. **P1 — připravenost a návrat k práci.** Strukturované chyby, výběr skriptu,
   connection repair, trvalé drafty, ochrana publikace před souběhem.
5. **P1 — rozsáhlé recepty.** Skupiny a skutečné datové závislosti, podběhy,
   položky foreach a pokusy. Aktivní kód neprohlašovat za podporu trvalých
   lidských čekání uvnitř každé položky.
6. **Později — volitelné prostředí autora.** AI spoluautor, pokročilá mapa,
   srovnání a nástroje pro rozsáhlé recepty až na společném modelu.

Nejdřív ověřit klientský směr z oddílu 16. Neimplementovat všechny varianty jako další
povinné přepínače. Dashboard a plný kalendář ponechat; opravit jejich vazby
na zvolený detail bez plošné další změny vzhledu.

## 13. Přejímací podmínky

- Klient bez znalosti DSL vysvětlí vstup, dvě hlavní akce a výsledek referenční
  rutiny bez rozklikávání technických panelů.
- Start zachová identitu a pojmenování práce; odlišení receptu a provedení je
  viditelné i při otevření přímého historického odkazu.
- Nový jednoduchý recept lze vytvořit, napojit a upravit bez Code.
- Nepodporovaná editace staršího/custom kroku je výslovná; nic se tiše nemaže.
- Chyby inputu, nedostupný účet, HTTP chyba, chybějící outcome, čekání, retry,
  stop a nedostupný historický snapshot mají pravdivé a akční zobrazení.
- Změna aktuální verze nepřekreslí dřívější běh ani jeho výsledky.
- Mobil nabídne jeden pracovní panel; klávesnice, fokus, čtečky a reduced motion
  fungují. Stav není vyjádřený jen barvou.
- HTML prototyp s ukázkami není důkaz provedení runtime; skutečné integrační
  testy následují až po implementaci vybraného směru.

## 14. Zdrojové body auditu

- `components/features/routines/routine-card-detail.tsx`: definice, graf,
  postranní karty a editace.
- `components/features/routines/routine-run-detail.tsx`: další hlavička,
  vnořená navigace, TraceCanvas, raw `<pre>` report a důkazy.
- `components/features/routines/routine-create-dialog.tsx` a
  `routine-recipe-steps.tsx`: současný průvodce a omezená vizuální editace.
- `routine-input-form-builder.tsx`, `routine-run-inputs-dialog.tsx`,
  `lib/routine-inputs.ts`: připravené odpovědi a sdílený vstupní kontrakt.
- `hooks/use-trace.ts`: historická definice konkrétního běhu.
- `internal/pipeline/types.go`, `executor.go`: typy a skutečný dispatcher.
- `internal/pipeline/code_runtimes.go`, `runner_code_multi.go`,
  `runner_script.go`: dostupné výpočty a skripty.
- `internal/pipeline/executor_foreach.go`: nepodporované parkování uvnitř body.
- `internal/orchestrator/outcome.go`, `internal/pipeline/runs.go`: technické
  dokončení versus outcome a chybějící potvrzení.
- `internal/pipeline/step_executions.go`: jednotlivé pokusy a cesty provedení.
- `docs/prd/HANDOFF-2026-09-08-ISSUES-EXECUTION.md`: zachovaný kontrakt Issues.

Lokální evidenci tvoří `/tmp/routines-ux-audit-data.json`, snímky
`/tmp/routines-ux-current-*.png` a `/tmp/routines-directions-test.log`.
Raw API data a snímky živého workspace nejsou součástí veřejných HTML návrhů.

## 15. Ověření návrhů

Ve všech třech HTML prošlo proklikání editace a lokálního uložení, nezměněná
historická definice, náhled reportu a chyby zdroje, lidské schválení a Stop,
lokální plánování, prázdná ruční tvorba s přidáním kroku a Start demo run.
Při 390 px nebylo zjištěno horizontální přetékání celé stránky a nevznikly
JavaScriptové chyby. Přímé vstupy `#edit` a `#run` byly ověřeny.

Samostatná kontrola na desktopu 1500 × 1100 změřila posun prvního kroku mezi
Recipe a Run: **A 0/0 px, B 0/0 px, C 0/0 px**. Nejde o měření všech prvků ve
všech breakpointech ani o uživatelský výzkum. Prototyp není důkaz runtime podpory
položek knihovny; tu určuje popsaný backendový kontrakt.

Aktuální soubory se zpřístupňují v existující dev1 cestě
`/design/crewship-1-0/`. Nebylo potřeba měnit proxy, restartovat instanci,
nasadit nový backend ani upravovat aplikaci. Žádná živá rutina nebyla spuštěna.


## 16. Revidované doporučení: klientská práce a prokazatelný výsledek

Nový [klikací klientský návrh](https://crewship-dev1.unifylab.cz/design/crewship-1-0/routines-directions-20260908/client-workspace.html)
rozvíjí předchozí kritiku. Nejde o čtvrtý volitelný režim aplikace, ale návrh
jednoho výchozího detailu. A/B/C zůstávají archivem srovnání, nikoli třemi
produktovými povrchy. Předchozí preference A byla předčasná.

**Hlavní investice:** jeden pravdivý záznam práce, srozumitelný od přípravy
po výsledek. Před spuštěním dominuje co vznikne, vstupy a účinky. Za běhu
aktuální práce. Při čekání příčina a konkrétní akce. Po dokončení výsledek
s kontrolami. Stejný sidebar, identita, vizuální jazyk a navigace; stejná
pozice grafu není cílem. Mapa zůstává pro detail a Activity.

Návrh rozlišuje klientskou úroveň (účel, smysluplné části práce, výsledek)
a autorskou úroveň (skutečné kroky, vazby, agent, limity, kód). Editace používá
kompaktní modal podobný Credentials: přímé sekce, návrat k rozepsané práci,
jedno zhodnocení dopadu publikace. Nevyžaduje průchod všemi sekcemi.
Text o účincích a podmínkách musí vycházet ze skutečného receptu. Autorské
shrnutí není důkaz, že backend danou podmínku vynucuje.

### Priority funkcí

Čísla jsou **návrhové skóre důležitosti 0–100 v procentech**, zaokrouhlené
na 5 bodů. Nejde o měření, procento zrychlení, pravděpodobnost ani podíl z celku;
sloupce se nesčítají. Klient = správné pochopení a rozhodnutí. Provoz =
spolehlivost, menší plýtvání, kontrola nákladů a oprav. 100 znamená základní
podmínku v dané dimenzi, kolem 50 užitečné zlepšení. Nejde o přesný žebříček
ROI; ten potřebuje náklady implementace a skutečné měření.

| Funkce | Klient | Provoz | Fáze |
| --- | ---: | ---: | --- |
| Pravdivý výsledek a kontroly | 100 % | 90 % | P0 |
| Jeden záznam běhu pro Activity a návaznosti | 95 % | 100 % | P0 |
| Co Start způsobí a co potřebuje | 100 % | 90 % | P0 |
| Čekání a konkrétní další akce | 100 % | 85 % | P0 |
| Oprava, opakování a zastavení | 90 % | 100 % | P0 |
| Historická verze a bezpečné úpravy | 90 % | 95 % | P0 |
| Čitelné části práce a skutečná mapa | 95 % | 65 % | P1 |
| Přímá editace a typované vazby | 85 % | 85 % | P1 |
| Limity, timeouty, souběh a volba modelu | 65 % | 100 % | P0 |
| Kalendář s ikonami a opakováním | 85 % | 70 % | P1 |
| Animace a vizuální doladění | 55 % | 15 % | P2 |

P0 = základ release důvěry, P1 = navazující použitelnost, P2 = doladění.
Dostupná existující funkcionalita se tím nemaže ani neodkládá; hodnotíme
potřebné doplnění a sjednocení. První implementační celek: sdílený run kontrakt,
stav a výsledek, Activity, čekání a jeho řešení, předstartovní připravenost.
Potom úplnější autorství nad stejným kontraktem.

Backend: sdílet existující journal, runs, step executions, artefakty, verze a
waitpointy. Doplnit strukturované důvody problémů, skutečné preflight výsledky,
pravidla opakování účinků, limity a souběh. Draft a revize mají řešit obnovu
práce a konflikt publikace; nezavádět novou paralelní historii. Slabší model
není automaticky ekvivalent silnějšího: hodnotit cenu za přijatý výsledek na
stejné sadě úloh. Samotné UI výkon agentů nezrychluje.

Měření: klient bez pomoci vysvětlí Start a jeho účinky, najde výsledek a vyřeší
čekání. Zaznamenat správnost, čas a chybné akce. Provoz: duplicitní účinky,
zbytečně zahájené běhy, čas do odstranění blokace a cena za přijatý výsledek.
Srovnat stejnou sadu úloh před a po změně; neslibovat předem procento úspory.

### Rozsah nové HTML ukázky

Vše běží pouze v paměti prohlížeče, bez API a modelu. Produktové texty jsou
anglicky, hodnocení česky. Ukázka má vlastní tříkrokový recept s review;
není přepisem skutečné dvoukrokové feed-change-report. Obsah reportu je fiktivní.
Obsahuje přepnutí přípravy, chybějícího připojení, fronty, běhu, lidského/event/date
čekání, neověřeného výsledku, chyby, success/no-change/partial/work-created a Stop.
Přímé odkazy: `#edit`, `#run`, `#priorities`.

Editace simuluje jméno, ikonu, účel, instrukci, lidskou kontrolu a opakování;
publikace zachová historický snapshot. Nenahrazuje úplný knihovní editor kroků,
picker agentů, typované formuláře ani plný kalendář. Tyto existující schopnosti
se mají zachovat. Workflow Map je zde schematická ukázka umístění; produkce
má využít stávající renderer. Prototyp simuluje společnou publikaci receptu a
plánu: produkce to musí umět transakčně, nebo jasně nabídnout oddělené uložení.
Recovery nabízí nový běh, nepředstírá podporu resume. AI connection je lokální
simulace, nikoli provedená credential kontrola. Approval nemůže opravit chybějící
completion. Odkazy na Activity, Inbox a Issue jsou kontextové náhledy.

Ověření nové ukázky: v Playwright prošlo zachování rozepsaných polí mezi sekcemi,
publikace v4 se zachovaným historickým v3, dvanáct provozních stavů, lokální
oprava připojení, Start/Stop, schválení i odmítnutí, jedenáct položek hodnocení,
mobil 390 px bez horizontálního přetékání, odkaz z hubu, ZIP a offline `file://`
editace. Bez JavaScriptových chyb. Skript `/tmp/routines-client-test.cjs`;
snímky `/tmp/routines-client-{overview,edit,result,priorities,mobile}.png`.
Jde o ověření návrhu HTML, nikoli backendu nebo uživatelského porozumění.
V tomto kroku se nemění aplikace ani Go kód a nebyla spuštěna živá rutina.
