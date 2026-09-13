# Oprávnění složek Pages po vzoru Linuxu — analýza

Datum: 2026-09-13. Stav: **analýza, nic z §4–§7 není implementováno.** Navazuje
na `pages-collections-access-analysis-2026-09-12.md` v3 (dále „PRD v3“) a
nahrazuje jeho fáze F3–F5 jednodušším modelem. Sledování: #2521.

Zadání od vlastníka produktu: *„když stránku přesunu do složky, dávám právo
i té složce; kdo má oprávnění do složky, má ho na všechno, co v ní je, jako
v Linuxu; jednodušší a smysluplnější.“*

## 1. Co už existuje (k 2026-09-13, nasazeno na dev3 jako kombinace větví)

| Kus | Kde | Co umí |
|---|---|---|
| Složky | PR #2531 (backend), #2529 (frontend) | vytvořit / přejmenovat / ikonka a barva / smazat prázdnou; stránku přidat, přesunout, odebrat s verzemi a 409; složka má vlastnickou crew; sidebar podle složek, Nezařazené, menu řádku, dialogy, klávesnice; CLI `page folder …`, `page move` |
| Dosah stránky | PR #2526 | `reach` na každém řádku seznamu: `owner`, `role`, `crew:`, `panel_crew:`, `grant` |
| Efektivní přístup | PR #2530 | kdo dosáhne na stránku a kudy; jen ke čtení; skryté crew nikdy nepojmenuje |
| Sidebar podle vlastníka | PR #2525 | dnešní skupiny MINE / OPS / … (crew, ne složky) |
| Granty na stránku | v `main` od srpna | subjekt user / crew / agent, úrovně read / produce / write, `produce` zúžené na panely; vydává vlastník nebo admin |

**Složka dnes nemá žádná oprávnění.** Přesun stránky do složky nic nemění na
tom, kdo ji vidí. To je záměr fáze F1 (bez nové autorizační sémantiky) a
přesně to, co vám na dev3 chybí.

## 2. Co PRD v3 navrhovalo pro oprávnění složek a proč je to složité

PRD v3 (§5, F3–F5) mělo tři schopnosti složky (`read`, `write`, `manage`),
delegovatelnou správu sdílení přes `manage` s autoritou vázanou na konkrétní
grant, expiraci grantů, hromadné dávky s evidencí změn a jejich vracení.
Oponentura na tom našla devět nálezů a většina z nich pramení z jednoho
zdroje: **delegace `manage`** (životní cyklus odvozených grantů, oživení po
novém vydání, eskalace, vracení dávek). Linuxový model tuto část nemá vůbec:
práva mění vlastník a root, nikdo jiný. Tím padá nejméně pět z devíti nálezů
a tři z pěti plánovaných PR.

## 3. Mapování na Linux, přesně

| Linux | Crewship | Poznámka |
|---|---|---|
| adresář | složka | jedna úroveň; `/` = workspace |
| soubor | stránka | |
| vlastník souboru/adresáře | vlastník stránky (user xor crew), vlastnická crew složky | jako dnes |
| root | workspace ADMIN / OWNER (`canRole(role,"manage")`) | jako dnes |
| skupina | crew | crew je jediná skupina lidí, jako v PRD |
| `o` (others) | **workspace** — všichni členové workspace | **nový druh subjektu**; dnes chybí a je to největší praktická díra: „ať to vidí všichni“ dnes znamená N grantů |
| `r` na adresáři | vypsat stránky složky (názvy) | filtrováno podle práv na soubory, viz níže |
| `x` na adresáři | vstoupit, otevřít stránku uvnitř | v UI se `r` a `x` neoddělují: **`r` složky = vidět a otevřít její stránky** |
| `w` na adresáři | přidat / odebrat / přesunout stránky, přejmenovat složku, ikonka | |
| `r` na souboru | vidět stránku a její panely | **panel má vlastní bity: §7.1/2 platí dál** (sealed placeholder) |
| `w` na souboru | upravovat uspořádání stránky (`write` grant) | |
| `chmod` | změna ACL složky nebo stránky | **jen vlastník nebo root**; žádné delegování |
| `chown` | převod vlastnictví | jako dnes, jen admin / vlastník |
| default ACL (`setfacl -d`) | práva složky se dědí na stránky uvnitř | to je to, co chcete: *„kdo má právo do složky, má ho na vše uvnitř“* |
| sticky bit | — | nezavádět |
| `umask`, setuid | — | nezavádět |

