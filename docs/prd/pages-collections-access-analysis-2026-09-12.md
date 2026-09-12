# Pages: kolekce, hromadné sdílení a sidebar — analýza k oponentuře

Datum: 2026-09-12. Stav: **návrh k oponentuře, nic z toho není implementováno.**
Sledování: #2521. Základ: `main` 84615795. Navazuje na `docs/prd/pages.md` §7.1 (pravidla
přístupu), na #2502 (write grant nesmí rozšířit viditelnost panelu) a na PR
#2516 (editační režim). Podnět: uživatelský požadavek „skupiny v sidebaru,
přiřadit celou crew nebo více Pages někomu naráz“.

Dokument je záměrně psaný tak, aby se dal zpochybnit: každé tvrzení o dnešním
stavu odkazuje na kód, každé pravidlo má důvod, a §11 říká, co má oponent
napadnout.

## 1. Rozhodnutí, která navrhuji

1. **Přidat kolekce Pages** jako jediný nový objekt: pojmenovaná skupina
   stránek vlastněná crew, stránka patří nejvýš do jedné kolekce.
2. **Granty na kolekci se dědí na její stránky, jen aditivně.** Efektivní
   přístup je sjednocení všech cest; žádné deny pravidlo, žádné odečítání.
3. **Pravidlo §7.1/2 zůstává nadřazené:** viditelnost panelu je viditelnost
   jeho vlastnické crew. Grant na kolekci nesmí odhalit panel cizí crew o nic
   víc než dnešní grant na stránku. Přesně tato hranice se prolomila v #2502.
4. **Hromadné sdílení je dávka grantů, ne nová entita.** Výběr N stránek a
   jedno „Share“ zapíše N řádků se společným `batch_id` v journalu.
5. **Čtvrtá úroveň `manage`** (spravovat granty kolekce nebo stránky bez
   práva na obsah panelů), delegovatelná z vlastníka nebo admina.
6. **Volitelná expirace grantu** (`expires_at`), stejně jako ji mají veřejné
   odkazy povinně.
7. **Pohled „efektivní přístup“** počítaný serverem: kdo dosáhne na stránku
   a kterou cestou; a obráceně, na co dosáhne subjekt.
8. **Neskupinovat lidi mimo crew.** Crew je skupina lidí; druhá by byla
   dvojí pravda.

Pořadí dodávky je v §10. Sidebar bez backendu (seskupení podle vlastnické
crew, čip vlastníka, facety) může jít první a nezávisle.

## 2. Dnešní stav, ověřeno v kódu

| Fakt | Kde |
|---|---|
| Stránka má právě jednoho vlastníka: `owner_user_id` xor `owner_crew_id`. Převod při odchodu uživatele, nikdy sirotek. | `docs/prd/pages.md` §7.1/1, 1b; `pages.owner_user_id ON DELETE RESTRICT` |
| Viditelnost panelu = členství ve vlastnické crew nebo role `manage`. Nevidím-li panel, dostanu sealed placeholder. | `internal/api/pages_authz.go` `canSeePanel` |
| Dosah na stránku bez grantu: role manage, vlastník, člen vlastnické crew, nebo člen crew vlastnící aspoň jeden panel. Jinak explicitní grant. | `pages_authz.go` `pageReachedWithoutGrant`, `canSeePage` |
| Granty: `(page_id, subject_type ∈ user/crew/agent, subject_id, level ∈ read/produce/write, panel_ids?)`; `panel_ids` jen pro `produce`; `granted_by_user_id NOT NULL`. | `internal/database/migrations/20260812155322_pages.sql:240` |
| Granty spravuje jen role manage nebo vlastník stránky. | `pages_grants.go` `mayAdministerGrants` |
| `write` je autorita nad uspořádáním, ne nad obsahem; a od #2502 neotevírá celý dokument, pokud volající nevidí každý panel. | `pages_project_authoring.go`, `docs/prd/pages-project-authoring-access.md` |
| Změna grantu se journaluje (`EntryPageGrantAdded/Removed`). | `pages_grants.go:253, 383` |
| Veřejné odkazy: token, volitelné heslo, **povinná** `expires_at`, revokace. Granty expiraci nemají. | `page_public_tokens` |
| CLI: `page grant <slug>`, `page grants <slug>`, `page links <slug>`. Žádný hromadný příkaz. | `cmd/crewship/cmd_page_grants.go` |
| Sidebar: hledání podle názvu a vlastníka, facety `states` a `owners`, jedna plochá sekce „PAGES“ s počtem panelů. | `hooks/use-pages.ts` `PageFilters`, `pages-rail.tsx` |
| Workspace role: OWNER, ADMIN (manage), MANAGER (create), MEMBER, VIEWER + capabilities (`page.create`). | `lib/permissions/tiers.ts`, `internal/api/capabilities.go` |

