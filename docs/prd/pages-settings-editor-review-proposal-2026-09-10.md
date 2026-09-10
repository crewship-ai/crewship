# Pages editor — návrh v2 po oponentuře a ověření

Stav: návrh k rozhodnutí, nikoli implementace ani splněná akceptace produktu.
Revize 2 zapracovává [nezávislou oponenturu](pages-settings-editor-independent-review-2026-09-10.md).
Oponentura zůstává beze změny jako nezávislý dokument; její popis v1 je historický.
Podklad ověření: main `01d4849cd8f61b70b64a290accf52617dbc7fac4`, screenshot
Operations Lab a cílené regresní testy. Produktové popisky jsou anglické.

[Klikací vizuální prototyp](pages-settings-editor-prototype.html) používá pouze
syntetická data. Ukazuje pracovní plochy a stavy, ne skutečné API nebo autorizaci.

## 1. Rozhodnutí

Člověk je u vlastní aplikace primárně kontrolor agentovy změny, sekundárně editor.
U panelové Page zůstává hlavní úlohou správa obsahu. Jeden vstup Edit proto vede
do odpovídajícího pracovního kontextu, ne vždy do stejného formuláře.

- Čtyři sekce: **Content / Data & actions / Access / History**.
- Seznam Pages zůstává na desktopu. Editor nahrazuje pouze hlavní obsah.
- Obecné údaje jsou v Content. Workspace appearance zůstává nastavením workspace.
- Vydávání webhookového oprávnění je v **Access → Producer tokens**.
  Data & actions ukazuje producenta a příjem dat, s odkazem na správu přístupu.
- Porovnání zdrojů i definice a obrazovka kontroly změn jsou **P0**.
- Žádný nový vizuální builder, duplikování, autosave ani agent uvnitř editoru v P0.
- Close without publishing zavře kontrolu a zachová koncept. Neexistuje tlačítko
  Reject bez příjemce. Text přizná, že autorovi nebyla odeslána žádná zpráva.
- Jeden ambientní stav na režim: v prohlížení omezení prostředí, v editoru označení
  kontrolovaného konceptu. Ukládání, build a čerstvost dat jsou u vlastních ovládacích prvků.

## 2. Co ověření potvrdilo a co v oponentuře opravuje

Všechny cesty níže jsou relativní ke kořeni repozitáře v uvedeném commitu.

| ID | Ověřený fakt a zdroj | Dopad / rozhodnutí |
|---|---|---|
| V01 | `components/features/pages/pages-layout.tsx:170`: dokumentový canEdit vyžaduje nulový počet sealed panelů. Settings tím omezené není. `internal/api/pages_project.go:33`: zdroje navíc vyžadují serverové mayEditSpec. | Edit nesmí být globálně schovaný podle starého canEdit. Sekce musí mít samostatné capabilities; ACL oprávnění samo neodemyká zdroje. |
| V02 | `internal/api/pages_versions.go:181,197,336`: panelový rollback přímo zapisuje živou definici a u dotčených panelů resetuje data. `pages_project_history.go:164`: obnova zdrojů volá uložení konceptu. `page-publications.tsx:52`: obnova publikace volá publish. | Jednotné „History → Restore to draft“ v oponentuře je nesprávné. Jsou tři různé operace, každá má jiný název a potvrzení. |
| V03 | `internal/api/pages_project_history.go:15,43`: API historie vrací actor jako jeden řetězec ID; `pages_project_internal.go:19,30`: uložený actor_json obsahuje ID agenta/crew, nikoli snapshot jmen nebo chat ID. | Hlavička „agent ops-writer · crew Ops“ potřebuje autorizované rozšíření DTO/resolver. Do té doby pravdivé ID nebo Unknown author; žádná domnělá historická jména ani neexistující odkaz do chatu. |
| V04 | `internal/api/pages_project_publish.go:17`: publish request obsahuje build/revizi/publikaci/reviewed_code, ale ne hash uživatelem zkontrolované živé definice ani hash rutin. `:201–266`: živou definici načítá až uvnitř publish požadavku. | Stávající ochrana není kontrakt „publikuji přesně dopad, který jsem viděl před pěti minutami“. Pro nový review flow je nutný serverový review token / očekávané verze kontrolovaných podkladů. Samotný refetch v UI nestačí. |
| V05 | `pages_project_publish.go:48–80`: kandidát má zdroje a archivovanou definici; `pages_project_history.go:84`: stará revize bez definice dává 409. | Kód porovnávat proti zdrojům živé publikace, dopad definice proti současné živé Page. Rozchod těchto dvou základů explicitně ukázat. Chybějící archiv není „bez změn“. |
| V06 | `internal/api/pages_application_routines.go:20–34`: hash pokrývá definition_json rutiny, ne volané skripty. CheckProject vrací routine_definitions, historie publikací je nyní nevrací jako součást DTO. | U review potřebujeme autorizovaný podklad původních a aktuálních hashů. Formulace „Routine definition changed“, nikdy „All execution code reviewed“. Chybějící starý hash znamená Unknown, ne Unchanged. |
| V07 | `internal/api/pages_webhooks.go:158–200` a `pages_webhooks_inbound.go:31–39`: token vydává člověk s potřebnou autoritou, je pro jeden panel a při příjmu se znovu ověřují práva vydavatele. | Přesun do Access je správný. Oprávnění dodávat data a možnost vydat token nejsou totožné. Token nesmí naznačovat vlastnictví cílovou osobou jen proto, že jí jej správce pošle. |
| V08 | `pages_webhooks.go:354`: seznam počítá live jen jako neodvolaný token. Neověřuje tím úspěch dalšího push ani aktuální práva vydavatele. | V UI „Not revoked“, nikoli zelené „Working“. Last accepted je historie úspěšného příjmu, ne záruka dostupnosti. Nevymýšlet expiry, automatickou rotaci ani znovuzobrazení secretu. |
| V09 | `internal/api/pages_public.go:133`: veřejné DTO nese panely, ne aplikační artefakt. | Public links vysvětlují veřejné panely. Publikovat aplikaci není totéž co ji zveřejnit mimo workspace. |
| V10 | `components/features/pages/page-preview.tsx:121`: draftový preview nedostává onRequest; RPC handler je volitelný. | Současný náhled nelze označit za kompletní ověření akcí a historie. P0 zachová neprováděcí preview a označí jeho omezení. Živý test akce je samostatná vědomá operace po publikaci. |

