# Pages: Složky, sdílení a sidebar — návrh v3

Datum: 2026-09-13, verze 3. Historie: v1 (4be01545) → oponentura OpenAI
2026-09-12 (devět nálezů) → v2 (46e63492) → validace implementace sidebaru
2026-09-13 (čtyři nálezy, všechny opraveny, viz §0) → **v3: zadání „vlastní
složky s ikonkami a skutečnou správou oprávnění“**. Sledování: #2521.
Základ: `main` 84615795 plus otevřené PR uvedené v §0.

V rozhraní se objekt jmenuje **Složka** (anglicky *Folder*); interně a v API
zůstává `collection`. Složka je skupina stránek, nikdy skupina lidí.

## 0. Co už existuje a co je tento dokument

| Věc | Stav | Hlava |
|---|---|---|
| Sidebar seskupený podle vlastníka, hledání přes sbalené skupiny, uložený stav, klávesnice, facet „Shared with me“ (P0a) | PR #2525, CI zelené, ruční review v PR; validace 2026-09-13 našla dvě chyby, **opraveny** (výběr druhé stránky ve sbalené skupině; sbalování během hledání) | `f87845f5` |
| `reach` v list API pro volajícího, seznam 7 dotazů pro libovolný počet stránek (P0b) | PR #2526, CI zelené, ruční review v PR | `2ec8a71b` |
| Editační režim přes celou stránku, přepínač Application/Panels | PR #2516; validace 2026-09-13 nález 4 (hlášení dostupnosti v `useEffect`) **opraven** přechodem na `useLayoutEffect` | `ce8b36e6` |
| `dev.sh status` hlásí zastaralý build | PR #2518; validace nález 3 (nahrazená binárka čtená přes `readlink -f`) **opraven** čtením `/proc/<pid>/exe` | `7dfc4863` |
| Tento dokument | PR #2522 | tento commit |

Seskupení podle vlastníka v #2525 **není** cíl; je to navigace nad dnešními
daty. Cíl jsou složky z §3–§6. Nic z §3–§6 není implementováno.

Nálezy druhého kola oponentury (v2 §11) zatím nedorazily; §9 je proto
seznam bezpečnostně neuzavřených bodů, které jdou k cílené oponentuře
**před** implementací sdílení složek. Části bez nové autorizační sémantiky
(§10, PR F1 a F2) lze stavět hned.

## 1. Rozhodnutí

1. **Složka** = pojmenovaná skupina stránek vlastněná jednou crew, s ikonkou
   z existující sady `CREW_ICONS` (osm ikon, `lib/crew-icons.ts`) a
   volitelnou barvou, stejný výběr jako u crew (`CrewIconPickerDialog`).
2. **Jedna úroveň, stránka nejvýš v jedné složce.** Stránky mimo složku
   jsou v sekci **Nezařazené** (*Unfiled*).
3. **Tlačítka a klávesnice jsou plnohodnotné ovládání** přesunu i výběru;
   drag-and-drop je doplněk a nikdy jediná cesta.
4. **Oprávnění složky jsou tři oddělené schopnosti:** *číst* (`read`),
   *upravovat* (`write`), *spravovat sdílení* (`manage`). Není to žebříček;
   `manage` bez `read` je platná, i když neobvyklá kombinace, a UI ji
   pojmenuje.
5. **Sdílení složky nemění viditelnost panelů.** Pravidlo §7.1/2 platí beze
   změny; dialog sdílení to řekne před potvrzením.
6. **Žádné `produce` na složce.** Zápis dat zůstává grantem na stránku a
   její panely.
7. **`manage` z grantu není role administrátora workspace** a **nedá se
   delegovat**. Grant vydaný držitelem `manage` nese odkaz na konkrétní
   grant `manage`, ze kterého byl odvozen; zanikne s ním a nové vydání
   `manage` ho neoživí.
8. **Přesun stránky mění oprávnění.** Iniciuje ho vlastník stránky nebo
   admin; cílová složka ho přijímá autorizovaně; potvrzení nese verze a
   server zastaralý souhlas odmítne.