Co dnes **nejde**: dát přístup k deseti stránkám jinak než deseti granty;
dát někomu právo spravovat přístup bez toho, aby byl vlastník nebo admin;
dočasný přístup; zjistit, proč Petr stránku vidí (role? crew? grant?);
uspořádat sidebar jinak než abecedně pod jednou hlavičkou.

## 3. Scénáře, které mají projít

S1. Vedoucí Ops chce, aby celá crew Support četla všechny Ops stránky, včetně
    těch, které vzniknou příští týden. Jedna akce, jeden záznam v journalu.

S2. Admin vybere pět release stránek a dá crew QA `read` na 14 dní. Po
    uplynutí přístup zmizí sám a v journalu je vidět, že vypršel, ne že ho
    někdo odebral.

S3. Vlastník stránky s panelem crew Lookout dá crew Support `read` na
    kolekci. Support vidí stránku, panel Lookout vidí jako sealed placeholder.
    Nic víc než dnes.

S4. Petr ztratil přístup ke stránce, kterou včera viděl. Admin otevře
    „Efektivní přístup“ a vidí: přístup plynul z členství v crew Engine,
    které dnes skončilo. Žádný grant k odebrání, žádné hádání.

S5. Agent nikdy nezíská přístup přes kolekci automaticky: agent je subjekt
    grantu jen jmenovitě (§7.1b), a tak to zůstane i pro kolekce.

S6. Kolekci vlastní crew, která je smazána. Kolekce se převede podle stejného
    pravidla jako stránka (§7.1/1b), stránky v ní zůstávají.

S7. Uživatel v sidebaru vidí stránky seskupené po kolekcích, sekce se dají
    sbalit, a hledání najde stránku i podle toho, komu je sdílená.

## 4. Proč kolekce a ne skupiny lidí

RBAC v pojetí NIST má tři pojmy: uživatel, role, oprávnění; uživatel dostává
oprávnění přes role. V Crewship tu roli hraje **crew** (členství) a
**workspace role** (OWNER…VIEWER). Skupina lidí navíc by byla třetí místo,
kde se rozhoduje, kdo je kdo, a dvě z nich by se rozcházela. Chybějící kus
je na straně **objektů**: dnes je oprávnění vázané na jednu stránku a nic
nad ní. Hierarchie zdrojů (kolekce → stránka) je to, co dělají resource
groups v cloudech i relační modely typu Zanzibar (`page#parent@collection`,
`collection#reader@crew`). Přidává jeden objekt a jednu hranu.

## 5. Pravidla (návrh §7.4 do `pages.md`)

1. **Kolekce má právě jednoho vlastníka a je to crew.** Ne uživatel: kolekce
   je organizační celek a musí přežít odchod člověka bez převodu. Převod při
   smazání crew se řídí §7.1/1b (crew vlastnící nejvíc stránek v kolekci,
   jinak admin dostane notifikaci a kolekce čeká na přiřazení; nikdy sirotek).
2. **Stránka patří nejvýš do jedné kolekce.** Přesun je operace vlastníka
   stránky nebo admina; vlastník kolekce přesun přijímá, nemůže si stránku
   „vzít“. Bez vnořování: jedna úroveň stačí a druhá už potřebuje pravidla,
   která nikdo nedokáže z hlavy vyhodnotit.
