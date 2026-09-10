# Pages Apps — kontrola oprav (Claude Opus 5)

Datum: 10. září 2026. Navazuje na [nezávislou oponenturu z 9. 9.](pages-apps-independent-review-2026-09-09.md).
Předmět: reakce autora na nálezy R1–R10 a na architektonické body 5.1–5.5.
Stav větve: `feat/pages-apps-project`, 17 commitů nad `676e16e4`, pracovní strom čistý,
práce rozdělená do pěti PR #2475 → #2477 → #2479 → #2480 → #2481.

## Souhrn

Opravy jsou dobré. Devět z deseti nálezů je adresovaných, dva z nich lépe, než jsem
navrhoval. Autor navíc dodal věci, které jsem nežádal: `page project fsck` a `compact`,
otisk build profilu v artefaktu, opravu dělení UTF-8 na hranici stdin chunků a
povinné HTTPS pro runtime mimo loopback.

**Jedna věc se ale nezlepšila, jen přesunula:** rozdělení na pět navazujících PR
opravilo *tvar* dodávky, ale výsledek je dnes stejný jako předtím — většina kódu nemá
ani CI, ani strojové review, a všechny PR přitom svítí zeleně. Detail v N1.

Vlastní ověření v této relaci: `internal/pages`, `internal/pagebuild`,
`examples/pages-apps` zelené (exit 0); Pages frontend 46 souborů / **598 testů** zelených.

## Stav nálezů

| ID | Stav | Poznámka |
|---|---|---|
| R1 | **Tvar opraven, výsledek ne** | Viz N1 — stacked PR nedostane CI ani review |
| R2 | **Opraveno lépe, než jsem žádal** | Povinný job + kontrola, že se nic nepřeskočilo |
| R3 | **Opraveno správně** | Dva zámky, `release()` před kompakcí, UI přežije 503 |
| R4 | **Opraveno dobrým mechanismem** | Git `shallow` hranice + `compact` + `fsck`; reziduum v N2 |
| R5 | **Opraveno jinak a lépe** | Receipt říká pravdu místo 409 |
| R6 | **Opraveno přesně podle návrhu** | `has_application` v detailu Page; reziduum v N3 |
| R7 | **Z poloviny** | Forky pryč, kvótový walk zůstal |
| R8 | **Jen kosmeticky** | Hláška workeru zlepšena, Go validace nezměněna |
| R9 | **Opraveno správně** | Chyba kompilátoru je autoritativní |
| R10 | **Opraveno poctivě** | Komentář přiznává, že autoritou je server |
| A17 | **Implementováno** | Otisk definice routine v receiptu + varování v potvrzení |

### R2 — nejlepší oprava z celé série

