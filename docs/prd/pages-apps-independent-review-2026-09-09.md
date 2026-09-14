# Pages Apps — nezávislá oponentura (Claude Opus 5)

Datum: 9. září 2026. Autor: Claude Opus 5, na žádost vlastníka produktu.
Předmět: implementace ve worktree `.claude/worktrees/pages-apps-project`,
větev `feat/pages-apps-project`, základ `676e16e4`.
Posuzované dokumenty: `pages-apps-v1.md` (autoritativní rozsah),
`pages-apps-review-audit-2026-09-09.md` (autorova obhajoba), `pages-apps-handoff.md`.

Toto není potvrzení autorova auditu. Je to samostatná kontrola zdrojů, testů
a provozního stavu s vlastními nálezy. Kde souhlasím, říkám to; kde autor
podceňuje dopad, píšu proč; kde jsem našel něco, co v auditu není, je to
označené jako nový nález.

---

## 0. Verdikt

| Otázka | Verdikt |
|---|---|
| Pilot: interní Pages, reviewovaní autoři, Chromium, jedna instalace | **Podmíněně přijmout** |
| Placená distribuce / self-hosted zákazník | **Zatím odmítnout** |
| Je PRD „hotové“, jak zněl dotaz? | **Ne.** Čtyři otevřené gates v checklistu jsou skutečné, ne formalita. |

**Podmínky pro pilot** (v tomto pořadí, všechny jsou dnes nesplněné):

1. Rozdělit dodávku na reviewovatelné PR a doplnit CHANGELOG — dnešní tvar
   review fakticky vypne (nález **R1**).
2. Zapojit Docker a browser testy do CI — právě ta část, na které stojí
   bezpečnostní model, dnes v CI neběží vůbec (**R2**).
3. Opravit exkluzivní zámek údržby, který jednou za hodinu může shodit čtení
   publikované aplikace na 503 (**R3**).
4. Napsat a implementovat politiku podpory prohlížečů. Ne „Firefox zatím
   neověřen“, ale co konkrétně uvidí uživatel Firefoxu.

**Proč odmítám placenou distribuci:** ne kvůli kvalitě kódu, ale kvůli
instalaci a životnímu cyklu. Zákazník musí dostat druhou registrovatelnou
doménu s důvěryhodným TLS, Docker daemon dostupný serveru, `git` v PATH,
nový chráněný adresář v záloze a hodinovou údržbu — a k tomu dnes neexistuje
instalační postup ověřený na čisté instalaci. K tomu přistupuje **R4**
(kvóta Gitu je jednosměrná západka bez cesty zpět) a nevyřešená otázka, co
se stane, až se změní build profil u stovky zákaznických aplikací.

---

## 1. Co jsem skutečně ověřil

Rozlišuji stejně přísně, jako to dělá autorův audit.

| Ověřeno mnou v této relaci | Jak |
|---|---|
| `internal/pages`, `internal/pagebuild`, `examples/pages-apps` zelené | `go test -count=1`, exit 0 |
| Pages frontend zelený: 44 souborů / **592 testů** | `pnpm exec vitest run components/features/pages lib/pages hooks/__tests__/use-workspace.test.ts`, exit 0 |
| Nasazená binárka na dev3 odpovídá deklarovanému hashi | `sha256sum` = `f82b350d…31fe7`, shoda s auditem |
| Služba běží, stránka odpovídá 200 | `systemctl is-active crewship-ws@3`, `curl` na `/pages/custom-operations` |
| Rozsah změn | `git diff --numstat` + inventura untracked: **~159 souborů, ~18 000 řádků** |
| Docker/browser testy jsou v CI nezapojené | čtení `.github/workflows/*`, `grep` na `t.Skip` |
| Chybí CHANGELOG záznam | `git status` |

**Nepřevzal jsem, ale ani nepřepočítal:** celý běh 139 Go balíčků (984 s jen
API), 8 192 frontendových testů, živé Chromium/WebKit průchody, měření 24
souběžných čtenářů. Ty logy existují a jejich obsah odpovídá tvrzením; jejich
opakování by trvalo hodiny a nic by nezměnilo na nálezech níže.

**Neověřoval jsem vůbec:** živý průchod agentem z chatu, obnovu ze zálohy,
chování při výpadku disku, penetrační test, přístupnost.

---

## 2. Validace PRD jako dokumentu

Tohle je odpověď na „validuj to PRD“ — tedy kritika samotné specifikace,
ne implementace.

**Co je na PRD nadprůměrné.** Tvrzení jsou falzifikovatelná („nekonečná smyčka
zablokovala Studio“, ne „zvýšili jsme bezpečnost“). Non-goals jsou vypsané
konkrétně. Každé měření má u sebe větu, co z něj **nelze** vyvodit. Rozdíl mezi
„vykreslí se“, „nečte parent DOM“, „nezablokuje Studio“ a „má CPU limit“ je
v dokumentu explicitní. To je vzácné a je to hlavní důvod, proč se dá tato práce
vůbec seriózně oponovat.