3. **Grant na kolekci má stejný tvar jako grant na stránku:** subjekt user,
   crew nebo agent; úroveň `read`, `produce`, `write`, `manage`; `panel_ids`
   nedává na kolekci smysl a je zakázán (CHECK). Vydává ho člověk.
4. **Dědění je aditivní a jen směrem dolů.** Efektivní úrovně subjektu na
   stránce = sjednocení(role, vlastnictví, členství v crew vlastnící panel,
   granty stránky, granty kolekce). Neexistuje deny; odebrat přístup znamená
   odebrat grant nebo členství. Důvod: každý systém s deny pravidly musí
   definovat pořadí vyhodnocení a to je nejčastější zdroj bezpečnostních chyb
   v ACL.
5. **§7.1/2 je nadřazené všemu.** Žádná cesta z bodu 4 nezpřístupní obsah
   panelu, jehož crew subjekt není členem, s výjimkou role manage. Grant
   `write` z kolekce podléhá stejné kontrole úplného dokumentu jako dnes
   (`requireProjectDefinitions`).
6. **`manage` je právo spravovat granty a přesuny, ne obsah.** Držitel
   `manage` na kolekci může vydávat a odebírat granty kolekce a jejích
   stránek do úrovně, kterou sám drží, nikdy vyšší (žádná eskalace). Nemůže
   měnit vlastníka. Nevidí panely, které by jinak neviděl.
7. **Expirace je volitelná na grantu, povinná na veřejném odkazu.** Prošlý
   grant se při vyhodnocení ignoruje a při prvním dotyku se zapíše do
   journalu jako `page.grant_expired`; nikdy se nemaže potichu.
8. **Hromadná akce je dávka.** N grantů, jeden `batch_id`, jeden journal
   záznam se seznamem stránek, a vrácení dávky odebere přesně ty granty,
   které dávka vytvořila (ne ty, které existovaly dřív).
9. **Agent nikdy nezíská dosah implicitně.** Grant kolekce pro agenta je
   možný, ale jmenovitý; agent nezdědí nic přes crew, ve které běží (§7.1b/1).
10. **Efektivní přístup je serverový výpočet a jediný zdroj pravdy pro UI.**
    Klient nikdy neodvozuje, proč někdo něco vidí; dostane cestu (`role`,
    `owner`, `panel_crew`, `grant:page`, `grant:collection`) a ukáže ji.

## 6. Datový model a API (P1)

```sql
CREATE TABLE page_collections (
  id            TEXT PRIMARY KEY,
  workspace_id  TEXT NOT NULL REFERENCES workspaces(id),
  slug          TEXT NOT NULL,
  name          TEXT NOT NULL,
  owner_crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE RESTRICT,
  created_at    TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (workspace_id, slug)
);
ALTER TABLE pages ADD COLUMN collection_id TEXT REFERENCES page_collections(id) ON DELETE SET NULL;

CREATE TABLE page_collection_grants (
  collection_id      TEXT NOT NULL REFERENCES page_collections(id) ON DELETE CASCADE,
  subject_type       TEXT NOT NULL CHECK (subject_type IN ('user','crew','agent')),
  subject_id         TEXT NOT NULL,
  level              TEXT NOT NULL CHECK (level IN ('read','produce','write','manage')),
  granted_by_user_id TEXT NOT NULL REFERENCES users(id),
  granted_at         TEXT NOT NULL,
  expires_at         TEXT,
  batch_id           TEXT,
  PRIMARY KEY (collection_id, subject_type, subject_id, level)
);
-- page_grants: přidat level 'manage', expires_at TEXT, batch_id TEXT.
```

Endpointy (každý dostane CLI příkaz a akceptační test nad binárkou, jak
vyžaduje CLAUDE.md):