`.github/workflows/ci.yml` má nový povinný job `pages-apps` („no Docker env opt-in can
silently skip this lane"), který spouští `scripts/test-pages-apps.sh`. Skript postaví
tools image, exportuje `CREWSHIP_TEST_PAGE_BUILD_IMAGE`, spustí dosud přeskakované
testy — a pak **v Pythonu ověří, že konkrétní testy skutečně prošly a že se nic
neskipovalo**. To je silnější kontrakt, než jsem navrhoval: nestačí zelený běh,
musí být zelený z konkrétních důvodů.

Na #2475 job reálně proběhl (docker build → compiler testy → Chromium smoke, 1 m 12 s),
což jsem ověřil v logu běhu. Skript navíc roste po vrstvách spolu s PR, takže každý PR
zůstává samostatně zelený. To je disciplinovaná práce.

### R3 — přesně na místě

`internal/api/pages_project_retention.go:92` volá `release()` s komentářem
„The expensive Git pass must not hold the readers' exclusive lease", a teprve pak
`Compact`. Zámky jsou nově dva: `.maintenance.lock` (30 s) a `.git-maintenance.lock`
(2 min), pořadí je konzistentní (workspace → git), takže nehrozí deadlock. Uvolnění je
`sync.Once`, takže explicitní `release()` a `defer release()` se nebijí.

K tomu `PageApplicationView` rozlišuje 503 (`storageBusy`) a drží poslední ověřenou
verzi s hláškou „Showing the last verified version." Přesně to jsem doporučoval.

### R5 — lepší než můj návrh

Nechal dotaz beze změny a místo 409 přidal do receiptu `live_version`, `published`,
`is_current` a `replayed`; `lib/pages/publication-receipt.ts` z toho renderuje
„Version 3 was published earlier. The current live version is 4." To je správnější
než chyba: replay skutečně uspěl, jen není živý, a uživatel to teď vidí.

### R4 — mechanismus, který jsem nenavrhl, a je lepší

Místo squashe historie píše `internal/pages/project_boundaries.go` do bare repozitáře
soubor `shallow` se seznamem ponechaných checkpointů. Předci se tím stanou
nedosažitelnými a `repack -Ad` + `prune --expire=now` je uvolní. Hranice se přenášejí
i v záloze (`internal/backup/pageprojects.go:340`). Kvóta přestala být západkou.

Bonus, o který jsem žádal v bodě 5.4: `crewship page project fsck` ověří SQL, checkpoint
a artefakt; `compact` vyvolá kompakci ručně.

## Nové nálezy

### N1 — Stacked PR nedostane CI ani review, a všech pět svítí zeleně (blokátor)

`ci.yml` má `on: pull_request: branches: [main]`. Základy PR jsou:

| PR | base | soubory | co reálně proběhlo |
|---|---|---|---|
| #2475 | `main` | 48 | plné CI (Go 13 m 29 s, Frontend Test 9 m, `pages-apps` 1 m 12 s…), **CodeRabbit: „Review rate limited"** |
| #2477 | `feat/pages-apps-source-review` | 87 | jen `label` + `surface`; **CodeRabbit: „reviews are disabled for this base branch"** |
| #2479 | `feat/pages-apps-server-review` | 40 | totéž |
| #2480 | `feat/pages-apps-cli-review` | 44 | totéž |
| #2481 | `feat/pages-apps-ui-review` | 32 | totéž |

Dohromady **203 z 251 souborů dnes nemá ani jeden test z CI, ani strojové review** —
a `gh pr checks` u nich vrací samá `pass`. To je přesně ta vlastnost, kvůli které byl
R1 blokátor: zelený seznam, který nic neznamená. Navíc rozšířené pokrytí ze skriptu
`test-pages-apps.sh` (MCP, API, seed, collector) žije právě v těch PR bez CI, takže
ta nejsilnější nová kontrola se zatím na většinu kódu nespustila.

Není to chyba rozdělení — stack je správná odpověď na R1. Je to chyba **čtení výsledku**.

**Co udělat:** slučovat zdola nahoru. Nechat #2475 doreviewovat (CodeRabbit byl
rate-limited, což podle `CLAUDE.md` znamená: neschovávat to, doreviewovat ručně,
napsat do PR, co bylo a nebylo strojově zkontrolované, a `scripts/review-status.sh
--retrigger`), pak merge. GitHub přebází #2477 na `main` sám → teprve tehdy naběhne
CI a CodeRabbit → review → merge → a tak dál. Do té doby zelené řádky na #2477–#2481
nepoužívat jako důkaz o ničem. Počítat s tím, že každý přebázovaný PR bude mít
konflikt v `CHANGELOG.md`.

### N2 — Zápis `shallow` neověřuje, že commity existují

`SetCheckpointBoundaries` kontroluje tvar hashe (`ValidProjectCommit`), ale ne, že
objekt v repozitáři je. Zapsaný `shallow` s neexistujícím commitem způsobí, že
**každý** další `git` příkaz nad tím repozitářem selže — tedy i čtení publikované
aplikace a obnova. Vstup dnes pochází z SQL kořenů, takže je konzistentní; jde o
pojistku proti budoucí regresi za velmi malou cenu.

**Oprava:** před přejmenováním `shallow.lock` → `shallow` ověřit
`git cat-file -e <commit>^{commit}` pro každou hranici, případně dávkově přes
`cat-file --batch-check`.

### N3 — Nová podmínka shody verzí nemá timeout

`page-application.tsx:23` otevře aplikaci jen tehdy, když se
`page.publication_version` z detailu Page rovná verzi z `/application`. Když se ty dva
endpointy neshodnou, `opened` zůstane `null` a řádek 28 vykreslí **„Loading
application…" bez konce** — bez chyby, bez fallbacku na panely, bez watchdogu
(20 s hlídač je až v `PagePreviewFrame`, který se v tomto stavu nenamountuje).

