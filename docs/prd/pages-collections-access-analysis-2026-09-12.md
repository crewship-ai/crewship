# Pages: kolekce, hromadné sdílení a sidebar — analýza po oponentuře

Datum: 2026-09-12, verze 2 (verze 1 je commit 4be01545). Stav: **P0 sidebar
schválen k implementaci v zúženém rozsahu; kolekce a delegované `manage`
zůstávají návrhem a potřebují druhé kolo.** Sledování: #2521. Základ:
`main` 84615795. Navazuje na `docs/prd/pages.md` §7.1, na #2502 a na PR
#2516.

Nezávislou oponenturu (OpenAI, 2026-09-12, nad commitem 4be01545) jsem
prověřil proti kódu; všech devět tvrzení o dnešním chování je pravdivých a
každý nález je zapracován níže. §0 říká, co se změnilo a proč. Tam, kde jsem
oponenta neposlechl, to stojí výslovně.

## 0. Co oponentura změnila

| # | Nález | Závažnost | Co se v návrhu změnilo |
|---|---|---|---|
| 1 | Chybí životní cyklus delegovaných grantů: grant vydaný držitelem `manage` musí zaniknout s jeho autoritou; dnešní `loadPageGrantRecordsIn` ověřuje vydavatele při každém použití a řetězce nezná | vysoká | Nové pravidlo §5/11: grant nese `authority` (čím byl vydavatel oprávněn) a je platný jen dokud ta autorita trvá; `manage` se v první verzi **nedá delegovat dál**. Testy: odebrání `manage`, jeho expirace, přesun stránky mimo spravovanou kolekci. |
| 2 | „Čtyři úrovně“ nejsou žebříček; kolekční `produce` by opravňovalo k zápisu do všech panelů i budoucích stránek; `mayProduce` dnes nekontroluje `canSeePanel` | vysoká | §5/3 popisuje oprávnění jako **množinu schopností**, ne stupně; `manage` z grantu je výslovně jiná věc než `canRole(role, "manage")`. **Kolekční `produce` v první verzi neexistuje**; produkce zůstává na stránce a panelech. |
| 3 | Přesun má rozpornou autoritu (§5/2 vs §5/6) a potvrzení nemá definovanou platnost; odebrání ze sbírky nesmí blokovat správce kolekce | vysoká | §5/2 přepsáno: přesun iniciuje jen vlastník stránky nebo admin; za cílovou kolekci přijímá vlastnická crew nebo držitel `manage` kolekce; potvrzení nese verzi členství a verzi grantů cíle a při změně vrací 409; odebrání z kolekce nikdy nepotřebuje souhlas kolekce. Věta „N lidí přestane stránku vidět“ se počítá ze všech zbývajících cest. |
| 4 | `batch_id` nestačí k bezpečnému vrácení dávky; UPSERT v `PutGrant` mění vydavatele | vysoká | §5/8 a §6: dávka zapisuje ke každé změně `change_id` a stav (created / unchanged / modified s předchozí hodnotou); vrácení kontroluje aktuální oprávnění volajícího a shodu verze grantu; dávka a její journal záznam vznikají v jedné transakci. Dávka není produktová entita. |
| 5 | Expirace přijímaná v P1, vynucovaná až v P2 | střední | Expirace **přesunuta celá do P1**; do té doby API `expires_at` odmítá (400). Journal `page.grant_expired` nese čas vypršení i čas zaznamenání a zapíše se jednou. S2 už neslibuje „přístup zmizí“, ale „tato cesta zmizí“. |
| 6 | Převod kolekce po smazání crew není dnešní pravidlo; „čeká na přiřazení“ odporuje povinnému vlastníkovi | střední | §5/1 přepsáno: smazání (i soft delete) crew, která vlastní kolekci, se **odmítá**, dokud kolekci někdo ručně nepřevede. Kdo jedná za crew: člen crew s rolí MANAGER+ ve workspace, nebo admin. |
| 7 | Čtení kolekce potřebuje vlastní pravidlo; dosah na jednu stránku nesmí odhalit ostatní ani ACL; prázdná kolekce musí být otevřitelná vlastníkovi | střední | Nové §5/12: běžný čtenář vidí jen stránky, na které dosáhne, a jejich počet; granty a journal kolekce vyžadují `manage` nebo vlastnictví; vlastník a admin otevřou i prázdnou kolekci; každá vazba ověřuje shodný workspace. |
| 8 | Efektivní přístup nevysvětlí včerejší stav; stránka může mít víc cest naráz; hledání podle příjemců nesmí prozradit ACL | střední | §5/10: vrací **množinu** cest, ne jeden důvod; historie odděleně z journalu (S4 zúženo); facet „sdíleno se mnou“ ukazuje jen vlastní cesty volajícího. |
| 9 | P0 není celé bez backendu (`reach` na drátě neexistuje); chybí pravidla sidebaru | střední | §7 rozděleno na **P0a navigace bez backendu** a **P0b serverový `reach`**; doplněna pravidla pro cizí osobní stránky, hledání ve sbalených sekcích, stabilní výběr a fokus, klávesnici, úzkou obrazovku a dlouhé názvy; hover-checkbox a drag-and-drop nejsou jediné ovládání. |

