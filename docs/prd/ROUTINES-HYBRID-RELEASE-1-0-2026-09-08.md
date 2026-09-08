# Routines 1.0: řízená agentní práce nad dokumenty

Datum: 2026-09-08. Audit aktuálního kódu na dev1, větev
`dev1/issues-preview` @ `492c90ad9`. Navazuje na předání Issues a oba audity
v `docs/ux/routines-*-2026-09-08.md`. Tento dokument mění jejich produktové
priority: určujícím příkladem jsou podklady pro účetní, nikoli deploy Terraformu.
Je to návrh a evidence, ne oznámení implementace nebo živého E2E ověření.

## Produktový kontrakt

Rutina je opakovatelný pracovní postup týmu. Vymezuje cíl, vstupy, zdroje,
fáze, pravomoci, kontroly a výstup; uvnitř agentní fáze dovoluje účelné
zkoumání dokumentů a použití nástrojů. Není nutné zakódovat každé agentovo
otevření souboru jako uzel grafu. Skripty a agenti se mohou střídat opakovaně.

Autor receptu může být silný model. Vykonavatel nemusí být tentýž model.
Kvalita se však měří splněním přejímacích kritérií a správností podkladů,
nikoli identickým textem nebo vlastní deklarací confidence. Automatická
kontrola má přednost tam, kde lze správnost rozhodnout programem; člověk
nebo kvalitnější agent řeší skutečnou nejednoznačnost.

Pracovní návrh pro diskusi: připravit hotovou část, nejasnosti seskupit do
Inboxu a finální předání pozastavit; první pilot končí balíčkem a přehledem,
ne zápisem do účetního systému. Tyto dvě preference byly položeny uživateli
a zatím nejsou potvrzené. Návrh nesmí předstírat, že čas bez odpovědi je souhlas.

## Referenční průchod

1. Člověk zvolí firmu, období, zdrojové účty/složky a místo výsledku.
2. Zdrojové adaptéry stáhnou úplný seznam zpráv a příloh, výpisy a další
   podklady. Evidují stránkování, chyby a hranici načteného období.
3. Skripty normalizují názvy, formáty, extrahují text/tabulky a hledají duplicity.
4. Agent čte podklady a podle potřeby originální PDF/obrazové stránky. Zjišťuje
   účel dokladu, souvislosti a možné vazby na platby. Název souboru je indicie,
   nikoli autoritativní zdroj. Nečitelný dokument nevyplňuje odhadem.
5. Programové kontroly ověří úplnost evidence, formáty, součty a vazby; agent
   řeší významové rozpory. Automatické párování může být zčásti skriptové,
   nejednoznačné případy agentní.
6. Nejasnosti vzniknou jako konkrétní položky s důkazem a otázkou, nikoli jako
   obecné „potřebuji více informací“. Jedno rozhodnutí může vyřešit více položek.
7. Po odpovědi se obnoví jen dotčená práce a zneplatní odvozené výsledky,
   které se změnou podkladu přestaly platit. Hotová část se zbytečně neopakuje.
8. Výsledek je balíček originálů, přehled a seznam vyřešených/nevyřešených
   případů s odkazy ke zdrojům. Publikace a přijetí používají existující
   kontrakt Issues; nebudovat druhý systém review vedle něj.

## Co engine skutečně poskytuje

