# Pages Apps v1 — archived implementation chronology

Historical document preserved before issue #2472 hardening. Its progress and test
claims describe earlier sessions, not current release acceptance. Current contract:
[Pages Apps v1](pages-apps-v1.md).

# Pages Apps v1 — dashboardy z kontejnerů

Stav k 2026-09-09: P0–P5 implementované; **vývojová ukázka je nasazená na dev3**:
[Operations Lab](https://crewship-dev3.unifylab.cz/pages/custom-operations).
Používá explicitní same-origin režim pro zkontrolovaný kód, nikoli produkční
procesovou izolaci. Přihlášení, skutečný React, filtrování, SDK historie a
zastavení/opětovné otevření prošly v živém Chromiu. Publikace 2 navíc ověřuje
skutečné SDK spuštění routine v Ops, stav běhu a data z kontejneru; minutový plán
je zapnutý s limitem tří po sobě jdoucích chyb. Poslední frontend build,
lint a cílené testy prošly. Závěrečný Go průchod po integraci demo seedu prošel
(139 balíčků), stejně jako celý CLI balíček po poslední ochraně existující stránky,
`go vet` a reálný Docker build/publikace vestavěné ukázky. Binárka s novým seedem
je nasazená na dev3; živé demo nebylo resetováno ani globálně reseedováno. Firefox/WebKit stop-loop
a finální produkční nasazení zůstávají otevřené.
Operations Lab je součástí demo seedu včetně exportovaného React projektu,
routine a sběrného skriptu; opakovaný seed zachovává vlastní úpravy aplikace
a stažené publikace. Běžný seed nezapíná periodický plán.
Přesné výsledky a zbývající gates: [aktuální handoff](pages-apps-handoff.md).
Tento dokument je autoritativní rozsah první dodávky. Nahrazuje rozsah širšího
[architektonického průzkumu](pages-apps-architecture.md), nikoli jeho bezpečnostní
invarianty. Základ kódu: `main` @ `676e16e45897bf6767f47156a894fb01bfb56162`.

## Společný vzhled a vztah k agentům — rozšíření 2026-09-09

Publikace **3** je nasazená na dev3: společná firemní paleta a animace. Živý
test ověřil změnu barev mezi záložkami bez restartu aplikace, reduced motion,
responzivitu a skutečné SDK spuštění kolektoru. Detaily testů a opravených
nálezů jsou v aktuálním handoffu.

Page patří crew, nikoli jedné instanci kontejneru nebo jednomu nenahraditelnému
agentovi. Agent je autor/udržovatel; zdroje a Git historie jsou v chráněném Pages
úložišti. Skripty a routines běží v kontejneru crew a zapisují data přes stávající
producentní oprávnění. Frontend čte snapshoty a spouští jen deklarované akce pod
oprávněními uživatele. Restart kontejneru ani výměna agenta nemění identitu Page.

Vzhled je nadále plně vlastní. Společný kontrakt přidává volitelnou paletu
`pages_theme` do nastavení workspace: `accent`, `background`, `surface`, `text`,
`muted`, `border`, vždy `#RRGGBB`. OWNER/ADMIN ukládá přes stávající workspace
PATCH; členové čtou pouze v rámci členství. Žádné libovolné CSS ani URL se do
nastavení neukládají. Prázdný objekt obnoví defaulty, chybějící barvy je dědí.

Studio Settings → General → Pages appearance nabízí paletu, náhled a upozornění
na nízký kontrast hlavního textu. SDK dostane pouze normalizované barevné tokeny
přes existující snapshot kanál. `usePageTheme()` je zpřístupní Reactu; automaticky
se nastaví `--crewship-page-{token}` a kontrastní `--crewship-page-on-accent`.
Nový starter a Operations Lab tokeny používají. Vlastní aplikace je může záměrně
přepsat nebo ignorovat. Starší artefakty bez podpory tokenů vyžadují úpravu zdrojů
nebo nový build s aktuálním SDK; nelze slíbit přebarvení libovolného HTML/CSS.

Uložení vysílá `workspace.updated`; otevřené Pages obnoví společný workspace
snapshot, navíc při reconnectu/návratu do okna. Změna barev nevyžaduje rebuild
aplikace. Agentní `page_project read` dostává stejnou paletu a přesný SDK kontrakt.
Workspace záloha nese nastavení workspace; samostatný Page YAML nepřepisuje
firemní paletu cílové instalace a jeho CSS tokeny zdědí její nastavení.

Operations Lab přidává krátký nástup obsahu a hover/active odezvu tlačítek přes
CSS. Žádná nová animační knihovna, nekonečná animace ani datový polling. Media
query `prefers-reduced-motion: reduce` animace, přechody a posuny vypíná.
Významové warning/error stavy zůstávají odlišené od firemního akcentu.

## Plynulé otevření aplikace — 2026-09-09

Načítání publikace nesmí krátce zobrazit původní panely. Host drží plochu
v barvě workspace a iframe zakryje do prvního snapshotu a dvou vykreslovacích
cyklů runtime. Poté aplikaci odkryje 300ms opacity přechodem; aktualizace dat
ji znovu nespouští. Nejde o potvrzení správnosti libovolného asynchronního
obsahu aplikace. Ta si vlastní později načítaný obsah řeší sama.

Handshake používá stávající MessagePort, bez nových API volání, oprávnění nebo
knihoven; funguje i pro existující artefakty. Chybějící dokončení po 20 sekundách
v aktivní záložce vede k chybě s možností zavřít/znovu otevřít aplikaci.
V neaktivní záložce timeout neběží, protože prohlížeč může pozastavit rendering.
Omezený pohyb vypíná přechod. Horní ovládání aplikace se zalamuje, výška shellu
používá dynamický viewport pro mobilní Safari. Název v mobilním subbaru se
nesmí zúžit pod svou šířku a překrýt s akcemi.

Závěrečné ověření: celá Go sada (139 balíčků), frontend (8 192 testů),
`go vet`, lint a build prošly. Živé Chromium/WebKit testy prošly se zpomaleným
bootstrapem, šířkami 1600/900/390 px, službami, rozbalenou historií a omezeným
pohybem. Skutečná SDK akce v kontejneru po nasazení také prošla.

Uživatel potvrdil běžné zobrazení v Safari. To je samostatné ověření od dosud
otevřeného testu zacykleného skriptu a podpory jeho zastavení.

## Aktuální checklist před uzavřením v1 — 2026-09-09

Podklad pro nezávislou validaci: [audit implementace, rozhodnutí a rizik](pages-apps-review-audit-2026-09-09.md). Obsahuje také konkrétní nesoulad validátorů cest, limit růstu Git historie, kompatibilitu tools profilu a nepřipnutou verzi backendové routine. Tyto nálezy nejsou automaticky opravené ani uzavřené zelenými testy.

Tento checklist je aktuální stav. Pozdější sekce P1–P5 zachycují historii práce;
jejich tehdejší poznámky „zbývá“, „testy běží“ a „nenasazeno“ nejsou nový backlog.

- [x] React aplikace v Pages, SDK data/historie/akce, serverové RBAC a policy.
- [x] Git revize, YAML přenos, build/check/publish, rollback a stažení aplikace.
- [x] Zálohy/obnova, retence, restart a omezené zátěžové ověření.
- [x] Živé Operations Lab: skutečný sběr z Ops, tlačítko s potvrzením a plán.
- [x] Demo seed se zdroji/routine/skriptem a ochranou uživatelských úprav.
- [x] Závěrečné Go/CLI/vet kontroly a skutečný Docker build; vývojové nasazení dev3.
- [ ] Živá akceptace autorství z chatu: agent vytvoří a upraví Page přes MCP,
  člověk zkontroluje/publikuje návrh, akce aktualizuje data. Samostatně ověřit
  čtenáře a uživatele po odebrání oprávnění v tomto živém průchodu. Serverové
  negativní RBAC testy existují; ukázku dosud vytvořilo CLI, ne živý chat agent.
- [ ] Produkční instalační postup pro oddělený runtime, dostupnost adresy/TLS a
  konfiguraci buildu; ověřit na čisté instalaci. Dev3 používá explicitní vývojovou
  same-origin výjimku. Uživatel dál otevírá `/pages/<slug>`; jde o technickou
  konfiguraci instalace, nikoli vlastní doménu pro každou stránku.
- [ ] Uzavřít podporu prohlížečů a implementovat odpovídající chování UI:
  Firefox/WebKit neprošly stop-loop testem. Produkční odolnost nelze odvodit
  z toho, že se dashboard správně vykreslil; není slíben přenositelný CPU/RAM limit.
- [ ] Commit, review/PR a merge do hlavní větve dle repozitářového postupu.
  Vývojová binárka na dev3 již běží; zdrojové změny jsou zatím necommitnuté.

Volitelná následná vylepšení, nikoli nehotové povinnosti P0–P5: dlouhodobější
časový graf (současná křivka je osm vzorků posledního měření), uživatelské
ovládání plánu/stavu kolektoru přímo v demo aplikaci a agentní diagnostika.
Existující **Check application** ověřuje zdroje, artefakt a vazby. Neprovádí
samostatný agentní audit správnosti aplikace ani automatickou opravu backendu.
Taková diagnostika vyžaduje samostatně vymezené oprávnění, rozsah a rozpočet.

## Výsledek pro zákazníka

Agent v chatu vytvoří vlastní React dashboard nebo jednoduchou aplikaci.
Producent v crew kontejneru posílá data stejně jako u dnešních Pages. Uživatel
vidí poslední stav a jeho stáří a může spustit konkrétní povolenou routine.
Jedním YAML souborem přenese definici stránky a zdrojový projekt do jiné instalace.

Piloty: MySQL health přehled a Ansible run přehled s jednou deklarovanou akcí.
Pravidelný sběr je deterministický skript/routine, nikoli opakované LLM volání.

## Cílová platforma v1

- React + TypeScript + Vite, statické artefakty, jeden podporovaný starter.
- Volitelné Tailwind/shadcn komponenty; vlastní design zůstává dovolený.
- Existující Page/panel data, sidecar identita, serverové RBAC a routine fronta.
- Malé SDK: snapshot, omezená historie, aktualizace, deklarovaná akce a vlastní run.
- Git zdroje, draft, omezený build, preview, ověřená publikace a rollback UI.
- Lokální úložiště, jeden volitelný tools image, žádný Vite server na každou Page.

Mimo v1: vlastní databáze obchodních dat, SSR, trvalé app servery, marketplace,
Puck/Refine integrace, další frontend frameworky, plné API SDK a delegace akcí
nad současná práva uživatele. Nejvyšší prioritu mají bezpečné hranice a užitečný
průřez; žádné nové služby jen pro hypotetické budoucí škálování.

## Adresa stránky a instalace runtime — upřesnění 2026-09-09

Uživatel stále otevírá `/pages/{slug}` ve Studiu, například
`https://crewship-dev3.unifylab.cz/pages/operations`. Navigace, přihlášení,
oprávnění a ovládání zůstávají v Crewshipu. Existující panelová stránka běží
bez custom runtime: data kreslí komponenty dodané Crewshipem. Vlastní React
projekt navíc spouští JavaScript vytvořený agentem; ten patří do izolovaného
iframe uvnitř téže stránky.

Runtime adresa je technická konfigurace **jednou pro instalaci**, ne doména pro
každou Page a ne úkol pro jejího autora. Obsluhuje ji stejný Go server; není
potřeba další aplikační server, databáze ani Vite proces. Pro distribuovaný
produkt má instalační proces zajistit/ověřit adresu, routing, TLS a dostupnost
z klientského prohlížeče. Automatické DNS/TLS provisionování zatím implementované
není. Spravovaná distribuce může adresu zajistit na doméně provozované Crewshipem;
self-hosted provoz může použít vlastní veřejné nebo interní DNS s důvěryhodným TLS.
Žádná závislost na veřejném runtime hostingu není v nynější implementaci nutná.

Lokální vývoj může používat různé loopback adresy. Alias jen v kontejneru nebo
`/etc/hosts` serveru však neřeší přístup uživatele na vzdálené dev3: název musí
rozpoznat jeho prohlížeč a `localhost` znamená jeho počítač. HTTPS Studio vyžaduje
HTTPS runtime. Server nemůže sám přidělit libovolné veřejné DNS jméno či klientem
důvěryhodný certifikát bez odpovídající infrastruktury.

Výchozí produkční kontrola vyžaduje jiný browser site než Studio. Důvodem je naměřené
zablokování Studia nekonečnou smyčkou při původním `srcdoc` vložení; doménové
oddělení je hranice procesové izolace v desktopovém Chromium, nikoli slib
univerzálního limitu CPU/RAM. [Chromium Site Isolation](https://www.chromium.org/Home/chromium-security/site-isolation/).
Podpora Firefox/WebKit zůstává otevřená podle výsledků v handoffu. Pro vývojovou ukázku je nově implementovaná explicitní výjimka:
`CREWSHIP_PAGE_RUNTIME_DEVELOPMENT_SAME_ORIGIN=true`. Povoluje přesně stejný
origin a zachovává sandbox/CSP/RBAC, ale **negarantuje oddělení procesu**.
Vadný kód může zaseknout i Studio. Povolení je serverové nastavení, není součástí
přenosného Page projektu. Tato vědomá výjimka pro zkontrolované demo neruší
výchozí produkční požadavek.

## Datový a akční kontrakt

Producent → sidecar → autorizovaný a validovaný snapshot → realtime invalidace
→ oprávněná Page. Čtenář neprobouzí producenta a počet otevřených Pages nezvyšuje
počet kontrol MySQL. Případná explicitní refresh akce je řízená a limitovaná.

Použít existující payload schemas. Nový obecnější payload vyžaduje verzované
schéma, velikostní limity a serverovou validaci, nikoli pouze TypeScript typ.
Neznámý stav není úspěch; výpadek producenta zachová údaj s viditelným stářím.
Detailní logy se čtou u konkrétního runu, ne v celém opakovaném snapshotu.

Start → ověřené action ID a vstupy → RBAC/policy → pending ID → run ID → výsledek.
Žádný shell či routine slug dodaný browserem mimo uloženou deklaraci akce.
Dva návštěvníci stejné Page sledují své vlastní běhy. Timeout ani odpojení
streamu nesmějí být vykázané jako potvrzené ukončení procesu.

## Jeden exportovatelný YAML soubor

Uživatelská akce zůstane **Export Page** a **Import Page**. Rozšířit současné
`crewship page export` / `crewship page import`; nevytvářet vedlejší produktové
menu ani požadovat, aby zákazník ručně skládal adresář nebo ZIP.

Nová aplikace používá verzi `crewship-page-bundle/v2`. Existující panelové v1
balíčky zůstávají importovatelné. V1 importer už odmítá neznámou verzi; nesmí
nový zdrojový projekt potichu zahodit. Kontrakt v2 je sdílený Go typ pro API a CLI.

| Obsah souboru | Pravidlo |
|---|---|
| Definice Page a panelů | Včetně authored actions, nastavení a SLA; explicitní vlastníci jako přenositelné reference |
| React/TS/CSS/HTML zdroje | Přesný obsah souborů s relativními cestami |
| `package.json`, `pnpm-lock.yaml` | Přenést přesné bytes; build používá frozen lockfile |
| Obrázky a další malé binární assets | Canonical base64 v YAML; limity i po dekódování |
| Vazby | Crew/routine/agent jako reference pro cílovou instalaci; místní UUID se nepřenášejí jako grant |
| Verze | Formát zdrojové sekce a podporovaný runtime profil |
| Producenti | Standardně odkazy na existující resources; chybějící vazby musí import vyjmenovat |

Export není záloha celé instalace. Neobsahuje payload historii, živá data,
credentials, přihlašovací relace, aktivní granty, Git historii, `node_modules`
ani build výstupy. Zdrojové texty mohou obsahovat ručně vložená tajemství;
zákaz `.env` souborů není důkaz, že kód žádné tajemství neobsahuje.

Požadavek „vše v jednom souboru“ v první dodávce znamená celou **Page definici
a její zdroje**, včetně assets. Neznamená automatické přibalení běžící MySQL,
Ansible inventáře, vzdálených systémů ani celé crew. Sběrný skript, který je
součástí projektu, se přenese jako zdroj; jeho instalace jako producent vyžaduje
explicitní vazbu a oprávnění. Volitelný export závislých Routine/Crew manifestů
do téhož souboru je následné rozšíření standardního manifest bundle procesu.

Současný transfer v1 přenáší omezenou podmnožinu panelu; zejména authored
chování není celé v `pageBundlePanel`. V2 musí tato pole přenést a znovu
autorizovat při importu. Neslibovat jejich round-trip pouze proto, že typ
existuje v jiném exportéru `kind: Page`.

Zdrojová sekce má vlastní verzi, aby šlo měnit Page envelope a build profil
nezávisle. Tvar zdrojové sekce (v kompletním v2 exportu je pod klíčem `project`):

```yaml
format: crewship-page-source/v1
runtime: react-vite-typescript/v1
files:
  - path: src/main.tsx
    encoding: utf8
    content: |
      // Přesný zdrojový obsah.
  # Dále package.json, pnpm-lock.yaml, index.html a ostatní soubory.
```

Výchozí zdrojové limity: 256 souborů, 512 KiB na soubor, 2 MiB celkem po
dekódování, 4 MiB serializovaná zdrojová sekce v YAML i API JSON. Velký text, který
JSON escapováním překročí limit, lze přenést jako base64. Celý v2 envelope potřebuje
samostatný omezený rozpočet pro Page metadata a reference; stávající panelový
spec limit se nezvyšuje plošně. Větší projekt obdrží jasnou chybu, nikdy
ořezaný export. Cesty jsou relativní, jednoznačné i na case-insensitive disku;
žádné symlinky, traversal, duplicitní soubory nebo export runtime adresářů.

## Import, důvěra a obnova

1. Přečíst právě jeden známý verzovaný balíček v limitu, odmítnout neznámé
   položky, duplicitní klíče, YAML aliasy, neplatné cesty a neplatné kódování.
2. Ověřit všechny vazby, cílový workspace a právo importovat/upravovat Page.
   Chybějící reference ukázat najednou. Žádné automatické přidělení cizích práv.
3. Vytvořit nový draft; existující slug je konflikt, pokud uživatel nepožádal
   o explicitní update proti očekávané revizi. Nezměnit stávající publikaci.
4. Teprve samostatná build operace může instalovat závislosti a spustit
   projektový build v sandboxu. Import neprovádí hooks ani skripty.
5. Preview/test a publikace proběhnou v cílovém prostředí s jeho policy.
   Žádné přenesené public flags, grants nebo automaticky aktivované schedulery.

Stažený zdrojový soubor vyžaduje právo číst zdroje, ne pouze zobrazit dashboard.
V2 export s nepřístupnou povinnou částí selže; nesmí vytvořit neúplný soubor,
který po importu odstraní cizí panely. Digest identifikuje zdrojové bytes, nikoli
důvěryhodnost autora. Build schválení se nepřenáší z původní instalace.

Edit oprávnění chrání stažení původního projektu, nikoli utajení JavaScriptu
před čtenářem dashboardu. Budoucí browser runtime musí stáhnout spustitelný
bundle, který si čtenář může prohlédnout. Tajemství a rozhodování o oprávnění
proto musí zůstat na serveru/sidecaru; minifikace ani iframe tuto hranici nemění.

## Implementační postup a stav

| Etapa | Obsah | Stav |
|---|---|---|
| P0 | Přesný scope, přenosový zdrojový model, strict parser, deterministický export a digest, negativní testy | Source codec implementován a cíleně ověřen |
| P1 | Sdílený Page bundle v2, uložení draft projektu, autorizované export/import API a CLI, binding validace | Implementováno a integračně ověřeno |
| P2 | Git checkpoint, omezený build, neměnný artefakt a izolovaný React runtime s read SDK | Git + build/preview/read SDK implementované |
| P3 | Akce, korelace runu, agent starter, check/publish/rollback a piloty | Publikace/akce/CLI starter implementované; skutečné MySQL/Ansible kolektory ověřeny v izolovaných testech |
| P4 | Agentí tvorba přes existující MCP, autorství a policy | Implementováno, skutečný MCP → API → Git → Docker průřez ověřen |
| P5 | Lifecycle UI, SDK historie, souborové zálohy/obnova, retence, restart a zátěž | Implementováno a cíleně ověřeno; konečný plný Go běh prošel |
| Release gate | Živé autorství z chatu, produkční instalace, podpora prohlížečů a review/merge | Otevřené body viz aktuální checklist výše; testy a dev3 demo již hotové |

P1 dodává API/CLI pro zdroje a přenos. Průřez P2 přidává v2 YAML/JSON import
v UI, YAML export se zdroji a editorový React preview. Export Page bez
připojeného projektu zůstává v1. Průřez P3 přidává publikovanou aplikaci; vydání stále čeká na provozní gates.

Akceptace exportu: texty včetně Unicode/newlines a binární assets se vrátí
byte-for-byte; pořadí vstupních souborů nemění digest; export nepozmění pracovní
kopii; chybný import nic nezapíše ani nespustí; chybějící binding je srozumitelný;
v1 panelový export/import dál funguje. API i CLI musí mít round-trip integrační
testy, ne jen test samotného kodeku.

## P1 — provozní a API kontrakt

Funkce je opt-in: `storage.page_projects_path` nebo
`CREWSHIP_PAGE_PROJECTS_PATH` musí ukazovat na absolutní adresář mimo crew
`storage.base_path`. Konfigurace odmítá překryv i přes existující symlinky.
Adresář nesmí operátor přimountovat do agentových kontejnerů. Prázdná hodnota
funkci vypíná; čtení existujícího projektu pak selže, nikoli tichým v1 exportem.

SQLite drží aktuální revizi, digest, draft definici a audit změn zdrojů. Obě
nové tabulky jsou zahrnuté do existujícího registru workspace záloh a obnovy;
filtruje se podle vlastnící Page, nikoli podle účtu autora.
Immutable YAML snapshots jsou v chráněném adresáři, oddělené podle workspace;
čtení znovu ověřuje digest. Jeden proces serializuje přijetí snapshotů a limity:
256 uložených snapshotů a 128 MiB na workspace. Stejné zdroje se deduplikují.
Soubor vzniká před krátkou DB transakcí. Neúspěšná transakce může zanechat
neodkazovaný soubor, který se počítá do limitu. Automatický GC ani Git zatím
nejsou dodané; tato omezení záměrně brání neomezenému růstu pilotu.

**Záloha:** spolu se SQLite je nutné zálohovat i celý source adresář, ideálně
při pozastavených zápisech. Samotný současný DB backup zdroje neobnoví.
Integrace zálohování/obnovy, správa retence a ověření výpadku napájení jsou
release gate před produkčním zpřístupněním. Nejde o hotové produkční úložiště.

- `GET /api/v1/pages/{slug}/project`: zdroje, draft definice, digest a revize.
- `PUT /api/v1/pages/{slug}/project`: `expected_revision`, `project`, volitelná
  `definition` (`kind: Page`). Revize 0 vytváří první draft; zastaralá revize
  vrací 409. Slug definice musí zůstat stejný. Odpověď vrací novou revizi.
- Oba endpointy i export vyžadují stávající Page edit právo (owner/admin/write
  grant). Samotné právo zobrazit Page nedává přístup ke zdrojům. Scope workspace
  se bere z autentizovaného kontextu; bundle nemůže přidělit práva.
- Aktualizace draftu nemění živou Page. První save bez `definition` pořídí kopii
  současné definice; další save bez ní zachová předchozí draft definici. Živý
  panel editor tedy automaticky nepřepisuje draft.
- V2 zachovává `call`/`toggle`, wake, refresh a on_failure v draftu; `link` a
  `custom` zatím odmítá jako nepřenositelné. Import ověřuje reference ve workspace
  a vytvoří inertní panelovou Page s draftem. Neinstaluje producenty, neprobouzí
  agenty, nepublikuje a nespouští deklarované akce.

Příklad pro již existující Page `mysql-health` a zdrojový dokument podle sekce
výše (musí obsahovat všechny požadované soubory):

```sh
crewship page project set mysql-health --file source.yaml --revision 0
crewship page project get mysql-health
crewship page project set mysql-health --file source.yaml --definition page.yaml --revision 1
crewship page export mysql-health > mysql-health.yaml
crewship page import mysql-health.yaml --slug mysql-health-copy
```

`page.yaml` je standardní `kind: Page` dokument; `source.yaml` je dokument
`crewship-page-source/v1`. Uživatel přenáší jen výsledný `mysql-health.yaml`.
CLI `project get/set` vrací standardně JSON; `--format` podporuje i YAML
a NDJSON. Agent pracuje stejným autorizovaným API/CLI;
specializovaný starter/chat nástroj a balení adresáře přijdou v P2/P3.

## Ověření P1

Cílené API/CLI testy pokrývají YAML round-trip Unicode a binárních assets,
zachování neaktivních akcí, validaci a přemapování vazeb, zákaz cizího workspace,
odmítnutí čtenáře bez edit práva a konflikt souběžných save. Testy úložiště
ověřují opětovné otevření, limity, poškození digestu a únik přes symlink.
Celá API sada prošla (823 s). Celá CLI sada po opravě formátování prošla
(258 s); celá sada záloh po registraci nových tabulek prošla (111 s). Test
workspace backup/restore navíc ověřuje zachování draftu a auditu bez autora.
`go vet ./...`, migration lint, agents invariants a `git diff --check` prošly.
Kompletní běh `GOMAXPROCS=4 go test -p 2 -parallel 4 ./... -count=1
-timeout=30m` doběhl bez timeoutu; databázová sada prošla (941 s). První běh
odhalil chyby ve třech balíčcích: CLI formátování, klasifikace nových tabulek v
zálohách a citované počty OpenAPI operací. Všechny byly opravené a celé tyto
balíčky při samostatném opakování prošly, stejně jako OpenAPI generátor a
kontroly dokumentačního povrchu. Ostatní balíčky prošly v celkovém běhu.
Celý repozitář nebyl po těchto opravách znovu spuštěn v jednom příkazu; poslední
změnu omezení zdrojového formátu a migrace navíc ověřily cílené Pages/API a
backup round-trip testy. Podrobné lokální logy: `/tmp/pages-p1-*.log`.

Pracovní větev: `feat/pages-apps-project` ve worktree
`.claude/worktrees/pages-apps-project`. Změny nejsou commitnuté ani nasazené;
běžící instance dev3 nebyla restartovaná. Uchovaný stash
`pages-apps-p1-before-base-refresh` je pouze záložní kopie před aktualizací
základu větve; obnovené změny jsou v pracovním stromu.
Frontend se v P1 nemění; jeho build tato etapa neověřuje.


## P2 — implementovaný průřez preview

Editor může z uložené revize spustit izolovaný build a otevřít vlastní React
aplikaci nad současnými daty Page. Agent dostává `page project init`, společný
starter a příkazy `build`/`preview`. Samostatná integrace do chat nástrojů a
balení pracovního adresáře ještě zbývají. Podrobný provozní návod a reprodukce
jsou v [tools/pages-build/README.md](../../tools/pages-build/README.md).

### Build a výkon

`POST /api/v1/pages/{slug}/project/build` přijme `expected_revision`, uloží job a
vrací 202. Zastaralá revize vrací 409. Jeden globální slot na instanci, souběh
vrací 429 s Retry-After; žádná neomezená čekací fronta. Kompiluje se zmrazená
revize, nikoli proměnlivý draft. Stav přežije refresh UI; po restartu je
nedokončený job `interrupted`.

Volitelný lokální Docker tools image je připnutý digestem. Žádný Node proces
ani Vite server neběží trvale pro každou Page. Kontejner běží bez sítě, host
mountů a secrets, s UID 1001, read-only rootfs a cap-drop ALL. Limity: 1 CPU,
1 GiB RAM, 128 PID, omezené tmpfs a výstupy, vlastní 120s deadline. Go obal
ruší klientský proces a uklízí kontejner; vlastní deadline platí i při pádu
Crewshipu. Přístup Go serveru k Docker daemonu zůstává operátorská hranice důvěry.

Profil připíná React/ReactDOM 19.2.8, TypeScript 7.0.2 a Vite 8.2.2. Build nejprve
ověří TypeScript a pak vytvoří jediný IIFE JS + CSS artefakt (max. 2 MiB decoded).
Lokálně ověřený tools image hlásí v Dockeru 123 249 199 B (cca 118 MiB);
ukázkový starter má 192 564 B JS+CSS před transportní kompresí. Jsou to měření
tohoto profilu, nikoli limit velikosti budoucích aplikací. Balíčky se během nedůvěryhodného buildu nestahují. Dependency mapy a lockfile
musí odpovídat starteru. Uživatelské package scripts ani konfigurace build
pluginů se nespouští. Vlastní React a CSS design ano; libovolný npm profil,
Tailwind build plugin, SSR a serverové procesy zatím ne. Entry je `src/main.tsx`,
root `#root`; runtime vlastní HTML shell. Importované assets se musí vejít do
výsledného artefaktu. `public/` a externí runtime zdroje nejsou podporované.

Artefakty jsou neměnné, oddělené podle workspace a ověřované SHA256 při čtení.
SQLite drží pouze metadata. Limity na workspace jsou 256 artefaktů / 128 MiB a
512 build záznamů navíc k P1 source limitům. Automatický GC a retence zbývají.
DB backup obsahuje všechny tři metadata tabulky; soubory zdrojů a artefaktů je
stále nutné zálohovat zvlášť. To je blokátor produkčního vydání, ne hotová obnova.

### Náhled a RBAC

`GET /api/v1/pages/{slug}/project/preview` vrací revizi, poslední job, připravený
artefakt a `runtime_url`; odpověď je `no-store`. Build i preview vyžadují Page
edit authority a aktuální workspace, stejně jako source API. Reader ani cizí
workspace nedostane zdroje či artefakt. UI reaguje na WS dokončení; 60s polling
běží pouze jako záloha při rozpracovaném buildu. Stop odstraní iframe a porty.

Náhled se inicializuje přes pevný veřejný bootstrap
`GET /api/v1/pages/runtime/bootstrap`. Ten nečte DB a neobsahuje aplikaci ani
data. Kód přichází pouze od přesně nakonfigurovaného Studio parent okna přes
structured clone. Bootstrap se servíruje jen na runtime Host. Jeho CSP zakazuje
fetch, externí subresources, workers, forms a vnořené frames; iframe má pouze
`allow-scripts`, bez `allow-same-origin`. Nedostává cookies, token ani URL broker
pro API. Přístup ke zdrojům zůstává v autentizovaném Studio API.

**Změna podložená testem:** původní `srcdoc` izoloval DOM/storage/fetch, ale
nekonečná smyčka uvnitř zablokovala i Studio. Runtime proto vyžaduje **jiný site**
(jinou registrable doménu), ověřený přes public suffix list. Další port ani
sourozenecká subdoména nestačí. DNS/proxy alias obslouží stejný Go listener,
bez nové trvalé služby. Konfigurace a kontrola skutečného API Host odmítnou
same-site kombinaci. Chromium test pak potvrdil, že host stále reaguje a umí
nekonečnou smyčku odstranit. To není univerzální hard CPU/RAM quota v každém
prohlížeči; Firefox/WebKit a paměťová zátěž ještě patří do release gate.

Sandbox/CSP navíc není úplná ochrana před exfiltrací: iframe se může navigovat
sám. Publikovaný kód proto bude vyžadovat důvěryhodný/reviewovaný zdroj a další
návrh navigace; do panelů nesmějí přijít secrets. Automatická kontrola buildu
nemůže garantovat neškodnost libovolného JavaScriptu. Přímé spuštění routine
ze sandboxu není zatím implementované.

### Read SDK a přenos

`@crewship/pages` exportuje `usePageSnapshot`, `usePanel`, `getSnapshot` a
`subscribe`. Předává slug, název a viditelné panely včetně času producenta;
vynechává sealed panely, identity a authoring actions. Podkladová data dál
přicházejí existujícím producent → sidecar → Page snapshot tokem.

MessagePort má nejvýše jednu nepotvrzenou zprávu a jeden poslední čekající
snapshot. SDK potvrzuje přijetí; host slučuje aktualizace, odesílá max. 10×/s
a odmítá snapshot nad 1 MiB viditelnou chybou. P2 původně nemělo reverse API/action broker; omezený P3 broker je popsán níže. Zaseknutý renderer proto nehromadí neomezenou frontu snapshotů.

UI importuje v1 i v2 z JSON/YAML, odmítá YAML aliases, duplicity a více dokumentů.
Neznámá v2 pole zachovává pro strict server validation. V2 export je jediný
`.bundle.yaml` se zdroji a deklarací. Import zůstává inertní draft bez spuštění.

### Co zbývá před hotovou dodávkou v1

- Přímé authoring nástroje pro agenty v chatu/sidecaru, s identitou agent/crew/run
  a stejnou policy; nové projektové CLI zatím používá autorizovanou uživatelskou
  relaci. Existující `save_page` strukturu vytváří, ale nový source workflow
  není automaticky dostupný skrz toto staré volání.
- Historie publikací a rollback selector v UI, odpublikování a obnova Git
  archivu. Draft historie/restore v UI a publication rollback přes CLI už existují.
- Omezené čtení panelové historie v SDK; aktuálně snapshoty a vlastní run status.
- Skutečný pilot MySQL/Ansible v crew s policy a reálnými cíli; připravené
  příklady nejsou důkaz živého provozu.
- Integrovaná souborová záloha/obnova a správné přemapování workspace/Page IDs,
  retence/GC, výpadky a zátěž souběžně s hlavním Crewshipem.
- Firefox/WebKit a revokace oprávnění za běhu ve skutečných víceuživatelských
  scénářích. Reader má 60s backstop, realtime invalidace a podmíněné ETag čtení;
  neznamená to okamžité odebrání již předaných dat.

Technické reference: [Vite JavaScript API](https://vite.dev/guide/api-javascript.html),
[Vite build options](https://vite.dev/config/build-options.html),
[MDN iframe](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe).


## Ověření průřezu P2 (2026-09-08)

- 33 frontendových souborů / 549 testů prošlo; následné tři testy dialogu
  ověřily odstranění iframe při odmítnutí oprávnění, chybějící Page a ručním
  Stop, plus lokální chybu neplatné runtime konfigurace. Jednotkové testy
  transportu pokrývají tisíc aktualizací bez ACK, sloučení poslední hodnoty,
  ignorování chybného ACK, limit 1 MiB a zrušení čekajícího odeslání.
- `pnpm lint`: 0 chyb, 32 existujících warnings. `pnpm build`: úspěšný Next
  static export. Nové testy navíc prošly samostatným ESLintem.
- Skutečný Docker compiler sestavil starter a odmítl chybný TypeScript i jiný
  lockfile. Následný test přes API a skutečné úložiště ověřil celý průchod až
  k načtení artefaktu a jeho digestu (6,1 s).
- Chromium smoke test s reálným Go bootstrapem a Docker artefaktem ověřil
  React render, aktualizaci SDK, zákaz parent DOM/storage/fetch a odstranění
  zaseknutého child rendereru bez zamrznutí hosta. Testy nepoužívají živou instanci.
- Celá Go sada byla spuštěna s `GOMAXPROCS=4 go test -p 2 -parallel 4 ./...
  -count=1 -timeout=30m`. Databázová sada prošla (1017 s), CLI také (234 s).
  Odhalené integrační nedostatky byly registrace background buildu, evidence
  veřejného bootstrapu a kopírování nového Go starter balíčku do Dockerfile.
  Byly opraveny a příslušné guardy znovu prošly. Serverový build v prvním běhu
  zachytil průběžně měněné runtime rozhraní; po ustálení zdrojů celý balíček
  serveru prošel (44 s), stejně jako konfigurace a pagebuild.
- Poslední opakování celé API sady doběhlo za 876 s. Jediný neúspěch byl
  explicitní allowlist veřejných OpenAPI operací: testovací binárka vznikla
  před doplněním bootstrapu do očekávané množiny. Aktuální allowlist a všechny
  související API/OpenAPI guardy následně prošly. Po finálních opravách byly
  opakovány dotčené testy a balíčky; celý repozitář nebyl znovu spuštěn najednou.
- Aktuální OpenAPI generátor, schema/docs inventory, public-route guardy,
  workspace/role guardy, Pages build lifecycle/RBAC/revision testy a Dockerfile
  source guard prošly. Samostatné CLI projektové testy rovněž prošly.
- `go vet ./...`, migration lint, agents invariants a `git diff --check` prošly.
  Migration lint kontroluje již verzované SQL; obě nové migrace navíc skutečně
  aplikovaly databázové/API fixture testy.

Toto je ověřený editorový preview průřez, nikoli hotová produkční dodávka.
Lokální logy jsou `/tmp/pages-p2-*.log`, pracovní větev zůstává
`feat/pages-apps-project` v `.claude/worktrees/pages-apps-project`.
Živá dev3 nebyla restartovaná, konfigurace nebyla změněná a žádná Page nebyla
publikovaná. Změny zatím nejsou commitnuté.


## Průřez P3 — Git, publikace a akce (2026-09-08)

Zdrojové revize mají skutečný bare Git checkpoint: `source.yaml`, `page.json`
a `project/` s přesnými soubory. Git používá pouze `hash-object --no-filters`,
`mktree`, `commit-tree` a content-addressed refs; nespouští checkout, hooks,
filtry, remotes ani přenesené konfigurace. SQLite určuje aktuální draft; CAS
konflikt může nechat osiřelý archivní kandidát, nikdy nepřepne draft nazpět.
Git storage má vlastní 128 MiB/workspace kvótu včetně rezervy na stromy,
zdrojová historie 512 revizí/Page. Historie je stránkovaná po 50 položkách.
Restore vytváří novou revizi se starým zdrojem/deklarací a novým auditem.
Legacy revize bez uložené deklarace nejsou vydávány za plně obnovitelné.

CLI umí `project init --dir`, `pack`, `unpack`, `get --revision`, `history`,
`restore`, `check`, `publish`, `rollback`, `application` a `action-status`.
Existující `page action --publication` používá stejný publikovaný action fence. Pack nepouští build
ani install, vynechá `.git/node_modules/dist`, odmítne symlinky, environment
soubory a překročení limitů. Unpack přijímá jen nový cílový adresář.

Publikace atomicky zapíše immutable receipt, přepne live pointer, deklaraci
Page, panely, wake automations a historii definice. Přezkoumá Git/source SHA,
artefakt a aktuální reference/policy; pro normální publish vyžaduje aktuální
source revizi. Owner/admin gate je přísnější než samotný Page write grant.
`reviewed_code` je explicitní potvrzení důvěry, nikoli automatický security
scanner. Check má `browser_review` a `security_review` stále `required`.
Rollback je nová publikace starého artefaktu, nepřepisuje draft a nevrací
vedlejší účinky routines. Přesný retry vrací původní receipt bez další změny.

Reader stahuje artefakt přes standardní Page reach gate, nedostává původní
projekt. Při chybě oprávnění se iframe odstraní. Novou publikaci otevře výslovně,
aby mu refresh neshodil formulář; může aplikaci zastavit a otevřít panely.
Artefakty fungují i s vypnutým compilerem. Periodické čtení používá ETag:
server před 304 znovu ověří Page reach i publikační práva, ale stejný artefakt
nečte z disku ani nepřenáší. ETag zahrnuje workspace/Page, publikaci, runtime
a možnost publish; neslouží jako veřejná cache ani náhrada autorizace.

SDK má `runAction` a `getActionStatus` přes privátní MessagePort. Host načte
uloženou deklaraci a zobrazí vlastní potvrzení. Do backendu odešle pouze
publication, panel/action ID a vstupy. Backend ověří RBAC, deklaraci, routine
status a běžná queue/policy pravidla. Ověření stejné publikace/deklarace a
zařazení pending runu jsou v jedné SQLite write transakci. Starý frame nebo
Page upravená mimo publikaci dostane 409. Idempotency/debounce se oddělují
podle uživatele a publikace; status vrací jen vlastní pending/run a kontroluje
aktuální přístup k panelu. Neexistuje univerzální shell/HTTP/SQL API v browseru.

Limity brokeru: 32 KiB/request, 1 aktivní RPC/frame, rozestup alespoň 250 ms,
60s čekání na potvrzení a 90s SDK timeout. Zavření frame zruší nepotvrzenou
akci; již odeslaný požadavek může pokračovat. `pending` není dokončený run.
Backend routine se stále vykonává podle existujícího executor kontraktu;
Git verze Page nepřipíná implementaci routine.

Příklady jsou v `examples/pages-apps/`: společná React aplikace, MySQL/Ansible
Page definice, dvoukrokové routines a omezený Python collector. Collector
neposílá stderr/logy, připojovací soubory ani inventář. `page.write` provádí
executor s run identitou; nejsou potřeba uživatelské API klíče v crew.

Ověřeno v tomto průřezu: skutečný CLI proces přes router, auth, migrovanou
SQLite, Git a Docker compiler provedl init/pack/save/history/restore/build/
preview/check/publish/retry/rollback/read/action/status. Testy pokrývají konflikt revize,
source/artifact integritu, neprovedené akce při chybě publikace/deklarace,
repeat receipt a nečitelnost cizího pending runu. Metadata publications/live
přešla workspace-scoped dump/restore v pořadí respektujícím FK. Toto zatím
není souborová záloha zdrojů/Gitu/artefaktů.

Lokální image: `crewship-pages-build:dev3-p3`, digest
`sha256:903feb5c6b5ca9c3f09a6bda5454551ba3a458f6f726a8367186a34e84e5a731`.
Logy tohoto průřezu: `/tmp/pages-p3-*.log`.

### Závěrečné ověření P3

- Celá Go sada: `GOMAXPROCS=4 go test -p 2 -parallel 4 ./... -count=1
  -timeout=30m`. API prošlo za 603 s, původní CLI za 212 s. Dvě selhání:
  `TestNoRawFileWritesOutsideDurableHelper` zachytil CLI unpack mimo společný
  helper; `TestForeignKeyIndexPolicy` zachytil dvě neindexované nové FK vazby.
  Unpack nyní používá `memory.WriteFileDurableRoot`, nová (dosud nevydaná)
  publikační migrace obsahuje oba indexy. Zbytek celé sady prošel; databázový
  balíček doběhl za 717 s s uvedeným jediným FK-policy selháním.
- Po opravě celý `internal/consolidate` prošel (29 s), celý CLI balíček znovu
  prošel (219 s) a FK policy prošla nad čerstvě migrovanou DB. Po následném
  ETag doplnění prošly všechny dotčené Pages/API/OpenAPI/backup/CLI testy,
  role/read guardy a docs inventory. Celý repozitář nebyl znovu spuštěn najednou;
  netvrdit tedy, že finální stav má jeden úplný zelený repo-wide běh.
- Poslední skutečný CLI → auth/router → SQLite/Git → Docker průchod (9 s)
  prošel včetně publikačních akcí, opakovaného klíče, vlastního pending statusu
  a odmítnutí staré publikace. Test používá jen izolované fixture prostředí.
- 39 frontendových souborů / 560 testů prošlo. Nové testy pokrývají trusted
  confirmation bez předčasného POSTu, abort, bounded RPC, explicitní načtení
  nové publikace, odstranění kódu při chybě oprávnění, historii/CAS a ETag
  revalidaci s následným odebráním přístupu.
- `pnpm lint`: 0 chyb / 32 stávajících warnings. Finální `pnpm build` a
  TypeScript kontrola prošly. `go vet ./...`, migration lint, agents invariants,
  docs inventory a `git diff --check` prošly. Migration lint sám nové
  untracked SQL neeviduje; všechny čtyři nové migrace aplikovaly reálné fixtures.
- Chromium otestoval reálný Go bootstrap + Docker artefakt, SDK snapshoty,
  DOM/storage/fetch omezení a zastavení nekonečné smyčky bez zaseknutí hosta.
  Pilotní sestavené SDK navíc round-trip runAction/status zprávy a odlišení
  queue receipt od dokončeného runu. Host confirmation a backend fronta jsou
  testované zvlášť; browser smoke si odpověď RPC simuluje.
- Obě pilotní Routine DSL prošly skutečným `crewship routine validate`.
  Čtyři Python testy ověřily neznámý stav, nedostupný collector, timeout,
  nepřenášení diagnostiky a předání failure do `page.write`. Skutečná MySQL
  ani Ansible infrastruktura v tomto testu neběžela.
- Firefox i WebKit byly zkusmo spuštěny, ale host postrádá jejich systémové
  knihovny (např. GTK/Pango). Tyto browser gates zůstávají neověřené.

Živá dev3 stále běží s původními PIDy a konfigurací. Pracovní implementace
není commitnutá, mergnutá ani nasazená. Další přesné kroky jsou v
[implementačním handoffu](pages-apps-handoff.md).

Git reference: [commit-tree](https://git-scm.com/docs/git-commit-tree),
[mktree](https://git-scm.com/docs/git-mktree),
[hash-object](https://git-scm.com/docs/git-hash-object).

### P4 — přímé autorství z agentního chatu (2026-09-09)

Implementován `page_project` v existujícím sidecar MCP serveru
`crewship-routines`. Postup: existující `save_page`, pak
`init → read → save → build → status → check`. `init` vyžaduje revision 0;
`save` přijímá dávky celých souborů a smazání konkrétních cest s CAS nad
aktuální revizí. Uloží je stejný handler jako veřejné source API: validace,
Git checkpoint, kvóty a SQLite revize nemají druhou implementaci.
`read` vrací seznam souborů, definici, Studio cestu a přesný zdroj SDK ze
stejného build profilu; s `path` vrací jeden soubor. MCP envelope má 1 MiB.
`status` vrací stav a diagnostiku bez JS artefaktu. Check je statický; nedělá
browser/security review. Produkční data dále přicházejí přes `page.write`.

Privátní interní principal vzniká až po ověření workspace/crew tokenu,
existujícího agenta a přesného owner crew Page. Model nemůže předat user,
workspace ani agent ID do tool argumentů. Sidecar ověří individuální bearer
agenta i při sdílení kontejneru. Init/save/build používají existující
`page_create` policy: held/denied nezmění draft ani nepustí compiler.
Onboarding může explicitně delegovat stejně jako `save_page`; běžná crew
nikoli. Sdílí se jedna instance compileru a jeho slot s veřejným API.

Nová append-only migrace přidává `actor_json` k revizím a buildům. U člověka
zůstává user FK; agent má FK NULL a snapshot skutečné crew/agent/workspace
identity oddělený od cílového owner crew. Snapshot přežije DB dump/restore.
Transport zatím neposkytuje ověřenou run identitu, proto se run ID nevymýšlí.
Toto není kompletní záloha source/Git/artifact souborů.

Tool nemá publish operaci. Důvěru ve zdroj a publikaci potvrzuje oprávněný
uživatel v samostatném kroku. Po síťové chybě je výsledek neznámý: nejdřív
read/status, ne slepé opakování zápisu. Podrobný workflow je v
`docs/cli/page.mdx`, implementační předání v `pages-apps-handoff.md`.

Ověřování P4 se provádí proti izolovaným fixture, nikoli živé dev3. Reálný
MCP HTTP transport používá odvozený crew token a dva individuální agentní
tokeny; kontroluje Git, offline Docker build, check a odmítnutí publikace.
CLI Docker acceptance používá `PAGES_TEST_BUILD_IMAGE`, protože globální
CLI test setup správně odstraňuje ambientní `CREWSHIP_*`. Původní název
znamenal přeskočení Docker větve; tento testovací problém je opraven.
Logy tohoto průřezu: `/tmp/pages-agent-*.log`.

#### Závěrečné ověření P4

Celé `GOMAXPROCS=4 go test ./... -p 2 -parallel 4 -count=1 -timeout 30m`
skončilo 2026-09-09 přibližně v 08:19 UTC s exit 0: 138 testovaných balíčků
prošlo, 11 dalších nemá testy. Včetně API (741 s), databáze (606 s), backup
(131 s), CLI (244 s), orchestrátoru a sidecaru. Tím je uzavřena dřívější P3
mezera, kdy neexistoval jeden finální zelený běh celého repozitáře.

Zvlášť prošel skutečný MCP → API → Git → Docker → check (6,9 s) a CLI
source/build/publish/retry/rollback/action/status (17,8 s). `go vet ./...`,
migration lint, agents-invariants, route/OpenAPI kontroly,
`docs-inventory -strict`, `docs-surface-check` a `git diff --check` prošly.
Frontend runtime se v P4 neměnil; dřívější 560 frontend testů a build nejsou
vydávány za nové P4 spuštění. Živá instance nebyla nasazena ani restartována.
Souborové zálohy, lifecycle UI, retenční pravidla a skutečný provozní pilot
zůstávají samostatnými otevřenými body uvedenými v handoffu.

### P5 — lifecycle, úplné zálohy, retence a historie (rozpracováno 2026-09-09)

UI nyní nabízí přehled publikací, kontrolu uložených zdrojů, rollback s potvrzením
konkrétní verze a stažení aplikace. Stažení zachová panely i rostoucí čítač verzí;
starý retry publikace aplikaci znovu nezapne. CLI má odpovídající příkazy.

Šifrovaná workspace záloha zahrnuje zdroje, artefakty a ověřené Git objekty.
Příprava obnovy proběhne před databázovou transakcí, soubory se uloží před commitem.
Čistá obnova i fork do jiného workspace prošly integračním testem. Historické
identity v commitech zůstávají proveniencí, nikoli novými oprávněními. Chybějící
nebo poškozené soubory znamenají chybu, nikoli úspěšnou metadata-only obnovu.

Retence drží posledních 64 revizí, 64 dokončených buildů a 32 publikací na Page,
navíc všechny závislosti aktuálního návrhu, live verze a běžících buildů. Údržba
jednou za hodinu používá výhradní workspace zámek; zálohy a čtení/zápisy souborů
sdílený zámek. Počet verzí může růst, kvóty omezují uložené záznamy a soubory.
Git předky nepřepisujeme: jejich velikost zůstává omezená 128 MiB/workspace.

SDK přidává `getPanelHistory(panelId, {limit, before})`: nejvýše 20 snapshotů a
1 MiB, kontrola přístupu k panelu i aktuální publikace při každém požadavku.
Plné regresní, browser a provozní ověření P5 ještě není dokončené. Nasazení zatím
neproběhlo; podrobný stav a chybějící gates jsou v handoffu.

#### Ověření P5 a omezení prohlížečů (2026-09-09)

Frontend: 666 souborů / 8 177 testů prošlo, produkční build i lint bez chyb.
Prošel skutečný CLI a MCP/Docker průchod, čistá a fork obnova kompletních záloh,
negativní testy archivů, retence, odebrání přístupu a reconnect. Měření 24
souběžných čtenářů / 480 revalidací: 0 opakovaně přenesených bytů artefaktu,
p95 přibližně 25–45 ms na sdíleném vývojovém hostu; nejde o produkční SLA.
Skutečné MySQL a Ansible kolektory ověřily úspěšné i chybové stavy a doručení
přes CLI do panelu. Instalační aktivace rutiny a zákaznické prostředí jsou další
samostatný akceptační krok.

Browser testy nově běží v izolovaném Playwright kontejneru. Chromium prošel včetně
zastavení nekonečné smyčky ve frame. Firefox a WebKit prošly vykreslením a základním
oddělením dat, ale **neprošly zastavením nekonečné smyčky**. Firefox selhal i při
použití dvou různých registrable domén. Proto nelze deklarovat plnou podporu těchto
prohlížečů pro odolnost proti vadnému kódu. Oddělená doména a CSP nejsou přenositelná
záruka CPU/memory izolace; pro v1 je ověřený runtime Chromium a důvěryhodný,
zkontrolovaný kód. Politika podpory/fallback musí tento rozdíl výslovně uvést.
Kompletní Go sada v době tohoto zápisu ještě dobíhá; konečný výsledek patří do
handoffu. Veřejné nasazení zatím neproběhlo.

#### Aktuální stav dodávky — 2026-09-09 09:37 UTC

**Na dev3 stále běží původní verze; P5 není veřejně nasazená.** Připravené jsou
binárky serveru i sidecaru a ověřený build image. Nasazení je uživatelem schválené;
chybí určení druhé hlavní domény runtime a následný live Studio/chat smoke.

Prošel rozšířený test dvou skutečných serverových procesů s násilným ukončením
mezi nimi: zachování stažení aplikace, nové publikování při vypnutém buildu a
načtení publikace i JavaScript artefaktu po dalším startu. Chromium ověřil čerstvě
sestavenou pilotní aplikaci včetně akcí, stavu běhu i historie panelů přes SDK.

První kompletní P5 Go běh měl jediný neúspěšný test, dokumentační kontrolu počtu
API operací. Čísla jsou opravená a kontrola prošla. Dodatečně jsme opravili přesné
počítání 1 MiB limitu historie po JSON escapování a ověřili regresním testem.
Závěrečný kompletní Go běh ještě probíhá; konečný výsledek bude doplněn do handoffu.
Všechny browser limity z předchozí sekce zůstávají platné.
