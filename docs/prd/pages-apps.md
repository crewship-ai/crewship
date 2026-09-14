**Pages jako interní aplikace — návrh po ověření kompatibility**

> Historický průzkum z 2026-09-08, nikoli aktuální stav dodávky. Aktuální rozsah,
> implementaci a YAML přenos určuje [Pages Apps v1](pages-apps-v1.md), výsledky
> testů a překážky nasazení [handoff](pages-apps-handoff.md). Starší doporučení
> Refine/Puck níže nejsou součástí zvoleného v1 stacku.

Stav: návrh, nikoli implementované rozšíření Pages. 2026-09-08. Navazuje na [současné Pages](pages.md). Zadání: agent vytváří plně přizpůsobitelné firemní stránky s daty a operacemi Crewshipu; zákazník používá a upravuje výslednou aplikaci bez nutnosti znát framework, YAML nebo provoz kontejnerů.

**Aktuální implementační návrh:** [Pages Apps: architektura](pages-apps-architecture.md) sjednocuje zvolený React/TypeScript/Vite směr, projektový lifecycle, Git, SDK, izolaci, worker infrastrukturu, výkonové rozpočty a podmínky vydání. Tento dokument uchovává průzkum a rozsah předchozího experimentu; při rozdílu doporučení platí novější architektonický návrh.

**Produktový princip: plně vlastní aplikace je plnohodnotná výchozí cesta.** Katalog komponent je nabídka pro rychlejší tvorbu, nikoli povinné omezení. Agent smí vytvořit celou vlastní stránku, navigaci, styl a interakce. React je podporovaný výchozí starter, Refine je volitelná pomůcka pro datové aplikace a Puck volitelný editor. Bezpečnostní izolace se vztahuje na pravomoci kódu, nikoli na seznam dovolených vizuálních bloků.

**Doporučené rozhodnutí po upřesnění tvorby v chatu.** Zachovat současné panelové Pages a jejich producenty. Vlastní aplikace vytváří agent přímo jako TypeScript/React projekt. Pro datové aplikace použít jako doporučený starter Refine Core + shadcn/Tailwind + Vite, připojený přes společný datový a akční kontrakt Crewshipu. Puck přidat jako volitelný vizuální editor podporovaných bloků. json-render a amis zatím nezavádět. Každá Page má mít verzovatelný projekt spravovaný Crewshipem a operaci „Zkontrolovat aplikaci“. Git integrace, Refine integrace a kontrolní operace jsou návrh; níže uvedený starší experiment je neověřoval.

Toto zpřesnění mění původní prioritu json-render: hlavním požadavkem je plná aplikace tvořená agentem v chatu, nikoli generování omezeného JSON UI jako primárního výstupu. Technické výsledky předchozího experimentu dále platí ve svém uvedeném rozsahu.

Rozsáhlejší průzkum trhu teď není hlavní chybějící práce. Je potřeba integrační pilot s reálnou routine, identitou uživatele a izolovanou aplikací. Níže popsaný technický experiment ověřil pouze soužití rendererů a sdílení jednoduché React komponenty; není takovým integračním pilotem.

**Co oproti původnímu doporučení zpřesňujeme.**

- Zákazník nevybírá „json-render versus Puck versus React“. Výchozí tok je „popiš aplikaci → náhled → uprav → publikuj → sdílej“. Zdrojový kód je pokročilá možnost.
- Kompatibilita závislostí, komponent, dokumentů, interakcí a provozního rozhraní jsou různé závazky. První dva byly omezeně ověřeny; univerzální přenositelnost dokumentů neslibujeme.
- Vlastní komponenta může publikovat typovaná nastavení pro texty, barvy, filtry nebo layout. To umožňuje běžné úpravy bez převádění její implementace do vizuálního schématu. Agent/vývojář nadále mění vnitřní logiku.
- „Všechno, co umí API“ je cíl pokrytí funkcí, nikoli plošné oprávnění každé aplikace. Veřejné a interní aplikace mají odlišný rozsah dat a operací.
- Publikace UI, schválení oprávnění a provedení konkrétní operace jsou tři různé události. Politika určuje, kdy je potřeba lidské schválení; běžné opakované úkony nemají být zaplavené dialogy.
- Vytvořit aplikaci jednou nestačí. Vlastník, revize, souběžné změny, kompatibilita závislostí, provozní chyby a obnova jsou součást první produkční verze.