Standardy (oponentura §standardy): opraveno v §4. NIST RBAC neříká, že crew
má být jediná skupina lidí; je to produktové zjednodušení a tak je to
napsáno. SOC 2 CC6.2/CC6.3 podporují životní cyklus delegací, ne počet
úrovní. ISO odkaz je na 27001:2022 / 27002:2022, ne na starou strukturu
„A.9“, a dokument netvrdí shodu s normou.

Kde jsem oponenta neposlechl: nikde v pravidlech. V §7 nechávám drag-and-drop
jako doplněk vedle tlačítek, protože oponent žádal, aby nebyl *jedinou*
cestou, ne aby zmizel.

## 1. Rozhodnutí

1. **Sidebar P0a** (navigace bez backendu) jde do implementace hned.
2. **Sidebar P0b** (`reach` v list API a facet „sdíleno se mnou“) jde po
   něm, jako malý backendový krok nezávislý na kolekcích.
3. **Kolekce** jako jediný nový objekt: pojmenovaná skupina stránek
   vlastněná crew, jedna úroveň, stránka nejvýš v jedné kolekci.
4. **Granty na kolekci se dědí aditivně a jen dolů**; žádné deny.
5. **Pravidlo §7.1/2 zůstává nadřazené** viditelnosti panelů. Zápis do
   panelu (`produce`) se v první verzi přes kolekci neuděluje vůbec.
6. **Grant nese svou autoritu** a platí, jen dokud ta autorita trvá.
   `manage` z grantu není totéž co role manage a nedá se delegovat dál.
7. **Hromadné sdílení je dávka změn s trvalou evidencí každé z nich**,
   vrácení je samostatně autorizovaná operace nad konkrétními změnami.
8. **Expirace grantu** přichází s vynucováním, nikdy napřed.
9. **Efektivní přístup** je serverový výpočet množiny cest; historie je
   z journalu.
10. **Neskupinovat lidi mimo crew** (produktové zjednodušení, §4).

## 2. Dnešní stav, ověřeno v kódu