Toto jsou závěry statického ověření konkrétních cest, nikoli úplný bezpečnostní audit.
Cílený běh existujících testů pro webhooky, source restore, policy/CAS a izolaci
receiptů prošel: `internal/api`, 5.022 s. Log lokálně:
`/tmp/pages-settings-review-verification.log`.

Klikací prototyp byl ověřen v headless Chromium: čtyři sekce při šířkách
360, 768 a 1440 px (12 kombinací bez horizontálního přetečení dokumentu),
souhlas s publikací, konflikt, první publikace, panelová Page, omezení správce
přístupu a odstranění jednorázového demonstračního tokenu. Další průchod ověřil
blokování při chybějícím buildu, chybě buildu, chybějícím porovnání a konfliktu;
nový build neodstraňuje konflikt podkladů. Návrat z preview resetuje souhlas.
Bez chyb JavaScriptu; dodatečný průchod nevytvořil žádné HTTP(S) požadavky.
Desktopový a mobilní screenshot byly také vizuálně prohlédnuty.

To neověřuje skutečnou autorizaci, API, ukládání ani publikaci: prototyp je
simulace, diff je ilustrativní a preview nespouští kandidátův kód. Kompletní
správa focusu, neuložených změn a přístupnost zůstávají akceptací implementace.
Neběžel živý průchod novým editorem v produkční aplikaci ani uživatelský test.

## 3. Navigace a pracovní plochy

Prohlížení: Page title · Share · Edit · More. Import patří pod New page.
Stop application je přímo dostupný hostitelský ovladač mimo iframe; nesmí být
schovaný pouze v menu. Uživatelova aplikační tlačítka zůstávají v aplikaci.

Desktop: levý seznam 208–240 px, obsah má vlastní hlavičku a čtyři pojmenované
sekce. Formuláře mají čitelnou šířku, ne pole natažená přes celý ultrawide monitor.
Review má seznam změněných souborů a čitelný diff; preview je samostatná pracovní
plocha se zřetelným návratem. Nevnucovat současně formulář, diff a živý iframe do
úzkých tří sloupců. Barva rozlišuje význam, nikoli každou kartu; změny mají i textové značky.

Mobil 360 px: globální seznam otevírá standardní navigace, sekce jsou select.
Identita stránky a návrat zůstávají viditelné. Diff je unified, jednotlivé dlouhé
řádky mohou mít vlastní scroll; celá stránka nesmí horizontálně přetékat.
Dotykové cíle min. 44 px i na tabletu s velkou šířkou (coarse pointer).

URL zůstává kompatibilní se statickým exportem, např.
`/pages?slug=operations-lab&mode=edit&section=access`. Tvar není produktový cíl;
Back/Forward, refresh a sdílený odkaz musí zachovat správnou stránku a sekci.
Žádné nové API routes v app/. Přechod stránky/workspace respektuje neuloženou práci.

