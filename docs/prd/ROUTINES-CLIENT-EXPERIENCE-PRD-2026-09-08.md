# Routines: srozumitelná obsluha a spolehlivé autorování

Datum zadání: 2026-09-08. Aktualizace implementace: 2026-09-10.
Toto je živý dokument rozsahu, akceptace a důkazů Routines. Historická předání
zůstávají archivem. Opravy A–D jsou sloučené; čitelnost se ověřuje v samostatné
větvi. Nasazení této změny a uživatelské přijetí Edit/Test zatím nejsou doložené.

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
| R4 | P0 | Vím, zda kontroluji nebo skutečně spouštím | Test routine / Test this step / Run mají odlišnou, ověřenou semantiku |
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

Závazná architektura editoru má nejvýše čtyři souběžné pracovní plochy:
**lišta akcí**, **dokument receptu**, **seznam kroků** a **panel vybraného kroku**.
Dokument je jeden scroll: identita → Inputs → kroky → rozvrh. Seznam kroků je
součástí dokumentu; Map jej nahrazuje, neotevírá další inspektor. Vybraný krok
má jedinou dvojici **Edit / Test**. Test nemá vnořené taby. **Publish** je jedna
akce v liště; Publication changes je sbalený obsah dokumentu, nikoli další
navigační sekce. **Save draft** ukládá rozpracovanou verzi. Neexistují kroky
průvodce ani Continue. Přepínač Code nahrazuje dokument stejným živým bufferem.
Počítání čtyř ploch nezahrnuje jednotlivá pole a sbalené technické údaje;
přidaný samostatný editor, inspektor nebo navigační sekce by limit porušily.

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

**Vstupní preset R7 ověřen na dev1 (2026-09-10):** souhrn je vidět u
opakovaného i jednorázového plánu v detailu a kalendáři. Změna přes Edit inputs
se promítla do obou míst; ověřeny prázdné vstupy, dlouhý text a detail při 390 px.
Důkazy: [protokol R7](reports/r7-preset-visibility-2026-09-10.md). Toto splňuje
část §5.5 o viditelnosti presetu; nenahrazuje přejímku DST/§9 ani uživatelovo
potvrzení Edit/Test/Run.

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

- **Test routine**: parser, schéma, reference a statické předpoklady. Nespouští
  agenty, skripty ani HTTP volání. Úspěch není důkaz úspěchu skutečného běhu.
- **Test this step**: transformace vypočítá výsledek nad dodanými vzorky.
  Agent, HTTP a skript vyžadují výslovně dodaný náhradní výstup; nevolají službu
  ani model. Výsledek říká, co bylo vypočteno a co jen nahrazeno. Ostatní typy
  se tímto režimem nespouštějí. Technický API název `fixture_test` se nemění.
- **Run** a **Compare versions**: skutečná práce, možná změna dat a náklady.
  Porovnání je v detailu **Versions**, mimo editor. Uživateli před spuštěním
  sděluje účinky; obecný sandbox se neslibuje.

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
3. **První vertikální změna:** Recipe → Run → Run detail → problémový krok → Edit
   draft → Test → Publish; historie a snapshoty zůstávají pravdivé.
4. **Dokončení P0:** kalendář, oprávnění, společná rozhodnutí, zátěž dat a regrese
   Issues. Každý PR obsahuje ověřitelný uživatelský výsledek a cílené testy.
5. **P1 samostatně:** rich human forms; fixture/debug/recovery; eval porovnání.

Před migrací agent předloží konkrétní delta schema a kompatibilitu ve svém
implementačním plánu. Před nasazením provede předepsané repo kontroly; nasadí pouze
dev1, ověří v browseru a doplní přesné odkazy, scénáře, výsledky i neověřená místa.
Neslučovat PR bez skutečného review. Hotovo neznamená jen zelené testy: P0 musí
mít prokázanou funkčnost a UX musí být samostatně vyhodnocené, nikoli domněle přijaté.


## 11. Release 1.0: pozorovatelná přejímka a hranice

Následující rozhodnutí nahrazují stavovou tabulku historického ENDING dokumentu.
Existence kódu a zelené testy nejsou označením přijatého uživatelského chování.

