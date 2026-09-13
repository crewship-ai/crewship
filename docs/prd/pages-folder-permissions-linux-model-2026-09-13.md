# Děděná oprávnění složek Pages — návrh F3′ (verze 2)

Datum: 2026-09-13, verze 2 po oponentuře (OpenAI, nad commitem e543324e).
Stav: **schválený směr, k implementaci jako jedno PR F3′.** Navazuje na
`pages-collections-access-analysis-2026-09-12.md` v3 a nahrazuje jeho fáze
F3–F5. Sledování: #2521.

Zadání vlastníka produktu: *„kdo má oprávnění do složky, má ho na všechno,
co v ní je; jednodušší a smysluplnější.“* Tři volby rozhodnuty: **(a) ano**,
`w` složky dědí úpravy stránek; **(b) ano**, `workspace` i na stránce, jen
pro read a write; **(c) ne**, nová složka je výchozí jen pro vlastnickou
crew a adminy.

## 0. Co oponentura upřesnila (všech šest zapracováno)

| # | Upřesnění | Kde v dokumentu |
|---|---|---|
| 1 | Není to mapování POSIX: Linuxová default ACL určuje počáteční práva nového objektu a přesun ACL zachovává, zde jde o **průběžné dědění, které se přesunem mění**; Linuxové *others* není aditivní skupina. Název: **děděná oprávnění složek**. | §1 |
| 2 | ACL patří složce a **trvá i po odchodu člověka, který ji nastavil**; `set_by_user_id` je jen audit. Výslovně jiný režim než dnešní stránkové granty, které ověřují vydavatele při použití. Řešeno i smazání uživatele. | §3/5, §5 |
| 3 | **`w` ⇒ `r`, vynucené serverem.** Přidání stránky vyžaduje jejího vlastníka nebo admina; `w` cílové složky samo neopravňuje vzít cizí stránku. Odebrání cizí stránky držitelem `w` je záměrně povolené a v popisu oprávnění to stojí, protože ostatním odebere zděděný přístup. | §3/2, 6, 7 |
| 4 | **Plné ACL vidí jen správci** (vlastnická crew MANAGER+, admin). Ostatní vidí jen vlastní efektivní přístup a obecné označení „sdílená“. **Ani 409 nevrací ACL** tomu, kdo ji nesmí číst. | §3/10, §4, §6 |
| 5 | **„Přesunout zpět“ není vrácení sdílení.** Návrat je nový autorizovaný přesun s aktuálním náhledem. Hromadný přesun je atomický a kontroluje každou stránku. | §3/9 |
| 6 | **Hranice revokace:** oprávnění se vyhodnocuje na začátku požadavku; zápis autorizovaný před odebráním `w` může doběhnout, další požadavek už ne. Testy souběhu přesunu, změny ACL a zápisu. | §3/12, §7, §8 |

UI slova: **„Může zobrazit“ / „Může upravovat“** (v produktu *Can view* /
*Can edit*); `r`/`w` zůstávají jen v CLI a v dokumentaci API.

## 1. Model

**Složka má oprávnění `subjekt → může zobrazit / může upravovat`, kde
subjekt je uživatel, crew nebo všichni lidé ve workspace. Oprávnění složky
platí průběžně pro každou stránku, která je ve složce právě teď; přesunem se
mění. Oprávnění stránky může přidat, nikdy odebrat. Oprávnění složky mění
jen vlastnická crew nebo admin.**

Přirovnání k Linuxu je vodítko, ne specifikace: složka jako adresář,
vlastnická crew jako vlastník, admin jako root, crew jako skupina, „všichni
ve workspace“ jako *others*. Dvě vědomé odlišnosti: dědění je průběžné (ne
default ACL při vytvoření) a `w` složky dává i úpravy stránek uvnitř (v
POSIX ne). Panel cizí crew zůstává zapečetěný jako soubor s vlastními bity,
které adresář nepřebije (§7.1/2 v `pages.md`).

## 2. Co už existuje