### Běžná panelová Page

Content začíná názvem/popisem a seznamem panelů. Každý panel má typ, producenta
nebo stav bez dat a jednoznačný vstup k editaci. Save changes výslovně mění živou
definici. Žádné prázdné aplikační záložky, checklist publikace ani disabled Publish.

Add custom application je sekundární nabídka na konci, jen s realizovatelným
postupem. Bez oprávnění nebo nakonfigurovaného compileru uvést konkrétní omezení.
Nelze slibovat vytvoření jedním kliknutím, když dnes potřebuje agentův MCP workflow.

### Vlastní aplikace: kontrola změny

Otevře se při skutečné změně kandidáta oproti relevantnímu základu. Číslo zdrojové
revize nelze porovnávat s číslem publikace: jsou to jiné řady. Kontrola zahrne
zdroje, definici a změnu běhových závislostí; samostatně označí rebuild stejného
zdroje s jiným artefaktem/profilem. Novější číslo samo nedokazuje změněný obsah.

1. Hlavička: Page, konkrétní koncept, skutečně dostupná identita autora a čas.
2. **Definition changes:** deterministicky odvozené změny panelů, producentů,
   akcí, cílových rutin, veřejnosti a dalších podporovaných polí. Tvrzení se týká
   deklarace, ne libovolného vizuálního chování Reactu. Neznámé pole ukázat v raw diffu.
3. **Source changes:** A/M/D, přejmenování konzervativně jako odstranění + přidání,
   diff po souborech. Shrnutí agenta je volitelný oddělený komentář, ne důkaz.
4. **Execution dependencies:** změněná definice rutiny / neznámý podklad / skripty
   nejsou připnuté. Nečekat s tímto upozorněním až na živou akci.
5. **Preview:** Not built / Building / Failed / Ready. Ready znamená konkrétní
   build kandidáta, ne automatické ověření akcí. Změněný nebo nový panel může mít
   v preview jen prázdný stav: dnešní preview používá dostupná živá data, nevyrábí
   odpovídající testovací snapshot nového schématu. Nevymýšlet zákaznická měření.
6. **Publish application:** souhlas vázaný na kontrolovaný kandidát a podklady.
   Při změně podkladu zobrazit důvod zneplatnění a vyžádat novou kontrolu.
7. **Close without publishing:** koncept zůstává; autorovi se neposílá zpráva.

První publikace: žádná stará publikace; ukázat Initial publication a celý rozsah
kandidáta. Definiční dopad lze stále porovnat proti existující živé panelové Page.
Po withdrawal: No live application, nepovažovat poslední receipt za živý základ.
Chybějící historie: Comparison unavailable a konkrétní důvod; přípustnou alternativu
plné kontroly musí produkt výslovně schválit, jinak publikaci pro tento flow blokovat.

## 4. Uložení a obnova

| Kontext | Ovládání | Skutečný dopad |
|---|---|---|
| Živá panelová definice / metadata | Save changes | Okamžitý zápis dle serveru |
| Aplikační zdroje a kandidátní definice | Save draft | Živá publikace se nemění |
| Aplikační build | Build preview | Nevydává publikaci |
| Kandidát aplikace | Publish application | Nová živá publikace a její definice |
| Panelová historie | Restore panel version | Změní živou definici; dotčená data mohou čekat na nové měření |
| Zdrojová historie aplikace | Restore to draft | Nový koncept, bez publikace |
| Historie publikací aplikace | Publish this version | Nová živá publikace, nejde o návrat čítače času |
| Grant / token / veřejný odkaz | Konkrétní akce | Samostatný okamžitý zápis |

Data & actions nesmí mít jedno globální Save bez informace, zda právě mění živou
panelovou definici, nebo aplikační koncept. Granty a tokeny se konceptem nikdy
nestávají. Historie není plná obnova celé Page včetně ACL, dat, tokenů a skriptů.

Žádný globální autosave ani Save all. Po síťové chybě se zachovají editované hodnoty;
403 odebere přístup k nepovoleným datům. Částečný úspěch samostatných zápisů je
viditelný. Ochrana dirty i saving stavu platí při přepnutí Page/workspace a Back;
pro zavření procesu/browseru neslibovat záchranu dat nad možnostmi beforeunload.

## 5. P0 a potřebné změny API

