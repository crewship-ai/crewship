# Routines: srozumitelná obsluha a spolehlivé autorování

Datum zadání: 2026-09-08. Tento dokument určuje cílový stav, nikoli potvrzení
hotového produktu nebo přijetí UX. Implementace následně proběhla; aktuální
rozsah, testy a otevřené body jsou v
[předání pro revizi z 9. září](ENDING-2026-09-09-ROUTINES-REVIEW.md).

## 1. Rozhodnutí a problém

Routines jsou verzované recepty kombinující skripty, integrace, agentní práci
a lidské rozhodování. Klient potřebuje vědět, co objednává, co se právě děje,
co vzniklo a co může udělat dál. Autor potřebuje recept sestavit a ověřit.
Tyto dvě činnosti mají sdílet data, grafiku a názvy kroků, ale nepotřebují stejný
objem ovládacích prvků. **Doporučení: jednoduchá obsluha a samostatný režim Edit.**

Dosavadní úpravy byly opakovaně uživatelem odmítnuty jako nepřehledné. Úspěšné
testy nejsou akceptace UX. Další fáze nemá přidávat další karty a vysvětlující
odstavce. Musí zkrátit cestu od otázky klienta k odpovědi nebo konkrétní akci.

Tento PRD určuje nový cílový UX a pořadí práce; nenahrazuje technické kontrakty
v [předání Routines](HANDOFF-2026-09-08-ROUTINES-WORKSPACE.md) ani
[předání Issues](HANDOFF-2026-09-08-ISSUES-EXECUTION.md). Starší wireframy nejsou
závazné tam, kde odporují tomuto návrhu. Nový commit `42aa533b3` již část směru
realizuje; nejprve ho posoudit a využít, ne začít od starých screenshotů.

## 2. Podklady a míra jistoty

[Konkurenční výzkum](../ux/routines-competitive-review-2026-09-08.md) obsahuje
oficiální zdroje, omezení edic a konkrétní lokální soubory. Zkoumány Windmill,
n8n, Dify, Make, Trigger.dev, LangSmith a AWX. Neproběhlo přihlášené testování
konkurence, uživatelská studie ani výkonnostní benchmark.

Převzaté principy: formulář místo konfigurace pro obsluhu; oddělení draftu od
publikované verze; práce se zachycenými daty při opravě; lidské rozhodnutí jako
plnohodnotný krok; měření kvality nad opakovatelnými příklady. Jde o naši syntézu,
nikoli prokázané procentuální zlepšení Crewship.

## 3. Pevné hranice

- Zachovat barevné tokeny, šedé podklady, ikony, avatary a komponenty aplikace.
  Vzor hustoty a formulářů: Credentials a New Project. Žádný nový vizuální jazyk.
- Levý explorer s hledáním a filtry zůstává; na mobilu dostupný jako drawer.
- Workflow Map zachovat jako alternativu ke krokům a pro Activity.
- UI texty anglicky. Datum lokalizovat explicitně anglicky, časovou zónu uvádět;
  nezaměňovat jazyk s časovou zónou. Nepoužívat nejednoznačné číselné datum.
- Zachovat kalendář den / 3 dny / týden / měsíc / rok, ikony a plánování klikem.
- Žádný nový engine, plošný redesign Issues ani paralelní systém schvalování.
- Neslibovat všechny use cases, exactly-once libovolné integrace ani totožnou
  kvalitu různých modelů. Rozšiřitelnost zajistí smlouvy mezi kroky a adaptéry.

## 4. Priority

P0 = podmínka dokončení této změny pro Release 1.0. P1 = navazující samostatné
inkrementy. Čísla dopadu bez měření nepoužívat jako procentuální jistotu.

