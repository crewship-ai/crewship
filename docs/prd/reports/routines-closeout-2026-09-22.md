# Routines — integrační uzavírání PR (22. září 2026)

## Aktuální stav

- **#2641 MERGED**, merge `ff0c8b95601643e7141a1e3d7ca06d34c764a149`.
  Povinné HTTP credentials se nesmějí tiše změnit na anonymní volání.
  Celé finální CI prošlo, včetně Race; CodeRabbit skutečně schválil přesný
  head `98a651d35`. Sloučení proběhlo běžnou cestou, bez administrátorského bypassu.
- **#2631 je nyní společné integrační PR**: katalog/performance + Work v Activity
  z #2638 + autorování rozhodovacích formulářů z #2640 + pravdivé výsledky
  odpojených agentích běhů z #2628. Zachovává jejich původní
  commity v integrační větvi. Aktivní pravidlo main vyžaduje lineární historii,
  proto při squash merge uzavřeme zdrojová PR s odkazem na skutečně začleněnou
  změnu; nebudeme tvrdit, že jejich původní hlavy jsou předky squash commitu.
- **#2635 MERGED**, squash `6ceddb52dbaab2048075a7638f2a03531f0f49f6`.
  Aktualizace připnutých CI akcí má věcné manuální review přesného headu
  `d18c20aeb` a celé zelené CI, včetně image buildu a release packaging rehearsal.
  Změny upstream zdrojů byly prohlédnuté; nejde o CodeRabbit review.
- Při spojení produkční strom integrace odpovídal ověřenému `d4dfae3b7`; rozdíl vůči
  původní integrační větvi při spojení tvořily pouze dva dokumenty z #2631.
  Nová hlava má přesto vlastní CI a musí mít skutečné review před merge.
- Následně byl nalezen a opraven #2644: současný Edit při reloadu bez
  varování ztratil neuložený text. Browserový negativní důkaz i tři negativní
  komponentové regrese jsou zaznamenané; dialog nyní registruje existující
  ochranu navigace pouze při `open && dirty`. Zahození jasně zachovává
  serverový draft. Nové cílené kontroly mají 392 úspěšných testů; tato změna
  není pokrytá pouhou shodou se včerejším integračním stromem. Nový produkční
  build, typová kontrola a lint (0 chyb) prošly. Browser potvrdil reload bez
  varování pro čistý draft, zachování textu při odmítnutém reloadu, zachování
  serverového draftu a návrat fokusu při zahození, odmítnuté Back/Forward i
  následné povolené Forward na původní cíl. Vlastní rutina smazána 204; žádné
  JS chyby. Finální CI a review musí pokrýt novou hlavu.
- Původní finální CI #2631 selhalo při získávání BuildKitu: spojení na
  `auth.docker.io` bylo resetované před buildem aplikace. Neúspěšné joby byly
  zopakovány; nejde o doloženou chybu produktových testů. Další integrační
  push vytváří novou CI evidenci, stará zelená kontrola ji nenahrazuje.

## Nasazení a přejímka

Dodatečně prošlo 60 živých API kontrol v novém vyhrazeném workspace na
dosavadním DEV1 `2836a43d3`: pět rolí, čtení/autorství/publikace/spuštění,
vytváření a správa plánů, explicitní `routine.run`, okamžité odvolání a
odmítnutí vlastního povýšení. Testovací workspace, rutiny a plány jsou uklizené.
Nejde o úplnou matici všech endpointů nebo agentích tokenů. První verze
pomocného skriptu nesprávně očekávala čtení draftu pro MEMBER; opraveno na
správné serverové pravidlo MANAGER+. Tento harness omyl není produktový nález.

Navazující kontrola UI ale našla #2645: Run/Run again a kalendář používaly
jiná pravidla než API a Plan nabízel změny všem rolím. Oprava sdílí explicitní
mapování skutečných oprávnění: run včetně odloženého startu, samostatné
vytvoření opakovaného plánu, MANAGER+ pro autorování/zrušení odkladu a ADMIN+
pro správu existujících plánů. Copy nadále používá administrátorský
`skip_test_gate`, proto se také řídí ADMIN+. Pět nových případů Plan před
opravou selhalo; po opravě prošlo 400 komponentových/pomocných testů v 55
souborech, včetně výslovně delegovaného VIEWER. Toto jsou nové změny, které
musí zahrnout finální CI, review a následující browserové ověření.