Složky bez oprávnění (PR #2531, #2529), `reach` (#2526), efektivní přístup
jen ke čtení (#2530), stránkové granty s ověřením vydavatele při použití
(`loadPageGrantRecordsIn`), pravidlo o panelech (`canSeePanel`), kontrola
úplného dokumentu pro `write` (#2502). Vše nasazeno na dev3 jako kombinace
větví; nic z §3–§6 není implementováno.

## 3. Pravidla

1. **Subjekty ACL složky:** `user:<id>`, `crew:<slug>`, `workspace`
   (současní lidští členové workspace, nikdy agenti). Agent získává dosah jen
   jmenovitým grantem na stránku (§7.1b), složkové ACL ho nepřijme (CHECK).
2. **Práva:** `r` = *může zobrazit*: vidět složku, její dostupné stránky a
   otevřít je; `w` = *může upravovat*: přejmenovat složku, ikonka, barva,
   odebrat stránku ze složky, upravovat uspořádání stránek uvnitř (`write`).
   **`w` ⇒ `r`, server odmítne zápis `w` bez `r` (400) a ukládá vždy obě.**
   Nikdy `produce`, nikdy viditelnost cizích panelů, nikdy `manage`.
3. **Dědění:** efektivní práva subjektu na stránce = sjednocení(admin role,
   vlastnictví stránky, členství v crew vlastnící panel, granty stránky,
   ACL složky, ve které stránka právě je). Aditivní, jen dolů, bez deny.
   `write` ze složky podléhá kontrole úplného dokumentu (#2502) stejně jako
   `write` ze stránky.
4. **Panely:** `r` z čehokoli výše otevře stránku; obsah panelu vidí jen
   člen jeho vlastnické crew nebo admin. Beze změny.
5. **ACL patří složce, ne tomu, kdo ji nastavil.** Záznam trvá, dokud ho
   správce nezmění nebo složka nezanikne; `set_by_user_id` je auditní údaj
   s `ON DELETE SET NULL`. To je vědomě jiný režim než stránkové granty,
   kde se vydavatel ověřuje při každém použití (`pages_grants_authz.go`);
   dokumentace API to říká u obou. Důvod: složka je organizační objekt s
   vlastníkem-crew, správce je zaměnitelný, a „práva zmizí, když odejde
   Petr“ by bylo překvapení, ne bezpečnost.
6. **Kdo mění ACL složky:** vlastnická crew (člen s rolí MANAGER+) nebo
   admin. Kdo mění granty stránky: vlastník stránky nebo admin (jako dnes).
   Žádná delegace; potřebuje-li někdo spravovat sdílení, stane se MANAGERem
   v crew.
7. **Přidání a přesun stránky:** iniciuje vlastník stránky nebo admin
   (vlastnictví se přesunem nemění); cíl musí volajícímu dávat `w` (nebo je
   admin, nebo MANAGER+ vlastnické crew cíle). Samotné `w` cíle bez
   vlastnictví stránky nestačí: nikdo si nemůže „vzít“ cizí stránku do své
   složky. **Odebrání ze složky:** vlastník stránky, admin, **nebo držitel
   `w` složky** — to poslední je záměrné a popis práva „může upravovat“
   uvádí: *„může také odebrat stránky ze složky, čímž ostatní přijdou o
   přístup zděděný ze složky“*. Držitel `w` stránku nepřesune jinam ani ji
   nesmaže.
8. **Přesun nese verze:** `pages_version` stránky a `acl_version` cílové
   složky. Zastaralé potvrzení → 409. Tělo 409 obsahuje aktuální ACL cíle
   **jen tehdy, když volající smí ACL číst (§3/10)**; jinak jen věta
   „oprávnění složky se změnila, zobraz náhled znovu“ a nové verze.
9. **Návrat = nový přesun.** UI nenabízí „vrátit sdílení“; nabízí přesun
   s aktuálním náhledem dopadu. Hromadný přesun (více vybraných stránek)
   je jedna transakce: každá stránka se ověří zvlášť (vlastnictví, verze),
   při jediném odmítnutí se nezapíše nic a odpověď říká, která stránka a
   proč.
10. **Kdo čte ACL složky:** vlastnická crew MANAGER+ a admin vidí celé ACL.
    Ostatní vidí u složky jen **označení** (`shared: "crew" | "workspace" |
    "none"` bez jmen) a vlastní efektivní přístup (`GET /pages/{slug}/access`
    zůstává pro vlastníka/admina; pro běžného čtenáře přibude
    `GET /pages/{slug}/access/me`, jen jeho cesty).
11. **Seznamy, počty a hledání** obsahují jen dosažitelné stránky; složka
    bez dosažitelné stránky a bez `r` odpoví 404.
12. **Hranice revokace:** oprávnění se vyhodnocuje na začátku každého
    požadavku (žádná cache, `loadPageGrantRecordsIn` per request). Zápis,
    který začal s platným `w` a mezitím o něj přišel, doběhne; každý další
    požadavek je odmítnut. Přesun a změna ACL se serializují přes
    `acl_version` (CAS v transakci). Test: přesun ↔ `chmod` ↔ `PUT project`
    v prokládaném pořadí (harness `interleave` z `pages_opponent_regression_test.go`).
13. **Smazání složky** jen prázdné; smazání uživatele nechává ACL (§3/5),
    jeho vlastní `user:` záznamy se odstraní kaskádou.
14. **`workspace` na stránce** (volba b): stránkový grant se subjektem
    `workspace` pro `read` a `write`, nikdy `produce`; vydává vlastník
    stránky nebo admin jako dnes.
15. **Nová složka** (volba c): žádný ACL záznam; dosah má vlastnická crew a
    admini. „Všichni ve workspace“ se zapíná výslovně a UI to řekne větou.

## 4. Oprávnění operací

| Operace | Kdo |
|---|---|
| Číst složku, seznam dostupných stránek, počet | kdo má `r`, vlastník stránky uvnitř, admin, vlastnická crew |
| Číst celé ACL složky | vlastnická crew MANAGER+, admin |
| Číst označení sdílení a vlastní cesty | každý, kdo složku vidí |
| Nastavit / odebrat záznam ACL | vlastnická crew MANAGER+, admin |
| Přejmenovat, ikonka, barva | admin, vlastnická crew MANAGER+, držitel `w` |
| Smazat prázdnou složku | admin, vlastnická crew MANAGER+ |
| Přidat / přesunout stránku | vlastník stránky nebo admin **a zároveň** `w` cíle / admin / MANAGER+ cílové crew |
| Odebrat stránku | vlastník stránky, admin, držitel `w` složky |
| Upravovat dokument stránky uvnitř | jako dnes plus držitel `w` složky (s kontrolou #2502) |

## 5. Datový model

```sql
ALTER TABLE page_folders RENAME COLUMN grants_version TO acl_version;
CREATE TABLE page_folder_acl (
  folder_id      TEXT NOT NULL REFERENCES page_folders(id) ON DELETE CASCADE,
  subject_type   TEXT NOT NULL CHECK (subject_type IN ('user','crew','workspace')),
  subject_id     TEXT NOT NULL DEFAULT '',                      -- '' pro workspace
  can_read       INTEGER NOT NULL DEFAULT 1 CHECK (can_read = 1), -- w ⇒ r: r je vždy
  can_write      INTEGER NOT NULL DEFAULT 0 CHECK (can_write IN (0,1)),
  set_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,   -- audit, ne autorita
  set_at         TEXT NOT NULL,
  PRIMARY KEY (folder_id, subject_type, subject_id)
);
-- page_grants: subject_type přijme 'workspace' (subject_id ''), jen level read|write.
```

`can_read` je vždy 1: záznam buď existuje (zobrazit) nebo má navíc
`can_write`; „upravovat bez zobrazení“ neexistuje ani v datech.

Vyhodnocení: `loadPageGrantRecordsIn` načte stránkové granty (s dnešní
kontrolou vydavatele) a **zvlášť** ACL složek přes `pages.folder_id` (bez
kontroly vydavatele, §3/5) jedním dotazem každé; sjednocení v paměti.
`pageReach` přidá `folder:<slug>`. Seznam zůstává na konstantním počtu
dotazů (test z #2526, rozšířený o složky v #2531).

## 6. API a CLI

| Metoda a cesta | Kdo | Odpověď | CLI |
|---|---|---|---|
| `GET /page-folders/{slug}/acl` | správci | `{acl:[{subject_type, subject_id, label, can_read, can_write, set_by, set_at}], acl_version}` | `page folder acl <slug>` |
| `PUT /page-folders/{slug}/acl` `{subject_type, subject_id?, can_write}` | správci | 200 záznam; 400 pro agenta nebo `can_write` bez `r` | `page folder share <slug> <subject> --view\|--edit` |
| `DELETE /page-folders/{slug}/acl/{subject}` | správci | 204 | `page folder unshare <slug> <subject>` |
| `GET /page-folders` a `GET /page-folders/{slug}` | jako dnes | přibude `shared: "none"\|"crew"\|"workspace"` bez jmen; `acl_version` | `page folder list/show` |
| `POST /page-folders/{slug}/pages` `{page, pages_version, acl_version}` | §4 | 200; 409 s verzemi, ACL jen pro správce | `page move` |
| `POST /page-folders/{slug}/pages:batch` `{pages:[{page, pages_version}], acl_version}` | §4 pro každou | vše nebo nic; 403/409 jmenuje stránku | `page move a b c --folder` |
| `GET /pages/{slug}/access/me` | kdo stránku vidí | jen vlastní cesty | `page access <slug> --me` |
| `GET /pages/{slug}/access` (#2530) | vlastník, admin | přibude `folder:<slug>` | `page access` |
| `PUT /pages/{slug}/grants` | jako dnes | přijme `subject_type: "workspace"` pro read/write | `page grant … --workspace` |

Každý endpoint má CLI příkaz, akceptační test nad binárkou, řádek v
`docs/api-reference/pages.mdx` a `docs/cli/page.mdx`.

## 7. Obrazovky

- **Složka → Sharing** (jen správci): řádek vlastnické crew (upravovat,
  nelze odebrat), pak záznamy s přepínačem *Může zobrazit* / *Může
  upravovat*, řádek *Všichni v tomto workspace* vypnutý ve výchozím stavu.
  Věta nad tabulkou: „Oprávnění složky platí pro všechny stránky, které v
  ní právě jsou, včetně těch, které do ní přibudou. Panely vlastněné jinou
  crew zůstanou zapečetěné. Kdo může upravovat, může také stránky ze složky
  odebrat.“ Ostatní vidí místo tabulky: „Sdílená s crew“ / „Sdílená se
  všemi ve workspace“ / „Jen vlastnická crew“ a svůj přístup.
- **Přesun** (dialog z #2529): blok „Po přesunu“: pro správce jmenovitě
  („uvidí crew Support a všichni ve workspace; crew Ops bude moci
  upravovat“), pro ostatní obecně („uvidí všichni ve workspace“ / „uvidí
  členové sdílených crew“). Po 409 se blok přegeneruje z nových verzí.
- **Sidebar:** u složky sdílené se všemi ve workspace malý symbol a `title`
  „Sdílená se všemi ve workspace“.
- **Access sekce stránky:** *Direct*, *Ze složky Ops* (jen ke čtení; pro
  správce složky odkaz na Sharing, pro ostatní věta), *Effective access*.

## 8. Akceptační scénáře

A1 `w` bez `r` odmítnuto 400; uložený záznam má vždy `r`. A2 Držitel `w`
složky: přejmenuje, odebere cizí stránku (200, ostatní ztratí zděděný
přístup — test ověří `reach` po odebrání), upraví uspořádání stránky uvnitř
(s #2502: bez viditelnosti panelu 403), **nepřesune** stránku jinam (403),
**nesmaže** složku (403), **nezmění** ACL (403). A3 Přidání: `w` cíle bez
vlastnictví stránky 403; vlastník bez `w` cíle 403 s větou; admin 200.
A4 Odchod správce: záznam ACL trvá; smazání uživatele nechá záznamy
ostatních (`set_by` NULL). A5 `workspace: r` na složce: každý člen vidí
stránky uvnitř; nečlen 404; agent nikdy. A6 Sealed panel přes složku:
stránka ano, panel zapečetěný, `GET /project` 403. A7 ACL jen správcům:
běžný čtenář dostane 403 na `/acl`, 409 při přesunu bez ACL v těle.
A8 Přesun se zastaralou `acl_version` 409; správce dostane ACL, čtenář jen
verze. A9 Hromadný přesun: jedna odmítnutá → nic; odpověď jmenuje stránku.
A10 Souběh: prokládané `chmod` (odebrání `w`) ↔ `PUT project` držitelem `w`:
zápis započatý před odebráním doběhne, další je 403; prokládaný přesun ↔
`chmod`: jeden z nich 409. A11 Konstantní počet dotazů pro seznam s ACL.
A12 `workspace` grant na stránce: read/write ano, produce 400.

UI: U1 Sharing jen pro správce, ostatní vidí označení; U2 dialog přesunu
ukáže dopad podle role; U3 klávesnice; U4 360/768/1440.

## 9. Co záměrně nenavrhuji

Delegace `manage`, expirace grantů, dávky grantů a jejich vracení, deny,
vnořené složky, `produce` přes složku, agenti v ACL složky, tlačítko
„vrátit sdílení“, výchozí sdílení se všemi.

## 10. Dodávka

Jedno PR **F3′** nad #2531 + #2529 (backend a frontend mohou jít jako dva
stohované PR), s testy z §8 a dokumentací. Před merge cílená oponentura
implementace na A2, A7, A10.