V běžném provozu je nesoulad přechodný, protože obojí invaliduje `page.updated`.
Ale je to stav bez východiska, a přitom jde o tutéž třídu problému, kterou R6 řešil.

**Oprava:** po ~10 s v tomto stavu spadnout na panely s hláškou, nebo vůbec
nepodmiňovat otevření rovností a řešit nesoulad až tlačítkem „Load new version".

### N4 — R8 zůstal nesjednocený

`tools/pages-build/build.mjs:26` má teď konkrétní hlášku
(„use ASCII letters, digits, _ . / @ + -; hidden paths are unsupported"), což je
zlepšení. `internal/pages/project.go:validateProjectPath` je ale beze změny, takže
`src/čísla.tsx` i `src/my file.tsx` **stále projdou uložením a padnou až v buildu**.
Pro agentní smyčku to znamená minutu čekání na chybu, které šlo předejít při zápisu.

Chápu argument, že přenosový formát má být širší než profil. Pak ať `save` vrátí
varování a `page_project read` vydá charset jako součást popisu profilu — jinak
agent to pravidlo nemá odkud znát.

### N5 — Podpora prohlížečů: rozhodnutí, které má potvrdit vlastník produktu

`lib/pages/runtime-support.ts` gatuje aplikaci na `Chrome|Chromium` a vylučuje
mobilní UA; jinde se ukáže „Custom Page applications require desktop Chrome or Edge."
Politika je konečně napsaná a implementovaná, což byl otevřený gate — dobře.

Ale má dva důsledky, které nejsou technické:

1. **Desktop Safari nově aplikaci nedostane vůbec**, přestože jsi sám hlásil, že ti
   v Safari fungovala. Je to bezpečnostně obhajitelné (stop-loop tam neprošel), ale je
   to produktové rozhodnutí, ne implementační detail. Potvrď ho, nebo řekni, že chceš
   Safari pustit s varováním.
2. Detekce je přes user agent, ne přes schopnost. Je to jediné, co dnes jde
   (izolace procesů se nedá feature-detectovat), ale je to křehké; `navigator.userAgentData.brands`
   je odolnější tam, kde je k dispozici.

### N6 — Drobnosti

- `Compact` běží v rozpočtu 2 minut na workspace a repozitáře prochází vždy od začátku
  bez kurzoru. U workspace s mnoha Pages se na poslední repozitáře nikdy nedostane.
- Otisk routine pokrývá `pipelines.definition_json`, ne skripty, které routine volá.
  Dialog to říká správně („uses its current definition and scripts"), ale varování má
  tím pádem falešně negativní případy. Publikace vytvořené před touto změnou nemají
  `routine_definitions`, takže u nich flag nikdy nenaskočí a UI nerozliší „nezměněno"
  od „nevím".
- Nové pravidlo „HTTP runtime jen na literal loopback" je správné a zvyšuje laťku pro
  self-hosted instalaci o další položku (důvěryhodné TLS na druhé doméně). Patří to
  do instalačních požadavků jako tvrdá podmínka, ne do poznámky.
- `internal/pages/project_pack.go` píše packfile ručně a nechává ho ověřit přes
  `git index-pack --stdin --strict`. To je správné pořadí důvěry: chybné bajty git
  odmítne, místo aby se zapsaly. Dobré řešení R7.
- Kvótový `filepath.WalkDir` přes celé Git úložiště zůstal na každém `Checkpoint`
  (`project_git.go:202`). Zbytek R7 tedy platí dál.

## Co dělat teď

1. Merge stacku zdola: #2475 (po doreviewování, protože CodeRabbit byl rate-limited),
   pak postupně ostatní, každý až s vlastním CI po přebázování na `main`.
2. N3 a N2 jsou malé opravy s jasným scénářem; stihnou se před mergem příslušné vrstvy.
3. N5 potvrdit jako produktové rozhodnutí.
4. N4 dořešit buď validací při zápisu, nebo vydáním pravidla do profilu.
5. Zbytek beze změny: živé autorství z chatu a čistá instalace zůstávají otevřené gates.
