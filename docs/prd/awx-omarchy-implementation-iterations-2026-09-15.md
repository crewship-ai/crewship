# AWX + Omarchy: deset implementačních iterací po oponentuře

Datum 2026-09-15. Stav: revidovaný plán k předávání Claude Code, nikoli potvrzení implementace nebo schválení nových oprávnění. Navazuje na [PRD](awx-omarchy-product-improvements-2026-09-14.md) a [oponenturu](reports/awx-omarchy-prd-opponent-2026-09-15.md). Reporty nepřepisovat: zachovávají původní důkazy a rozdíly proti této revizi.

## Jak práci předávat

Preferovat deset postupných relací. Každá dostane PRD, oponenturu, tento plán a handoff předchozí relace. Nezávislou paletu lze dělat vedle bezpečnostní opravy v oddělené větvi; souběh deseti editorů stejných souborů není doporučený.

Iterace není automaticky jeden PR. Zejména 3, 7, 8 a 10 lze rozdělit na menší PR, které mají vlastní verifikaci. Po každém dokončit review podle repo pravidel a zaznamenat přesný commit. Nezačínat závislou práci na neurčeném WIP. Souběžné #2556/#2562/#2558 jsou snapshot oponentury: při každém převzetí zjistit aktuální stav a živé claims.

Společná akceptace: zachovat WIP; žádné změny mimo scope; cílené testy plus povinné repo gates odpovídající změně; nové API/CLI vyžaduje aktuální OpenAPI a docs gates, nikoli jejich obcházení. Nepředepisujeme izolovaný port z jiného reportu: volný port/data/socket zvolit podle CODEX a skutečné dostupnosti. Živou instanci nezabíjet ani neseedovat kvůli reprodukci.

Každý handoff obsahuje: HEAD, implementované požadavky, soubory, výsledky testů včetně skipů, ověřené API/autorizační kontrakty, nevyřešené závislosti, PR/review stav a přesný vstup pro další relaci. „Hotovo“ neznamená pouze zelený unit test.

## Rozhodnutí a jejich stav

| Téma | Doporučený směr | Stav / vliv |
|---|---|---|
| #2562 askLead | Copy-only, raw chyby nepatří do URL; mutující auto-send nepatří pod diagnostiku | Zachovaný požadavek PRD, žádné odeslání komentáře či editace cizího PR tímto plánem |
| routine.run | Sjednotit Page action + replay + schedule-run; batch ponechat zvlášť | Návrh rozšíření přístupu, čeká na rozhodnutí; bezpečnostní část iterace 1 může pokračovat |
| Multi-crew credential scope | Sjednotit resolver/probe/visibility/sidecar podle schváleného zdroje členství | Změna dostupnosti tajemství; vyřešit před runtime změnou a finálním would-resolve v iteraci 9 |
| Page cílový agent/session | Autorizovaný lead owner crew, jinak picker; nová session; pouze metadata | Doporučení; před automatickým přenosem doložit práva cíle; obecný draft 10a není blokován |
| VIEWER raw API data | O2 nepřidává oprávnění; minimální allowlist exportu | Zachování stavu není schválení jeho bezpečnosti. Případná redakce API samostatný úkol; access/me samo scope neřeší |
| Per-run credential evidence | „Nezaznamenáno“, deklarace ze snapshotu | Minimální rozsah; nový writer není release podmínka |
| Per-routine execute grant | Mimo 1.0 | Nezavádět workspace capability pod názvem grant na jednu rutinu |
| Secret inputs | Přiznat doslovnou archivaci; tajemství dodávat přes credentials | Bez neohlášeného zákazu existujících názvů polí; upozornění nenahrazuje redakci |
| Recent | Klíč user+workspace, jednorázové opuštění staré historie; Page recent filtrovat autorizovaným listem | Technické řešení, zmínit změnu historie v release handoffu |
| O5 role/zdroj | Zvolit skutečnou cílovou agendu a zdroj | Obsahový pack čeká; nevydávat fiktivní objednávkový konektor za hotový |

## Iterace 1 — bezpečnost původu běhu a inventura spouštěcích oprávnění (dokončeno v #2567)

**Stav:** dokončeno a sloučeno v #2567. Následující rozsah a testy jsou historickým zadáním, nikoli otevřenou prací.

**Vstup:** klastr S, nález K1; pipelines_exec, auth context, waitpoints a existující triggered_by forge testy.