**Pět konkrétních vad PRD:**

1. **Specifikace a kronika jsou v jednom souboru.** 758 řádků, kde sekce P1–P5
   obsahují věty typu „zbývá“, „nenasazeno“, „testy běží“, které už neplatí.
   Autor to přiznává (A13) a přidal na začátek aktuální checklist. To je záplata,
   ne oprava: čtenář, který dokument otevře poprvé, nedokáže bez vodítka
   rozhodnout, která věta platí. Pro release dokument je to funkční vada, ne
   estetická. **Oprava:** `pages-apps-v1.md` = jen současný stav a kontrakt;
   celá chronologie do `pages-apps-handoff.md`.
2. **Chybí akceptační kritéria formulovaná pro zákazníka.** Všechno je psané
   jako interní gate („celá Go sada prošla“). Neexistuje věta typu „nový
   uživatel s rolí ADMIN vytvoří přes chat Page a publikuje ji do 15 minut bez
   zásahu operátora“. Bez toho nelze rozhodnout, kdy je funkce hotová pro
   někoho jiného než pro nás.
3. **Chybí matice podpory jako produktový výrok.** PRD ví, že Firefox/WebKit
   neprošly stop-loop testem, ale nikde neříká, co s tím uživatel má dělat.
   To je jediná věc z celého dokumentu, kterou uvidí zákaznická podpora.
4. **Není plán životního cyklu build profilu.** A07 a A15 problém pojmenují
   (`react-vite-typescript/v1` + SDK vložené z aktuálního image), ale PRD
   neobsahuje pravidlo, jak dlouho se stará aplikace sestaví, kdo ji přebuilduje
   a co se stane, když nejde. Přitom je to jediný závazek, který poroste
   s počtem zákazníků.
5. **Metriky úspěchu chybí v PRD** a objevují se až v bodě 12 autorova auditu.
   Metrika, kterou nikdo neschválil, není metrika.

**Kde PRD naopak správně odolalo tlaku:** neslíbilo libovolné npm knihovny,
neslíbilo SSR, neslíbilo marketplace a nesnížilo bezpečnostní hranice kvůli
demu. Vývojová same-origin výjimka je pojmenovaná jako výjimka a je zapnutá
serverovým přepínačem, ne tichou konstantou. To je správně.

---

## 3. Co je opravdu dobré

Píšu to konkrétně, protože obecná pochvala nemá pro rozhodování cenu.

**3.1 Autorizační model akcí.** `internal/api/pages_application_actions.go` je
nejlepší část celé dodávky. Prohlížeč neposílá routine ani shell, jen ID
deklarované akce. Server načte **publikovanou** definici, ověří, že akce je
druhu `call`, a — to je ta podstatná část — kontrola verze publikace i vložení
do fronty proběhnou v **jedné SQLite transakci** (`UPDATE page_project_live …
WHERE version=? AND EXISTS(… spec_json=?)` bere writer lock před `Enqueue`).
Mezi kontrolou a zařazením se tedy nedá podstrčit jiná deklarace. Idempotency
klíč je odvozený jako `sha256(user:version:klientský klíč)`, takže nejde
zopakovat cizí receipt ani receipt starší publikace. `ApplicationActionStatus`
filtruje na `invoking_user_id` a znovu ověřuje viditelnost panelu.
Tohle je lepší, než co se běžně v „dashboard s tlačítkem“ produktech vidí.

**3.2 Build sandbox.** `internal/pagebuild/docker.go`: image připnutý digestem,
`--pull=never`, `--network=none`, read-only rootfs, `noexec` tmpfs, `cap-drop=ALL`,
`no-new-privileges`, UID 1001, limity PID/nofile/fsize, `timeout --signal=KILL`
**uvnitř** kontejneru (deadline přežije pád serveru) a úklid podle
nepředvídatelného jména kontejneru. Worker navíc porovnává `dependencies` a
`pnpm-lock.yaml` byte na byte proti nainstalovanému profilu a odmítá vlastní
`vite/postcss/tailwind` konfiguraci. Nedůvěryhodný vstup se tu bere vážně.

**3.3 Backpressure na MessagePortu.** `PreviewSnapshotChannel`: nejvýše jedna
nepotvrzená zpráva a jeden poslední čekající snapshot, ACK od dítěte, 100ms
plánovač. Zaseknutý renderer nenafoukne frontu. Většina implementací tohohle
vzoru prostě posílá a doufá.

**3.4 Potvrzovací dialog kreslí Studio, ne aplikace.** Deklaraci akce si host
načte ze serveru (`/panels/{id}/actions`), ne z iframe, a v dialogu vždy ukáže
slug, verzi publikace, **jméno routine** a syrové vstupy. Autor Page si sice
může zvolit vlastní titulek a text, ale nemůže přepsat ty tři pravdivé údaje.
Dobrý kompromis mezi použitelností a UI redressem.

