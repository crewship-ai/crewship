# Pages Apps: vlastní kontrola a oponentura oponentury

Datum: 2026-09-09. Autor: Codex. Zadání: nezávisle analyzovat oponenturu,
ověřit tvrzení proti implementaci a připravit podklad pro další práci na Pages.

Posuzovaný kód je v `.claude/worktrees/pages-apps-project`, větev
`feat/pages-apps-project`, HEAD `676e16e45897bf6767f47156a894fb01bfb56162`
s necommitnutými změnami. Hlavní adresář dev3 je jiná větev
(`feat/credentials-complete-ux`); kontroloval jsem skutečný Pages worktree.
Níže uvedené cesty a řádky kódu patří tomuto worktree, ne hlavnímu adresáři.
Odkazovaný Claude artifact nebyl přes web dostupný; podkladem je jeho místní
plné znění `pages-apps-independent-review-2026-09-09.md` a zdrojové soubory.

**Verdikt:** směr implementace dává smysl a další řízené ověřování na dev3
je odůvodněné. V1 není uzavřená a zákaznickou distribuci zatím nelze schválit.
Oponentura odhaluje skutečné nedostatky, ale obsahuje jednu vyvrácenou věcnou
chybu, několik příliš kategorických tvrzení a nebezpečně zjednodušený návrh
opravy údržby. Navíc jsem reprodukoval další blokaci buildů, kterou nepopisuje.

Práce v této relaci je review. Produkční implementaci, živou databázi ani
nasazení jsem neměnil. Vlastní reprodukce používají dočasné Go overlays a
izolované testovací databáze/úložiště. Nevznikl commit, PR ani veřejný komentář.

## 1. Nejdůležitější nový nález: build kvóta a retence si odporují

**C1 — vysoká závažnost pro více Pages v jednom workspace; reprodukováno.**

`internal/api/pages_build.go:100` počítá všechny buildy workspace a od 512
vrací HTTP 507. `internal/api/pages_project_retention.go:27` však zachovává
posledních 64 dokončených buildů **na každou Page**, včetně neúspěšných.

Vlastní test vytvořil osm Pages, každou s 64 neúspěšnými buildy. Potom zavolal
skutečný `RetainPageProjects` a skutečný `BuildProject`. Výsledek:

```text
after retention: 512 failed builds remain
new build HTTP 507: {"error":"Page build history quota reached"}
```

Žádný build nepotřeboval artefakt ani publikaci. Údržba neodstraní nic a nový
build už nelze založit. Problém zasáhne i další Page v témže workspace.
Pouhé čekání na další hodinovou retenci nepomůže. Jde o konkrétnější a dříve
dosažitelný stav než dlouhodobé zaplnění Git historie.

Oprava má sjednotit úroveň kvóty a retence: workspace potřebuje rozpočet pro
uchovávané dokončené buildy, který rezervuje prostor pro další build a respektuje
publikované/running kořeny. Nestačí zvýšit 512. Je třeba prověřit obdobný vztah
u zdrojů: `internal/pages/project_store.go:104` dovoluje 256 snapshotů na
workspace, SQL retence ponechává 64 revizí na Page. Tento druhý kapacitní
scénář jsem samostatně nereprodukoval.

## 2. Kontrola R1–R10

| Nález | Vlastní závěr | Co mění rozhodnutí |
|---|---|---|
| R1: rozsah a review | Potvrzuji potřebu rozdělení, opravuji inventuru i vedlejší nález. | Aktuálně 201 souborů; refresh routa v základu nechybí. |
| R2: CI | Potvrzuji chybějící skutečné Docker/browser průchody, odmítám formulaci „žádná regresní ochrana“. | Běžné bezpečnostní unit a API testy existují. |
| R3: údržba blokuje čtení | Potvrzuji mechanismus z kódu. | Dlouhý produkční repack ani hodinové 503 jsem nenaměřil; návrh opravy potřebuje ochranu souběžných zápisů. |
| R4: Git kvóta | Potvrzuji neomezené předky a chybějící podporovanou cestu odříznutí historie. | Kvóta je workspace; repack umí uvolnit bajty i bez odstranění předků. |
| R5: retry po rollbacku | Reprodukováno: HTTP 200, receipt v2, live v3. | Zachování receiptu je záměrná idempotence; nejednoznačný je kontrakt odpovědi a UI. |
| R6: loading panelových Pages | Potvrzuji z komponenty a jejích testů. | Dodatečný round trip je skutečný; řešení musí zachovat reakci na publish/unpublish a revokaci. |
| R7: cena checkpointu | Potvrzuji proces na soubor a workspace walk. | Nemám benchmark maximálního projektu; procesy nejsou samy o sobě důkaz nesplněného latency cíle. |
| R8: validace cest | Potvrzuji nesoulad Go/worker. | Chyba vzniká před kompilací, nikoli nutně „po minutách“. |
| R9: lease po buildu | Potvrzuji z kódu: přepis chyby a zahození artefaktu. | Samotný delší timeout není úplná oprava. |
| R10: hostname v klientu | Potvrzuji rozdíl proti serverovému site checku. | Nenalezl jsem tím obejití serverové validace. |

