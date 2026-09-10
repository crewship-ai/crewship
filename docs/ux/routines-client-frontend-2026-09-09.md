# Routines — klientský frontend, 9. září 2026

Navazuje na `docs/prd/ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md`.
Jde o implementovanou etapu UI nad existujícím backendem; není to potvrzení
uzavření celé akceptace PRD.

## Co je doplněno

- Testovací pracovní oblast rozděluje kontrolu definice, fixture test jednoho
  kroku a živé porovnání publikovaných verzí. Používá stávající Radix tabs,
  barvy, ikony a komponenty. Neaktivní panely jsou skryté i pro asistivní
  technologie, ale zachovávají rozepsaná data. Běžící živé porovnání zůstává
  označené i při prohlížení jiné testovací metody. Odchod z celé Test oblasti
  zachovává dosavadní zastavení další fronty, nikoli zrušení přijatých běhů.
- Nový draft vysvětluje nutnost prvního publikování před živým porovnáním;
  nenačítá neexistující archiv jen kvůli otevření záložky.
- Fixture podklady se editují po výsledcích jednotlivých předchozích kroků.
  Chybějící hodnota a explicitně prázdný výstup jsou rozdílné stavy. Add sample
  přesune focus do nové hodnoty, Remove sample vrátí stav na chybějící.
  Úplný JSON zůstává v Advanced; neplatný JSON se nezahazuje ani nepřepisuje.
- Schvalovací krok má vizuální editor reviewer otázek, tlačítek a jejich
  pokračovat/zastavit chování. Sdílí formulářový builder i skutečný komponent
  HumanDecisionForm s Inboxem a detailem běhu. Náhled je lokální, neposílá
  verdikt. Stabilní action IDs se úpravou popisku nemění. Neznámé konfigurace
  se zachovávají; nesrozumitelný formát vede na Code. Úprava je stále draft,
  nikoli změna už čekajícího rozhodnutí.
- Publish ukazuje přidané, změněné, odebrané i přeuspořádané kroky/otázky/
  výsledky podle stabilních identit. Rozlišuje 0, false, null a nepředané
  pole; nehodnotí změnu pořadí JSON klíčů jako změnu chování. Ostatní změněné
  nastavení shrnuje jménem bez vypisování hodnot. Celý technický diff je
  rozbalitelný. Nedostupná výchozí verze není prázdný recept. Duplicitní ID
  či neznámý formát nemají falešné úplné srovnání.

Sidebar, kalendář a barevný/ikonový systém nejsou nahrazené dalším stylem.
Backendové limity fixtures, recovery a evaluací zůstávají pravdivé.

## Ověření

- 77 frontendových sad / 692 testů prošlo, včetně startu, draftů, historie,
  fixture importu, porovnání a společných human decisions.
  Log `/tmp/dev1-routines-professional-final-ui.log`.
- Lint: 0 errors / 32 existujících warnings. Production build prošel.
- Playwright nad skutečnými komponentami a repo CSS v izolovaném lokálním
  preview (`127.0.0.1:4911`), se vzorovým receptem, bez produkčních API akcí:
  zachování sample hodnoty po změně testovací metody; lokální human preview;
  žádné page errors; šířka390px bez horizontálního overflow u Test, Publish
  a Decision; reduced-motion režim; klávesové šipky mezi tabs; právě jeden
  přístupný tabpanel. Toto není přihlášený průchod celé aplikace.
- Screenshoty: `/tmp/dev1-routines-test-desktop.png`,
  `/tmp/dev1-routines-publish-desktop.png`,
  `/tmp/dev1-routines-decision-desktop.png`,
  `/tmp/dev1-routines-test-mobile.png`,
  `/tmp/dev1-routines-decision-mobile.png`,
  `/tmp/dev1-routines-comparison-mobile.png`.
- Celý go vet prošel. První Go příkaz skončil143; opakovaná celá sada
  dokončila všechny balíky a našla jedinou chybu prostředí: archive bomb test
  v internal/devcontainer narazil na plný TMPDIR v /run/user/1000.
  Celý devcontainer balík opakován s TMPDIR=/tmp: exit0,25,982s. Ostatní
  balíky celé sady prošly. Logy professional-verified-go a
  professional-devcontainer v /tmp. Žádný commit/PR/merge v této etapě.
- Adversariální mobilní data: dlouhý název receptu původně roztáhl390px
  stránku na4174px; po opravě390px. Dlouhá reviewer otázka v náhledu původně
 3532px; sdílený HumanDecisionForm nyní drží390px i s dlouhými popisky akcí.
  Oprava platí také pro stejný formulář v Inboxu/detailu běhu.

## Co tento výsledek neuzavírá

Uživatel následně viděl nasazené UI a výslovně označil Edit/Test za nesrozumitelný:
neví, co má vyklikat ani co funkce znamenají. Toto je nevyřešená UX připomínka,
nikoli akceptace. Samostatný nový agent má přepracovat orientaci klienta při
zachování stávající grafiky. Aktuální souhrn pro revizi je v
[závěrečném předání](../prd/ENDING-2026-09-09-ROUTINES-REVIEW.md).

Přihlášený end-to-end průchod, uživatelský UX protokol a měření velkých dat
jsou stále otevřené. File/credential reference vyžadují backendový kontrakt;
obecný recovery a sdílené datasety nejsou touto UI etapou implementované.
Pokročilé vazby a větvení stále používají Code. Neprohlašovat celou aplikaci
ani celé PRD za dokončené pouze na základě těchto komponentových testů.