| Metoda a cesta | Kdo | CLI |
|---|---|---|
| `GET/POST /pages/collections`, `GET/PATCH/DELETE /pages/collections/{slug}` | čtení: každý s dosahem na aspoň jednu stránku kolekce; zápis: admin, vlastník crew, `manage` | `page collection list/create/show/update/delete` |
| `PUT/DELETE /pages/collections/{slug}/grants` | admin, vlastnická crew, `manage` na kolekci | `page collection grant/revoke` |
| `POST /pages/collections/{slug}/pages` (přidat), `DELETE …/pages/{pageSlug}` | vlastník stránky nebo admin; vlastník kolekce potvrzuje | `page collection add/remove` |
| `POST /pages/grants:batch` `{pages:[…], subject, level, expires_at?}` | pro každou stránku zvlášť `mayAdministerGrants`; dávka se buď celá zapíše, nebo celá odmítne s výčtem odmítnutých stránek | `page grant --pages a,b,c` |
| `DELETE /pages/grants/batches/{batch_id}` | ten, kdo dávku vydal, nebo admin | `page grant revoke-batch` |
| `GET /pages/{slug}/access` (efektivní přístup: seznam subjektů a cest) | admin, vlastník, `manage` | `page access <slug>` |
| `GET /pages/access?subject_type=&subject=` (na co subjekt dosáhne) | admin; uživatel sám na sebe | `page access --subject user:petr` |

Vyhodnocení: `pageReachedWithoutGrant` se nemění; `grantsFor` načte navíc
granty kolekce stránky (jeden JOIN přes `pages.collection_id`), vyfiltruje
prošlé a sjednotí. `canSeePanel` se nemění vůbec, to je bod 5.

Cache: dnešní kód nic nekešuje per uživatel kromě viewer standingu v rámci
požadavku; kolekce to nemění. Pozor na `page.updated` broadcast: změna
grantu kolekce se musí projevit invalidací seznamu stránek u všech, ne jen
u jedné stránky (nová událost `page.collection.updated`).

## 7. Sidebar a UI

**P0 bez backendu**

- Sekce podle vlastnické crew (`SidebarSection`, skládací, s počtem), pořadí:
  „Mine“ (vlastním já), pak crew, jejichž jsem členem, pak ostatní. Stav si
  pamatuje `localStorage` per workspace.
- Čip vlastníka u řádku místo počtu panelů; počet panelů do tooltipu nebo
  jako druhý údaj. Stavová tečka zůstává.
- Facety: k `states` a `owners` přidat `shared` (sdíleno se mnou grantem /
  vlastním / dosahuji přes crew). Vyžaduje, aby list API vracelo důvod
  dosahu, což je malý krok k §5/10 a lze ho udělat dřív než kolekce.

**P1 s kolekcemi**

- Sekce podle kolekce; stránky bez kolekce v sekci „Unfiled“. Přetažení
  řádku do sekce = přesun (s potvrzením, protože mění, kdo stránku vidí).
- Výběr více řádků (checkbox při hoveru, Shift-klik), spodní lišta akcí:
  Share, Move to collection, Export. Share otevře stejný formulář jako
  Access sekce editoru, s předvyplněným seznamem stránek a volitelnou
  expirací.
- Access sekce editoru dostane blok „Inherited from collection Ops“, jen ke
  čtení, s odkazem na kolekci; a blok „Effective access“ (§5/10).
- Stránka kolekce: název, vlastník, stránky, granty, journal.

**Vizuální slovník:** vše ze `sidebar-kit` a `SectionCard`, žádná nová
komponenta, stejné ikony jako Settings. Přesně to, co si vynutil PR #2516.

## 8. Bezpečnostní hrany, na které oponent má tlačit

- **Eskalace přes `manage`:** držitel `read`+`manage` nesmí vydat `write`.
  Test: pokus vrátí 403 a nic se nezapíše.
- **Kolekce jako obcházení #2502:** subjekt s `write` z kolekce a bez
  členství v crew panelu musí na `GET /project` dostat 403 stejně jako dnes.