| Oblast | Co musí klient skutečně zvládnout | Rozsah Release 1.0 |
| --- | --- | --- |
| R1, F1/F3 | Bez hoveru přečte účel rutiny v hlavním seznamu, otevře ji a vlastními slovy popíše kroky. | Hlavní seznam, popis, počet kroků a lidské názvy. Chybějící popis se odvodí z první a poslední skutečné činnosti, bez tvrzení o pořadí DAG. |
| R2 | Změní typovaný vstup, uvidí neplatnou hodnotu u pole a později dohledá přesně odeslanou hodnotu. | Typované primitivní vstupy a defaulty. Autorizovaný výběr souborů a credential referencí je **mimo Release 1.0**, nepodporované typy zůstávají blokované. |
| R3 | Uloží draft bez změny běžící rutiny, publikuje explicitně, v druhém editoru dostane konflikt a zachová svou práci. | Draft CAS/ABA, verze, publikace a review rizika; žádná nová maker-checker policy. |
| R4, F4 | Před Test rozliší kontrolu receptu od dodaných vzorků a před Run ví o skutečných účincích. | Test v panelu kroku, bez průvodce a bez vnořených testovacích tabů. |
| R5, F5/F6 | Najde verdikt, chybu a lidský název chybného kroku nahoře; krok je otevřený i za limitem prvních 12. | Historický snapshot, důvod chyby, explicitní nový běh s volbou verze. Pokračování od vybraného selhaného kroku/iterace je **mimo Release 1.0**. |
| R6 | Přihlášeně otevře stejné čekající rozhodnutí v Inbox i Routines, odešle typovanou odpověď a dohledá jediný přijatý verdikt na témže run ID. | Vlastní autentizovaný Inbox, sdílený kontrakt rozhodnutí. Přejímka vyžaduje konkrétní run ID, obě UI cesty a výsledek souběžných odpovědí; unit test ji nenahrazuje. |
| R7 | Změní plán a najde stejné datum i vstupy v detailu a kalendáři; zrušený jednorázový start po editaci neožije. | Připnuté jednorázové starty, pravidelné plány, kalendář, historické verze. |
| R8 | Autor vytvoří typované otázky a vlastní rozhodovací akce; rozhodující vidí odpovídající formulář. | Existující builder zachován. Obecný vizuální návrhář rozhodovacích větví není součástí. |
| R9 | Autor importuje zachycená data a pozná, co test skutečně spočítal. | Lokální vzorky, podporované vazby a zachycené výstupy. Řízené pokračování se odkládá stejně jako v R5. |
| R10 | Autor ve Versions výslovně zvolí publikované verze, spustí porovnání a vyexportuje výsledky. | Lokální dataset a porovnání zachovány. Sdílené serverové datasety a sémantické hodnocení jsou **mimo Release 1.0**. |

Editor vazeb podřízené rutiny zatím nenabízí pole podle jejího načteného schématu.
Runtime validuje vstupy před spuštěním dítěte; schema-aware návrhy jsou odložené.
Zachované hooky, DAG, retry, foreach, guardrails, eval a execution tiers se
nepřepisují. Jediná změna kontraktu kroku pro čitelnost je nepovinné `name`;
identita, reference `needs` i cesty zaznamenaných běhů nadále používají `id`.

## 12. Důkazy oprav a stav přejímky (10. září)