| ID | Priorita | Výsledek pro klienta | Rozsah |
| --- | --- | --- | --- |
| R1 | P0 | Chápu recept i běh na stejném místě | Sdílená identita a seznam kroků; jasný stav a další akce |
| R2 | P0 | Spustím práci bez znalosti YAML | Jediný typovaný formulář pro spuštění a plánované vstupy |
| R3 | P0 | Editací nerozbíjím běžící práci | Oddělený draft, publikovaná verze, archiv běhu a explicitní změna aktivní verze |
| R4 | P0 | Vím, zda kontroluji nebo skutečně spouštím | Validate / Test with fixtures / Run mají odlišnou, ověřenou semantiku |
| R5 | P0 | Po chybě najdu důvod i dostupné řešení | Zachované výsledky, konkrétní problematický krok, pravdivé možnosti opakování |
| R6 | P0 | V Inboxu a Routines řeším totéž | Sdílené ID rozhodnutí, oprávnění, race ochrana, journal a návaznost Issues |
| R7 | P0 | Plán odpovídá tomu, co uvidím v kalendáři | Jednorázové i opakované položky společně; časová zóna, stav, verze |
| R8 | P1 | Mohu doplnit data a rozhodnout více způsoby | Typované lidské formuláře a pojmenované výstupní větve |
| R9 | P1 | Autor opravuje bez opakování celé práce | Zachycená data, test vybraného kroku, řízená obnova podporovaných kroků |
| R10 | P1 | Vybírám model podle kvality a ceny | Testovací sady, srovnání verzí/modelů a export výsledků |

R8–R10 nejsou automaticky součást prvního PR. P0 pro ně nesmí zavést slepou
datovou větev; existující approve/reject a bezpečné replay musí dál fungovat.

## 5. Informační architektura a chování

### 5.1 Přehled Routines

Explorer obsahuje filtry podle stavu, crew a vlastníka. Obsah zachová Overview
a Calendar; přehled má zvýraznit čekající rozhodnutí, běžící práci, nejbližší
plán a nedávné výsledky. Není nutné přidat další graf jen proto, že data existují.
Kliknutí na čekající položku otevře konkrétní rozhodnutí/běh, nikoli obecný detail.
Počet čekajících znamená otevřené požadavky, nikoli všechny historické chyby.

### 5.2 Detail receptu a běhu

Jedna identita: ikona, název, krátký účel, crew/agent s avatarem a odkazy.
Navigace: Overview, History, Versions, Schedule. Konkrétní běh je položka
historie s viditelným kontextem a návratem; nevytvářet druhý paralelní produkt.

```text
Explorer | [icon] Routine name       [agent avatar]       [Edit] [Start]
         | Overview · History · Versions · Schedule
         | Purpose / selected run and version
         | Outcome or current work                    [relevant action]
         | Result and available files (when they exist)
         | Steps                              [List | Map]
         |  [type icon] Human-readable task       [state] [duration]
         |    selected step: evidence, attempts, action
         | Provided information ▸   Activity ▸   Technical details ▸
```

U receptu seznam říká, co se stane. U běhu stejné kroky ukazují, co se skutečně
stalo. Rozvětvení zobrazit jako závislosti/skupiny, nikoli předstírat sekvenční
provedení číslovaným seznamem. Skryté kroky musí mít počet a možnost rozbalení.
Smyčky zobrazují položky a pokusy pod rodičem; event není krok ani pokus.

První obrazovka běhu odpoví: běží/čeká/skončil, kdo nebo co ho blokuje, co vzniklo,
co mohu udělat. Při čekání upřednostnit rozhodnutí; při úspěchu výsledek; při
chybě konkrétní důvod a dostupnou nápravu. Zachované dílčí výstupy nezahazovat.
Prázdné sekce nezabírají celou kartu. Chyba načtení není prázdný výsledek.

Historie: datum, způsob spuštění, verze, výsledek, trvání; filtrování a stránkování.
Spuštění otevře nově vrácené run ID, zachová explorer a nezmění vizuální systém.

### 5.3 Start a vstupy

Jediný formulář odvozený ze schématu receptu. Label, krátká nápověda pouze podle
potřeby, předvyplněná hodnota, povinnost a chyba přímo u pole. Podporované existující
typy zachovat; nepodporované schéma zobrazit explicitně, nikoli tiše změnit na string.
Výběr jedné/více možností a vlastní hodnota jen tam, kde ji schéma dovoluje.
Soubory jsou oprávněné reference na uložené objekty; credentials jsou reference,
nikoli běžné textové pole se secretem. Object/array mají strukturovaný detail.

Před Start vyřešit skutečně chybějící údaje, dostupnost crew a oprávnění. Nežádat
klienta o technický idempotency key. Dvojklik používá stejnou startovací identitu;
vědomé Run again vytvoří novou. Neznámý odhad ceny nebo délky nevydávat za nulu.
U bezvstupové rutiny nevynucovat prázdný wizard.

Historické vstupy zobrazit ze schématu a hodnot tohoto běhu: odlišit false, 0,
prázdný text, null, nepředané pole a nedostupná data. JSON pouze v detailu.

### 5.4 Autorování: New a Edit