**3.5 Přenosový kodek.** `internal/pages/project.go`: striktní YAML bez aliasů,
merge klíčů a druhého dokumentu; kanonický base64 (dekódování a zpětné
zakódování musí dát stejný řetězec); odmítnutí case kolizí i na Linuxu; kontrola
konfliktu soubor/adresář; rezervovaná jména Windows; digest počítaný přes
`map[string][]byte`, takže je nezávislý na pořadí souborů a na tom, jestli je
text uložený jako utf8 nebo base64. Tady byl někdo důsledný.

**3.6 Paleta.** Validace `^#[0-9a-fA-F]{6}$` na serveru, v `lib/pages/theme.ts`
i v SDK před `setProperty`. Žádná cesta, jak dostat do CSS proměnné cokoli
jiného než hex barvu. Tohle je typicky místo, kde se dělá injekce; tady není.

**3.7 Poctivost dokumentace.** Opakuji ji jako přednost, protože je to
konkurenční výhoda při auditu: dokument, který sám říká „`reviewed_code=true`
je prohlášení uživatele, ne scanner“, se dá použít jako podklad pro rozhodnutí.
Dokument, který tvrdí „aplikace jsou bezpečně izolované“, ne.

---

## 4. Nálezy

Závažnost je vztažená k **pilotnímu nasazení**, ne k hypotetické produkci.

| ID | Závažnost | Nález | Nový? |
|---|---|---|---|
| R1 | **Blokátor** | Rozsah dodávky vypne automatické review a shodí changelog gate | ano |
| R2 | **Blokátor** | Docker a browser testy nejsou v CI — bezpečnostní jádro bez regresní ochrany | ano (audit se toho dotýká) |
| R3 | Vysoká | Hodinová údržba drží exkluzivní zámek → čtenáři dostanou 503 | ano |
| R4 | Vysoká | Git kvóta je jednosměrná; po naplnění nelze Page dál ukládat a není cesta zpět | rozšiřuje A06 |
| R5 | Střední | Retry publikace může po rollbacku vrátit falešný úspěch | ano |
| R6 | Střední | Nový loading blokuje i Pages, které žádnou aplikaci nemají | ano |
| R7 | Střední | Checkpoint = až 256 forků `git` + walk celého úložiště na každý save | ano |
| R8 | Nízká | Rozdíl validace cest Go vs. worker (potvrzuji A08, rozšiřuji o mezery) | A08 |
| R9 | Nízká | Selhání lease zahodí hotový build a přepíše skutečnou chybu | ano |
| R10 | Nízká | Klientská kontrola runtime domény porovnává jen hostname | ano |

### R1 — Dodávka v tomto tvaru nebude reviewovaná (blokátor)

**Stav:** 61 změněných + 97 nových souborů ≈ **159 souborů, ~18 000 řádků**,
necommitnuté, v jedné větvi. Žádný záznam v `CHANGELOG.md`.

**Proč je to blokátor a ne organizační detail:** hlavička
`.github/workflows/changelog-guard.yml` v tomto repozitáři sama popisuje, jak
PR #2049 prošel bez jakéhokoli review, protože **narazil na 100souborový limit
CodeRabbitu** — a status přesto svítil zeleně. Tato dodávka je o 59 souborů nad
tím limitem. Zároveň mění HTTP API, CLI i webovou aplikaci, což jsou přesně tři
povrchy, na které changelog-guard vyžaduje záznam v sekci `## [Unreleased]`;
ten chybí, takže gate spadne.

Výsledek, pokud se to otevře jako jeden PR: 18 000 řádků bezpečnostně
relevantního kódu (sandbox, RBAC, spouštění akcí, obnova záloh) projde bez
strojového review a s velmi malou šancí na poctivé lidské review.

**Minimální oprava:** rozdělit podle vrstev, každá pod 100 souborů a samostatně
zelená:
`(1)` `internal/pages` kodek + transfer + CLI `pack/unpack` →
`(2)` úložiště, Git, lease, retence →
`(3)` `internal/pagebuild` + build API →
`(4)` runtime, SDK, frontend preview →
`(5)` publikace, akce, historie →
`(6)` zálohy/obnova →
`(7)` MCP autorství →
`(8)` paleta + loading UX →
`(9)` seed, examples, docs.
Pořadí odpovídá závislostem. Do každého PR patří jeho testy a jedna věta
v changelogu.

**Vedlejší zjištění:** větev obsahuje i řádek
`POST /api/v1/credentials/{credentialId}/refresh` v
`internal/api/testdata/route-roles.txt`. Ta routa je registrovaná už v základním
commitu `676e16e4` (`internal/api/router_crews.go:461`), ale v manifestu tam
chybí — a `TestMutationRouteRolesMatchManifest` označuje neznámou routu za
chybu. **Z toho plyne, že `main` je na tomto testu pravděpodobně červený**
a tato větev to mimochodem opravuje. Doporučuji ověřit
(`go test ./internal/api -run TestMutationRouteRolesMatchManifest` nad čistým
`main`) a poslat opravu samostatným malým PR, ne schovanou v Pages dodávce.

