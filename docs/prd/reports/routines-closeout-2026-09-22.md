# Routines — integrační uzavírání PR (22. září 2026)

## Aktuální stav

- **#2641 MERGED**, merge `ff0c8b95601643e7141a1e3d7ca06d34c764a149`.
  Povinné HTTP credentials se nesmějí tiše změnit na anonymní volání.
  Celé finální CI prošlo, včetně Race; CodeRabbit skutečně schválil přesný
  head `98a651d35`. Sloučení proběhlo běžnou cestou, bez administrátorského bypassu.
- **#2631 je nyní společné integrační PR**: katalog/performance + Work v Activity
  z #2638 + autorování rozhodovacích formulářů z #2640. Zachovává jejich původní
  commity. Po jeho sloučení budou tyto hlavy obsažené v main; zdrojová PR
  není třeba zavírat jako neprovedenou práci nebo jejich změny kopírovat.
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

Veřejný DEV1 dosud nebyl aktualizován společnou integrací. Předchozí ověřená
identita `2836a43d3` a browserové preview důkazy jsou popsány v
[předání z 21. září](routines-takeover-2026-09-21.md).
Po schváleném merge následuje DEV1 deploy, kontrola identity a veřejný browser
průchod Work → Activity a formuláře author → draft → publish → odpověď.

§11 zůstává **NOT VERIFIED**: žádná nová data od pěti reprezentativních lidí
nejsou dodána. [Recorder výsledků](../wireframes/routines-acceptance-recorder.html)
slouží k zaznamenání skutečných průchodů; automatické a interní testy nejsou
jejich náhradou. Historická omezení crash/externího zápisu/full role matrix
zůstávají uvedena v předání, nikoli odškrtnuta dnešním merge.

## Další otevřená PR

- #2633/#2634: opravená příčina červeného drift gate v izolovaných checkoutech.
  #2634 nyní řeší pouze požadované @types/node 26.6.1; #2633 deklaruje přesných
  14 zamýšlených aktualizací a zachovává ostatní přímé závislosti. Samostatná
  evidence a navazující ověření jsou vedené pod #2642.
- #2632/#2635: hlavní CI prošlo, dodatečné zrušené kontroly byly obnoveny.
  Aktualizace SQLite a release workflow nebudou označené za bezvýznamné ani
  sloučené jen proto, že jde o Dependabot.
- #2628/#2619/#2622: CI zelené, GitHub nadále eviduje CHANGES_REQUESTED.
  Je třeba uzavřít nálezy a skutečné review finálního headu.
- #2630: draft pilotu Jev; chybějící živé inference není doplněno cizími
  credentials ani odhadnutými výsledky. Není to otevřený bod Routines PRD.

Původních 17 WIP souborů zůstává byte-identických dle SHA-256. DEV2/DEV3
nebyly měněny. Tato zpráva je stav integrace, nikoli vyhlášení Release 1.0.