9. **Odebrání stránky ze složky nikdy nečeká na správce složky. Smazání
   složky je možné jen u prázdné složky.**
10. **Expirace grantu se přijímá až s vynucováním.** Do té doby API
    `expires_at` odmítá.
11. **Hromadné změny mají vlastní trvalou evidenci** (`page_grant_changes`),
    ne journal; vrácení kontroluje aktuální oprávnění a verze.
12. **Efektivní přístup se počítá na serveru a je potřeba už pro sdílení a
    přesuny** (náhled dopadu), proto je v dodávce před sdílením, ne po něm.
13. **Seznamy, počty, hledání a vysvětlení přístupu nikdy neodhalí
    nedostupnou stránku ani cizí ACL.**
14. **Dosah stránky přes `write`/`produce` zůstává** jak je; není to
    viditelnost panelů a dokument to nikde neslučuje.
15. **Objekt „projekt“ se nezavádí.** Složka pokrývá seskupení, sdílení a
    navigaci; projekt by přidal jen životní cyklus (stav, termín), pro který
    zatím nikdo nepředložil potřebu, kterou složka neřeší. Kdyby vznikla,
    složka je jeho podmnožina, ne konkurent.

## 2. Dnešní stav (ověřeno v kódu, doplněno o #2525/#2526)