**Ověřený základ Crewshipu.** Checkout při analýze: `005470ed7157ea2504c77484617ace97b1daeb4c`. Veřejná dev2 byla dostupná, ale nepřihlášený Playwright skončil na přihlášení a API Pages vrátilo 401. Konkrétní neveřejné stránky ani shoda nasazené verze s checkoutem nebyly ověřeny.

Současná definice je YAML/JSON se známými panely, vlastnící crew, producentem, SLA, mřížkou a záložkami. Producent posílá validovaný payload. Panelové akce se dohledávají podle ID v uloženém specu a jejich `call` zařazuje routine do `pending_runs`. Script runner vykonává deterministický skript v crew kontejneru bez nutného LLM volání. Aktualizace používají WebSocket a invalidaci React Query. Tuto infrastrukturu znovu nestavět. Zdroje: [spec](../../internal/pages/spec.go), [akce](../../internal/api/pages_actions.go), [runner](../../internal/pipeline/runner_script.go), [realtime hook](../../hooks/use-pages.ts).

Současné `embed.v1` vybírá operátorem schválený externí zdroj. Není to distribuce agentem sestavené aplikace ani SDK most k datům a akcím. Jeho sandbox nemá být prostě rozšířen o volná oprávnění, aby cizí web „nějak fungoval“. [Policy](../../internal/pages/embed.go), [renderer](../../components/features/pages/panels/embed-panel.tsx).

**Výsledek technického experimentu, nikoli pouze čtení peerDependencies.**

V odděleném dočasném projektu se společně nainstalovaly React/React DOM `19.2.8`, `@json-render/core` a `@json-render/react` `0.20.0`, `@puckeditor/core` `0.23.0`, Zod `4.4.3`, TypeScript `6.0.3` a Vite `8.2.2`. Finální ověření použilo `--frozen-lockfile --strict-peer-dependencies --ignore-scripts`, Node `24.14.0` a explicitně připnutý pnpm `10.11.0`, stejně jako repo. Předběžná instalace běžela přes pnpm `10.32.1`; po připnutí projektového packageManageru proběhl znovu celý instalační, build a browser průchod.

Prošel TypeScript check s `skipLibCheck`, produkční Vite build a osm kontrol v Chromium:

1. Samostatný React vykreslí sdílený `RunPanel` a lokální akci.
2. json-render vykreslí tutéž komponentu a používá společný React context.
3. Puck `Render` vykreslí tutéž komponentu a používá stejný context.
4. Výslovný adaptér převede jeden statický `RunPanel` z JSON specu do Puck dat; vykreslení a lokální akce fungují.
5. Nativní json-render `$bindState` mění vstup a druhá komponenta změnu přečte přes `$state`.
6. Adaptér odmítne nepodporovanou složitější strukturu namísto tichého zahazování obsahu.
7. Základní akce funguje při šířce 390 px bez horizontálního přetékání dokumentu.
8. Finální průchod nemá nezachycené chyby prohlížeče.

První browser průchod odhalil chybějící `ActionProvider` i pro katalog bez deklarovaných akcí. Po doplnění provideru test prošel. Praktický důsledek: renderer potřebuje správně integrovaný runtime, samotné přeložení importů nestačí.

Experiment neověřuje skutečné API, kontejnery, autentizaci, sandbox, editor Puck/drag-and-drop, integraci Next.js, adaptaci dnešních panelových komponent, jiné prohlížeče, úplnou přístupnost ani produkční výkon. Simulovaná akce pouze zvyšuje lokální počítadlo. Typové deklarace závislostí nebyly plně kontrolované kvůli `skipLibCheck`.

Výchozí kombinovaný build navíc vypsal upozornění na JS chunk přes 500 kB. To není srovnání rychlosti knihoven ani měření reálného načítání; je to důvod držet editor mimo čtecí bundle a změřit skutečně načítané prostředky v pilotu.

Reprodukovatelné zdroje a lockfile jsou přiloženy k výstupu této relace jako archiv; dočasná cesta relace je `/tmp/crewship-pages-research-20260908/compatibility`. Nejde o build závislost Crewshipu. Instalované hlavní balíčky byly rovněž porovnány s aktuálními metadaty veřejného registru.

**Jakou kompatibilitu slibovat zákazníkům.**