### R1: review ano, tvrzení o main ne

Inventura `git diff HEAD --numstat` + `git ls-files --others --exclude-standard`
dává 62 změněných tracked souborů (+6 109/−819 řádků) a 139 untracked souborů
(16 290 řádků), celkem **201 souborů**. Je to okamžitý stav, nikoli záruka
budoucího počtu souborů PR. `git status --short` bez `-uall` seskupuje nové
adresáře, takže jeho počet řádků není počet souborů. CHANGELOG změna chybí.

Historie v `.github/workflows/changelog-guard.yml:17` skutečně zmiňuje
100souborový limit CodeRabbitu. To je důkaz známého incidentu v tomto repo,
ne ověření aktuální konfigurace externí služby nebo budoucího výsledku PR.
Podlimitní PR samo nezaručí review: zůstává rate limiting i nutnost číst těla
review. Rozdělení má sledovat reviewovatelné, testovatelné závislosti;
navržených devět PR není ověřený jediný správný rozpad.

Vedlejší tvrzení o chybějící routě je **nesprávné**:

```text
git show 676e16e4:internal/api/testdata/route-roles.txt
191: POST /api/v1/credentials/{credentialId}/refresh roleCreate

git show 676e16e4:internal/api/router_crews.go
461: registrace stejné POST routy
```

Diff má jak odstraněný, tak přidaný stejný řádek: jde o přesun v řazení.
`TestMutationRouteRolesMatchManifest` na Pages worktree prošel. Čistý aktuální
remote main jsem netestoval a místní `main` není totéž co základ Pages větve;
z tohoto diffu každopádně nelze odvozovat chybějící routu ani zakládat opravný PR.

### R2: chybí integrace v CI, část ochrany už funguje

Ve workflows/actions jsem nenašel zapojení Pages smoke skriptů ani nastavení
`CREWSHIP_TEST_PAGE_BUILD_IMAGE`, `PAGES_TEST_BUILD_IMAGE` či runtime harnessu.
Ale `TestDockerBuildPolicy` kontroluje bezpečnostní parametry kontejneru,
`TestRuntimeRequiresSeparateSite` site policy a
`TestRuntimeHeadersAndConstantBootstrap` CSP/bootstrap. API testy kontrolují
publikační fence, cizí receipt, izolaci workspace a revokaci podmíněného čtení.
Tyto kontroly nejsou vázané na Docker env a byly součástí vlastních zelených běhů.

Oponentura navíc zaměňuje harness za hotový browser test:
`TestServeRuntimeBrowserHarness` pouze otevře HTTP server a čeká na ukončení.
Nestačí nastavit proměnnou a „spustit čtyři testy“; musí běžet browser driver,
který harness ovládá a vyhodnotí izolaci i loop. Seed vynechává pouze podtest
`publish`, nikoli celý lifecycle test. Další volitelný skutečný Docker test je
`internal/sidecar/pages_project_integration_test.go:19`, který seznam opomíjí.

CI potřebuje vybudovat/připnout image, provést compiler negativní případy a
spustit browser driver s kontrolou, že testy opravdu proběhly. Filtry musí
zahrnovat i `components/features/pages`, `hooks`, API, runtime server, sidecar,
SDK a samotnou CI konfiguraci; tři adresáře z návrhu nepokrývají celý kontrakt.
Při napojení do required lane je třeba vyřešit i výsledek pro nerelevantní PR.

### R3/R9: správný problém, opravu nelze zredukovat na přesun GC