| Oblast | Doložený stav | Praktický význam |
| --- | --- | --- |
| Plný agentní krok | `runner_orchestrator.go`: RunStep řeší skutečného agenta, skills, MCP, kontejner, konfiguraci, tool stream a transcript | Agent může v jednom kroku zkoumat více souborů a používat nástroje. Není to jen jednorázové completion. |
| Izolace kroků | Každý krok dostává nový chat a renderovaný prompt, výstup je text v AgentStepResult | Předchozí práce se musí explicitně předat: inventář, odkazy, fakta, důkazy a otázky. Sdílené soubory existují, ale čerstvý chat sám nezaručuje neměnné prostředí ani determinismus. |
| Alternativní runner | `runner_llm.go`: direct LLM nemá tool loop, skills ani paměť | Jeho testy nejsou důkaz, že agent skutečně prohlédl PDF v kontejneru. |
| Mechanické kroky | script/http/transform/code CEL, kombinovatelné s agent_run | Hybridní kostra už existuje; nový obecný engine není potřeba. |
| Kontroly | `executor_validate.go`: struktura JSON, délka a obsah; `outcomes.go`: hodnoticí agent | Lze kontrolovat strukturu i význam, ale jejich výsledek musí být pravdivě vynucen. |
| Výstup běhu | `runs.go`, `runs_outcome_test.go`: technical status a outcome jsou oddělené | Dokončené vykonávání může mít FAILED/NEEDS_HUMAN outcome. Nesmí se slít do zeleného Completed. |
| Trvalý stav | `state_store.go`: key/value po rutině a schedule, last-write-wins | Základ pro watermark existuje; není to hotový transakční registr jednotlivých zpracovaných dokladů. |
| Integrace | `crew_capabilities.go`: katalog nástrojů a integrací; integrační/credential/resource gates | Máme na čem postavit přenositelné zdrojové adaptéry; připojená integrace není ověřená úplnost získaných dat. |

## Potvrzené mezery a konkrétní priority

### P0 — nepředat neověřenou práci jako hotovou

`executor.go` v outcomes větvi při chybě graderu emituje validation_failed,
ale vrací workerův výstup s nil error. `TestExecutor_Outcomes_Paths` obsahuje
test `grader infrastructure error returns worker output`, který přímo očekává
COMPLETED. Potřebujeme oddělit povinnou kontrolu od informativního hodnocení;
u povinné kontroly chyba/verdict-nepřišel blokuje předání nebo jde k člověku.
Změna nesmí bez rozmyslu změnit sémantiku všech dosavadních advisory graderů.

API `ListRunRecords` už vrací outcome, ale `hooks/use-pipeline-run-records.ts`
jej nemá v typu a `routine-card-detail.tsx` vykresluje barvy podle status.
Test `TestMarkTerminal_NoOutcomeReported_DefaultsToFailedWithReason` výslovně
ukazuje status completed a outcome FAILED zároveň. Primární klientský výsledek
musí vycházet z obou polí. Není potřeba pro každý odstín vytvářet nový DB enum.

### P0/P1 — položky dávky a řešení výjimek

`executor_foreach.go` při první chybě ruší ostatní položky. Vnitřní kroky běží
s depth+1 bez běžného top-level checkpointu; WAITING uvnitř body vrací chybu
„foreach body cannot park on a wait step“. Nativní foreach tedy není hotový
mechanismus „97 dokladů hotovo, tři čekají, restart nic neopakuje“.

Pro 1.0 lze nejprve použít trvalý manifest položek a sběr výjimek s jedním
top-level waitpointem po zpracování dávky. Než rozšířit obecný foreach engine,
ověřit tuto užší variantu. Chyba dokumentu a nedostupnost celého zdroje mají
jinou politiku: první lze izolovat, druhá může znamenat neúplný vstup celé dávky.
Dokumentům dát stabilní identitu, stav, pokusy a odkazy na důkazy. Nesmí to být
pouhý JSON soubor bez definovaného chování při souběhu a pádu uprostřed zápisu.

### P1 — explicitní předání dokumentů a výsledků

`AgentStepRequest` nese Prompt, `AgentStepResult` Output string. `InputSpec`
má obecné JSON typy; standardní RoutineRunInputsDialog není hotový dokumentový
picker. `OutputSpec` je popisné rozhraní bez obecného vynucení typu souborového
výstupu. Navrhnout malý společný formát artefaktu: ID, původní ID/účet/složka,
název, MIME, hash/revize, odkaz na čitelný obsah a rozsah přístupu. Extrakce má
držet odkaz na originál a stránku. Velké PDF nepředávat jako obrovský prompt.