Veřejný DEV1 byl 22. září aktualizován **nesloučeným kandidátem `6a236506f`**
pro uživatelem zadané testování společné větve. Build 12:37:09 UTC; autentizované
API, frontendový marker, tři veřejně stažené JS assety a SHA-256 běžící binárky
souhlasí. `dirty=true` je ponecháno; 17 původních WIP souborů je byte-identických.
Synchronizace s main po #2635 měnila pouze CI workflow. Následné začlenění
#2628 už mění backend: tato oprava dosud na DEV1 nasazena není. Předchozí identitu `2836a43d3` neoznačujeme za aktuální.

Veřejné browserové sady prošly: Work → Activity (včetně 390 px, klávesnice,
reloadu a historie), R8 author → draft → publish → číselná odpověď → dokončení,
ochrana draftu a navigace a pět kombinací rolí/oprávnění. R8 běh
`run_cmucnubme00095522ada1` uložil `action_id=continue`, `amount=42.5`.
Také všech 60 API kontrol bylo zopakováno na novém nasazení. Bez JS chyb;
vlastní rutiny/plány smazané 204 a workspace 200, auditní historie zachována.
První navigační harness nezafixoval workspace při souběžném vytváření jiného;
po explicitním výběru vlastního workspace prošel celý test. Tento opravný
průchod je rozlišený od negativního produktového důkazu #2644.

[Veřejný protokol](https://github.com/crewship-ai/crewship/pull/2631#issuecomment-5776697448)
obsahuje identitu a přesné vymezení. **#2631 stále potřebuje finální CI a
nezávislé schválení.** CodeRabbit odmítl re-review znovu v 12:34 UTC a posunul
okno přibližně na 13:34. Vlastní review nenahrazuje pravidlo GitHubu vyžadující
schválení jiným účtem po posledním pushi. Kandidátní deploy není merge.

§11 zůstává **NOT VERIFIED**: žádná nová data od pěti reprezentativních lidí
nejsou dodána. [Recorder výsledků](../wireframes/routines-acceptance-recorder.html)
slouží k zaznamenání skutečných průchodů; automatické a interní testy nejsou
jejich náhradou. Tvrdý restart, nejistý externí zápis a oprávnění mimo vybrané
veřejné endpointy nejsou tímto čerstvě kompletně ověřené.

## Další otevřená PR

- #2633/#2634: opravená příčina červeného drift gate v izolovaných checkoutech.
  #2634 nyní řeší pouze požadované @types/node 26.6.1; #2633 deklaruje přesných
  14 zamýšlených aktualizací a zachovává ostatní přímé závislosti. Obě finální
  CI prošla; zbývá aktuální základ a nezávislé schválení. Evidence pod #2642.
- #2632: celé CI a věcné manuální review původního headu prošly; čeká aktualizace
  základu a finální kontrola. SQLite 1.59 ponechává stejný SQLite engine a
  správně páruje libc. Upstream výkonové údaje nejsou náš benchmark.
- #2628: převzatá oprava dalšího závodu při současném ukončení dvou odpojených
  běhů. Negativní regrese selhala na předchozím kódu; všech 146 Go balíků a vet
  na `7b2514396` prošlo, cílené Race testy desetkrát (30 000 souběžných párů).
  Finální CI samostatného PR na tomto headu prošlo. Změna je nyní začleněna
  do #2631; společný strom vyžaduje vlastní CI a nezávislé schválení. Po squash
  merge bude zdrojové PR uzavřeno jako začleněné. Ochrana odpojených běhů je
  procesová, ne trvalá přes restart serveru.
- #2646: nově otevřený draft jiného vlastníka navazující na #2628; řeší
  sdílené přijímání běhů. Není součástí této integrace a není označen za hotový.
- #2619/#2622: oddělené providerové změny. #2622 drží aktivně crewship_3;
  do jeho větve tato relace nezasahuje. Otevřené review nálezy zůstávají vlastníku.
- #2630: draft pilotu Jev; chybějící živé inference není doplněno cizími
  credentials ani odhadnutými výsledky. Není to otevřený bod Routines PRD.

Původních 17 WIP souborů zůstává byte-identických dle SHA-256. DEV2/DEV3
nebyly měněny. Tato zpráva je stav integrace, nikoli vyhlášení Release 1.0.