New: Describe with AI, Use existing, Build yourself vedou do stejného draftu.
Chat musí vrátit odkaz na konkrétní draft a nesmí vedle něj vytvořit jiný recept.
Kopie existující rutiny má novou identitu; původní credentials oprávnění se nekopírují.

Edit otevře předvyplněný editor ve stylu Credentials. Identita s ikonou a názvem,
účel, crew, odpovědný agent; agent není automaticky vykonavatel všech kroků.
Konkrétní agentní krok může mít jiného vykonavatele, viditelného v jeho detailu.

Tři srozumitelné pracovní oblasti: **Recipe / Test / Publish**. Recipe obsahuje
identitu, kompaktní otázky a očekávané výsledky, seznam kroků a inspektor vybraného
kroku. Volitelné bloky rozbalitelné; nepovinná prázdná sekce nevyžaduje Continue.
Code je alternativní editor téhož dokumentu. Přepnutí nesmí ztratit neznámé klíče,
výrazy, pořadí nebo pokročilé konstrukce; nepodporovaná vizuální editace je read-only
s přesným vysvětlením. Vizuální názvy nikdy nepřejmenovávají stabilní step ID.

Krok stručně sděluje činnost, vykonavatele, vstupní zdroj a výsledek. Autor může
vybrat `Provided information → field` nebo `Previous step → output`, vidí typ
a případný příklad. Nezadává šablonové výrazy tam, kde stačí výběr. Cykly, chybějící
reference a nekompatibilní typy hlásit u konkrétního kroku.

Publish ukáže změny oproti živé verzi, výsledky validace/testů a dopad na spouštění.
Save draft nic nespouští. Test není povinné provedení reálných externích akcí jen
kvůli uložení. Změna živé verze je explicitní akce s oprávněním, ne každé uložení.

### 5.5 Kalendář a verze

Schedule obsahuje společný seznam jednorázových startů i opakování a příslušnou
kalendářní projekci. Prázdný stav „No schedules“ nesmí nastat při existujícím
jednorázovém startu. Položka ukazuje ikonu, čas/zónu, vstupní preset a stav.
Výběr dne předvyplní datum; výběr rutiny načte její vstupy. Recurrence nabízí
čitelné dny a intervaly, konec opakování a náhled nejbližších výskytů.

Cílová pravidla: běh při přijetí uloží efektivní verzi a vstupy. Jednorázové
naplánování připne zvolenou publikovanou verzi. Opakovaný trigger standardně
použije publikovanou verzi platnou při vytvoření běhu; Publish tuto skutečnost
ukáže a ověří kompatibilitu presetů. Rozpracované/ve frontě přijaté běhy se
nemění publikováním. Migrace starších plánů nesmí tyto významy změnit potichu.
Neplatný preset po změně schématu blokuje aktivaci změny s odkazem na dotčené plány.

DST pravidla doložit podle scheduleru, testovat chybějící i dvojí lokální čas.
Kalendář odlišuje plánovaný výskyt od skutečného běhu; plán není příslib úspěchu.

## 6. Kontrakt stavů a akcí

Následující jsou produktové významy, nikoli pokyn přejmenovat existující DB enum.
Implementace vytvoří explicitní mapování ze skutečných backend stavů a evidence.

| Situace | Co klient vidí | Akce |
| --- | --- | --- |
| Accepted / queued | Čeká na kapacitu, známý důvod | Stop, pokud server dovolí |
| Running | Aktuální kroky a vykonavatelé | Otevřít krok, Stop |
| Waiting for a person | Otázka, vlastník, platnost | Oprávněné rozhodnutí |
| Waiting / retry delay | Důvod čekání a další pokus, pokud známý | Detail, dostupné zrušení |
| Completed and result confirmed | Výsledek, soubory a čas | Open result, Run again |
| Execution ended, result unconfirmed | Co je doložené a co chybí | Inspect step; ne falešný úspěch |
| Failed / interrupted | Konkrétní problém a zachované výsledky | Dostupná náprava nebo nový běh |
| Cancelled | Co bylo zastaveno, co už proběhlo | Detail; ne tvrzení o vrácení účinků |
| Data unavailable | Chyba načtení / chybějící historická evidence | Retry loading, technický detail |

Oddělit execution state, business outcome a dostupnost artefaktu. Hlavní shrnutí
musí být jedno, odvozené podle dokumentované priority. Nevytvářet dva nezávislé
badge se zdánlivě protichůdnými verdikty. Odhadované procento dokončení neodvozovat
z počtu journal eventů ani počtu statických uzlů u větvení/smyček.