### R2 — Bezpečnostní jádro nemá v CI žádnou regresní ochranu (blokátor)

**Zjištění:** tyto testy se bez proměnné prostředí přeskočí a **žádný workflow
v `.github/workflows/` je nenastavuje**:

- `internal/pagebuild/docker_test.go:33` — `CREWSHIP_TEST_PAGE_BUILD_IMAGE`
- `internal/api/pages_build_docker_test.go:19` — pinned tools image
- `cmd/crewship/cmd_seed_page_app_test.go:26` — `PAGES_TEST_BUILD_IMAGE`
- `internal/pagebuild/runtime_test.go:47` — „browser harness only“

Skripty `e2e/pages-loading-smoke.mjs`, `e2e/pages-appearance-smoke.mjs`
a `e2e/pages-preview-smoke.mjs` nejsou v žádném workflow; spouštěly se ručně
v Playwright kontejneru.

**Dopad:** offline compiler (odmítnutí cizího lockfile, zákaz package scripts),
izolace sandboxu, zastavení zacyklené smyčky a skrytí prázdného dokumentu jsou
dnes ověřené výhradně tím, že je jeden člověk jednou spustil na dev3. První
refaktor `containerArgs`, `bootstrap.js` nebo CSP je rozbije potichu a `go test
./...` zůstane zelené.

**Minimální oprava:** repozitář už má lane `playwright-pr` v `ci.yml:677`
i akci `setup-playwright-browser`. Přidat (a) job, který postaví tools image
z `tools/pages-build/Dockerfile`, exportuje digest do
`CREWSHIP_TEST_PAGE_BUILD_IMAGE` a spustí čtyři přeskakované testy;
(b) jeden Playwright case do existující PR lane, který ověří přesně dvě věci:
sandbox nedosáhne na parent DOM a zacyklený frame nezablokuje host. Nemusí to
běžet na každý PR — stačí `paths:` filtr na `internal/pagebuild|tools/pages-build|lib/pages`.

### R3 — Údržba může jednou za hodinu vrátit čtenářům 503

**Kód:** `internal/api/pages_project_retention.go:15` vezme
`Lease(ctx, workspace, true)` — **exkluzivní flock** — a drží ho přes celou SQL
transakci a `Prune`. `Prune` uvnitř spouští `git repack -Ad` s vlastním 30s
timeoutem **na každý repozitář** (`internal/pages/project_retention.go:117`).
Celý průchod má rozpočet 2 minuty na workspace.

Proti tomu každé čtení publikované aplikace (`PageApplication`) volá
`h.pageLease(...)` → sdílený flock s **30s** limitem, po kterém handler
odpoví `503 „Page storage is busy; try again shortly“`.

**Scénář selhání:** workspace s deseti Pages, po retenci se u několika změnily
refy → několik `repack` běhů po sobě. Údržba drží exkluzivní zámek přes 30 s.
Každý uživatel, který má v tu chvíli otevřený dashboard, dostane při 60s
revalidaci 503 a UI zobrazí chybu. Jednou za hodinu, tiše, bez alertu.

**Minimální oprava:** exkluzivní zámek držet jen kolem SQL transakce a mazání
souborů; `repack`/`prune` provádět po jeho uvolnění (jsou to idempotentní
operace nad již odstraněnými refy), nebo je posunout do samostatného, méně
častého průchodu. Zároveň: čtenářský 503 by neměl shodit už načtenou aplikaci —
UI má mít poslední známý artefakt.

### R4 — Kvóta Gitu je západka bez cesty zpět

**Kód:** `internal/pages/project_git.go:175` sečte velikost celého
`git/` adresáře workspace a `Checkpoint` odmítne zápis, pokud by se překročilo
`MaxProjectGitBytes = 128 MiB`. Retence maže refy, ale komentář
`internal/pages/project_retention.go:114` říká pravdu: **předci zůstávají**.
Protože každý checkpoint má jako rodiče předchozí, je z nejnovějšího
ponechaného commitu dosažitelná celá historie až k revizi 1. `prune --expire=now`
tedy prakticky nikdy nic neuvolní.

**Důsledek, který audit (A06) neříká dost tvrdě:** až se kvóta naplní, začne
`Checkpoint` vracet `ErrProjectGitFull` a **Page už nikdy nepůjde uložit**.
Neexistuje žádný podporovaný příkaz, kterým by operátor místo uvolnil — žádné
`page project archive`, `squash`, ani „zahodit historii starší než X“. Jediné
řešení je smazat Page (což smaže repozitář) nebo ruční zásah do souborů.