**Dodat:** opravu JWT spoofingu invoking crew/agent a test legitimních identit. Hlavičky nejsou autoritou: vycházet z ověřeného contextu, crew se musí rovnat scope tokenu a agent být oprávněně svázán s crew/workspace. Zkontrolovat všechny zápisy invoking identity, zachovat správný user-driven fallback. Prokázat, že cizí crew nemůže ovlivnit trust grant.

Samostatně zapsat skutečnou matici direct run/slash/Page/replay/schedule/batch. Rozšíření routine.run podle rozhodnutí výše až jako oddělená změna. Nezavádět per-rutina grant ani nový UI.

**Testy:** JWT + podvržené hlavičky, interní token crew A + hlavička crew B, agent z cizí crew/workspace, správný interní caller, chybějící identita, legitimní user a trust fallback. HTTP→persist→trust test; samotné ověření prázdného DB pole nestačí pro důsledek na grant.

**Konec:** doložená oprava, negativní i pozitivní testy, aktuální auth kontrakt. Handoff upozorní, že staré uložené provenance nejsou zpětně důvěryhodné. Odhad oponenta 1–1,5 dne je orientační, širší dotčené cesty znovu ocenit.

## Iterace 2 — Pages v paletě

**Vstup:** klastr P, O1; bez závislosti na 1.

**Dodat:** konkrétní Pages přes současný otevřením aktivovaný fan-out, správný glyph a navigaci. Recent podle user/workspace pro všechny skupiny; Page recent ověřit stejným autorizovaným listem, při neúspěšném ověření je neukazovat jako dostupné. Bez nového endpointu a bez trvalého realtime hooku.

**Testy:** autorizované výsledky, klik/Enter, logout, ztráta workspace, pozdní request, smazání/revokace, chyba listu, ostatní skupiny funkční. Původní historie se nepřenese mezi identitami.

**Konec:** testy a skutečná navigace v izolovaném/dev prostředí podle pravidel. Handoff popíše reset staré Recent historie.

## Iterace 3 — evidence kontrakt a korelace

**Vstup:** klastr D, handoff O2 a upřesnění revidovaného PRD.

**Dodat odděleně:** čistý TS builder/formatter a JSON fixtures; korelační write-side opravu pro události, které lze skutečně navázat; případně aditivní invoking pole detailu. Žádný nový evidence endpoint. Reference s explicitním druhem, 16 KiB/20 událostí po sestavení, sanitovaný allowlist.

**Testy:** zakázaná pole a sentinely, UTF-8 limit, unknown event bez textového fallbacku, více pokusů, dva souběžné běhy téhož agenta, producent→uložení→filtrovaný dotaz. Přerušený run/assignment proti stále RUNNING journalu = přiznaný rozpor. Bez run_id a se selháním emitu nepsat automaticky „nespustil se“.

**Konec:** fixtures použitelné později pro O3, jasný seznam nekorelovaných/neexportovaných událostí. Volitelný MarkInterrupted emit není skrytá podmínka následujících balíků; bez něj přiznat neúplnost.

## Iterace 4 — diagnostický panel rutin

**Vstup:** 3 a znovu zjištěný stav #2562. Jeho failure klasifikaci konzumovat, je-li skutečně přítomná; neduplikovat parser/banner. Předávání raw chyby přes auto-send v této ploše vyřešit podle PRD.

**Dodat:** reference, poslední doložená činnost a stáří, náhled podkladu, kopírování odkazu/podkladu, warnings odlišené od failure. Izolovaná komponenta omezí konflikt s operator-console prací.

**Testy:** clipboard odmítnutí, loading/error/empty, dlouhé hodnoty, keyboard/focus, žádný nový run nebo inbox při otevření a kopírování. Izolovaná neškodně selhávající rutina a souběžný běh bez promíchání.

**Konec:** anonymizovaný podklad porovnán se zdroji, screenshot a reprodukovatelná akceptace. Žádný LLM, opravy ani restart.

## Iterace 5 — detail agentního běhu issues

**Vstup:** 3, sdílený panel 4, klastr D. Současný routine detail nepovažovat za agent detail.

**Dodat:** lehký agent-run-detail, správné typové routování/404 fallback a načtení existujícího run API, doplnění dostupných hard-stop/outcome údajů. Zachovat assignment i run reference. 403/500 nesmí vyvolat širší data fallback. Při přímém deep linku bez autoritativního assignmentu přiznat mez důkazů.

**Testy:** více assignmentů, nedohledatelný run, čekání na dispatch, prokázané odmítnutí před startem, selhaný journal emit, hard stop, souběžný jiný issue, reload/deep link.