**Jedna vědomá odchylka od Linuxu:** v Linuxu `w` na adresáři nedává `w` na
souborech v něm. Vaše zadání říká, že právo do složky platí pro vše uvnitř.
Navrhuji proto, aby se **`r` i `w` složky dědily na stránky** (`r` složky =
`read` na každé stránce, `w` složky = `write` na každé stránce plus správa
členství). Je to model „default ACL“, ne přísný POSIX, a je srozumitelnější:
kdo smí spravovat složku, smí upravovat i její stránky. Co se **nedědí
nikdy**: `produce` (zápis dat do panelů, zůstává na stránce a panelech) a
viditelnost panelů cizí crew.

## 4. Model v jedné větě a v pravidlech

**Složka má ACL se záznamy `subjekt → {r, w}`, kde subjekt je user, crew
nebo workspace; ACL složky je výchozí ACL každé stránky v ní; ACL stránky
může přidat, nikdy odebrat; práva mění jen vlastník složky (crew, MANAGER+
člen) nebo admin.**

1. **Subjekty:** `user:<id>`, `crew:<slug>`, `workspace` (všichni členové).
   Agent jen jmenovitě jako dnes a jen na stránce; složkové ACL agenty
   neobsahuje (agent nesmí získat dosah implicitně, §7.1b).
2. **Práva složky:** `r` (vidět a otevřít stránky složky), `w` (přidávat,
   odebírat, přesouvat stránky; přejmenovat; ikonka; plus `write` na
   stránkách uvnitř). Nic dalšího. Bez `manage`, bez `produce`, bez expirace.
3. **Dědění:** efektivní práva subjektu na stránce = sjednocení(role admin,
   vlastnictví stránky, členství v crew vlastnící panel, ACL stránky, ACL
   složky). Aditivní, jen dolů, žádné deny.
4. **Panely:** `r` z čehokoli výše otevře stránku; obsah panelu vidí jen
   člen jeho vlastnické crew nebo admin (§7.1/2). Beze změny.
5. **Kdo mění ACL složky:** vlastnická crew (člen s rolí MANAGER+) nebo
   admin. Kdo mění ACL stránky: vlastník stránky nebo admin (jako dnes).
   **Žádná delegace.** Potřebuje-li někdo spravovat sdílení, je to důvod
   udělat ho MANAGERem v crew, ne vymýšlet čtvrté právo.
6. **Přesun stránky do složky = změna jejích výchozích práv.** Iniciuje
   vlastník stránky nebo admin; cílová složka musí volajícímu dávat `w`
   (nebo je admin / MANAGER+ vlastnické crew). Dialog před potvrzením ukáže
   ACL cílové složky větou: „Ve složce Ops stránku uvidí crew Support a
   všichni ve workspace; crew Ops ji bude moci upravovat.“ Potvrzení nese
   `pages_version` stránky a `acl_version` složky; server zastaralé
   potvrzení odmítne 409 (mechanismus z #2531 zůstává, jen `grants_version`
   se přejmenuje na `acl_version`).
7. **Odebrání ze složky** provede vlastník stránky nebo admin kdykoli;
   stránka tím ztratí zděděná práva. Držitel `w` složky může odebrat
   stránku ze složky také (je to `w` na adresáři), ale nemůže ji tím
   přesunout jinam ani smazat.
8. **Smazání složky** jen prázdné (jako `rmdir`).
9. **Hromadné sdílení** = přesun více stránek do sdílené složky. Žádný
   samostatný endpoint pro dávky grantů, žádné vracení dávek; „vrátit“ =
   přesunout zpět. Evidence je journal přesunu s před/po.