Realisticky je to vzdálený horizont (jednotky kB na revizi → řádově tisíce
revizí), ale je to **terminální stav**, ne degradace. To je jiná kategorie rizika
než „narazíme na limit“.

**Minimální oprava (na výběr):** (a) `page project compact`, který založí nový
kořenový commit z aktuální revize a stará historie se stane nedosažitelnou;
(b) tvrdý strop na počet revizí v Gitu s automatickým odříznutím předků;
(c) minimálně varování v UI a metrika při 80 % kvóty, aby to nikdo neobjevil
až selháním zápisu.

### R5 — Retry publikace může po rollbacku vrátit falešný úspěch

**Kód:** `internal/api/pages_project_publish.go:173–180`. Přesný retry se
dohledá dotazem
`SELECT … FROM page_project_publications WHERE page_id=? AND version=?
AND build_id=? AND source_revision=? AND actor_user_id=? AND COALESCE(rollback_of,0)=?`
a při nálezu vrátí `200` s původním receiptem. Dotaz **nekontroluje aktuální
`page_project_live`**.

**Scénář selhání:** admin publikuje v3. Kolega mezitím udělá rollback, čímž
vznikne v4 (obsah v2) a live ukazuje na 4. Adminův starý tab (nebo CLI retry po
síťové chybě) pošle znovu publish s `expected_publication = 2`. Normální cestou
by dostal `409 „Publication changed“`. Místo toho dostane `200` a receipt v3,
UI napíše „Published version 3“ — ale živá je pořád v4. Admin odejde
s přesvědčením, že jeho verze běží.

**Minimální oprava:** do dotazu přidat podmínku, že tato verze je stále živá
(`JOIN page_project_live l ON l.page_id=… AND l.version=p.version AND l.published=1`),
jinak spadnout do standardní CAS větve a vrátit 409.

### R6 — Nový loading blokuje i Pages bez aplikace

**Kód:** `components/features/pages/page-application.tsx:20`:

```tsx
if (!query.isError && (query.isPending || (query.data?.publication && !opened)))
  return <div role="status">Loading application…</div>
```

`PageApplicationView` obaluje v `pages-layout.tsx:289` **každou** vybranou Page.
`usePageApplication` běží i pro Pages, které žádnou publikaci nemají, a
`query.isPending` je při prvním načtení `true` vždy. Panelová stránka bez
aplikace tedy nově čeká na round trip na `/application` a mezitím ukazuje
celoplošné „Loading application…“ — text, který pro ni navíc není pravdivý.
Test `page-application.test.tsx` toto chování zafixoval jako záměr
(„does not flash the panel fallback while publication metadata is loading“).

**Posouzení:** ta oprava správně řeší nahlášený problém (probliknutí panelů
u aplikačních Pages), ale platí za to novou bariérou pro všechny ostatní.
Kořenová příčina je architektonická: „má tahle Page aplikaci?“ je druhý
požadavek. Řešilo se to čekáním místo odstranění toho čekání.

**Minimální oprava:** vrátit `publication_version` (nebo `has_application`)
už v detailu Page, který layout stejně načítá. Pak layout ví bez dalšího
requestu, jestli má rezervovat plochu pro aplikaci, nebo rovnou vykreslit
panely — a interstitial se objeví jen tam, kde aplikace skutečně je.

### R7 — Cena jednoho uložení projektu

**Kód:** `internal/pages/project_git.go:117` volá
`git hash-object -w --no-filters --stdin` **jako samostatný proces pro každý
soubor**, plus `mktree` pro každý adresář, plus `commit-tree` a `update-ref`.
Projekt s 256 soubory tedy znamená ~260 forků na jeden save. Před tím ještě
`filepath.WalkDir` přes **celý** `git/` adresář workspace kvůli kvótě
(řádek 175), tedy `stat` nad všemi loose objekty.

Hlavní autorský tok je přitom „agent uloží malou změnu, přečte, uloží znovu“.
Deset iterací nad středně velkým projektem = tisíce procesů a desítky průchodů
úložištěm. Na sdíleném dev boxu s běžícími agenty to je měřitelné.

**Minimální oprava:** `git hash-object -w --stdin-paths` (jeden proces na dávku)
nebo `fast-import`; a cachovaná velikost úložiště aktualizovaná při zápisu
místo `WalkDir` na každý checkpoint.

### R8 — Rozdíl validace cest (potvrzuji A08, rozšiřuji)

Go (`internal/pages/project.go:validateProjectPath`) povoluje libovolné
neřídicí Unicode znaky včetně mezer uvnitř názvu. Worker
(`tools/pages-build/build.mjs:21`) povoluje jen `^[\w./@+-]+$`, kde `\w` je
v JS bez `u` flagu čistě ASCII, a navíc zakazuje segmenty začínající tečkou.

Prochází tedy uložení a padá až build: `src/čísla.tsx`, `src/my file.tsx`,
`src/.keep`. Chyba se objeví jako `Unsafe source path` ve stderr buildu, tedy
po minutách a bez vazby na konkrétní soubor.