Retry failed step nabídnout pouze pro doloženě podporovaný bezpečný případ.
Run again je nový běh s explicitní volbou/verzí, nikdy přepsání historie.
Resume po restartu není obecná oprava změněného receptu. Přesný rozsah opakování
a případné externí zápisy musí autor vidět před provedením.

## 7. Backend a databáze: minimum nutných změn

Nejprve inventura existujících API, oprávnění a tabulek. Názvy níže označují
logické entity a požadované invarianty; nepředepisují nové endpointy/tabulky.

| Oblast | Doloženo v kódu | Požadovaná práce |
| --- | --- | --- |
| Replay a verze | `internal/api/pipeline_runs_replay.go`, pinned version | Propojit UI a audit; ověřit účinky, nikdy tichý fallback na HEAD |
| Obnova | `internal/pipeline/resume.go`, at-least-once rozpracovaný krok | Otestovat a přesně popsat hranice, ne přepsat engine |
| Dedup | `internal/pipeline/idempotency.go` | Ověřit souběžný Start a TTL; krokové externí účinky jsou samostatný problém |
| Krokové testy | `cmd/crewship/cmd_routine_step_run.go` | Reálné účinky HTTP/script; chybí důkaz izolace fixture režimu |
| Evaluace | routine backtest, pipeline EvalConfig, quartermaster | Ověřit propojení, zachovat; klientské experimenty až P1 |
| Historie a výstupy | Předání Routines, archive/executions/artifacts | Znovupoužít uložené identity a autorizované artefakty |
| Publikování | Obecný kontrakt zatím nedoložen | Inventura; podle výsledku draft revision a atomický published-version pointer |
| Lidská práce | approval banner, durable waitpoint, Inbox | P0 zachovat; P1 přidat schema/action IDs bez duplikace rozhodnutí |

Draft potřebuje base version/revision a optimistic concurrency. Konflikt dvou
editorů nesmí přepsat cizí práci. Publikace musí atomicky validovat verzi,
oprávnění a dopad na triggery; emitovat audit. Rollback přepíná budoucí spuštění,
nemění historické běhy. Zachovat kompatibilitu CLI, manifestů a AI autorování;
existující Save s jiným významem nelze potichu reinterpretovat pro staré klienty.

Snapshot běhu obsahuje verzi/hash, efektivní vstupy, zdroj spuštění a reference
na vykonavatele, pokusy a původní běh při replay. Nepřidávat kopie secret hodnot.
Zachycené testovací podklady mají workspace oprávnění, původ a retenční pravidla.
Případné migrace append-only podle AGENTS; žádná nová DB pro druhou historii.

Rozšiřitelný kontrakt kroku: stabilní ID, typ, vstupní/výstupní schéma, závislosti,
timeout/retry, deklarované účinky a reference na oprávněné capabilities.
Deklarace účinků sama o sobě není bezpečnostní hranice. CLI/script adaptér musí
respektovat skutečnou izolaci, credentials proxy, policy a limity runtime.

### Testovací režimy

- Validate: parser, schema, reference a statické kontroly; žádné spuštění práce.
- Test with fixtures: jen tehdy, pokud engine skutečně nahrazuje externí akce
  fixtures nebo je blokuje v ověřené izolaci. Neznámý efekt nesmí tiše projít.
- Live test / Run: reálná práce s reálnými oprávněními a možnými náklady; jasně
  označená a dohledatelná. Pokud izolace není k dispozici, nenabízet falešný sandbox.

## 8. Integrace a výkon

Activity používá stejné run/execution/decision IDs a kanonický journal.
Routines zobrazuje jeho projekci, ne další zdroj pravdy. Odkaz otevře konkrétní
run/krok; klient si zachová cestu zpět. Inbox rozhodnutí se řeší transakčně právě
jednou; zrušení, timeout nebo lidské převzetí v Issue odmítne zastaralou odpověď.
Crew/agent vlastnictví se nesmí zaměnit s identitou klienta a runtime oprávněním.
Credentials UI ukáže dostupnost a autorizovaný odkaz; nikdy secret v aktivitě.