10. **Vše, co UI ukazuje, počítá server:** seznamy filtrované na dosažitelné
    stránky, počty jen dosažitelných, efektivní přístup jako množina cest
    (`folder:<slug>` přibude k `owner | role | crew | panel_crew | grant`).
11. **`workspace` jako subjekt** má stejnou váhu jako ostatní: složka
    „r pro workspace“ = každý člen ji vidí. To je nejčastější reálný případ
    (statusové stránky) a dnes nejde bez N grantů.

## 5. Co se tím zjednoduší proti PRD v3

| PRD v3 | Linuxový model |
|---|---|
| 3 schopnosti složky (`read`, `write`, `manage`) | 2 práva (`r`, `w`) |
| delegace `manage`, autorita grantu, `change_id`, oživení po novém vydání | neexistuje; mění jen vlastník a root |
| expirace grantů, journal `grant_expired` | neexistuje (veřejné odkazy mají expiraci dál) |
| dávky grantů, `page_grant_changes`, vracení | neexistuje; hromadně = přesun do složky |
| 5 PR (F1–F5) | F1 hotovo + **jedno PR F3′** (ACL složky + dědění + dialog přesunu + Access) |
| 9 nálezů oponentury | odpadají 1, 2 (částečně), 4, 5; zůstávají 3 (přesun), 7 (filtrované čtení), 8 (efektivní přístup), a dva nové v §7 |

Co se **nezjednoduší** a zůstává: pravidlo o panelech, jmenovité granty
agentům, filtrované seznamy a počty, verze při přesunu, efektivní přístup
počítaný serverem.

## 6. Datový model a API (návrh F3′)

```sql
-- page_folders: přejmenovat grants_version → acl_version
CREATE TABLE page_folder_acl (
  folder_id     TEXT NOT NULL REFERENCES page_folders(id) ON DELETE CASCADE,
  subject_type  TEXT NOT NULL CHECK (subject_type IN ('user','crew','workspace')),
  subject_id    TEXT NOT NULL DEFAULT '',     -- '' pro workspace
  can_read      INTEGER NOT NULL DEFAULT 0 CHECK (can_read IN (0,1)),
  can_write     INTEGER NOT NULL DEFAULT 0 CHECK (can_write IN (0,1)),
  set_by_user_id TEXT NOT NULL REFERENCES users(id),
  set_at        TEXT NOT NULL,
  PRIMARY KEY (folder_id, subject_type, subject_id)
);
-- page_grants: přidat subject_type 'workspace' (subject_id ''), jinak beze změny.
```