To má pro agentní tvorbu horší dopad, než se zdá: agent, který pojmenuje soubor
česky, se to dozví až z neúspěšného buildu a nemá z chyby jak odvodit pravidlo.
**Minimální oprava:** sjednotit charset už v Go validaci (odmítnout při `save`,
s konkrétní hláškou), a pravidlo vydat i v `page_project read` jako součást
popisu profilu.

### R9 — Selhání lease zahodí hotový build

`internal/api/pages_build.go:132`: `err = leaseErr` přepíše případnou chybu
buildu; a když se lease nepodaří získat do 10 s (typicky během hodinové údržby,
viz R3), zahodí se **úspěšně sestavený** artefakt a build se označí jako
`failed` s hláškou o zaneprázdněné údržbě. Uživatel po dvou minutách čekání
dostane chybu, která s jeho kódem nesouvisí.

**Minimální oprava:** lease brát s delším timeoutem a s retry, chybu lease
nezaměňovat s chybou buildu, a stav označit jako `interrupted` (existuje),
ne `failed`.

### R10 — Klientská kontrola runtime domény je slabší, než tvrdí

`lib/pages/preview-runtime.ts:20` porovnává `runtime.hostname === studio.hostname`,
tedy přesný hostname, ne eTLD+1. `pages.firma.cz` vs. `studio.firma.cz` projde
klientskou kontrolou a hláška „The preview needs a separate application domain“
se neobjeví, přestože jde o stejný site. Server to zachytí správně
(`ValidateRuntimeOriginForDevelopment`, přes public suffix list) a vrátí 503,
takže **nejde o zneužitelnou díru** — ale defense-in-depth kontrola tvrdí víc,
než dělá, a komentář nad ní to nepřiznává.

---

## 5. Architektonická kritika (advokátský blok)

Tady nejde o chyby, ale o rozhodnutí, která by měl vlastník produktu vědomě
potvrdit nebo změnit.

### 5.1 Nejdražší bezpečnostní kontrola chrání proti nejméně pravděpodobnému útočníkovi

Požadavek na **druhou registrovatelnou doménu** je největší instalační náklad
celé funkce: DNS, TLS, dostupnost z prohlížeče uživatele, u self-hosted i
interní CA. Vznikl z jednoho měření — `srcdoc` verze umožnila zacyklenému
skriptu zablokovat Studio.

Jenže:

- Chrání proti **vadnému** kódu, ne proti zlému. Model důvěry přitom stojí na
  tom, že kód je reviewovaný člověkem (`reviewed_code`). Reviewovaný autor
  nepíše `while(true)` často; když ano, uživatel zavře záložku.
- **Nefunguje mimo Chromium.** Firefox neprošel ani se dvěma různými
  registrable doménami. Platíme tedy plnou instalační cenu za záruku, kterou
  máme jen v jednom prohlížeči.
- Kanál, který by použil skutečně zlý autor — **navigace vlastního iframe** na
  externí URL s daty v query stringu — tahle kontrola nezavírá a zavřít nemůže
  (CSP nemá směrnici pro navigaci vlastního dokumentu, `sandbox` bez
  `allow-top-navigation` brání jen navigaci rodiče). Audit to přiznává (A03),
  ale nespojuje si to s tím, že hlavní obrana míří jinam.

**Nedoporučuji to zrušit** — pro spravovanou distribuci na naší doméně je to
levné a správné. Doporučuji tři věci:
(1) přeformulovat z „bezpečnostní hranice“ na „odolnost proti vadnému kódu,
prokázaná v Chromiu“;
(2) pro self-hosted instalaci povolit **vědomý** provoz bez druhé domény se
stejným přepínačem, jaký má dev3, včetně varování v UI — jinak si to operátoři
stejně zapnou, jen hůř;
(3) přidat levnou **detekci** místo nemožné prevence: 20s watchdog v
`page-preview.tsx` už existuje, stačí ho rozšířit o „iframe se přenavigoval“
(druhý `onLoad` už se dnes ignoruje, ale nikdo o něm neřekne) a událost zapsat
do auditu. Detekovaná exfiltrace je proti nedetekované obrovský rozdíl.

### 5.2 „Plně custom“ versus připnutý profil — největší zdroj budoucího zklamání

Uživatelské zadání znělo „vlastní web, plně animovaný, plně pod kontrolou“.
Dodáno je: vlastní React, vlastní CSS, vlastní layout — nad **pevným seznamem
závislostí**, bez vlastního `vite.config`, bez `public/`, bez externího fetch,
s povinným `index.html`, jehož skutečný shell stejně kreslí runtime.