- **Přesun stránky mezi kolekcemi mění dosah:** kdo přesouvá, musí být
  vlastník stránky nebo admin; UI to říká před potvrzením („12 lidí ze crew
  Support přestane stránku vidět“), a journal to zapíše s před/po.
- **Expirace a hodiny:** vyhodnocení bere čas serveru (`evaluator().Now()`),
  ne klienta; test s falešným časem, ne se `sleep`.
- **Dávka je atomická:** 409/403 na kterékoli stránce vrátí celou dávku;
  jinak vznikne polovina grantů a nikdo neví která.
- **Efektivní přístup nesmí prozradit víc než sealed placeholder:** výpis
  cest pro stránku, na které mám `manage` z kolekce, nesmí vyjmenovat
  panely crew, do které nepatřím. Stejný neutrální jazyk jako
  `pageWithheldChangeMessage`.
- **Veřejné odkazy zůstávají na stránce.** Kolekce nemá veřejný odkaz;
  „sdílet kolekci veřejně“ by byl jeden token pro N stránek s jednou
  expirací a jedním heslem, tedy přesně to, co §7.3 zakazuje.
- **Rate limit** na `grants:batch` (max. 100 stránek v dávce) a na
  `pages/access` dotazy.

## 9. Co záměrně nenavrhuji

- Deny granty (§5/4).
- Vnořené kolekce (§5/2).
- Skupiny uživatelů mimo crew (§4).
- Role definované uvnitř Pages nad rámec čtyř úrovní; ABAC podmínky
  („jen v pracovní době“) nemají dnes v produktu obdobu.
- Automatické zařazení stránek do kolekce podle vlastníka; bylo by to
  pohodlné a nečitelné.

## 10. Fáze a akceptace

| Fáze | Obsah | Akceptace |
|---|---|---|
| P0 | Sidebar: sekce podle vlastnické crew, čip vlastníka, facet `shared`, důvod dosahu v list API | Vitest na řazení a facety; list API vrací `reach` pro každou stránku; žádný nový dotaz na stránku navíc |
| P1 | Kolekce, dědění, `manage`, dávka, CLI | Go testy: S1, S3, S5, S6, eskalace, atomická dávka; akceptační test CLI; mutace na `grantsFor` (odstranění JOINu kolekce shodí S1) |
| P2 | Expirace, efektivní přístup, journal `grant_expired`, UI Effective access | Test s falešným časem; test, že výpis cest neprozradí sealed panel |

Produktové měření (stejný formát jako §7 editoru): 4 z 5 lidí bez znalosti
implementace dají crew přístup ke třem stránkám najednou do 60 s a
správně řeknou, co se stane se stránkou přidanou do kolekce zítra.

## 11. Zadání pro oponenturu

Prověř především:

1. Je jedna úroveň kolekcí opravdu dost, nebo model bez vnoření selže na
   reálné organizaci (např. „Ops → Release → 2026-Q4“)? Uveď scénář.
2. Je aditivní sjednocení bez deny udržitelné, když crew vlastní panel na
   stránce, kterou nemá vidět celá? Najdi únik.
3. Může `manage` na kolekci eskalovat přes přesun stránky nebo přes změnu
   vlastníka crew?
4. Je `batch_id` dostatečná auditní stopa, nebo má být dávka vlastní entita
   s vlastním stavem?
5. Dá se efektivní přístup spočítat bez N+1 dotazů na stránku při seznamu
   80 stránek? Navrhni dotaz.
6. Jsou čtyři úrovně (`read/produce/write/manage`) správně ortogonální k
   workspace rolím, nebo se překrývají s MANAGER/ADMIN?
7. Co z P0 sidebaru je opravdu bez backendu a co potajmu vyžaduje `reach`
   v list API?
8. Který standard (NIST RBAC, ISO 27001 A.9, SOC 2 CC6) něco z toho
   vyžaduje jinak, než je navrženo, a kde je návrh přísnější, než je nutné?

U každého nálezu: závažnost, scénář, které pravidlo z §5 porušuje nebo
chybí, a nejmenší změna návrhu, která to řeší.