| Fakt | Kde |
|---|---|
| Stránka má právě jednoho vlastníka: `owner_user_id` xor `owner_crew_id`. Při odchodu uživatele se převádí; bez nástupce erasure **odmítne**, nikdy sirotek. | `docs/prd/pages.md` §7.1/1, 1b; `pages_transfer_owner.go`; `ON DELETE RESTRICT` |
| Viditelnost panelu = členství ve vlastnické crew nebo role manage; jinak sealed placeholder. | `pages_authz.go` `canSeePanel` |
| Dosah bez grantu: role manage, vlastník, člen vlastnické crew, člen crew vlastnící aspoň jeden panel. Jinak grant. | `pages_authz.go` `pageReachedWithoutGrant`, `canSeePage` |
| Granty `(page_id, subject user/crew/agent, subject_id, level read/produce/write, panel_ids?)`, `granted_by_user_id NOT NULL`. | migrace `20260812155322_pages.sql:240` |
| **Grant platí jen tehdy, když jeho vydavatel má dnes právo ho vydat**: `loadPageGrantRecordsIn` při každém použití ověřuje vydavatele proti vlastnictví, členství a roli. Řetězce delegace neexistují. | `pages_grants_authz.go:233–260` |
| **`produce` grant opravňuje k zápisu do pokrytých panelů bez ohledu na `canSeePanel`.** Viditelnost chrání čtení, ne zápis. | `pages_authz.go` `mayProduce` |
| `PutGrant` je UPSERT; opakované vydání **přepíše vydavatele a čas**. | `pages_grants.go:242–248` |
| Granty spravuje role manage nebo vlastník stránky. | `pages_grants.go` `mayAdministerGrants` |
| `write` neotevírá celý dokument bez viditelnosti všech panelů (#2502). | `pages_project_authoring.go` |
| Změny grantů se journalují (`EntryPageGrantAdded/Removed`). | `pages_grants.go:253, 383` |
| Veřejné odkazy: povinná `expires_at`, revokace. Granty expiraci nemají. | `page_public_tokens` |
| CLI: `page grant`, `page grants`, `page links`. Žádná dávka. | `cmd/crewship/cmd_page_grants.go` |
| Sidebar: hledání podle názvu a vlastníka, facety `states`, `owners`, jedna plochá sekce. **Na drátě není důvod dosahu.** | `hooks/use-pages.ts` `PageFilters`, `WirePage`; `pages-rail.tsx` |
| Seznam stránek načítá granty hromadně (`loadPageGrantRecordsIn(..., "")`), ne per stránku. | `pages_grants_authz.go` |

## 3. Scénáře

S1. Vedoucí Ops chce, aby crew Support četla všechny Ops stránky včetně
    budoucích. Jedna akce, jeden journal záznam s výčtem stránek.

S2. Admin vybere pět release stránek a dá crew QA `read` na 14 dní. Po
    uplynutí **tato cesta** zmizí; pokud QA dosáhne jinak (panel, jiný grant),
    stránku dál vidí a efektivní přístup to ukáže. Journal nese čas vypršení
    i čas, kdy to systém zaznamenal.

S3. Vlastník stránky s panelem crew Lookout dá crew Support `read` na
    kolekci. Support vidí stránku, panel Lookout jako sealed placeholder.

S4. Petr přestal vidět stránku. Admin otevře „Access“: **aktuální** cesty
    žádné. Historie z journalu ukáže: 2026-09-11 skončilo členství v crew
    Engine, které bylo jedinou cestou. Dokument neslibuje víc než to, co je v
    journalu.

S5. Agent nikdy nezíská dosah implicitně; grant kolekce pro agenta je
    jmenovitý.

S6. Crew, která vlastní kolekci, má být smazána: operace se odmítne, dokud
    kolekci někdo nepřevede. Kdo: člen crew s rolí MANAGER+ nebo admin.

S7. Petr má `manage` na kolekci Ops a dal Janě `read`. Vlastník Petrovi
    `manage` odebere. Janin grant přestane platit tím okamžikem, protože jeho
    autorita (`manage` Petra na Ops) zanikla. Totéž při expiraci Petrova
    `manage` a při přesunu stránky mimo Ops, pokud grant byl na stránku.

S8. Uživatel v sidebaru vidí stránky ve sbalitelných skupinách, hledání
    najde stránku i ve sbalené skupině, výběr a fokus přežijí přepnutí
    stránky, a na 360 px se nic neláme.

## 4. Proč kolekce a ne skupiny lidí

RBAC podle NIST rozlišuje uživatele, role, oprávnění a zvlášť skupiny; nic v
něm neříká, že skupina lidí má být jedna. Crewship má dnes tři místa, kde se
rozhoduje o lidech: workspace role, členství v crew a přímé granty. Čtvrté
místo (uživatelské skupiny) by přidalo další zdroj pravdy, který by musel
zůstat konzistentní s crew. **Je to produktové zjednodušení, ne norma.**
Chybějící kus je na straně objektů: nad stránkou dnes nic není. Hierarchie
zdrojů o jedné úrovni je nejmenší přidaná struktura, která scénář S1 řeší.

SOC 2 CC6.2/CC6.3 (autorizace, odebírání přístupu, nejmenší oprávnění,
oddělení povinností) podporují §5/11 (životní cyklus delegací) a §5/8
(audit dávky). Neříkají nic o počtu úrovní. ISO/IEC 27001:2022 a 27002:2022
(řízení přístupu) jsou relevantní rámec; tento dokument shodu s normou
netvrdí a neověřuje.

## 5. Pravidla (návrh §7.4 do `pages.md`)

1. **Kolekce má právě jednoho vlastníka a je to crew.** Smazání crew, včetně
   soft delete, se odmítá, dokud každou její kolekci někdo nepřevede na
   jinou crew. Za vlastnickou crew jedná člen crew s rolí MANAGER nebo vyšší,
   nebo admin workspace. Žádný automatický nástupce, žádný stav „čeká“.
2. **Stránka patří nejvýš do jedné kolekce, bez vnořování.** Přesun **do**
   kolekce iniciuje vlastník stránky nebo admin a **přijímá** vlastnická
   crew cílové kolekce nebo držitel jejího `manage`. Potvrzení nese
   `membership_version` stránky a `grants_version` cílové kolekce; změní-li
   se mezi zobrazením a zápisem, server vrátí 409 a UI ukáže nové důsledky.
   Odebrání **z** kolekce provádí vlastník stránky nebo admin a nepotřebuje
   souhlas kolekce: revokaci přístupu nesmí nikdo blokovat. Věta o dopadu
   („N lidí přestane stránku vidět“) se počítá ze všech zbývajících cest
   (§5/10), ne z jedné.
3. **Oprávnění je množina schopností, ne žebříček.** `read` = vidět stránku
   a panely, na které dosáhnu; `write` = měnit uspořádání (s omezením #2502);
   `produce` = zapisovat do vyjmenovaných panelů; `manage` = vydávat a
   odebírat granty. Žádná schopnost neobsahuje jinou. Grant `manage` je
   **jiná věc než workspace role manage** (`canRole(role, "manage")`): role
   dává vše, grant dává jen správu grantů. Grant na kolekci může nést `read`,
   `write` a `manage`; **`produce` na kolekci neexistuje** (panelový rozsah
   nelze stanovit pro stránky, které teprve vzniknou).
4. **Dědění je aditivní a jen dolů.** Efektivní schopnosti = sjednocení
   (role, vlastnictví, členství v crew vlastnící panel, platné granty
   stránky, platné granty kolekce). Žádné deny. Odebrat přístup = odebrat
   grant nebo členství.
5. **§7.1/2 je nadřazené.** Žádná cesta z bodu 4 nezpřístupní obsah panelu
   cizí crew mimo roli manage. `write` z kolekce podléhá
   `requireProjectDefinitions` stejně jako `write` ze stránky.
6. **Držitel `manage` vydává granty jen se schopnostmi, které má sám na
   témže objektu, a nikdy `manage`.** `read`+`manage` tedy vydá jen `read`.
   `manage` nemění vlastníka, nepřesouvá stránky a nevidí panely, které by
   jinak neviděl.
7. **Expirace je volitelná na grantu, povinná na veřejném odkazu.** Server
   vyhodnocuje čas svými hodinami (`evaluator().Now()`). Prošlý grant se
   ignoruje a při prvním dotyku se zapíše `page.grant_expired` s `expired_at`
   (čas vypršení) a `observed_at` (čas zápisu); zapisuje se jednou, ne při
   každém čtení. Přijímání `expires_at` a jeho vynucování jsou jedna dodávka.
8. **Hromadná akce je dávka změn, každá změna má identitu.** `POST
   grants:batch` zapíše pro každou stránku jednu z: `created`, `unchanged`,
   `modified` (s předchozím `expires_at` a vydavatelem). Dávka a její journal
   záznam vznikají v jedné transakci; jedna odmítnutá stránka odmítne celou
   dávku. Vrácení dávky je operace nad konkrétními `change_id`: vyžaduje
   **aktuální** oprávnění volajícího ke každé stránce, kontroluje, že grant
   je stále ve stavu, který dávka zanechala (verze), a `modified` vrací na
   předchozí hodnotu, `created` maže, `unchanged` nechává. Vydavatel bez
   dnešního oprávnění dávku nevrátí.
9. **Agent nikdy nezíská dosah implicitně.** Grant kolekce pro agenta je
   jmenovitý; agent nedědí nic přes crew, ve které běží.
10. **Efektivní přístup je serverový výpočet množiny cest.** Pro stránku a
    subjekt vrací všechny platné cesty (`role`, `owner`, `panel_crew:<crew>`,
    `grant:page:<change_id>`, `grant:collection:<change_id>`), ne jeden
    důvod. Historie je samostatný dotaz nad journalem (členství, role,
    granty, přesuny, expirace). Výpis nikdy nepojmenuje panel ani crew, které
    volající nevidí (stejný neutrální jazyk jako `pageWithheldChangeMessage`).
    Facet „sdíleno se mnou“ ukazuje jen cesty volajícího.
11. **Grant nese svou autoritu a platí, jen dokud trvá.** Každý grant
    zaznamená `authority`: `owner`, `role`, `manage:collection:<id>` nebo
    `manage:page:<id>`. Při vyhodnocení (rozšíření dnešní kontroly vydavatele
    v `loadPageGrantRecordsIn`) se ověří, že vydavatel autoritu **stále**
    má; jinak se grant ignoruje a journaluje jako `page.grant_orphaned`.
    `manage` se v první verzi **nedá delegovat**, takže řetězec má nejvýš
    dva články: vlastník/role → držitel `manage` → příjemce. Přesun stránky
    mimo kolekci, z jejíhož `manage` byl grant vydán, grant zneplatní.
12. **Čtení kolekce je filtrované.** Kolekci otevře každý, kdo dosáhne na
    aspoň jednu její stránku, a vidí jen stránky, na které dosáhne, a jejich
    počet, ne celkový. Granty a journal kolekce vidí vlastnická crew, držitel
    `manage` a admin; ti otevřou i prázdnou kolekci. Každá vazba (stránka ↔
    kolekce, grant ↔ kolekce) ověřuje shodný `workspace_id`.

## 6. Datový model a API (P1, po druhém kole oponentury)

```sql
CREATE TABLE page_collections (
  id            TEXT PRIMARY KEY,
  workspace_id  TEXT NOT NULL REFERENCES workspaces(id),
  slug          TEXT NOT NULL,
  name          TEXT NOT NULL,
  owner_crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE RESTRICT,
  grants_version INTEGER NOT NULL DEFAULT 0,   -- §5/2 potvrzení přesunu
  created_at    TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (workspace_id, slug)
);
ALTER TABLE pages ADD COLUMN collection_id TEXT REFERENCES page_collections(id) ON DELETE SET NULL;
ALTER TABLE pages ADD COLUMN membership_version INTEGER NOT NULL DEFAULT 0;

-- Společný tvar pro granty stránky i kolekce (page_grants se rozšíří o tytéž sloupce).
CREATE TABLE page_collection_grants (
  change_id          TEXT PRIMARY KEY,                       -- §5/8 identita změny
  collection_id      TEXT NOT NULL REFERENCES page_collections(id) ON DELETE CASCADE,
  subject_type       TEXT NOT NULL CHECK (subject_type IN ('user','crew','agent')),
  subject_id         TEXT NOT NULL,
  level              TEXT NOT NULL CHECK (level IN ('read','write','manage')),  -- bez produce, §5/3
  granted_by_user_id TEXT NOT NULL REFERENCES users(id),
  authority          TEXT NOT NULL,                          -- §5/11
  granted_at         TEXT NOT NULL,
  expires_at         TEXT,
  version            INTEGER NOT NULL DEFAULT 1,
  batch_id           TEXT,
  UNIQUE (collection_id, subject_type, subject_id, level)
);
-- page_grants: přidat change_id, authority, expires_at, version, batch_id; level += 'manage'.
-- page_grant_changes(batch_id, change_id, outcome created|unchanged|modified, previous_json)
-- pro vrácení dávky (§5/8).
```

Načtení kandidátů pro seznam (rozšíření `loadPageGrantRecordsIn`, ne
`grantsFor` per stránka; test hlídá konstantní počet dotazů pro 80 stránek):

```sql
SELECT p.id AS page_id, g.subject_type, g.subject_id, g.level,
       g.granted_by_user_id, g.authority, g.expires_at, 'page' AS origin, g.change_id
FROM pages p JOIN page_grants g ON g.page_id = p.id
WHERE p.workspace_id = :ws
UNION ALL
SELECT p.id, g.subject_type, g.subject_id, g.level,
       g.granted_by_user_id, g.authority, g.expires_at, 'collection', g.change_id
FROM pages p
JOIN page_collections c ON c.id = p.collection_id AND c.workspace_id = :ws
JOIN page_collection_grants g ON g.collection_id = c.id
WHERE p.workspace_id = :ws;
```

Nad kandidáty běží společná kontrola: expirace (§5/7), autorita vydavatele
(§5/11), a teprve pak sjednocení. Výpis všech subjektů kolekce je stránkovaný.

| Metoda a cesta | Kdo | CLI |
|---|---|---|
| `GET/POST /pages/collections`, `GET/PATCH/DELETE /pages/collections/{slug}` | čtení podle §5/12; zápis admin, vlastnická crew (MANAGER+), `manage` | `page collection list/create/show/update/delete` |
| `PUT/DELETE /pages/collections/{slug}/grants` (`read`, `write`, `manage`; `expires_at?`) | admin, vlastnická crew, `manage` podle §5/6 | `page collection grant/revoke` |
| `POST /pages/collections/{slug}/pages` `{page, membership_version, grants_version}` | iniciuje vlastník stránky/admin; přijímá cíl podle §5/2 | `page collection add` |
| `DELETE /pages/collections/{slug}/pages/{pageSlug}` | vlastník stránky nebo admin, bez souhlasu kolekce | `page collection remove` |
| `POST /pages/grants:batch` `{pages, subject, level, expires_at?}` (max 100) | `mayAdministerGrants` pro každou stránku; vše nebo nic | `page grant --pages a,b,c` |
| `POST /pages/grants/batches/{batch_id}:revert` | aktuální oprávnění ke každé dotčené stránce; verze musí sedět | `page grant revert-batch` |
| `GET /pages/{slug}/access` (množina cest per subjekt) | admin, vlastník, `manage` | `page access <slug>` |
| `GET /pages/{slug}/access/history` (journal) | totéž | `page access <slug> --history` |
| `GET /pages/access?subject=user:petr` | admin; uživatel sám na sebe | `page access --subject` |

Události: `page.collection.updated` invaliduje seznam stránek u všech
členů workspace; změna grantu kolekce se nesmí projevit jen na jedné
stránce.

## 7. Sidebar

**P0a — navigace, bez backendu (schváleno k implementaci)**

- Sbalitelné skupiny podle vlastníka (`SidebarSection` s počtem): „Mine“
  (stránky, které vlastním já), pak crew, jejichž jsem členem, pak ostatní
  crew, nakonec „Owned by others“ pro osobní stránky jiných lidí, ke kterým
  dosáhnu. Vlastník je v hlavičce skupiny, na řádku se neopakuje.
- Stavová tečka a počet panelů na řádku zůstávají.
- Hledání napříč skupinami: dokud je dotaz neprázdný, sbalené skupiny se
  dočasně rozbalí na výsledky a po smazání dotazu se vrátí do uloženého
  stavu. Skupina bez výsledku se skryje, ne vyprázdní.
- Stav sbalení uložený per uživatel a workspace (`localStorage` s klíčem
  `pages-rail:<workspace>:<user>`).
- Aktivní stránka je vždy vidět: je-li ve sbalené skupině, skupina se při
  výběru rozbalí; výběr a fokus přežijí přepnutí stránky a návrat z editoru
  (stejný uzel, viz test „keeps the rail mounted“).
- Klávesnice: šipky mezi řádky, Enter otevře, Left/Right sbalí/rozbalí
  skupinu; vše dosažitelné bez myši.
- Úzká obrazovka (360 px) a dlouhé názvy: řádek jednořádkový s `truncate`,
  celý název v `title` a v hlavičce stránky; hlavička skupiny neláme.
- Vše ze `sidebar-kit`; žádná nová komponenta.

Akceptace P0a: Vitest na řazení skupin, hledání ve sbalených, uložení
stavu, klávesnici; Playwright na 360/768/1440 px bez horizontálního
přetečení; „keeps the rail mounted“ zůstává zelený.

**P0b — serverový `reach` (malý backendový krok, nezávislý na kolekcích)**

- List API vrací u každé stránky `reach: ["owner" | "role" | "crew:<slug>" |
  "panel_crew:<slug>" | "grant"]` pro **volajícího**. Nic o jiných lidech.
- Facet „Shared with me“ = `reach` obsahuje jen `grant`.
- Žádný dotaz navíc: `reach` se odvodí z toho, co `List` už načítá.

**P1 — s kolekcemi**

- Skupiny podle kolekce, „Unfiled“ pro stránky bez kolekce.
- Výběr více řádků: checkbox viditelný trvale v režimu výběru (tlačítko
  „Select“ v toolbaru), Shift-klik, klávesnice (Space); drag-and-drop jen
  jako doplněk. Lišta akcí: Share, Move to collection, Export.
- Share otevře stejný formulář jako Access sekce editoru, s výčtem stránek,
  volitelnou expirací a náhledem „kdo nově dosáhne“ z §5/10.
- Přesun ukáže před potvrzením dopad ze všech cest a nese verze z §5/2.
- Access sekce: blok „Inherited from collection“ (jen ke čtení, s odkazem) a
  „Effective access“ (§5/10).

## 8. Bezpečnostní hrany pro druhé kolo

- Eskalace přes `manage`: `read`+`manage` vydá `write` → 403, nic se
  nezapíše; `manage` nevydá `manage`.
- Zánik autority (§5/11): test odebrání, expirace, přesunu; grant zmizí
  z efektivního přístupu bez dalšího zásahu a journal to zapíše jednou.
- Kolekce jako obcházení #2502: `write` z kolekce bez členství v crew
  panelu → `GET /project` 403.
- Přesun mění dosah: 409 při změně verzí; dopad ze všech cest; odebrání
  z kolekce nikdy neblokuje kolekce.
- Dávka: atomická; vrácení s aktuálním oprávněním a verzí; `modified` se
  vrací na předchozí hodnotu, ne maže.
- Expirace: serverový čas, test s falešnými hodinami; jednorázový journal.
- Efektivní přístup a čtení kolekce neprozradí sealed panel ani cizí ACL.
- Veřejné odkazy zůstávají na stránce; kolekce veřejný odkaz nemá.
- Rate limit na `grants:batch` a `pages/access`; stránkování výpisů.
- Konstantní počet dotazů pro seznam 80 stránek (test).

## 9. Co záměrně nenavrhuji

Deny granty; vnořené kolekce; skupiny uživatelů mimo crew; `produce` na
kolekci; delegování `manage`; ABAC podmínky; automatické zařazování do
kolekcí; veřejný odkaz na kolekci.

## 10. Fáze a akceptace

| Fáze | Obsah | Stav | Akceptace |
|---|---|---|---|
| P0a | Sidebar: skupiny podle vlastníka, hledání ve sbalených, uložený stav, klávesnice, úzká obrazovka | **schváleno** | §7 P0a |
| P0b | `reach` v list API, facet „Shared with me“ | schváleno, po P0a | žádný dotaz navíc; jen cesty volajícího |
| P1 | Kolekce, dědění `read/write/manage`, autorita grantů, dávka s `change_id`, expirace s vynucováním, CLI | **druhé kolo oponentury** | Go testy S1, S3, S5, S6, S7, eskalace, atomická dávka, zánik autority, falešné hodiny; akceptační test CLI; mutace: odstranění JOINu kolekce shodí S1, odstranění kontroly autority shodí S7 |
| P2 | Efektivní přístup (množina cest), historie z journalu, UI Access | po P1 | test, že výpis neprozradí sealed panel; konstantní počet dotazů |

Produktové měření: 4 z 5 lidí bez znalosti implementace najdou stránku ve
sbalené skupině hledáním do 20 s (P0a); dají crew přístup ke třem stránkám
najednou do 60 s a správně řeknou, co se stane se stránkou přidanou do
kolekce zítra (P1).

## 11. Otevřené otázky pro druhé kolo

1. Stačí `authority` jako řetězec, nebo má odkazovat na konkrétní
   `change_id` grantu `manage`, ze kterého byl odvozen (přesnější zánik)?
2. Má `page_grant_changes` žít v journalu, nebo ve vlastní tabulce, když
   journal není určen ke zpětnému čtení pro autorizaci?
3. Je pro P0b bezpečné vracet `panel_crew:<slug>` v `reach`, když název
   crew vlastnící panel dnes sealed placeholder už nese?
4. Jak se `manage` na kolekci chová k novým stránkám přidaným po vydání
   grantu (dědí se na ně jeho `read`, ale má držitel `manage` právo je
   sdílet dál)? Návrh: ano, protože §5/11 váže platnost na trvání `manage`,
   ne na okamžik přidání.
5. Potvrzení přesunu nese dvě verze; je to dost, nebo má nést i verzi
   grantů zdrojové kolekce?