To je pro v1 správné rozhodnutí a nezpochybňuji ho. Zpochybňuji, že to nikde
není strojově čitelné. Agent se dozví, co nesmí, tak, že mu selže build.
`page_project read` už dnes vrací zdroj SDK — je to jediné správné místo, kam
patří i seznam povolených závislostí, zakázaných konfiguračních jmen a
povolený charset cest. Jinak bude agent generovat nepodporované projekty
opakovaně a uživatel to bude vnímat jako „nefunguje to“.

### 5.3 Publikace nepřipíná to, co se skutečně spustí

A17 je v auditu uvedený jako „známá mez“. Podle mě je to nejpodceněnější
položka celého dokumentu, protože míří přímo na hlavní produktovou hodnotu.

Publikace zmrazí UI artefakt a deklaraci akce. Nezmrazí **routine ani skript**,
který se po stisku tlačítka vykoná. Kdokoli s právem editovat routine může
změnit chování tlačítka, které schválil někdo jiný, a schvalovací stopa
(`checks_json`, `reviewed_code`, `actor_user_id`) o tom nic neví. U tlačítka,
jehož smysl je „spusť skutečnou operaci nad produkcí“, je to díra ve smyslu
schválení, ne jen nepřesnost.

**Doporučení:** do `page_project_publications` uložit identitu routine
v okamžiku publikace (revizi/hash definice) a při dispatchi porovnat. Nemusí to
blokovat běh; stačí, aby to bylo v receiptu a aby UI u potvrzení řeklo
„definice routine se od schválení změnila“.

### 5.4 Tři reprezentace zdroje

SQLite (ukazatele) + immutable YAML snapshot (obsahově adresovaný) + bare Git
(historie). Autor to označuje za úmyslnou redundanci — souhlasím s odůvodněním,
ale redundance bez nástroje na ověření konzistence je dluh. Chybí příkaz typu
`crewship page project fsck`, který projde SQL kořeny, ověří existenci a digest
souborů, dosažitelnost commitů a osiřelé soubory. Dnes to lze zjistit jen tím,
že se něco pokazí při publikaci nebo obnově.

### 5.5 Priorita práce v posledních dvou relacích

Kritika, kterou je fér vyslovit nahlas: poslední dvě pracovní relace šly do
společné palety, animací, probliknutí při načtení a mobilní hlavičky. Je to
odvedené dobře a uživatel to sám vyžádal. Ale dodávka má čtyři otevřené gates,
z nichž **A04 — živé autorství z chatu — je zároveň hlavní deklarovaný
diferenciátor produktu** a je stále neprokázané. Vyleštěný přechod na funkci,
která nemůže být vydaná, je optimalizace špatné veličiny. Doporučuji další
relaci věnovat výhradně živému průchodu agent → Page → review → publish → akce
→ odebrání práv, protože to je jediná věc, která může ještě celý návrh vyvrátit.

---

## 6. Jakou hodnotu to přidalo Crewshipu

### 6.1 Strategicky

Crewship dosud uměl „agenti dělají práci a hlásí ji“. Tato funkce přidává
„agent dodá rozhraní pro tu práci a rozhraní umí práci spustit“. To je posun
kategorie, ne feature. Konkrétně:

- **Bez toho** musí každou vertikálu (MySQL přehled, Ansible přehled) naprogramovat
  tým Crewshipu do produktu. **S tím** ji dodá agent zákazníkovi na míru za
  minuty a Crewship zůstane platformou.
- Uzavírá se smyčka **data → zobrazení → akce → data** uvnitř jednoho produktu
  a jednoho oprávnění. Většina konkurence umí buď dashboard (BI), nebo
  automatizaci (runbooky), ne obojí pod jedním RBAC.
- Vzniká první **přenositelný artefakt zákaznické hodnoty**: jeden YAML, který
  nese Page i její zdroj. To je materiál pro budoucí sdílení mezi instalacemi
  i pro onboarding.

Pořadí hodnoty bych proti autorovi upravil takto:

1. Řízený přechod od přehledu k akci (tohle je jediná část, kterou nikdo jiný
   nemá zadarmo).
2. Agentní autorství stejným SDK a profilem — **potenciálně** největší hodnota,
   ale dnes neprokázaná; proto až druhé místo.
3. Vlastní UI nad existujícími daty.
4. Draft/publish/rollback a vlastnictví crew (ochrana ostatních uživatelů;
   nudné, ale bez toho to nelze pustit k lidem).
5. Přenosný YAML a seed.
6. Paleta a plynulé načtení — reálný UX přínos, nejnižší strategická váha.

### 6.2 Kolik to stálo a bude stát

| Rozměr | Hodnota |
|---|---|
| Rozsah změny | ~159 souborů, ~18 000 řádků nad repozitářem s ~470 000 řádky Go |
| Nové trvalé služby | **žádná** (žádný Node server, žádná další DB) |
| Nové závislosti provozu | `git` v PATH, přístup k Docker daemonu, druhá doména + TLS, nový chráněný adresář, hodinová údržba |
| Nový obraz | tools image ~117,5 MiB, připnutý digestem |
| Běžné čtení | levné: `N/60` podmíněných požadavků za sekundu při N otevřených aplikacích, 304 bez přenosu artefaktu |
| Build | drahý, ale ohraničený: 1 slot, 1 CPU, 1 GiB, 120 s |
| Skutečný dlouhodobý náklad | **údržba životního cyklu** — zdroje, buildy, publikace, retence, obnova, kompatibilita profilu |

