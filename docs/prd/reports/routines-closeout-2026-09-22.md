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
- Produkční strom integrace odpovídá již ověřenému `d4dfae3b7`; rozdíl vůči
  původní integrační větvi při spojení tvořily pouze dva dokumenty z #2631.
  Nová hlava má přesto vlastní CI a musí mít skutečné review před merge.
- Původní finální CI #2631 selhalo při získávání BuildKitu: spojení na
  `auth.docker.io` bylo resetované před buildem aplikace. Neúspěšné joby byly
  zopakovány; nejde o doloženou chybu produktových testů. Další integrační
  push vytváří novou CI evidenci, stará zelená kontrola ji nenahrazuje.

## Nasazení a přejímka

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