| Oblast | P0 / rozsah |
|---|---|
| Editor shell a navigace | Přesun existujících funkcí bez ztráty cest, samostatné capabilities sekcí |
| Content | Panelový obsah, aplikační kontrola, metadata; bez nového vizuálního builderu |
| Access | Granty, Producer tokens, veřejné odkazy, jednorázové zobrazení secretu |
| History | Tři pojmenované druhy historie a obnovy; bez falešné společné transakce |
| Diff | Lokální porovnání dostupných zdrojů a definic, omezené velikostí; ne úplná sémantická analýza Reactu |
| Autorizovaný review snapshot | Kandidát, aktuální živá definice a publikace, dostupnost historie, routine hashes a capabilities |
| Publish fence | Server ověří identitu review snapshotu a při změně relevantních podkladů vrátí konflikt; UI refetch tuto vlastnost nezaručí |
| Autor | Rozšířit autorizované DTO o dostupný typ a ID; lidská jména jen s vysvětlenou časovou sémantikou. Chat link pouze pokud existuje doložené spojení |

Review snapshot/fence jsou návrh nové serverové schopnosti, ne něco, co už dnešní
API umí. Přesný transakční kontrakt zejména vůči změnám rutin musí projít samostatným
review. P0 lze zmenšit na přesun existující správy, ale pak nelze dodávku vydávat za
hotové schvalovací rozhraní pro agentovy změny.

P1: vizuální builder, duplikace s definovanými vazbami, integrované předání agentovi,
řízené testování akcí v preview. Do této dodávky nepatří redesign grafiky Operations Lab.

## 6. Výkon, přístupnost a bezpečné zobrazení

Diff renderuje text, ne HTML/MDX ze zdrojů nebo autora. Binarity a nečitelné soubory
mají popis změny bez pokusu je spustit. Limity profilu jsou až 256 souborů, 512 KiB
na soubor a 2 MiB zdrojů (`internal/pages/project.go:40–43`); výpočet diffu nesmí
blokovat hlavní vlákno dlouhou úlohou. Velké diffs zobrazit po souborech, případně
počítat ve workeru. Zkrácené zobrazení přiznat; nesmí znamenat „vše zkontrolováno“.

Sekce načítají jen potřebná autorizovaná data; list Pages nefetchuje zdroje a
archivy všech stránek. Citlivé zdroje a secrets neukládat do localStorage ani do
URL. Klávesnice, focus po navigaci/dialogu, reduced motion, kontrast a význam mimo
barvu jsou kritéria, ne pouhé CSS požadavky. Vstup do editoru odpojí živý iframe;
návrat znovu ověří oprávnění k publikaci. Stop zůstává hostitelský a přímý.

## 7. Akceptační měření

Technická kritéria: oddělené capabilities; žádné obejití přes URL/cache; správné
okamžiky zápisu; konflikty při změně kontrolovaného podkladu; obnova tří typů;
žádné provedení akce v neprováděcím preview; odmítnutí neznámého základu; dirty/saving
navigace; 403/409/503; retained sources versus chybějící archiv; použitelnost 360,
768 a 1440 CSS px; žádné horizontální přetékání celé obrazovky.

Plánované produktové měření s nejméně pěti lidmi bez znalosti implementace:

| Úkol | Navržená hranice pro první test |
|---|---|
| Rozhodnout o předložené agentově změně | Alespoň 4/5 správně pojmenují deklarovanou změnu a najdou změněnou rutinu do 2 minut; neměříme rychlost bezpečného review celého libovolného kódu |
| Dát Petrovi produce grant na Services | Alespoň 4/5 dokončí do 90 s; nikdo při tom omylem nevytvoří veřejný odkaz nebo token se správce jako vydavatelem |
| Vrátit včerejší stav | Alespoň 4/5 před potvrzením správně vysvětlí rozdíl mezi změnou živých panelů, obnovou konceptu a publikací historické aplikace |
| Najít Access a vrátit se na jinou Page | Alespoň 4/5 bez nápovědy do 30 s, včetně návratu z editoru |

Žádná z těchto měření dosud neproběhla. Vizuální prototyp pouze umožňuje scénáře
probrat a otestovat navigaci; není důkazem správného backendu.

## 8. Zadání pro další oponenturu

Posuď čtyři obrazovky prototypu, jejich mobilní variantu a tento kontrakt. Ověř
V01–V10 proti uvedenému commitu. Kritizuj zejména oddělení živé definice od konceptu,
review snapshot/fence, capability model a pravdivost stavů tokenů a preview.

Výstup: přijmout / přijmout s podmínkami / přepracovat; tabulka ID, závažnost,
konkrétní scénář, důkaz nebo předpoklad, oprava a test. Odděl produktovou preferenci
od chyby. Navrhni maximálně tři nutná rozhodnutí vlastníka. Neprováděj implementaci
ani zprávy do PR. Výslovně napiš, co nebylo ověřeno a které scénáře jsou jen simulace.