| Fakt | Kde |
|---|---|
| Stránka: jeden vlastník (`owner_user_id` xor `owner_crew_id`); převod při odchodu uživatele; bez nástupce erasure odmítne. | `pages.md` §7.1/1, 1b; `pages_transfer_owner.go` |
| Viditelnost panelu = členství ve vlastnické crew nebo role manage; jinak sealed placeholder. | `pages_authz.go` `canSeePanel` |
| Dosah stránky: `pageReach` (od #2526): `owner`, `role`, `crew:<slug>`, `panel_crew:<slug>`, `grant`; `pageReachedWithoutGrant` = reach bez grantu neprázdný. | `pages_authz.go` |
| Granty `(page, subject user/crew/agent, level read/produce/write, panel_ids?)`, `granted_by NOT NULL`; platnost vydavatele se ověřuje při každém použití; UPSERT přepisuje vydavatele; žádné řetězce. | migrace `…_pages.sql:240`; `pages_grants_authz.go:233`; `pages_grants.go:242` |
| `produce` opravňuje k zápisu bez ohledu na `canSeePanel`. | `pages_authz.go` `mayProduce` |
| Granty spravuje role manage nebo vlastník stránky. | `pages_grants.go` `mayAdministerGrants` |
| Veřejné odkazy s povinnou expirací; granty expiraci nemají. | `page_public_tokens` |
| Seznam stránek: viewer, panely celého workspace, granty hromadně, slugy crew: 7 dotazů. | `pages_handler.go` `List`, `loadPanelsIn`, `loadCrewSlugs` (#2526) |
| Sidebar: skupiny podle vlastníka z `reach`, hledání, uložený stav `pages-rail:<ws>:<user>`, klávesnice po stromu. | `pages-rail.tsx`, `use-pages.ts` `groupPagesByOwner` (#2525) |
| Ikonky a barva: `CREW_ICONS` (8), `CrewIconPickerDialog` (`icon`, `color`, `onSave`). Crew má sloupce `icon`, `color`. | `lib/crew-icons.ts`, `components/features/crews/crew-icon-picker-dialog.tsx` |
| Access sekce editoru: seznam grantů, formulář subjekt/úroveň/scope, tokeny, veřejné odkazy. | `section-access.tsx`, `use-page-grants.ts` |
| CLI: `page list/get/create/update/grant/grants/links/...`; `page project …`. | `cmd/crewship/cmd_page*.go` |

## 3. Obrazovky

Vše ze sdílených komponent: `sidebar-kit` (rail), `SectionCard`, `Dialog`,
`CrewIconPickerDialog` (přejmenovaný na obecný `IconPickerDialog` bez změny
chování), `AlertDialog` pro potvrzení, formulář grantu z Access sekce.

**S-1 Sidebar se složkami.** Sekce = složky (ikonka, barva jako tečka před
názvem, počet *dostupných* stránek), pak **Nezařazené**. Sbalitelné, stav
uložený jako dnes. Hledání otevírá sbalené složky na shody, bez zápisu
stavu (#2525). Klávesnice: strom jako dnes. Kontextové menu řádku
(tlačítko „⋯“ i Shift+F10): *Move to folder…*, *Remove from folder*.
Tlačítko „New folder“ v hlavičce railu (jen pro koho §4 dovolí). Seskupení
podle vlastníka zůstává jako přepínač zobrazení „Group by: Folder | Owner“
v Filter popoveru; výchozí Folder.

**S-2 Nová složka / Přejmenovat / Ikonka.** Dialog: název (povinný, unikátní
slug ve workspace), vlastnická crew (výběr z crew, kde mám MANAGER+; admin
z každé), ikonka + barva (`IconPickerDialog`). Přejmenování a změna ikonky
tentýž dialog.

**S-3 Smazání složky.** `AlertDialog`, povolené jen pro prázdnou složku;
jinak tlačítko disabled s větou „Move its N pages out first“ (N = počet
stránek, které volající *vidí*; pokud existují i nedostupné, věta říká
„and pages you cannot see“ bez počtu, §1/13).

**S-4 Přesun stránky.** Z railu (menu, klávesnice), z hromadné lišty
(výběr více řádků, tlačítko *Select* v toolbaru; Shift-klik; Space) a z
Content sekce editoru („Folder: Ops · Change…“). Dialog ukáže cíl, a **před
potvrzením dopad**: „Po přesunu stránku uvidí navíc: crew Support (read),
Petr (write). Přestanou ji vidět: nikdo.“ z §6 efektivního přístupu, plus
větu „Panely crew Lookout zůstanou pro ostatní zapečetěné“. Potvrzení nese
verze (§5/8); při 409 dialog zobrazí nový dopad a žádá znovu.

**S-5 Složka: Sdílení.** Stránka složky (klik na hlavičku → ozubené kolo)
se dvěma kartami: *Pages* (dostupné stránky) a *Sharing* (granty složky:
subjekt, schopnosti jako tři checkboxy read/write/manage, vydal, kdy,
expirace až s P-F5). Formulář nad tím s větou: „Sdílení složky dává přístup
ke stránkám ve složce, včetně stránek přidaných později. Panely vlastněné
jinou crew zůstanou zapečetěné.“ Držitel `manage` bez `read` vidí kartu
Sharing, ne Pages.

**S-6 Access sekce stránky.** Tři bloky: *Direct* (dnešní granty),
*Inherited from folder Ops* (jen ke čtení, odkaz na S-5, jen pro toho, kdo
smí číst granty složky; ostatním jen věta „Some access comes from the
folder“), *Effective access* (kdo dosáhne a kterou cestou; §6).

## 4. Oprávnění jednotlivých operací

| Operace | Kdo | Poznámka |
|---|---|---|
| Vytvořit složku | admin; člen crew s rolí MANAGER+ ve workspace (vlastnická crew = ta jeho) | crew je vlastník od vzniku |
| Přejmenovat, ikonka, barva | admin; vlastnická crew (MANAGER+); držitel `write` na složce | `write` = upravovat složku, ne stránky |
| Smazat složku | admin; vlastnická crew (MANAGER+) | jen prázdná (žádná stránka, ani nedostupná volajícímu) |
| Přidat / přesunout stránku do složky | **iniciuje** vlastník stránky nebo admin; **přijímá** admin, vlastnická crew cíle (MANAGER+) nebo držitel `manage` cíle | v1: volající musí mít obě autority najednou, jinak 403 s větou, kdo může; požadavek „ke schválení“ je v §12 |
| Odebrat stránku ze složky | vlastník stránky nebo admin | nikdy nečeká na složku |
| Číst složku (seznam) | kdo dosáhne aspoň na jednu její stránku, nebo drží jakýkoli grant složky, nebo vlastnická crew, nebo admin | vidí jen dostupné stránky a jejich počet |
| Číst granty složky | admin; vlastnická crew; držitel `manage` | ostatní jen „some access comes from the folder“ |
| Vydat / odebrat grant složky | admin; vlastnická crew (MANAGER+); držitel `manage` v rozsahu vlastních schopností, nikdy `manage` | §5/6, §5/11 |
| Efektivní přístup stránky | admin; vlastník stránky; držitel `manage` stránky nebo její složky | jen cesty, žádná jména panelů cizí crew |
| Efektivní přístup subjektu (na co dosáhne) | admin; uživatel sám na sebe | |
| Hromadný grant / vrácení | `mayAdministerGrants` na každou stránku, v okamžiku volání | §5/8 |

Workspace role VIEWER nikdy nic z tabulky nemění; MEMBER jen jako vlastník
stránky nebo držitel grantu.

## 5. Pravidla (§7.4 do `pages.md`, verze 3)

1. **Složka má právě jednoho vlastníka a je to crew.** Smazání crew (i soft
   delete) se odmítá, dokud nejsou její složky převedeny; za crew jedná
   MANAGER+ člen nebo admin.
2. **Jedna úroveň, stránka nejvýš v jedné složce.** Bez vnořování.
3. **Schopnosti složky: `read`, `write`, `manage`; bez `produce`.** `read` =
   vidět stránky složky (panely podle §7.1/2), `write` = upravovat složku
   (název, ikonka), `manage` = spravovat granty složky. Žádná neobsahuje
   jinou. Grant `manage` ≠ role manage.
4. **Dědění aditivní, jen dolů.** Efektivní schopnosti subjektu na stránce =
   sjednocení(role, vlastnictví, členství v crew vlastnící panel, platné
   granty stránky, `read` ze složky). `write` složky se na stránku
   **nedědí** (upravovat složku ≠ upravovat dokument stránky); `manage`
   složky dává právo vydávat granty *složky*, ne granty stránek.
5. **§7.1/2 je nadřazené.** Žádná cesta z bodu 4 neodhalí panel cizí crew
   mimo roli manage.
6. **`manage` vydává jen schopnosti, které jeho držitel na složce sám má, a
   nikdy `manage`.** Nemění vlastníka, nepřesouvá stránky, nemaže složku.
7. **Expirace** volitelná, serverové hodiny, `page.grant_expired` jednou
   (`expired_at`, `observed_at`). Přijímá se až s vynucováním (P-F5).
8. **Přesun nese verze.** Požadavek obsahuje `pages_version` stránky
   (mění se každou změnou členství stránky ve složce) a `grants_version`
   cílové složky (mění se každou změnou grantů složky). Server odmítne 409,
   pokud kterákoli nesedí, a odpověď nese aktuální dopad, aby UI ukázalo
   nový náhled. Odebrání ze složky nese jen `pages_version` stránky.
9. **Agent nikdy nezíská dosah implicitně.** Grant složky pro agenta je
   jmenovitý.
10. **Efektivní přístup** = množina cest per subjekt: `role`, `owner`,
    `panel_crew:<slug>`, `grant:page:<change_id>`,
    `grant:folder:<change_id>`. Historie zvlášť z journalu. Výpis nikdy
    nepojmenuje panel ani crew, které volající nevidí.
11. **Autorita grantu.** Každý grant nese `authority`: `owner` | `role` |
    `manage:<change_id>` (konkrétní grant `manage`, ze kterého vzešel). Při
    vyhodnocení se ověří, že vydavatel autoritu **stále má a je to táž**:
    u `manage:<change_id>` musí grant s tímto `change_id` existovat, být
    platný a patřit vydavateli. Odebrání a nové vydání `manage` vytvoří nový
    `change_id`, takže staré odvozené granty zůstanou mrtvé (nikdy se
    „neoživí“) a journal je zapíše jako `page.grant_orphaned`. Vydavatel s
    autoritou `owner`/`role` se ověřuje jako dnes (`loadPageGrantRecordsIn`).
12. **Čtení složky je filtrované** (§4). Počty jsou počty dostupných stránek.
    Hledání prohledává jen dostupné stránky. Každá vazba ověřuje
    `workspace_id`.
13. **Hromadné změny.** `page_grant_changes(change_id, batch_id, op
    created|unchanged|modified|reverted, previous_json, actor, at,
    version)` je zdroj pravdy pro vrácení; journal je jen záznam.

## 6. Datový model, API, CLI

```sql
CREATE TABLE page_folders (
  id             TEXT PRIMARY KEY,
  workspace_id   TEXT NOT NULL REFERENCES workspaces(id),
  slug           TEXT NOT NULL,
  name           TEXT NOT NULL,
  icon           TEXT,                       -- jméno z CREW_ICONS; NULL = výchozí "folder"
  color          TEXT,                       -- jako crews.color
  owner_crew_id  TEXT NOT NULL REFERENCES crews(id) ON DELETE RESTRICT,
  grants_version INTEGER NOT NULL DEFAULT 0, -- §5/8
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (workspace_id, slug)
);
ALTER TABLE pages ADD COLUMN folder_id TEXT REFERENCES page_folders(id) ON DELETE RESTRICT;
ALTER TABLE pages ADD COLUMN pages_version INTEGER NOT NULL DEFAULT 0;   -- členství ve složce

CREATE TABLE page_folder_grants (
  change_id           TEXT PRIMARY KEY,
  folder_id           TEXT NOT NULL REFERENCES page_folders(id) ON DELETE CASCADE,
  subject_type        TEXT NOT NULL CHECK (subject_type IN ('user','crew','agent')),
  subject_id          TEXT NOT NULL,
  level               TEXT NOT NULL CHECK (level IN ('read','write','manage')),
  granted_by_user_id  TEXT NOT NULL REFERENCES users(id),
  authority           TEXT NOT NULL,          -- owner | role | manage:<change_id>
  granted_at          TEXT NOT NULL,
  expires_at          TEXT,                   -- P-F5; do té doby NULL vynuceno CHECKem
  version             INTEGER NOT NULL DEFAULT 1,
  UNIQUE (folder_id, subject_type, subject_id, level)
);
-- page_grants: + change_id, authority, expires_at, version (stejný tvar).
CREATE TABLE page_grant_changes (
  change_id     TEXT NOT NULL,
  batch_id      TEXT NOT NULL,
  target        TEXT NOT NULL,               -- page:<id> | folder:<id>
  op            TEXT NOT NULL CHECK (op IN ('created','unchanged','modified','reverted')),
  previous_json TEXT,
  actor_user_id TEXT NOT NULL,
  at            TEXT NOT NULL,
  PRIMARY KEY (batch_id, change_id)
);
```

`ON DELETE RESTRICT` na `pages.folder_id`: složku nelze smazat, dokud má
stránky (§1/9), včetně těch, které volající nevidí.

Vyhodnocení: `loadPageGrantRecordsIn` načte granty stránek a granty složek
jedním `UNION ALL` (v2 §6), přidá kontrolu autority (§5/11) a expirace
(§5/7); `pageReach` dostane novou cestu `folder:<slug>`; seznam zůstává na
konstantním počtu dotazů (test z #2526 se rozšíří o složky).

| Metoda a cesta | Kdo (§4) | CLI |
|---|---|---|
| `GET/POST /pages/folders`; `GET/PATCH/DELETE /pages/folders/{slug}` | čtení filtrované; zápis vytvořit/přejmenovat/smazat | `page folder list/create/show/update/delete` |
| `POST /pages/folders/{slug}/pages` `{page, pages_version, grants_version}` | přesun/přidání (obě autority) | `page folder add <folder> <page>` / `page move <page> --folder` |
| `DELETE /pages/folders/{slug}/pages/{page}` `{pages_version}` | vlastník stránky/admin | `page folder remove` / `page move <page> --unfiled` |
| `GET /pages/folders/{slug}/grants`; `PUT/DELETE …/grants` | §4 | `page folder grants/grant/revoke` |
| `GET /pages/{slug}/access` (množina cest per subjekt), `…/access/history` | §4 | `page access <slug> [--history]` |
| `GET /pages/access?subject=…` | admin; sám na sebe | `page access --subject` |
| `POST /pages/folders/{slug}/pages:preview` `{page}` → dopad přesunu (kdo získá/ztratí) | kdo smí přesun iniciovat | `page move --dry-run` |
| `POST /pages/grants:batch`, `POST /pages/grants/batches/{id}:revert` | §5/13 | `page grant --pages …`, `page grant revert-batch` |

Každý endpoint má CLI příkaz a akceptační test nad binárkou; dokumentace v
`docs/api-reference/pages.mdx` a `docs/cli/page.mdx` jde ve stejném PR.

## 7. Konflikty a souběh

- **Přesun vs. změna grantů cíle:** 409 `folder_grants_moved`, odpověď nese
  nový dopad; UI ukáže a žádá znovu.
- **Přesun vs. přesun:** `pages_version` stránky; druhý dostane 409.
- **Smazání složky vs. přidání stránky:** `ON DELETE RESTRICT` + transakce;
  smazání selže 409, nikoli sirotek.
- **Odebrání `manage` vs. vydání odvozeného grantu:** grant nese
  `authority: manage:<change_id>`; vydání v transakci ověří existenci a
  platnost; odebrání po vydání grant zneplatní při dalším vyhodnocení.
- **Hromadná dávka:** jedna transakce; jakákoli odmítnutá stránka → 403 s
  výčtem, nic zapsáno.
- **Expirace:** serverový čas; test s falešnými hodinami.
- **Seznam a `page.updated`:** změna grantu složky vysílá
  `page.folder.updated`; klient invaliduje seznam stránek, ne jednu stránku.

## 8. Akceptační scénáře (testy)

Autorizace (Go, `internal/api`, každý s odmítnutou variantou):
A1 MEMBER bez MANAGER+ nevytvoří složku (403). A2 Přejmenování držitelem
`write` ano, držitelem `read` ne. A3 Smazání neprázdné složky 409 i pro
admina; prázdné ano. A4 Přesun: vlastník stránky bez autority cíle 403 s
větou; admin ano; vlastník stránky, který je MANAGER+ v cílové crew, ano.
A5 Odebrání ze složky vlastníkem stránky bez souhlasu složky 200.
A6 `manage` vydá `read` ano, `write` (které nemá) 403, `manage` 403.
A7 Odebrání `manage` → odvozený grant zmizí z efektivního přístupu; nové
vydání `manage` ho neoživí. A8 Stránka se sealed panelem sdílená přes
složku: příjemce vidí stránku, panel zapečetěný, `GET /project` 403.
A9 Čtení složky bez dosahu na žádnou stránku 404; s dosahem na jednu: jen
ta jedna a count 1. A10 Efektivní přístup nepojmenuje panel cizí crew.
A11 Přesun s zastaralou `grants_version` 409 a odpověď nese dopad.
A12 Expirace: prošlý grant ignorován, journal jednou (P-F5).
A13 Dávka: jedna odmítnutá stránka → nic zapsáno; vrácení `modified`
obnoví předchozí hodnotu; vrácení bez dnešního oprávnění 403.
A14 Seznam: konstantní počet dotazů se složkami (rozšíření testu z #2526).

UI (Vitest + Playwright kde je server): U1 složky v railu s ikonkou, barvou
a počtem; Nezařazené; hledání přes sbalené složky. U2 Přesun z klávesnice
(menu, Enter) bez myši. U3 Dialog přesunu ukáže dopad a po 409 nový.
U4 Sdílení: věta o panelech před potvrzením; tři checkboxy. U5 Access:
tři bloky, zděděné jen ke čtení. U6 360/768/1440 px bez horizontálního
přetečení; dlouhé názvy složek s `truncate` a `title`.

Produktové měření: 4 z 5 lidí bez znalosti implementace vytvoří složku,
přesunou do ní tři stránky a nasdílí ji crew do 90 s; 4 z 5 správně
popíší, co se stane s panelem cizí crew.

## 9. Bezpečnostně neuzavřené body k cílené oponentuře (před P-F3)

1. **Autorita jako `manage:<change_id>`** (§5/11): stačí to proti oživení
   po odebrání a novém vydání? Je správně, že změna *rozsahu* `manage`
   (např. z read+manage na write+manage) je nový `change_id` a odvozené
   granty zaniknou?
2. **`write` složky se nedědí na stránky** (§5/4): je to správná hranice,
   nebo má `write` složky dávat `write` na stránky, které nemají vlastní
   granty?
3. **Přesun vyžaduje obě autority v jedné osobě** (§4): nebude to v praxi
   znamenat, že přesouvá jen admin? Alternativa je požadavek ke schválení
   (§12).
4. **Filtrované čtení složky** (§5/12): unikne z počtu nebo z hledání
   informace o nedostupných stránkách (např. rozdíl mezi „prázdná“ a „nic
   pro tebe“)? Návrh: 404 pro složku bez dostupné stránky *a* bez grantu.
5. **Dopad přesunu jako náhled** (S-4): endpoint `pages:preview` vrací
   subjekty, které získají přístup; je to samo o sobě únik ACL cílové
   složky vlastníkovi stránky, který její granty jinak číst nesmí?
   Návrh: vrací jen počty a typy subjektů, jména jen tomu, kdo granty číst
   smí.
6. **`ON DELETE RESTRICT` na `pages.folder_id`** vs. smazání stránky
   (kaskáda z `pages` je v pořádku) a erasure uživatele (převod stránky
   složku nemění).

## 10. Dodávka v malých PR

| PR | Obsah | Autorizační sémantika nová? | Může začít |
|---|---|---|---|
| **F1 Složky** | tabulka `page_folders`, `pages.folder_id`, `pages_version`; CRUD s ikonkou/barvou; přidat/odebrat/přesunout (obě autority, verze); `folder` v list API; rail se složkami, Nezařazené, kontextové menu, dialog přesunu **bez náhledu dopadu** (složka zatím nemá granty, dopad je nulový); CLI; docs | ne (jen §4 řádky bez grantů) | hned |
| **F2 Efektivní přístup** | `GET /pages/{slug}/access`, `/access?subject`, Access sekce blok *Effective access* (cesty dnešních grantů, role, crew), CLI, konstantní dotazy; **žádné nové granty** | ne (jen čtení, §1/13) | hned, souběžně s F1 |
| **F3 Sdílení složky** | `page_folder_grants` s autoritou, dědění `read`, `manage` bez delegace, S-5, S-6 *Inherited*, náhled dopadu v S-4, `folder:` v `reach` | **ano** | po oponentuře §9 |
| **F4 Dávka** | `page_grant_changes`, `grants:batch`, `:revert`, hromadná lišta | ano | po F3 |
| **F5 Expirace** | `expires_at` + vynucování + journal | ano | po F3 |

F1 a F2 jsou nezávislé a mohou běžet paralelně (F1 backend + F1 frontend +
F2 backend). F3 sahá do `loadPageGrantRecordsIn` a čeká na §9.

## 11. Co záměrně nenavrhuji

Deny granty; vnořené složky; skupiny uživatelů mimo crew; `produce` na
složce; delegování `manage`; dědění `write` na stránky (§9/2); objekt
projekt (§1/15); veřejný odkaz na složku; automatické zařazování.

## 12. Zbývající rozhodnutí

1. Přesun jako požadavek ke schválení (iniciátor bez autority cíle vytvoří
   žádost, správce cíle přijme) — ano/ne, a kdy.
2. Ikona výchozí složky a zda „Nezařazené“ má vlastní ikonu.
3. Zda `write` složky umí i mazat prázdnou složku (dnes ne).
4. Zda facet „Group by: Owner“ zůstává po zavedení složek, nebo se skryje.
5. Odpovědi na §9 od oponentury.