Vyhodnocení: `loadPageGrantRecordsIn` načte jedním `UNION ALL` granty
stránek a ACL složek přes `pages.folder_id`, sjednotí; `pageReach` přidá
`folder:<slug>`; seznam zůstává na konstantním počtu dotazů (test z #2526).
`write` ze složky podléhá stejné kontrole úplného dokumentu jako `write` ze
stránky (#2502): nevidím-li panel cizí crew, dokument neotevřu.

| Metoda a cesta | Kdo | CLI |
|---|---|---|
| `GET /page-folders/{slug}/acl` | vlastnická crew, admin, kdo má `r` (vidí jen záznamy, ne cizí ACL stránek) | `page folder acl <slug>` |
| `PUT /page-folders/{slug}/acl` `{subject, r, w}` (idempotentní, jako `setfacl -m`) | vlastnická crew MANAGER+, admin | `page folder chmod <slug> <subject> [+r|-r] [+w|-w]` |
| `DELETE /page-folders/{slug}/acl/{subject}` (`setfacl -x`) | totéž | `page folder chmod <slug> <subject> -rw` |
| `POST /page-folders/{slug}/pages` — beze změny tvaru, `grants_version` → `acl_version`; odpověď při 409 nese ACL cíle | vlastník stránky/admin + `w` cíle | `page move` |
| `GET /pages/{slug}/access` (existuje, #2530) + cesta `folder:<slug>` | jako dnes | `page access` |

## 7. Bezpečnostní body, které Linuxový model neřeší sám a musí je uzavřít F3′

1. **Přesun do složky s `workspace: r`** zveřejní stránku všem členům
   jedním krokem. Dialog to musí říct výslovně („uvidí všichni ve
   workspace“) a potvrzení nese `acl_version`; změna ACL po zobrazení
   dialogu → 409.
2. **`w` složky = `write` na stránkách:** držitel `w` složky získá právo
   měnit uspořádání stránek, které nevlastní. To je vaše zadání; dokument to
   pojmenovává jako vědomou odchylku od POSIX (§3). Kontrola #2502 platí,
   takže nikdy neuvidí ani nesmaže panel cizí crew.
3. **Filtrované čtení složky** (nález 7): kdo nemá `r` složky, ale dosáhne na
   jednu její stránku vlastním grantem, vidí ve složce jen tu stránku a počet
   1; složka bez jediné dosažitelné stránky a bez `r` odpoví 404.
4. **Efektivní přístup nepojmenuje panel cizí crew** (řešeno v #2530).
5. **Agent** nikdy přes složku; `page_folder_acl` agenty nepřijímá (CHECK).
6. **Souběh přesun vs. `chmod`:** `acl_version` na složce, CAS jako dnes.
7. **Odebrání `w` složky** okamžitě odebere zděděné `write` na stránkách;
   žádný stav k invalidaci, protože práva se počítají při každém požadavku
   (dnešní `loadPageGrantRecordsIn` je bez cache).

## 8. Obrazovky

- **Složka → záložka Sharing** vypadá jako `ls -l` v tabulce: první řádek
  vlastnická crew (rw, nelze odebrat), pak záznamy ACL se dvěma zaškrtávátky
  `r` `w`, poslední řádek „Everyone in this workspace“ s `r` `w`. Přidat
  záznam: výběr user / crew. Věta nad tabulkou: „Práva složky platí pro
  všechny stránky v ní, včetně těch, které do ní přibudou. Panely vlastněné
  jinou crew zůstanou zapečetěné.“
- **Přesun** (dialog z #2529) dostane blok „Po přesunu“ s větou z §4/6 a
  po 409 se přegeneruje.
- **Access sekce stránky:** *Direct* (dnešní granty), *From folder Ops*
  (jen ke čtení, s odkazem na Sharing složky), *Effective access* (#2530,
  přibude cesta `folder:ops`).
- **Sidebar:** u složky sdílené pro workspace malý symbol (jako `o+r`),
  aby bylo na první pohled vidět, co je veřejné v rámci workspace.

## 9. Doporučení a další krok

Nahradit F3–F5 z PRD v3 jedním PR **F3′** podle §4–§8, s testy: A6′ `w`
složky umí přidat/odebrat stránku a upravit stránku uvnitř, ne smazat
složku ani změnit ACL; A7′ odebrání `w` složky okamžitě odebere zděděné
`write`; A8 sealed panel přes složku zůstává zapečetěný a `GET /project`
403; A9 filtrované čtení; A11 přesun se zastaralou `acl_version` 409 s ACL
cíle v odpovědi; A14 konstantní počet dotazů; `workspace` subjekt viditelný
pro každého člena a pro nikoho mimo workspace.

Body §7/1 a §7/2 jsou jediné, které bych před implementací dal k cílené
oponentuře; zbytek je dnešní model rozšířený o jednu tabulku.

Otevřené volby pro vás: (a) má `w` složky dědit `write` na stránky (návrh
ano, podle zadání), nebo se držet POSIX a nechat `w` jen pro členství; (b)
má existovat `workspace` i jako subjekt grantu na stránce (návrh ano, kvůli
symetrii); (c) má být „Everyone in this workspace: r“ výchozí pro novou
složku (návrh ne, výchozí je jen vlastnická crew, jako `umask 077`).