Detail načte souhrn a první stránku kroků, nikoli celý journal nebo všechny blob
výstupy. Mapu a velká data načítat na vyžádání. Seznamy stránkovat; live události
slučovat podle identity, po reconnect dohledat mezery. Aktualizace nesmí krást
focus, resetovat rozbalené kroky ani přeskakovat scroll.

Fixture cache závisí na verzi kroku a vstupních datech; žádné skryté použití starého
výstupu v produkci. Paralelismus jen podle závislostí a limitů runtime. V P0 změřit
baseline a změnu počtu requestů, přenesených bytů a času do použitelného detailu
na stejných datech. Neslibovat rychlost modelové práce na základě zrychlení UI.

## 9. Akceptace a ověření

| Scénář | Nutný výsledek |
| --- | --- |
| Recept bez vstupů, jednoduchý úspěch | Start bez prázdného průvodce; výsledek a kroky ve společném shellu |
| Typované vstupy, defaulty, neplatné hodnoty | Shodná validace ručně i v plánu; historické hodnoty přesné |
| Dvojklik / opakovaný request | Jeden logický start; další vědomé spuštění nový run |
| Edit během běhu, publish během čekání ve frontě | Přijatý běh zůstane na svém snapshotu |
| Dva editoři | Konflikt revizí, žádný ztracený draft |
| Změna schématu s existujícími plány | Jasně určené nekompatibilní presety; žádný tichý rozbitý plán |
| Větev neprovedena, foreach, více pokusů | Skipped jen s evidencí; položky/pokusy nejsou počítány jako definované kroky |
| HTTP chyba po možném externím zápisu | Žádné neověřené „pokračovat bezpečně“; zachovaný důvod a rozsah replay |
| Restart u waitpointu a rozpracovaného kroku | Zachované identity; doložená at-least-once semantika |
| Dvě současná rozhodnutí / timeout / Issue takeover | Jediný přijatý verdikt; ostatní odmítnuté; Inbox konzistentní |
| Výsledek existuje, chybí completion signal | Výsledek přístupný, běh není falešně úspěšný |
| Načtení outputs/journal selže, archiv chybí | Chyba odlišná od prázdna; žádný fallback na současný recept |
| Jednorázový start + recurrence + DST | Stejné plány v detailu a kalendáři; test obou změn času |
| Neoprávněný uživatel, soubor jiného workspace | Server odmítne přístup i při přímém API odkazu |
| Klávesnice, úzký displej, reduced motion | Dostupné ovládání, vrácení focusu, bez přetečení; animace nepodmiňuje porozumění |
| 100 kroků, 1 000 pokusů, dlouhý journal | Stránkování/lazy load, použitelné první zobrazení; měření vůči baseline |

UX protokol před dokončením: na klikacím prototypu zadat 5 úloh — vysvětlit recept,
spustit se změněným vstupem, najít důvod selhání, vyřešit čekající rozhodnutí,
změnit plán. Cílově alespoň 4 z 5 reprezentativních uživatelů dokončí každou bez
nápovědy; jde o navržený cíl, ne naměřený výsledek. Nejsou-li uživatelé dostupní,
uvést pouze interní walkthrough a ponechat validaci použitelnosti jako neověřenou.

## 10. Realizační pořadí a hotovo

1. **Inventura:** porovnat aktuální HEAD s tímto PRD, zapsat existuje/chybí/nejisté
   pro každý P0; doložit draft/publish, test effects a API state mapping.
2. **Konkrétní prototyp:** jeden společný shell a 5 výše uvedených úloh, včetně
   chyby/čekání a Edit. Existující barvy a ikony. Ne pět dalších vizuálních stylů.
3. **První vertikální změna:** Overview → Start → Run → problémový krok → Edit
   draft → Validate → Publish; historie a snapshoty zůstávají pravdivé.
4. **Dokončení P0:** kalendář, oprávnění, společná rozhodnutí, zátěž dat a regrese
   Issues. Každý PR obsahuje ověřitelný uživatelský výsledek a cílené testy.
5. **P1 samostatně:** rich human forms; fixture/debug/recovery; eval porovnání.

Před migrací agent předloží konkrétní delta schema a kompatibilitu ve svém
implementačním plánu. Před nasazením provede předepsané repo kontroly; nasadí pouze
dev1, ověří v browseru a doplní přesné odkazy, scénáře, výsledky i neověřená místa.
Neslučovat PR bez skutečného review. Hotovo neznamená jen zelené testy: P0 musí
mít prokázanou funkčnost a UX musí být samostatně vyhodnocené, nikoli domněle přijaté.