`RetainPageProjects` drží exclusive lease od SQL kořenů až po oba file prunes.
`internal/pages/project_retention.go:117` pod ním provádí repack a na řádku
120 `prune --expire=now`; čtenářský lease má 30s strop
(`internal/pages/project_lease.go:32`). UI při query error přestane používat
otevřenou aplikaci (`components/features/pages/page-application.tsx:19`).
To dokládá cestu k 503, nikoli tvrzení, že se děje každou hodinu každému.

**Nepřesouvat současný prune bez další synchronizace mimo zámek.**
`Checkpoint` zapisuje objekty před vytvořením jejich pinu
(`internal/pages/project_git.go:215`). Souběžný `prune --expire=now` by mohl
smazat právě vytvořený, dosud nedosažitelný objekt. Je nutné rozlišit čistě
čtecí obsluhu immutable artefaktu, zápis Git objektů a destruktivní GC/restore,
zachovat jejich koordinaci a přidat souběžný regresní scénář.
Riziko okamžitého pruningu při souběžném zápisu popisuje také
[oficiální dokumentace Gitu](https://git-scm.com/docs/git-gc#Documentation/git-gc.txt---pruneltdategt).

Při dočasné storage 503 může UI zachovat poslední ověřený artefakt, ale nesmí
tím ignorovat 401/403/404, odebrání práv nebo stažení publikace.

`runPageBuild` po kompilaci opravdu používá samostatný 10s lease timeout a
přepisuje `err` hodnotou `leaseErr` (`internal/api/pages_build.go:127`). Navíc
celý build/persist kontext má 150s deadline, zatímco `Lease` interně maximálně
30 s. Pouhé zvýšení jednoho timeoutu nedává hotovému artefaktu spolehlivou
cestu do uložiště; je potřeba samostatný persistence rozpočet a jasný stav
infrastrukturního přerušení při zachování původní diagnostiky.

### R4/R7: kapacita vyžaduje politiku, nikoli absolutní tvrzení

Vlastní fixture přes `ProjectStore.Checkpoint` vytvořila 70 navazujících revizí
a přes skutečný `Prune` ponechala jen pin poslední revize. První checkpoint
zůstal čitelný: problém předků je potvrzen. Velikost repozitáře ale klesla
**111 085 → 50 931 B**. Repack komprimuje/deltuje dosažitelné objekty.
Neplatí tedy, že žádná retence neuvolní místo nebo že každé první odmítnutí
kvóty už musí být trvalé.

Platí užší, důležité tvrzení: není zaručen dlouhodobě omezený objem zachované
historie ani podporovaný způsob jejího odříznutí při vyčerpání komprimovatelné
rezervy. Navíc 128 MiB patří celému workspace a kontrola rezervuje i prostor
nad rámec skutečného přírůstku. Dopad není omezen na jednu Page. Varování při
80 % je užitečné, ale nepovažuji je za alternativu k cestě obnovy zápisů.
Squash musí respektovat Git hashe připnuté publikacemi a obnovou; nelze bez
migrace vazeb jen přepsat historii, kterou systém označuje za immutable.

R7 má další souvislost: procesový `s.mu` je držený přes celý checkpoint
(`project_git.go:162`), takže náklad serializuje zápisy obsluhované stejným
store i mezi workspaces. Optimalizaci ale předřadit až po měření počtu procesů,
p50/p95 save a velikosti skutečných projektů; navržené `--stdin-paths` není
přímá náhrada pro současné předávání souborů z paměti bez checkoutu.

### R5/R6/R8/R10: kontrakty a UX

R5 jsem ověřil doplněním existujícího integračního testu přes Go overlay.
Po publish v1, publish v2 a rollbacku vznikla live v3. Přesný retry publish v2
vrátil HTTP 200 a receipt v2, live zůstala v3. To potvrzuje popsaný stav.
Komentář handleru i test po unpublish výslovně vyžadují, aby retry vracel
receipt a neobnovil stažený kód (`pages_project_publish_test.go:160`).

Není to selhání CAS ani opětovná aktivace starého kódu. Je to nejasné rozlišení
„operace proběhla“ versus „tato verze je nyní živá“, zesílené textem
`Published version …` v `page-preview.tsx:101`. Doporučuji zachovat receipt
a doplnit aktuální live/published stav v odpovědi a UI. Alternativa s 409
je legitimní změna kontraktu, vyžaduje však změnit existující retry testy
a klientská očekávání. Samotný JOIN také nezaručí neměnnost live po odpovědi.

R6: `PageApplicationView` volá application query pro každou Page a blokuje
fallback při `isPending`. Rozšíření detailu/listu Page o stav aplikace pomůže,
ale rozhodnutí musí respektovat invalidaci při publikaci a stažení aplikace.
`publication_version > 0` samo nestačí: po unpublish zůstává čítač zachován.
Použít explicitní aktivní stav, ne zaměnit „někdy publikováno“ za „má aplikaci“.

R8: Go připustí `src/čísla.tsx`, `src/my file.tsx` či `src/.keep`, worker ne
(`internal/pages/project.go:101`, `tools/pages-build/build.mjs:21`). Chybová
hláška workeru nenese cestu. Fix patří do validace konkrétního build profilu
a do jeho kontraktu pro agenta. Není nutné bez rozmyslu zužovat obecný přenosný
formát na ASCII. Závislosti už strojově čitelné jsou přes starter package.json
a lockfile; chybí především úplný souhrn omezení profilu při prvním čtení.

R10: klient kontroluje hostname, server registrable site. Nemám důvod přidávat
do frontend bundle druhou public-suffix implementaci jen kvůli paritě této
předběžné kontroly. Server musí zůstat autoritou; klient má přesně popisovat,
co ověřuje, a srozumitelně zobrazit serverovou chybu konfigurace.

## 3. Oponentura architektonických a produktových závěrů

Souhlasím s úzkým SDK, autorizační transakcí, immutable publikací UI,
omezeným build profilem a absencí dalšího trvalého aplikačního serveru.
Nenašel jsem důvod zavádět další renderer ani libovolné npm instalace.

Nesouhlasím s argumentem „reviewovaný autor nepíše smyčku často, uživatel zavře
záložku“. Vadný generovaný React, effect nebo render může vzniknout i u
důvěryhodného autora. Uvíznutí celého Studia je reálná dostupnostní ztráta.
Oddělený site a ochrana před exfiltrací jsou odlišné vlastnosti; neúplnost druhé
nečiní první zbytečnou. V této relaci prošel loop test Chromia na odděleném
site. Z toho neodvozuji stejnou vlastnost dev3 se same-origin výjimkou.
Rozšíření výjimky na podporovaný self-hosted režim je nové produktové a
bezpečnostní rozhodnutí, nikoli automatická oprava instalačního dluhu.

Druhý iframe `load` může být signál neočekávané navigace. Sám o sobě nedokazuje
exfiltraci a přichází až po zahájení požadavku. Auditovat jej jako podezřelou
navigaci lze; označit to za detekci úniku dat by byla příliš silná záruka.

Nepřipnutá routine je podstatný kontrakt schválení. Hash v publikaci a UI
varování mohou zlepšit dohledatelnost, ale pokud chceme garantovat spuštění
schválené definice, musí kontrola/verze dosáhnout až do runneru a zahrnout
relevantní skripty. Porovnání pouze při enqueue neřeší změnu během čekání ve frontě.

Živé autorství z chatu zůstává důležitou otevřenou akceptací. MCP integrační
test je užitečný dílčí důkaz, nikoli měření toho, že živý agent správně použije
nástroje, pochopí chyby a vytvoří požadovaný výsledek. Požadavek na průchod
agent → editace → review → publish → akce → data → reader → revokace podporuji.

Výrok „hodnota pro zákazníka nulová, dokud není main“ směšuje distribuovatelnost
a hodnotu prototypu. Dnešní práce má ověřenou technickou a demonstrační hodnotu;
zákaznický přínos a náklady samostatného provozu zatím nemáme změřené.
Podobně tvrzení o převaze nad konkurencí není podloženo tímto review kódu.
Kritika palety jako minulého rozhodnutí je hodnotový soud: byla výslovně
vyžádaná. Pro další relaci ale souhlasím se zastavením kosmetického rozšiřování.

## 4. Vlastní ověření

Výsledky a reprodukční podklady doplňuje soubor
`reports/pages-apps-counter-review-2026-09-09/evidence.json`.

| Kontrola | Výsledek vlastní relace |
|---|---|
| `go test ./internal/pages ./internal/pagebuild ./examples/pages-apps -count=1` | PASS, všechny tři balíčky |
| Cílené API testy projektů, buildů, aplikací, retence a route manifestu | PASS; zahrnuje autorizační fence a vlastní receipt |
| Pages Vitest, stejný výběr jako oponentura | PASS, 44 souborů / 592 testů; stderr obsahuje varování React/test fixture |
| `go vet ./...` | PASS |
| `go test ./... -count=1` | **FAIL / exit 1**: 137 balíčků PASS, API a database ukončené výchozím 10min timeoutem; 11 balíčků bez testů |
| Skutečný Docker compiler, typecheck negativní případ, odlišný lockfile | PASS |
| Skutečný API Docker round trip a seed lifecycle včetně publish | PASS |
| Skutečný MCP → sidecar → API → Git → Docker průchod | PASS, 5,974 s; nejde o živého LLM agenta |
| Chromium + skutečný Go bootstrap + nově sestavený Docker artefakt | PASS: React/SDK, parent DOM, storage, fetch, odstranění infinite-loop frame |
| Vlastní stale publish retry reprodukce | PASS reprodukce: 200 / receipt v2 / live v3 |
| Vlastní Git ancestry/packing reprodukce | PASS reprodukce: předek přežil, repozitář se zmenšil |
| Vlastní workspace build quota reprodukce | PASS reprodukce: po retenci stále 512, nový build 507 |
| Samostatné `go test ./internal/database -run '^TestMigratePages' -count=1 -v` po timeoutu celé sady | PASS, 10,601 s; neznamená průchod všech migrací v repo |
| Služba dev3 a HTTP `/pages/custom-operations` | active / 200; toto není důkaz přihlášeného průchodu |

Použitý lokální tools image má ID
`sha256:cca058230111eea5d07fa69c7537647ee46ea1b5b841233e3ff632e65b0dafb2`.
Image jsem v této relaci z Dockerfile znovu nestavěl; testoval jsem skutečné
kompilace v již dostupném image shodném s uvedeným handoffem.

Celá Go sada nemá v této relaci zelený výsledek. API skončilo po 600,064 s,
database po 600,069 s. Log neobsahuje řádky `--- FAIL` s neúspěšnou assertion,
obsahuje dva package timeout panics. API v okamžiku ukončení běželo v
`TestPipelineSchedules_Create_CatchupPolicy` (hlášená doba 0 s); databáze
měla více paralelních migračních testů včetně Pages (jednotky sekund).
To není důkaz, že jmenované testy samy visí deset minut. Část běhu se překrývala
s dalšími kontrolami této relace; příčinu délky nelze bez dalšího měření
připsat Pages. Delší kompletní opakování jsem v rámci této oponentury nedělal.
Před merge je potřeba úplný běh s odpovídajícím časovým rozpočtem a kontrolou
výsledků; cizí předchozí PASS tímto nepotvrzuji ani nevyvracím.
Nespouštěl jsem kompletní frontend, nový static export ani lint: tato relace
nemění produktový kód. Živého chat agenta, čistou zákaznickou instalaci,
Firefox/WebKit, produkční zátěž, penetrační test a obnovu zálohy jsem samostatně
neopakoval. Zelený test znamená průchod jeho assertions, nikoli certifikaci
celého bezpečnostního modelu.

## 5. Doporučené pokračování

1. Zachovat a připravit práci k review: ověřit claim, vytvořit verzovaný bod,
   navrhnout závislosti menších PR a changelog. Nepřepisovat naslepo cizí WIP.
2. Opravit C1 a provázaný návrh R3/R9 s regresními scénáři pro kapacitu,
   souběžný save/GC a dokončení buildu během údržby. Ujasnit R5 kontrakt.
3. Zprovoznit skutečné Docker/MCP/browser CI průchody nad příslušnými změnami;
   vyhodnocovat skutečně provedené testy i skutečně dodané review.
4. Provést živý agentní akceptační scénář včetně nového uživatele, aktualizace
   dat akcí a revokace. Zapsat čas, ruční zásahy a důkazy výsledku.
5. Před podporovaným pilotem uzavřít browser/runtime režim a chování při
   nepodporované konfiguraci; R6/R8 vyřešit v autorském a čtenářském toku.
6. Před zákaznickou distribucí ověřit čistou instalaci, zálohu/obnovu,
   dlouhodobé kapacitní zotavení a životní cyklus build profilu/routine.

Merge je potřebný výsledek release práce, ale sám neuzavírá akceptaci produktu.
Další investici bych směřoval do prokazatelného autorství a spolehlivého
životního cyklu; nová paleta ani další runtime nám nynější nejistoty nevyřeší.