Agentní zadání má obsahovat cíl, seznam podkladů, známá fakta, kontrolní pravidla,
povolené akce a požadovaný výstup včetně evidence. Ne každý model/CLI umí stejný
způsob práce s PDF a obrazem: ověřit potřebné OCR/vision/reader nástroje na reálné
konfiguraci. Závěr „agent četl dokument“ nesmí vycházet jen z jeho tvrzení.

### P1 — opravdu přenositelné integrace

Recept oddělí získání podkladů od jejich zpracování. Vstupní adaptéry pro různé
poskytovatele normalizují stejný malý objekt dokumentu; nezačínat univerzálním
schématem všech účetních systémů. Vázat konkrétní připojený účet/složku a práva,
ne jen „gmail je připojen“. Zachovat author-crew scope, přenos do jiného týmu
musí vyžadovat přemapování zdrojů a novou kontrolu schopností.

`pipeline_integrations_gate.go` při resolver chybě fail-open, při Composio
wildcard propouští deklarované integrace. Toto není důkaz konkrétní bezpečnostní
díry, ale důvod nezobrazit „zdroje ověřeny“ jen podle průchodu gate. Readiness
musí umět unknown a skutečné read-only ověření zvoleného zdroje. Runtime musí
řešit stránkování, expirovaný přístup, rate limit, duplicitní webhook, kurzor
a opožděný doklad. „API vrátilo prázdno“ není vždy „žádné nové podklady“.

Vzor structuredContent/outputSchema a resource links již poskytuje MCP;
využít je tam, kde konektor podporuje, jinak adaptovat textový výsledek.
Tool annotations jsou pouze hints, ne vynucené oprávnění:
https://modelcontextprotocol.io/specification/2025-11-25/server/tools
https://modelcontextprotocol.io/specification/2025-11-25/schema

### P1 — model, kvalita, rozpočet a čekání na volného agenta

`runner_orchestrator.go` aplikuje req.Model, ale ne req.Adapter: změna modelu
není automatické přepnutí na jiného poskytovatele. Pro cross-provider práci
použít správně nakonfigurované agenty, nikoli náhodné model ID na starém CLI.
`outcomes.go` hodnotitele směruje na fast tier; hloubka kontroly musí být volbou
úkolu a důležitosti, nikoli univerzální předpoklad „hodnocení je levná práce“.
Grader prompt dostává worker output a run inputs, ne automaticky všechna data
předchozích kroků; přístup ke zdrojovým důkazům musí být explicitní.

Jeden agent sdílí AgentRunLock s Chatem a Issues. Busy v rutině vrací chybu,
kterou řeší retry politika; foreach implicitně spouští položky paralelně.
Více dokumentů pro stejného agenta tak může kolidovat i bez skutečného selhání
úlohy. Pro pilot použít omezený souběh a ověřit srozumitelné čekání ve frontě.

Výběr levnějšího modelu vyhodnocovat pomocí očekávaných faktů a edge cases,
nikoli podobnosti stylistiky. Eskalovat pouze nejasné položky, omezeně.
Řídit počet položek/pokusů, celkový čas a cenu; zvlášť prověřit souběžné dávky,
protože foreach sumuje náklady z položek až do výsledku vnějšího kroku.
Nejde o živě změřené překročení budgetu, nýbrž otevřený testovací požadavek.

## Paměť a učení

Oddělit důkazy pro jeden běh, trvalou evidenci zpracovaných dokumentů a
schválená klientská pravidla. Rozhodnutí „tento dodavatel je hosting“ může
zlepšit další měsíc, ale jen ve správné firmě a s historií změny. Jednorázová
výjimka se nesmí bez označení změnit na trvalé pravidlo. Originály účetních
podkladů nepatří do krátkodobé agentní paměti, která se může prořezávat.
Email/PDF je zdroj dat, nikoli oprávnění měnit recept či provádět externí akce.

## Výsledek a klientské UI