| PR | Sloučení | CI a review |
| --- | --- | --- |
| [A #2474](https://github.com/crewship-ai/crewship/pull/2474) | `4581b38f1706` | CI 34460894622 prošlo. Skutečné CodeRabbit review a vypořádaná vlákna; závěrečná dokumentace ručně. |
| [B #2476](https://github.com/crewship-ai/crewship/pull/2476) | `fa222ad2e1e5` | CI 34473215882 prošlo. Skutečné review, pět oprav potvrzeno; závěrečná změna CI ručně při vyčerpané kvótě. |
| [C #2478](https://github.com/crewship-ai/crewship/pull/2478) | `6ce5afd45089` | CI 34476869288 prošlo. Skutečné review, opravy potvrzeny; finální integrační delta ručně dle §3 work orderu. |
| [D #2482](https://github.com/crewship-ai/crewship/pull/2482) | `6a9857f5da45` | CI 34481621297 prošlo. Skutečné review, všech 16 vláken potvrzeno a vyřešeno; konečný celý head nebyl znovu strojově přečten kvůli limitu. Ruční pokrytí a retrigger jsou doložené v PR. |

Opravy zahrnují N1–N15 a kontraktní T1–T5: skutečné HTTP bajty publikace,
plochou odpověď rozhodnutí, všechny asynchronní starty, upgrade starší DB a
připnuté i zrušené jednorázové starty. Test reálného chunked HTTP navíc dokládá
zachování vstupů a verze bez Content-Length. Publikace neposuzuje důkaz podle
jinak escapované reprezentace JSON.

Pro D prošly lokálně všechny Go balíky a vet. Část běhu bylo nutné zopakovat
v soukromé cache po smazání sdílených build souborů jiným procesem; nešlo o
selhání asercí. Údaj 707 testů / 80 souborů v dřívější evidenci znamená cílený
frontendový výběr, nikoli celý frontend repozitáře.

Čitelnost: kontrola čisté funkce nad zmrazenými 81 kroky má nula neznámých typů
po doplnění pěti větví. To **nenahrazuje ověření všech 81 názvů v živém UI**.
Původní vzorek obsahuje 27 rutin, 25 popisů a nula vlastních názvů kroků.
Původní jedno interní měření prvního zobrazení: 470 ms, 32 API požadavků,
158 433 přenesených API bajtů. Není to uživatelská studie ani benchmark.

- [x] Nasazení čitelnosti na dev1 a nové měření stejným postupem (§14).
- [x] Pět přihlášených úloh z §9 interním browser walkthrough (§14); nejde o uživatelskou studii.
- [x] Restart čekajícího rozhodnutí a rozpracovaného kontrolovaného běhu;
      běžný reload nedokazuje recovery po náhlém ukončení ani exactly-once účinky.
      Doloženo 11. 9. dvěma `kill -9` na izolované instanci nad jedním během
      (`run_cmtwp7vt700033e66cfc6`): obnovené rozhodnutí na témže tokenu,
      rozpracovaný krok proveden znovu (`att=1 interrupted` → `att=2 completed`),
      přijatá v1 přežila publikaci v2. Exactly-once se **netvrdí**: at-least-once
      je změřeno recorderem na 1× dokončený a 2× rozpracovaný krok.
- [x] Souběh startů, editorů a rozhodnutí nad skutečným serverem (§14).
      11. 9. doplněno o timeout vs. opožděnou odpověď (409) a o čtyři souběhy
      dvou opačných odpovědí (vždy jedno 200 a jedno 409).
- [ ] Uživatel bez výkladu vysvětlil pět rutin a potvrdil Edit/Test.

Závěrečný bod smí potvrdit pouze uživatel. Žádné interní měření, screenshot,
review ani zelené CI není náhradou tohoto potvrzení.

## 12b. Technická přejímka §9 — 11. září

Protokol s run IDs, přesným rozsahem každého scénáře a seznamem toho, co
zůstává NEOVĚŘENO: [routines-acceptance-2026-09-11](reports/routines-acceptance-2026-09-11.md).
Doplněny byly náhlý pád a obnova, nejistý externí účinek, timeout a souběh
rozhodnutí, serverová izolace oprávnění, neprovedená větev / foreach /
skutečné pokusy, dva editoři a DST na skutečné dispatch cestě (dosud byla
doložena jen projekce kalendáře).

Protokol nese tři nálezy. **N1 je opravený** — běh zrušený jinak než tlačítkem
Cancel (odpojený klient, timeout proxy, deadline CLI, řádné vypnutí) se
zapisoval jako `failed` s důvodem `context canceled`, razil error fingerprint,
posílal failure notifikaci a pouštěl `on_failure` hook, zatímco journal tentýž
okamžik označoval `CANCELLED`. **N2 a N3 opravené nejsou** a jsou to změny
kontraktu pro vlastní PR: kontrola kompatibility presetů se obchází přímým
`routine save`, a server nevaliduje typované vstupy běhu ani presetu (UI je
validuje, a to jednou sdílenou komponentou pro ruční start i pro plán).

## 13. Historický podklad pro oponenturu — před živou přejímkou

Tento oddíl zachovává stav před nasazením; aktuální výsledky jsou v §14.

**Stav: opravy jsou integrované, čitelnost je připravená v PR, produktová
přejímka není hotová.** Předmětem oponentury je
[PR #2485](https://github.com/crewship-ai/crewship/pull/2485), aplikační head
`71725f860724647bdb1dc20e290fa66bc7f13b2d`. Tato část je podklad k revizi,
nikoli tvrzení o dokončeném nasazení nebo přijatém UX.

### Co se podařilo

Opravy byly rozděleny a sloučeny v předepsaném pořadí: A draft/publish,
B vzorová testovací data, C lidská rozhodnutí, D běhy/plány/porovnání.
Všechny čtyři PR prošly CI. Zásadní opravy chrání bajty publikačního důkazu,
asynchronní a idempotentní start, uložené vstupy, připnutou verzi běhu,
souběžné drafty a rozhodnutí a integritu zachycených výsledků. Doplněné
T1–T5 pokrývají skutečné HTTP bajty, tvar odpovědi rozhodnutí, všechny start
cesty, upgrade starší databáze a zrušené jednorázové starty. Mapa skutečného
CodeRabbit pokrytí a ručních dodatků při kvótě je v §12 a příslušných PR.

Následná čitelnost v #2485 používá data, která už aplikace měla: viditelné
popisy a počet kroků v hlavním seznamu, lidské názvy kroků a činnost jako
fallback. Přidává pouze nepovinné `Step.Name` a `step_count` v odpovědi API;
engine se nepřepisuje. Editor je jeden dokument s panelem kroku Edit/Test.
Publikace zůstává explicitní, porovnání publikovaných verzí je ve Versions.
Chyba a identifikace selhaného kroku jsou nahoře; důležitý krok se otevře i
za limitem prvních 12. Prázdné sekce se sbalí, duplicitní spodní dok zmizí.
Vzorky, publikační review, rozhodovací formuláře ani porovnání nebyly odstraněny.

### Jak silné jsou důkazy

- Úplný lokální Go běh: **138 testovaných balíků prošlo**, dalších 10 nemá
  testy. API 1 215,677 s, databáze 1 014,954 s, pipeline 15,369 s. Vet prošel.
- Úplný frontendový běh před posledním drobným dodatkem: **8 441 testů v 710
  souborech prošlo**. Dodatek zachovává ID kroku při nedostupném historickém
  receptu a dokončuje slovník; jeho nový test a všech 314 testů komponent
  Routines prošly, stejně jako typy a lint dotčených souborů. Aktuální CI
  celého PR ještě není dokončené.
- Produkční frontendové sestavení prošlo. Celý lint měl nula chyb a 30
  upozornění mimo změněný kód Routines. Strict docs inventory, parita schémat,
  migrační lint a agent invariants prošly.
- Funkce pro názvy pokrývá zmrazený vzorek všech **81 kroků / 27 rutin** bez
  neznámého typu. **Živé zobrazení všech 81 kroků zatím ověřeno není.**
- Na dev1 je zachycena původní chyba publikace: uložení 200, test 200,
  publikace 422 `save_token invalid (expired, malformed, or signed for a different definition/user)`. Samotný status příčinu nedokazuje. Pozitivní průchod opraveným nasazením
  ještě nebyl proveden. Přípravné skripty nejsou důkaz jeho úspěchu.
- #2485 zatím nemá dokončené nezávislé CodeRabbit review: první pokus skončil
  oznámením o kvótě. Vlastní revize autora tuto skutečnost nenahrazuje.

### Co netvrdíme

Dev1 zatím nebyl přepnut na tuto implementaci. Původní pracovní kopie je
zachovaná; integrační postup musí zachovat i nesouvisející WIP. Neproběhla
nová přihlášená přejímka pěti úloh, restartu a souběhu. Nemáme měření
zrychlení po změně ani uživatelskou studii. Počet nejvýše čtyř pracovních
ploch je návrhový limit s metodikou v §5.4, nikoli nezávisle přepočítaný
výsledek. **Zelené testy nedokládají, že klient Edit/Test rozumí.**

Typovaná rozhodnutí ve vlastní autentizované schránce jsou produktová
hypotéza diferenciace. Tržní rešerše vychází z veřejné dokumentace, nikoli
z přihlášeného srovnávacího testování. Nedokazuje obchodní převahu.

### Zadání oponentovi

Projděte změny a jejich testy, nehodnoťte jen tento souhrn. U nálezu uveďte
soubor/řádek, konkrétní reprodukci, dopad a chybějící důkaz.

1. Dokládají testy skutečné klient↔server kontrakty a historické snapshoty?
   Který test by přežil návrat původní chyby nebo pouze opakuje implementaci?
2. Je rozdíl mezi Test routine, vzorovým testem kroku a skutečným Run či
   porovnáním zřejmý před akcí? Neslibuje některý text neexistující izolaci?
3. Je editor skutečně jednodušší při stejné metodě počítání ploch, nebo jsme
   ovládání pouze schovali? Zachovávají Edit, Code a Test rozpracovaná data?
4. Jsou odvozené názvy a účely pravdivé u podmínek, DAG a neúplných dat?
   Neztratí se chyba, čekající rozhodnutí nebo důležitý krok při stránkování?
5. Jsou odklady R2, R5/R9 a R10 v §11 obhajitelné pro Release 1.0? Který
   chybějící průchod je překážkou vydání a který pouze další iterací?

Po oponentuře následuje vypořádání nálezů, zelené CI a merge #2485, bezpečné
přepnutí dev1 na úplný integrovaný zdroj, pět přihlášených úloh s konkrétními
run IDs, restart/souběh a nové měření. Poslední bránu tvoří uživatelovo
vysvětlení pěti rutin a výslovné potvrzení srozumitelnosti Edit/Test.


### Dodatek po oponentuře

`step_count` znamená počet definovaných kroků **vrchní úrovně**. Nezahrnuje
vnořené definice uvnitř foreach, počet položek ani pokusy; foreach s osmi
vnořenými kroky tedy má v seznamu jeden vrchní krok. Není to měřítko objemu práce.

Původní zkratka `invalid_save_token` byla chybná citace; výše je opravená
na přesnou odpověď. Dochovaný log `/tmp/crewship-1-go.log` obsahuje pro stejný
jedinečný slug `work-order-proof-mtvcinau` v čase `2026-09-10T09:49:34.749446989Z`
`pipeline save: save_token rejected`, `err="save_token: HMAC mismatch"`.
To upřesňuje odmítnutí na neshodu podpisu, nikoli expiraci. Pozorování je
ze starého nasazení; samo nedokazuje úspěch opravy. Reprodukční skript ukládá,
testuje a publikuje jednu definici s doslovnými `&`, `<`, `>` ve stejném
přihlášeném kontextu. Pozitivní opakování po nasazení zůstává povinné.

Závislosti `needs` byly už před oponenturou v rozbaleném detailu editoru i
spine; chyběly v zavřeném řádku. Dodatek je zobrazuje přímo v řádku a u
závislostí či automatického paralelismu odstraňuje pořadová čísla a vysvětluje
rozdíl mezi pořadím receptu a spuštění. Vlastní názvy se zkracují stejně jako
odvozené; uložený název a ID se nemění. Počet celé testové sady zůstává
regresní kontrolou, nikoli důkazem použitelnosti nebo pokrytí všech změn.


## 14. Odpověď na oponenturu a živý protokol — 10. září 2026

**Oponent měl pravdu: původní podklad nedokládal nasazení ani přejímku.**
Následující výsledky jsou nové pozorování přihlášeného Chromium na
[dev1](https://crewship-dev1.unifylab.cz/routines), nikoli dodatečné přejmenování
unit testů na uživatelský důkaz. Interní průchod nenahrazuje potvrzení člověka.

### Nasazení a ochrana rozpracované práce

Dev1 dostal úplný integrovaný zdroj, včetně upstream main `410563eca`.
Původní checkout `cd2074d0b` a jeho rozpracované soubory jsou zachované v
archivu a stash `a1420f0b69c03cb67e8efa192462f8e5d8174e3e`.
Šestnáct nesouvisejících souborů bylo obnoveno a jejich obsah ověřen SHA-256;
tři další změny již byly v upstream. Žádná jiná instance nebyla nasazena.
Sestavení i restart proběhly standardním `systemctl reload crewship-ws@1`.
Browser úlohy začaly na `bf83d6d21`, následná oprava obnovy běhu byla ověřena
na `353e0a588`. Revision binárky byla ověřena pomocí `go version -m`, nejen
podle Git checkoutu. Dev1 obsahuje zachované nesouvisející WIP.

### Pět úloh a autorův Edit/Test

Všechny běhy používají vlastní kontrolované recepty bez agentů a externích
HTTP akcí. Neproběhlo přihlášené srovnávání s konkurencí ani uživatelská studie.

| Úloha | Pozorování a důkaz |
| --- | --- |
| Přečíst účel a kroky | Popisy jsou viditelné v hlavním seznamu. Browser postupně otevřel všech 27 původních rutin včetně skrytých a všech 81 kroků; primární titulky odpovídají odvozeným názvům, žádný není jen technické ID. To dokládá render, nikoli porozumění člověka. |
| Změnit vstup a dohledat výsledek | `run_cmtvp6yn4001555f2552a`: vstup `message="Message changed in the browser"`; dokončený výstup odpovídá přesně odeslané hodnotě. |
| Najít neúspěch | `run_cmtvp710k0016dcaa8189`: `failed`, krok `intentional_failure` otevřený. Verdikt i chyba jsou v prvním viewportu 1440×1000. Skutečná chyba je ne-JSON vstup transformace vyžadující JSON, nikoli selhání výstupní kontroly. |
| Vyřídit rozhodnutí | `run_cmtvp8yvi001dd218715d`: z Routines otevřeno stejné rozhodnutí v Inbox; odpověď `count: 0, enabled: false` přijata HTTP 200, běh dokončen, rozhodnutí odstraněno z čekajících. |
| Naplánovat a zrušit | Jednorázový start `pnd_cmtvp94qf0012632eb36d` na `2026-09-12T10:30:00Z`, připnutá verze 2; vytvořen a zrušen přes browser, nepřítomnost plánu ověřena API. |
| Edit/Test/Publish | Vlastní název `Return the entered message`, stabilní ID `echo`, publikovaná verze 2. Test transformace vypočetl `Sample from browser`; UI správně uvádí, že výstupní kontroly nejsou deklarované. Statická kontrola receptu neslibuje spuštění agentů, skriptů ani HTTP. |

Snímky skutečného průchodu: [hlavní seznam](../ux/assets/routines-acceptance-2026-09-10/main-list.png),
[změněný vstup](../ux/assets/routines-acceptance-2026-09-10/changed-input.png),
[chyba nahoře](../ux/assets/routines-acceptance-2026-09-10/failure-desktop.png),
[rozhodnutí v Inbox](../ux/assets/routines-acceptance-2026-09-10/decision-in-inbox.png),
[jednorázový plán před zrušením](../ux/assets/routines-acceptance-2026-09-10/one-time-schedule.png).
Doplňující kontrola na `6d681bd85`: [úzký displej 390×844](../ux/assets/routines-acceptance-2026-09-10/failure-mobile.png)
a [Edit/Test](../ux/assets/routines-acceptance-2026-09-10/edit-test.png).
Na 1440×1000 i 390×844 není vodorovný overflow dokumentu a chyba je v prvním
viewportu (na mobilu y=547,94, výška 64 px). Kontrola běžela s reduced motion.
Escape nyní vrací fokus na původní Edit; před opravou tato browser aserce
selhala, po opravě prošla na obou šířkách. Cílených 59 testů prošlo.
Nejde o úplný audit přístupnosti ani zkoušku všech kombinací délky textu.

Snímky obsahují pouze demo data; autentizační stav a surové odpovědi s tokeny
nejsou součástí repozitáře.

### Publikační důkaz: původní chyba a pozitivní opakování

Původní serverový log je korelován přes slug `work-order-proof-mtvcinau` a čas
`2026-09-10T09:49:34.749446989Z`: `save_token: HMAC mismatch`.
Samotné 422 by chybu podpisu nedokazovalo. Po nasazení tentýž postup nad
jednou definicí s doslovnými `&`, `<`, `>` prošel: uložení 200, kontrola 200,
publikace 201, následné čtení 200 a zachování doslovných bajtů.
Pozitivní sonda `work-order-proof-mtvp2jq1` proběhla v 15:40 UTC a byla odstraněna.

### Souběh a restart odhalily další skutečnou chybu

Dva současné starty se stejným idempotency key vrátily 202/200 a stejné
`run_cmtvp9dkr001eaf494dda`. Dva zápisy draftu nad stejnou revizí vrátily
200/409; vítězná hodnota byla přečtena zpět.

**První restart čekajícího rozhodnutí neprošel.** Běh
`run_cmtvparzs002020412b2c` měl zachycenou původní definici, ale po publikaci
nové verze obnovovací cesta použila aktuální head a odmítla jej jako
`definition changed since run started (content hash mismatch)`.
To je nový runtime nález, nikoli chyba testovacího skriptu. Dva regresní testy
nejprve selhaly: restart i odpověď na čekání po změně publikované definice.

Commit `353e0a588` opravuje obnovu: používá uložený execution snapshot,
nepřepíše jej novějšími step overrides a při nečitelném zachyceném receptu
odmítne pokračovat. Starší běhy bez snapshotu zachovávají dosavadní kontrolu
verze/hash. Jde o 29 přidaných a 10 odebraných řádků runtime, bez migrace
nebo nového endpointu; **dřívější tvrzení o nulové změně exekuce již pro celý
PR neplatí**. Engine se nepřepisuje.

Opakování na opravené binárce prošlo: `run_cmtvpv3y000039bf2ed9a` po běžném
reloadu zachoval přijatou verzi 1 proti head 2, hash, token, formulář a Inbox
`ibx_waitpoint_c248743b52fa97fd289338b43291c54a`.
Ze dvou současných odpovědí přijal jednu (200/409), dokončil se s
`{count: 0, enabled: false}` podle původního receptu a Inbox přešel na `resolved`.
Anonymní veřejný callback na toto typované rozhodnutí byl odmítnut 403.

Rozpracovaný běh `run_cmtvpv41j000681bfa329` běžný reload ukončil jako
`failed: context canceled`, ale zachoval mezivýsledek `saved`.
**To nedokládá obnovení rozpracované akce po tvrdém pádu ani exactly-once
vedlejší účinky.** Tyto závěry si nelze odškrtnout z úspěchu čekajícího rozhodnutí.

### Regresní kontroly a dostupnost důkazů

Na aplikačním head `6d681bd85` prošel celý frontend: **8 451 testů / 710 souborů**,
173,13 s. TypeScript a lint změněných souborů prošly. Vet prošel pro runtime
`353e0a588`; celý Go běh tohoto runtime je při zápisu protokolu ještě spuštěný.
Celý pipeline balík po opravě snapshotu prošel (11,083 s). Strict docs inventory
ověřil 636 API operací a 878 CLI příkazů; agent invariants i migrační lint prošly.
Výsledek dokončeného Go běhu a CI bude doložen v PR na konkrétním head.

Čtyři kontrolované recepty `work-order-*-mtvp2jz6` zůstávají na dev1 pro
reprodukci a ruční přejímku. Závěrečné čtení API nepotvrdilo žádný jejich
aktivní běh; jednorázový plán byl zrušen. Nejde o původní vzorek 27 rutin.
Surové autentizační/tokenové reporty zůstávají mimo repo. Run IDs, scénáře,
pozorované výsledky a snímky jsou uvedeny výše; účelové sondy publikace byly
po pozitivním ověření odstraněny.

### Co se změnilo po připomínkách a co zůstává otevřené

DAG řádky ukazují závislosti a nepředstírají pořadí exekuce čísly.
`step_count` je výslovně počet definic vrchní úrovně, nikoli vnořených kroků.
Vlastní názvy se zkracují na první řádek a nejvýše 100 znaků bez změny uložené
hodnoty. Prázdné uložené vstupy mají jediný řádek; statická kontrola již
nevypisuje technický výstup `<dry-run>`.

Stejná jednorázová metrika otevření detailu: před 470 ms / 32 API požadavků /
158 433 bajtů, po 387 ms / 33 požadavků / 161 857 bajtů. Podmínky cache a
zatížení nejsou kontrolované; nejde o statistický důkaz zrychlení.

CodeRabbit provedl skutečné review původního head `25bfebfab`.
Osm věcných nálezů a dvě drobnosti bylo opraveno. Ze čtyř požadavků odstranit
jména konkurentů z evidenční rešerše bot tři stáhl; čtvrtý autor vypořádal
ručně, protože zadání výslovně vyžaduje zachovat dohledatelnou rešerši.
Všechna původní vlákna jsou uzavřená; to samo neznamená strojové review
pozdější opravy obnovy. Aktuální CI a pokrytí další revizí musí být ověřeno
na konečném head před merge.

**Závěr pro oponenta:** nyní existuje skutečné nasazení, render 81 kroků,
interní průchod pěti úloh, pozitivní publikace a serverový souběh. Živý test
navíc našel a následně prokázal opravu obnovy čekajícího běhu. Přetrvává
neověřená použitelnost člověkem a neprokázané zotavení rozpracované akce po
náhlém pádu. Před vydáním je nutné dokončit brány CI/review/merge a získat
uživatelovo potvrzení podle §9–§10. Počty regresních testů tyto brány nenahrazují.