**Konec:** skutečný issue run otevře správný detail; údaj „nikdy neběžel“ pouze s důkazem. Žádný retry engine.

## Iterace 6 — původ běhu

**Vstup:** 1, 3–5. Aditivní pole API a jejich schema podle aktuálního kódu.

**Dodat:** snapshot verze/hash, trigger, dostupný důvěryhodný iniciátor, odkazy. U historicky neověřené invoking identity nepoužívat označení ověřený původ. Inputs pouze existující oddělenou cestou, nikoli v exportu. Credential „deklarováno ve snapshotu“ a „použití nezaznamenáno“.

**Testy:** dnešní definice odlišná od executed snapshotu, missing version/iniciátor, historický run před bezpečnostní opravou. Změna dnešní definice nesmí změnit historický podklad.

**Konec:** žádné domnělé per-run použití credentialu. Nový credential.resolved event, pokud se později objedná, má vlastní PR/testy/retenci; není nutný k dokončení této iterace.

## Iterace 7 — verze při Run again

**Vstup:** klastr R, aktuální #2556/#2562. Backend lze oddělit od pozdější UI integrace.

**Dodat:** na skutečné /run cestě očekávaný definition hash, 409 při neshodě; předání právě ověřeného snapshotu/pinu executoru, aby HEAD nemohl přeskočit po samotné kontrole. UI načte aktuální metadata, ukáže konkrétní verze a při konfliktu nabídne nové načtení. CLI --version a případný hash s dokumentací. Vymezit delay/debounce, které nesmějí parametr tiše ignorovat.

**Testy:** publikace mezi otevřením a submitem i mezi kontrolou a dispatch, úspěšný pin, archivovaná verze 404, chybějící dependencies 422, governance historické verze a idempotentní retry nejistého startu.

**Konec:** vykonaná definice odpovídá zobrazenému příslibu; text přizná archivaci vstupů a možné opakování účinků. Bez přesunu UI na /replay a bez nového secret-input frameworku.

## Iterace 8 — access/me a Your access

**Vstup:** 1, rozhodnutá matice práv, aktuální UI po souběžných PR.

**Dodat:** rutinní a credential access/me ze stejných autorizačních funkcí jako akce; reason codes a přiznané dynamické brány. UI přestane na těchto plochách odhadovat práva jen z role. CLI/docs/OpenAPI podle repo pravidel.

**Testy:** role × capabilities × viditelnost × status, tenant izolace, revokace s dokumentovanou cache, parita access/me a skutečné autorizační fáze handleru. „Run != 403“ není dostatečný invariant: 404 může maskovat zákaz, 422 či 500 nesvědčí samy o povolení. Testovat význam rozhodnutí, ne libovolný HTTP kód.

**Konec:** čitelný důvod odmítnutí, unknown při chybě API, zachované gates reveal (interaktivní login/reason nemusí endpoint předpovědět). Živý druhý účet před uzavřením celé auth akceptace.

## Iterace 9 — credential dependents

**Vstup:** 8 a rozhodnutí zdroje multi-crew scope. Samotný scanner lze připravit dříve.

**Dodat:** sjednocený scanner deklarací/http/template referencí a metadata-only resolver preview bez decryptu. API/UI/CLI rozliší configured, would-resolve a skutečně zaznamenanou úroveň posledního fetch; bez run attribution. Respektovat crew-first prioritu i newest uvnitř scope. Agentní dynamiku přiznat, rekurzi call_pipeline explicitně vymezit. Opravit error≠empty auditu/fields a refetch chyby dependents.

**Testy:** dva credentials stejného typu v odlišných scopes, multi-crew, status/deleted, změna scope/rotace, skryté rutiny i agregované counts nesmí prozradit nepřístupné objekty. Náhled nic nepoužije ani nezapíše USE. Success→failure→recovery přes rodiče i dialogy.

**Konec:** známý dopad odebrání bez falešného „unused“. Jakákoli změna runtime distribuce tajemství oddělená od prezentačního PR a krytá schváleným kontraktem.

## Iterace 10 — draft handoff a Page kontext

**Vstup:** klastr P a rozhodnutí cíle/scope. Rozdělit na 10a a 10b.

**10a:** obecný neodesílající draft do explicitně nové session. Žádné přepsání cizího draftu, opakovaný mount bez duplikace, dosavadní ?prompt= změnit jen mimo tento scope se samostatným důvodem.