Souhlasím s autorovým shrnutím „lehký běžný provoz, středně náročné buildy,
významná údržbová složitost“ a doplňuji: ta údržbová složitost je nová
**produktová povinnost**, ne jen kód. Někdo bude muset rok co rok odpovídat na
otázku „proč se mi nesestaví aplikace, která fungovala loni“. V PRD zatím nemá
vlastníka.

### 6.3 Odpověď na otázku „kolik hodnoty to přidalo“

Ve své současné podobě: **vysoká potenciální hodnota, dnes nezinkasovaná.**
Kód je z větší části na úrovni, kterou bych čekal u produkčního systému.
Zablokované to není technickou kvalitou, ale čtyřmi otevřenými gates a tím, že
dodávka není v hlavní větvi ani reviewovaná. Dokud běží jen na dev3 z
necommitnuté větve, je hodnota pro zákazníka nulová a riziko ztráty práce
nenulové (jeden worktree, žádný commit).

Jinak řečeno: **největší návratnost teď nemá žádná nová funkce, ale
zaříznutí rozsahu a dostání téhle práce do `main`.**

---

## 7. Doporučené pořadí dokončení

Liší se od autorova pořadníku tím, že dává release mechaniku před bezpečnostní
diskuzi — protože bez ní se ta diskuze stejně nikdy neodehraje nad reálným kódem.

1. **Zmrazit rozsah.** Žádná nová funkce. Commitnout to, co je, ať to nemůže
   zmizet z worktree.
2. **R1** — rozdělit na PR podle vrstev, doplnit CHANGELOG. Tohle odblokuje
   jakékoli skutečné review, včetně strojového.
3. **R2** — Docker a browser testy do CI, se `paths:` filtrem.
4. **R3, R5, R9** — tři malé opravy s jasným scénářem selhání.
5. **A04 / živý průchod z chatu** — agent vytvoří a upraví Page, člověk
   publikuje, akce doběhne, čtenář vidí, po odebrání práv nevidí. Zapsat kroky
   tak, aby je zopakoval kdokoli.
6. **Politika podpory prohlížečů** jako produktový text + implementované chování
   (co uvidí uživatel Firefoxu).
7. **R4, R7, R8** — kvóta, výkon checkpointu, sjednocení cest.
8. **Instalační postup na čisté instalaci** a teprve pak diskuze o placené
   distribuci.

---

## 8. Co bych naopak neměnil ani nepřidával

- Nepřidávat Puck/Refine/JSON renderer. Autorovo odůvodnění je správné a
  přidání dalšího autorského modelu by rozsah zdvojnásobilo.
- Nerozšiřovat SDK na obecné API. Úzký broker je důvod, proč je model akcí
  obhajitelný.
- Nezavádět frontu buildů, dokud nemáme naměřený počet 429. Jeden slot je
  správný default.
- Nezavádět druhý runtime (Vue/Svelte/SSR) dřív než po pilotech u tří týmů.
- Neopravovat „redundanci“ SQL + YAML + Git jejím odstraněním. Přidat k ní
  kontrolu (5.4), ne ubrat vrstvu.

---

## 9. Co jsem neověřil a co může můj verdikt změnit

- Neprovedl jsem celý repo-wide běh Go testů ani celý frontend (8 192 testů).
  Přijímám cizí logy jako věrohodné, ale nejsou to moje měření.
- Neprovedl jsem žádný živý průchod ve Studiu, ani přihlášení na dev3.
- Neprovedl jsem penetrační test, test obnovy ze zálohy, ani zátěžový test.
- Nekontroloval jsem přístupnost (kontrast, klávesnice, čtečky) nad rámec toho,
  že komponenty mají `role="status"`/`aria-busy`.
- **R1 vedlejší zjištění** (červený `main` na route-roles manifestu) je odvozené
  ze čtení kódu a manifestu, ne z běhu testu nad čistým `main`.

Pokud kterýkoli z těchto bodů dopadne špatně, verdikt „podmíněně přijmout pro
pilot“ se posouvá dolů, nikoli nahoru.

---

## 10. Jednou větou

Je to nadprůměrně poctivě postavená a nadprůměrně poctivě zdokumentovaná
funkce, jejíž bezpečnostní jádro je lepší než většina toho, co se v téhle
kategorii vidí — a která je dnes ohrožená ne technickou kvalitou, ale tím, že
18 000 řádků leží v jednom worktree bez commitu, bez changelogu, bez
reviewovatelného PR a bez CI pokrytí právě těch částí, na kterých celý model
důvěry stojí.