| Vrstva | Co lze realisticky nabídnout | Co neslibovat |
|---|---|---|
| React verze | Podporovanou otestovanou kombinaci závislostí | Že každý React balíček funguje jen proto, že má široký peer rozsah |
| Komponenty | Sdílené React komponenty s malým adaptérem pro zvolený renderer | Přímé sdílení komponent React ↔ Vue/Svelte |
| Datové kontrakty | Stejné DTO, schémata vstupů a ID operací přes SDK/protokol | Sdílení React contextu nebo hooků mezi iframe |
| Šablony | Verzovaný formát zvoleného rendereru a migrace známých verzí | Univerzální JSON jako náhradu všech UI jazyků |
| Převod | Explicitní převod podporované podmnožiny, jinak vysvětlené odmítnutí | Libovolný TSX → plně editovatelný vizuální dokument |
| Ostatní frameworky | Později statický balíček komunikující stejným protokolem | Automaticky shodný vzhled, editor a podporu každé knihovny |
| Backend skriptů | Stejné typované vstupy/výsledky pro dostupné interpretery | Že jazyk frontendu určuje jazyk kontejnerové úlohy |

Puck data a json-render spec jsou odlišné formáty s odlišnými pravidly. Ukládat jeden jasný zdroj pravdy pro konkrétní revizi stránky. Export kódu je možný, ale json-render dokumentace výslovně počítá s generátorem specifickým pro projekt; nejde o hotový univerzální převod. [Puck Data](https://puckeditor.com/docs/api-reference/data-model/data), [json-render code export](https://json-render.dev/docs/code-export).

Větší vlastní aplikace může mít jednu obrazovku nebo vlastní interní navigaci. Musí jít vložit i jako blok do strukturované stránky. Rám stránky může upravovat člověk vizuálně, zatímco vnitřek vlastního bloku mění agent. Nikde nepředstírat, že viditelné nastavení bloku umožňuje editovat libovolný jeho vnitřní element.

**Ďáblův advokát: kde návrh může selhat a jak to poznáme.**

| Riziko | Konkrétní selhání pro zákazníka | Odpověď návrhu / důkaz před vydáním |
|---|---|---|
| Vytváření rychlejší než údržba | Každá crew má jinak fungující aplikaci, kterou nikdo neumí opravit | Vlastník, podporovaný starter, revize SDK, přehled chyb a aktualizace přes ověřený build |
| Dva rovnocenné editory | Agent upraví kód, vizuální editor jeho změny zahodí | Jeden zdroj pravdy na revizi; explicitní custom bloky a jednosměrné přechody |
| Přehnaná obecnost | Zákazník řeší framework, balíčky, datové bindingy a runtime | Výchozí tok přes zadání, preview a jednoduché nastavení; technické volby schované v pokročilém režimu |
| Přepis práce | Agent přegeneruje celou stránku a smaže ruční úpravy | Změny proti konkrétní revizi, patch/diff, detekce konfliktu, zachování rozepsaných formulářů |
| Drahé nebo pomalé kliknutí | Start čeká na agentovo rozhodnutí nebo neviditelně probouzí runtime | Běžná operace je deterministická; okamžitý receipt, srozumitelný stav fronty a probouzení |
| Nepravdivý průběh | Dva lidé spustí stejnou routine a jeden vidí běh druhého | Navázat sledování na `pending_id → run_id`, ne pouze na slug routine |
| Nepravdivé Stop | UI hlásí zrušení, ale script dál mění externí systém | Rozlišit požadavek na zrušení a potvrzené ukončení; ověřit proces i podprocesy |
| Nadbytečná oprávnění | Účetní musí být manager nebo aplikace zdědí pravomoci autora | Delegovat konkrétní operaci; server kontroluje uživatele, aplikaci, parametry a cílový záznam |
| Snapshot vydávaný za databázi | Editace faktur mizí nebo graf nemá slíbenou historii | Explicitní zdroj pravdy; panelové payloady pouze jako projekce; konflikty záznamů řeší backend |
| Izolace kazí běžné úkony | Upload, download, kopírování, klávesové zkratky nebo odkazy uvnitř rámu nefungují | SDK služby pro běžné úkony; browser testy skutečného sandboxu, mobilu a klávesnice |
| Náročný self-hosting | Každá Page potřebuje DNS, port a ruční server | Jednorázová instalace hosta aplikací, automatická distribuce artefaktů a kontrola připravenosti |
| Zdánlivě snadný rollback | Vrátí se UI, ale databázové změny a platba už proběhly | Oddělit obnovu UI revize od kompenzačních operací a migrací dat |
| Těžká klientská aplikace | Jednoduchá stránka stahuje celý editor nebo blokuje Studio | Lazy-load editoru, oddělený custom runtime, měření přenosu a odezvy; fallback při pádu |
| Únik dat přes vlastní kód | Custom UI obdrží data nepovolené crew nebo je pošle ven | Serverové filtrování před předáním, omezené schopnosti aplikace, CSP a izolovaný origin |

Dvě položky mají přímou oporu v aktuálním kódu: [UI akcí](../../components/features/pages/panels/panel-actions.tsx) hledá živý běh přes `bySlug`; [script runner](../../internal/pipeline/runner_script.go) při timeoutu výslovně uvádí, že zavření attach procesu nezaručuje jeho ukončení. Nejde o nově prokázané incidenty v produkci, ale o konkrétní důvody, proč nepovažovat současný akční základ za kompletní kontrakt pro nové aplikace.

**Proklientský tok a jeho ověřitelné podmínky.**

Uživatel napíše například „Udělej účetní přehled, nahraju doklady, uvidím nespárované platby a tlačítkem je zpracujeme“. Agent připraví první verzi z dostupných zdrojů. Pokud chybí připojení, aplikace nabídne konkrétní krok k jeho nastavení; nevymyslí data a nevydává demo za skutečný stav. Uživatel dostane náhled a může upravit texty, vzhled a filtry přímo nebo další instrukcí agentovi.

Publikace vytvoří stabilní verzi. Sdílení ukáže, kdo stránku může otevřít, jaká data uvidí a které operace má povolené. Běžný zaměstnanec nemá řešit, jak se jmenuje routine, kde je script soubor a jaký kontejner se probouzí. Stav však musí být poctivý: přijato, ve frontě, spouští se prostředí, běží, čeká na schválení, dokončeno, selhalo nebo zrušení požadováno. Nefalšovat procentuální průběh úlohy, která ho nehlásí.

Editor používá standardně fixture/read-only náhled. Samotné otevření preview nesmí spustit produkční deploy nebo účetní operaci; testovací provedení je odlišitelný explicitní úkon s politikou a auditem. Náhled jako jiná role musí ověřovat skutečnou serverovou projekci, nikoli pouze skrýt prvky v DOM.

Proklientskost budeme hodnotit na úkolech, nikoli podle toho, zda je stránka hezká na screenshotu. Navržené cíle pilotu:

- Uživatel z účetnictví a uživatel ze SRE vytvoří/upraví jednoduchou aplikaci bez otevření YAML, terminálu nebo zdrojového kódu; výzkumné sezení zaznamená každé místo, kde potřebovali pomoc.
- Změna popisku, barvy a výchozího filtru je možná přímo v nastavení, pokud je aplikace deklaruje; složitější požadavek lze předat agentovi bez ztráty ostatních úprav.
- Před publikací je vidět výsledek a rozsah změny. Po odmítnutí nebo konfliktu se neztratí práce.
- Po kliknutí se okamžitě objeví lokální potvrzení práce; stav přijatý serverem je od něj odlišen. Cíle odezvy změří pilot na studeném i teplém kontejneru.
- Výsledek má srozumitelný další krok: otevřít výstup, stáhnout soubor, opravit vstupy, případně bezpečně opakovat.
- Formuláře, tabulky a hlavní operace fungují na telefonu i klávesnicí; chyby jsou spojeny s konkrétním vstupem.
- Aplikace zůstane otevřitelná po restartu crew kontejneru. Výpadek producenta ukáže stáří dat, nikoli tiché „všechno v pořádku“.
- Správce nastaví hostování custom aplikací jednou. Pokud instalace nemá připravené bezpečné hostování, produkt nabídne vysvětlený setup; nesníží izolaci a nezobrazí prázdný rám.

**Architektura potřebná pro tento zážitek.**

| Vrstva | Zodpovědnost |
|---|---|
| Crewship Page | Identita stránky, navigace, metadata, vlastník, revize a sdílení |
| Definice UI | Současné panely, zvolený strukturovaný formát nebo odkaz na custom artefakt |
| Katalog komponent | Podporované komponenty a jejich typované props; metadata nastavení custom bloků |
| Datové služby | Snapshoty, autorizované dotazy, stránkování a realtime s kontrolou přístupů |
| Akční služby | Registrovaná operace, validace, idempotency, policy a existující queue/runner |
| Runtime aplikace | Oddělený origin/frame a verzovaný hostitelský protokol |
| Build a distribuce | Izolované sestavení, připnuté závislosti, immutable artefakt, autorizované načtení a cache |

SDK musí oddělit čtení, mutace, spouštění dlouhé práce a místní UI stav. OpenAPI pomůže s typy a transportem; nelze z něj automaticky odvodit bezpečné delegování operací, doménovou autorizaci nebo spolehlivou kompenzaci. Frontendové skrývání není ACL. Operace nese identitu volajícího a konkrétní revizi povoleného kontraktu; server ověřuje i cílové objekty.

Vlastní aplikace má běžet z předem sestaveného balíčku. Produkční otevření stránky nemá stahovat nové verze balíčků ani spouštět build. Sestavovací prostředí nemá provozní credentials. Privátní artefakty, přístup k assetům z opaque origin, CSP, CORS a platnost session musí řešit pilot společně: standardní výstup Vite ve stávajícím embed iframe není automaticky hotové řešení.

Cross-origin izolace umožní později připojit i Vue/Svelte aplikaci, protože přes hranici přecházejí zprávy. Tento protokol nesdílí React context. Sandbox může komplikovat soubory a navigaci, proto host nabízí konkrétní služby. Druhý origin musí být provozní vlastností instalace; zákazník nemá ručně registrovat URL každé vlastní Page. Cizí externí weby nadále zůstávají samostatným embed režimem.

Přímé React hooky z dnešního Studia nelze bez adaptace kopírovat do custom iframe: spoléhají na workspace context, autentizovaný transport a lokální providers. Komponentová knihovna pro Pages proto oddělí vzhled od transportu; obě autorské cesty využijí stejný kontrakt dat a operací přes své adaptéry.

Knihovny před 1.0 držet za malým adaptérem a migrovat uložené dokumenty explicitně. Každá publikovaná aplikace připne podporovaný runtime; automatický upgrade nesmí současně rozbít všechny firemní aplikace. Samotné připnutí ale není dlouhodobá strategie bezpečnostních aktualizací: potřebujeme testovatelný upgrade a ukončování podpory starých verzí. [json-render migrace](https://json-render.dev/docs/migration), [Puck migrace](https://puckeditor.com/docs/integrating-puck/data-migration).

**Rozsah první implementace a brány před rozšířením.**

1. Integrační pilot jedné SRE aplikace: vlastní React layout, filtrovaná tabulka a explicitní akce spouštějící read-only diagnostickou routine v kontejneru. Reálný receipt, vlastní run, výsledek a export artefaktu. Publikovaná UI revize přežije restart kontejneru.
2. Autorizace a provozní kontrakt: dvě crew a dvě role, omezená delegace, odmítnutí cizí akce/parametrů, odebrání oprávnění při otevřené stránce, skutečná izolace, cold start, retry a souběžné běhy téže routine. Zrušení nesmí tvrdit více, než runner zaručí.
3. Autorský pilot: agent v existujícím chatu vytvoří React/Refine aplikaci, uloží projektovou revizi, sestaví náhled a provede definované kontroly. Člověk upraví běžné vlastnosti; na podporovaných blocích ověřit Puck jako volitelný editor. json-render přehodnotit jen při konkrétní potřebě omezeného generovaného UI, nikoli jako další povinnou vrstvu.
4. První firemní šablony: SRE a účetní přehled s výslovným zdrojem dat. Rozšířit formuláře, soubory a detail záznamu; finanční operace a produkční deploy přidat až s odpovídajícím doménovým kontraktem.

Do první verze nezahrnovat univerzální databázový návrhář, automatický obousměrný převod všech frameworků, vlastní obecný programovací jazyk ani kompletní tržiště knihoven. Plná React aplikace zůstává podporovaným cílem; omezení se týká dodatečných nástrojů a převodů, nikoli možnosti vytvořit vlastní vzhled a interakce.

První produkční verze se může označit za použitelnou až po prokázání datové izolace, poctivého lifecycle akcí, publikace/obnovy, řešení souběžných úprav a základních uživatelských úkolů. Dosavadní experiment opravňuje pokračovat v integraci, nikoli tvrdit, že celé řešení je již kompatibilní a maximálně proklientské.

**Konkrétní volba balíčků a jejich úloh.**

| Součást | Rozhodnutí | Důvod |
|---|---|---|
| React + TypeScript + Vite | Základ custom aplikací | Agent tvoří skutečný webový projekt; publikovaný výstup jsou statické artefakty |
| `@refinedev/core` | Doporučený starter datových aplikací | Čtení/editace záznamů, stránkování a vazby na datové a realtime providery; jednoduchá Page jej nemusí importovat |
| shadcn/Radix + Tailwind | Sdílené UI komponenty a designový základ | Volný vzhled při znovupoužití existujících komponent Crewshipu |
| Recharts | Výchozí grafová knihovna | Je již v repozitáři; další vizualizační balíčky přidat podle konkrétní potřeby |
| `@puckeditor/core` | Volitelný editor bloků po pilotu základního runtime | Umožnit ruční úpravy podporovaných layoutů a props, aniž by zákazník otevíral kód |
| `@json-render/*` | Odložit | Není nutné, když agent přímo vytváří React; nepřidávat druhý povinný formát dokumentu |
| amis | Nezavádět do prvního řešení | Alternativní low-code systém, který zde nepřináší potřebnou další roli |
| Pages SDK / Refine provider | Doplnit integrační vrstvu Crewshipu | Připojí framework ke skutečným datům, operacím a autorizaci; nejde o nový UI jazyk |

Refine Core není hotový vizuální editor ani grafický styl. Je headless, jeho příklady získávají vzhled z konkrétní UI integrace a layoutu. Puck zase není datový/backendový framework. Jejich kombinace je architektonicky možná: podporovaný React blok v Pucku může uvnitř použít Refine. Musí ale mít správné providery a jasně oddělenou konfiguraci od živých dat. Přes iframe se React context nesdílí; v custom aplikaci má Refine vlastní providery používající Pages protokol. Společná integrace Refine + Puck zatím nebyla testována. [Refine UI](https://refine.dev/core/docs/guides-concepts/ui-libraries/), [datové providery](https://refine.dev/core/docs/data/data-provider/), [Puck](https://puckeditor.com/docs).

Nahradit transport Refine adaptérem na Crewship, ne otevírat z iframe volný přístup pod tokenem Studia. Mapovat čtení, filtry, řazení, stránkování a mutace na podporované operace. Dlouhá úloha vrací receipt a používá akční služby; nevydávat ji za synchronní CRUD editaci. Realtime provider musí mít stejné filtrování oprávnění a zneplatnění cache jako ostatní klienti. Refine autorizace v UI doplňuje, ale nenahrazuje serverovou autorizaci Crewshipu.

**Git jako historie projektu každé Page.**

Každá Page dostane projektovou identitu nezávislou na zobrazovaném názvu/slug. Crewship bude spravovat Git historii zdroje; zákazník nemusí mít GitHub účet ani ručně vytvářet repozitář. Pro první implementaci preferovat oddělený interní repozitář pro projekt aplikace: jednodušší izolace čtení zdroje a souběžné práce agentů. Pokud bude později podporován společný repozitář, musí sdílet odpovídající důvěru a oprávnění; adresář není hranice přístupu v Gitu. Externí remote je volitelná integrace.

Verzovat kód UI, manifest, případný Puck dokument, konfigurační schémata, projektové skripty, testy, syntetická testovací data a lockfile. U současných panelových Pages lze verzovat jejich existující YAML bez převodu do Reactu. Pro živá data, faktury, credentials, uživatelské formuláře, provozní logy a sestavené balíčky zůstává odpovídající databáze či úložiště artefaktů. Každý panelový push nevytváří Git commit.

Git je zdroj uložených projektových revizí; DB drží index stránky, provozní metadata, aktivní publikaci a případné rozpracované buffery. Editor i agent zapisují přes jednu projektovou službu, která atomicky vytvoří revizi s kontrolou očekávaného výchozího commitu. Nepřipustit dvě nezávisle editovatelné pravdy v DB a v Gitu. Stávající rollback/versions API musí dostat kompatibilní mapování na tento model; u existujících Pages zavést Git backing postupnou migrací.

Agent pracuje v izolované pracovní kopii/větvi pro konkrétní změnu. Koherentní změna nebo explicitní uložení vytvoří checkpoint, nikoli každý stisk klávesy. Současná práce člověka a agenta musí vyvolat detekovaný konflikt, ne přepis posledním zápisem. Uživatel vidí autora, srozumitelný popis změny, náhled a možnost návratu; nemusí vidět Git terminologii.

Publikace obsahuje minimálně projektový commit, digest sestaveného artefaktu, verzi SDK a revizi povolených operací. Commit sám neznamená nasazení a větev main není automatický ukazatel živé aplikace. Po kontrolách se atomicky přepne aktivní publikace. Rollback použije původní uchovaný artefakt, nikoli nový build starého kódu s aktuálními závislostmi.

Sdílená routine může být změněna mimo Git projektu. Publikace proto musí připnout podporovanou revizi routine/kontraktu, nebo výslovně uvést, že používá aktuální sdílenou službu a její kompatibilitu průběžně kontrolovat. Verze frontendu bez verze akčního kontraktu sama nezaručuje reprodukovatelnost. Git rovněž nevrací vedlejší účinky již provedených operací a lokální repozitář nenahrazuje zálohování databáze a artefaktů.

**Tlačítko „Zkontrolovat aplikaci“.**

Zákazník spustí jednu kontrolní úlohu. Ta pro konkrétní publikaci provede předem definované testy a agent v chatu vysvětlí výsledky, při selhání případně provede cílenou diagnostiku. Agent musí vycházet z výsledků nástrojů, ne z pouhého přečtení vlastního kódu. Platformní kontrolní sada zůstává nezávislá na testech, které agent napsal zároveň s aplikací.

| Kontrola | Co má výsledek skutečně dokládat |
|---|---|
| Dostupnost publikace | Správný commit/artefakt, načtené assety, otevřená aplikace bez zásadní browser chyby |
| Přístup a integrace | Dostupnost povolených zdrojů pro explicitní identitu kontroly; secrets se nezobrazují agentovi ani reportu |
| Data | Čerstvost vůči SLA, validita schématu a rozdíl mezi prázdným výsledkem a chybou |
| Operace | Existující routine, kompatibilní vstupy, povolení a stav runtime; skutečné spuštění jen definované read-only diagnostiky nebo odděleného testovacího scénáře |
| Uživatelská cesta | Připravené browser scénáře: načtení tabulky, filtr, otevření detailu, testovací formulář, podle deklarovaného pokrytí |
| Provoz | Dostupné výsledky posledních běhů a relevantní chyby s časem a rozsahem |

Rutinní kontrola již publikované verze nemusí pokaždé instalovat balíčky a přestavovat zdroj. Build/typecheck spouštět při změně zdroje nebo při diagnostice, která je vyžaduje. Kontroly mají timeout, limity prostředků a cenu/budget pro případnou agentní diagnostiku. Stejnou úlohu lze později naplánovat po publikaci nebo periodicky přes stávající orchestrace.

Report obsahuje testovanou revizi, identitu/rozsah, čas, důkazy, výsledky jednotlivých kontrol a neotestované oblasti. Stav „neověřeno“ nebo „přeskočeno“ není zelený úspěch. Úspěch kontroly nevydávat za důkaz správnosti celého účetnictví či všech uživatelských cest. Kontrola se nepovyšuje na identitu autora aplikace jen proto, aby prošla.

„Zkontrolovat“ nezahrnuje automatický deploy, rollback, platbu ani opravný zápis. Nález může nabídnout „Opravit s agentem“ ve stejném chatu: agent vytvoří opravnou větev/revizi, ukáže změnu a náhled, zopakuje relevantní kontroly a publikuje podle nastavené politiky. Zelený diagnostický report sám není autorizace pro změnu produkce.

**Tvorba a péče z existujícího chatu.**

Zachovat existující přímou tvorbu Pages agentem jako hlavní vstupní bod. Rozšířit ji o práci s projektem, očekávanou revizí, sestavením, náhledem, kontrolním během a publikací; nevytvářet paralelní chatbot. Agent může použít panelovou šablonu nebo React/Refine starter podle potřeby. Uživatel řekne například „Přidej účetní stránku“, „Uprav sloupce“, „Zkontroluj, proč se neaktualizuje“ nebo „Vrať předchozí verzi“.

Každá Page v UI nabízí otevření souvisejícího chatu, úpravu, historii a kontrolu. Z chatu vede odkaz přímo na náhled nebo publikovanou aplikaci. Změna layoutu přes Puck musí vytvořit revizi stejného projektu jako změna od agenta. Plná custom React aplikace může existovat bez Pucku; vizuální editor není podmínka jejího vytvoření.

První rozšířený pilot má prokázat celý řetězec: chat → projektová revize → build → izolovaný náhled → definovaná kontrola → publikace → změna od agenta → kontrola a případný návrat k předchozí publikaci. Git, Refine a kontrolní tlačítko nejsou touto aktualizací dokumentu implementovány.

**Plná customizace a izolace řeší dvě různé otázky.**

| Rozhodnutí | Doporučený kontrakt |
|---|---|
| Jak může aplikace vypadat | Vlastní HTML/CSS, React komponenty, grafy, navigace a klientská logika; dostupné balíčky podle build politiky instalace |
| Které komponenty musí použít | Žádné povinné Pages bloky; doporučené komponenty usnadňují tvorbu a následnou údržbu |
| K jakým datům a operacím má přístup | Explicitní API/SDK oprávnění ověřená serverem, včetně povolených externích integrací |
| Kde běží klientský kód | Oddělený origin a odpovídající browser sandbox; neprivilegovaný kód nemá relaci či DOM hlavního Studia |
| Kde běží backendová práce | Existující crew runtime přes routines/operace; build a diagnostika mají vlastní omezené prostředí |
| Co je garantovaně podporované | Ověřený React/Vite starter a Pages protokol; jiné statické webové balíčky mohou následovat po ověření |

Není nutné používat Puck ani json-render, aby agent mohl vytvořit plně vlastní Page. Neexistuje důvod přidávat je do každého runtime. Refine rovněž není povinný host celé platformy: aplikaci s vlastním interaktivním diagramem může stačit React a vhodná knihovna. Podpora dalších frameworků znamená stejný kontrakt publikovaného webového artefaktu a komunikace; ne univerzální sdílení komponent ani automatickou kompatibilitu všech balíčků. [Refine headless UI](https://refine.dev/core/docs/guides-concepts/ui-libraries/), [Vite](https://vite.dev/guide/).

Tento návrh první verze podporuje plně vlastní frontend s řízenými backendovými operacemi. Libovolný trvale běžící Node/Python webový server, SSR a vlastní databázová služba jsou další provozní režim: potřebují lifecycle, směrování, identitu, limity a zálohy. Neoznačovat statický aplikační runtime za automatický hosting jakéhokoli full-stack projektu.

Izolaci zachovat i pro aplikace vytvořené vlastním agentem: chybná aplikace nebo její závislost nesmí převzít účet uživatele ve Studiu či přístup jiné crew. Samotný iframe není celý bezpečnostní model; řeší se společně se sítí, CSP, autorizací a distribucí artefaktů. Úplná volnost vzhledu tuto hranici nijak nevyžaduje odstranit. [Browser iframe](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe).

**Co bude součástí instalace: SQLite, Git a artefakty.**

SQLite je již vestavěná knihovna používaná Go serverem (`modernc.org/sqlite` v [go.mod](../../go.mod)); nevyžaduje samostatný databázový server ani binárku `sqlite3`. Git je formát a sada nástrojů pro projektovou historii, ne další povinný trvale běžící server. GitHub/GitLab ani Gitea nejsou pro lokální verzování potřebné.

Aktuální finální [Dockerfile](../../Dockerfile) instaluje `ca-certificates` a kopíruje Crewship, ale neinstaluje Git. Některé dnešní funkce Gitem pracují na hostu nebo uvnitř kontejneru; to není záruka jeho přítomnosti ve všech instalacích. V `go.mod` rovněž nyní není go-git. Nelze tedy návrh implementovat slepým `exec("git")` v produkčním Go serveru.

Pro první verzi doporučit standardní Git CLI v automaticky spravovaném nástrojovém/build prostředí. Projektová služba nabízí úzké operace pro revize, pracovní kopie a publikace; repozitáře a artefakty leží na trvalém úložišti, nikoli pouze v životnosti pracovního kontejneru. Při nedostupnosti tohoto prostředí zůstává otevření poslední publikované aplikace nezávislé; nové sestavení/revize musí poctivě ohlásit nedostupnou službu.

Alternativa pro zachování jediné hostitelské binárky: vestavěná implementace Git, například go-git. Je to existující pure-Go projekt, ale před přijetím je nutné ověřit přesně potřebné operace a interoperabilitu s Git CLI agentů. Nepředpokládat automatickou paritu všech Git funkcí. [go-git](https://github.com/go-git/go-git). Rozhodnutí o backendu projektu nepřenášet na zákazníka.

| Uložiště | Co v něm žije |
|---|---|
| SQLite | Uživatelé, oprávnění, metadata Pages, index revizí, aktivní publikace a provozní stav |
| Git repozitáře | Zdrojové soubory a projektové revize |
| Souborové/objektové úložiště | Sestavené aplikace, přílohy a další artefakty |

Zálohování a obnova musí pokrývat tyto části konzistentně, včetně vazeb publikace na uchovaný artefakt a commit. Pokud bychom chtěli pouze interní historii a rollback, lze je vyřešit i verzovanými snapshoty bez Gitu. Při zde doporučeném skutečném Git workflow ale musí Crewship zajistit Git implementaci/nástroje jako spravovanou schopnost produktu; zákazník ji nemá ručně provozovat vedle databáze.