Detail ukazuje účel, období a zdroje → průběžný výsledek → nejasnosti →
ověření → předání; pak plán/kalendář a technické detaily. Příklad ilustračního
souhrnu: „100 položek: 92 připraveno, 5 duplicit, 3 potřebují doplnění“.
Tyto kategorie se nesmějí překrývat bez vysvětlení a čísla musejí být odvozena
z uložené evidence. Procento počtu agentních kroků neměří hodnotu ani kvalitu.

New routine: cíl → tým → zdroje a místo výstupu → kdy → jak řešit nejasnosti
→ čitelný návrh → skutečný zkušební běh → aktivace. Klient nenavrhuje DAG,
nevybírá parser PDF a nepíše cron. Recept tvoří Lead na základě capabilities.
Užití modelů, fallbacky a nástroje zůstanou pokročilá nastavení.

## Pilot a přejímací testy

Žádná skutečná účetní data nejsou pro první pilot potřeba. Připravit syntetickou
sadu dokumentů s očekávaným inventářem, vazbami, výstupy a výjimkami.

| Scénář | Přejímací podmínka |
| --- | --- |
| PDF s textem, sken a více stránek | Správný obsah i zdrojová stránka; nečitelná část označena |
| Nesprávný název souboru | Rozhoduje obsah, rozpor je dohledatelný |
| Duplicitní soubor z více zdrojů | Jedna položka, více proveniencí, bez dvojího započtení |
| Dvě podobné faktury / opravená verze | Neztratit odlišnou položku jen podle názvu; zachovat revize |
| Chybějící doklad / více možných plateb | Konkrétní otázka a důkazy; žádné vymyšlené spárování |
| Chyba zdroje / stránkování / 429 | Přiznaná neúplnost; žádné falešné „nic nového“ |
| Opožděná příloha pro minulé období | Správné doplnění bez opakování hotových dokladů |
| Grader padne nebo vrátí neplatný JSON | Povinná kontrola neprojde; výstup se nepředá jako ověřený |
| Rozhodnutí klienta a obnovení | Jen dotčené položky, stejná identita běhu/případu |
| Změna podkladu po schválení | Zastaralé schválení nelze použít na jiný obsah |
| Restart uprostřed dávky | Hotové položky zachovány, žádný dvojí externí zápis |
| Stop / timeout | Zastavit agentní i skriptovou práci; žádné pozdní oživení |
| Souběžný Chat/Issue stejného agenta | Čekání/retry bez kolize tmux a falešného business selhání |
| Externí instrukce uvnitř PDF/emailu | Nezmění recept, cíl předání ani povolené akce |
| Dva poskytovatelé stejného vstupu | Stejné zpracování po normalizaci, odlišné jen source binding |
| Dva skutečně dostupní výkonoví vykonavatelé | Porovnat fakta, úplnost, počet zásahů, cenu a čas; ne pouze JSON validity |

Pro testy jsou měřítka: pokrytí vstupních položek, správnost polí/vazeb,
podloženost závěrů, počet nesprávně automaticky přijatých položek, čas člověka,
úspěšné obnovení a cena. Předem stanovit, které chyby jsou blokující; hodnoty
si neodvozovat z výsledku testu. Rozsáhlý marketplace ani univerzální editor
workflow nejsou podmínkou tohoto pilotu.

## Ověření auditu

Přečteny aktuální runnery, foreach, validation/outcomes, routine state,
capabilities, integrační gate, předávání vstupů a výstupní projekce.
`go test ./internal/pipeline -run 'Test.*(Foreach|Outcomes|Tier|RoutineState|SystemPromptInputs)' -count=1 -timeout=5m`
prošel; mimo jiné reprodukuje existující non-fatal grader chování, ne jeho opravu.
Samostatně prošel `go test ./internal/pipeline -run '^TestMarkTerminal_NoOutcomeReported_DefaultsToFailedWithReason$' -count=1 -v`;
test potvrzuje současnou kombinaci technical completed + outcome FAILED.
Neproběhlo nové živé čtení PDF agentem, připojení klientského zdroje ani celá
release testovací sada. Nebyl měněn běžící backend, Issues ani cizí WIP.