**10b:** tlačítko v PagesLayout SubBar, odstranitelný kontext, whitelistovaná page_context provenance v bridge a zobrazení po reloadu. Metadata klienta jsou nedůvěryhodné. Před odesláním kontrolovat povoleného příjemce a rozsah i revokaci; pouhé usePage s lidskými právy nezajišťuje agentní práva. Pokud potřebná cesta neexistuje, odevzdat 10a a vyčíslit blokovanou 10b, nezjednodušovat bezpečnostní kontrakt.

**Testy:** nová session, draft neodeslán, kontext odebrán z content i metadata, změna cíle/session, reload, revokace lidská i agentní, nedostupné/hidden panely, untrusted obsah, schema/byte limity bridge. Žádný payload aplikace či DOM v úzké variantě.

**Konec:** handoff jednoznačně uvádí dokončené 10a/10b a schopnosti, které chybí. Pokud metadata-only přenos nesplňuje uživatelský slib „vysvětli data“, tlačítko/text musí jeho omezení přiznat.

## Mimo desítku, ale nikoli zapomenuté

O5/A5: obsahový pack po výběru role a skutečného zdroje. Základ již existuje, nevytvářet nový form engine; chybějící konektor ocenit zvlášť.

Závěrečná integrovaná akceptace je povinná pro celý scénář: paleta → Page → oprávněná akce → verze/běh/výsledek → podklad → Run again. Ověřit druhý účet, revokaci a obnovu po výpadku. Průběžné E2E patří do jednotlivých iterací; závěrečná fáze není omluva deset iterací pouze unit-testovat. Pokud není rozhodnutá Page execute cesta, příslušný průchod zůstává otevřený.

O3 scoped diagnostika a desktopové směry zůstávají Later. Odhad oponenta 15–22 dní pro desítku není garance a nezahrnuje všechny tyto další balíky, čekání na review ani nové auth mezery.

## Prompt pro první Claude Code relaci

Tento prompt byl použit pro dokončenou iteraci 1; je zachován pro dohledatelnost a nesmí být znovu zadán jako nový úkol.

```text
Implementuj pouze bezpečnostní část iterace 1 podle:
- docs/prd/awx-omarchy-product-improvements-2026-09-14.md (revize 2026-09-15)
- docs/prd/awx-omarchy-implementation-iterations-2026-09-15.md
- docs/prd/reports/awx-omarchy-prd-opponent-2026-09-15.md a klastru S

Nejprve přečti AGENTS.md, CODEX.md a CONTRIBUTING.md. Ověř HEAD, WIP,
claims a aktuální souběžné PR; podle repo postupu připrav izolovanou větev.
Zachovej cizí práci. Reprodukuj K1 na aktuálním kódu, nepředpokládej,
že starý report stále platí.

Oprav důvěryhodnost invoking crew/agent: JWT ani interní caller nesmí
podvrhnout identitu mimo autentizovaný scope. Nestačí test přítomnosti
interního tokenu; crew/agent musejí být ověřeně svázané s callerem.
Zkontroluj persistenci a trust grant použití, přidej pozitivní a negativní
integrační testy. Zachovej legitimní user-driven a autor-crew fallback.

Neimplementuj paletu, access/me, nový RBAC ani rozšíření routine.run na
nové cesty. Jejich aktuální matici pouze zaznamenej. Neřeš credential scope
změnou SQL bez samostatného rozhodnutí. Neupravuj cizí operator-console PR.

Dokonči relevantní repo gates, uveď skutečně provedené testy a skipy.
Odevzdej reviewovatelnou změnu a handoff v docs/prd s referencí na commit,
rozsahem opravy, limity historických dat a zbývajícími rozhodnutími.
Žádný seed živé instance, změny zákaznických ACL, nasazení či merge.
```

## Šablona pro relace 2–10

```text
Implementuj pouze iteraci N z docs/prd/awx-omarchy-implementation-iterations-2026-09-15.md.
Přečti společné PRD, integrovanou oponenturu, příslušný klastr a handoff
předchozí iterace [DOPLNIT SOUBOR A COMMIT]. Dodrž repo pravidla a claims.
Ověř, že předpoklady iterace jsou opravdu v aktuálním stromu a že potřebná
produktová rozhodnutí byla přijata. Nevyvozuj je z doporučení oponenta.
Při chybějícím předpokladu dokonči nezávislou část a přesně popiš blokaci;
nezaváděj skrytě novou schopnost či širší oprávnění.
Proveď vymezené změny, testy, repo gates a akceptaci této iterace.
Odevzdej reviewovatelnou změnu a handoff pro N+1. Nezasahuj do následující
iterace, nespojuj více balíků jen proto, že zbývají tokeny. Nenasaď ani nemerguj.
```
