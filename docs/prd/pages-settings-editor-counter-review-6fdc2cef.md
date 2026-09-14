# Oponentura PR #2492 — 6fdc2cef

Ověřeno 2026-09-11. Přesná hlava: `6fdc2cef98de4737a64bbe3145f82b3242773058`.
Izolovaný checkout: `/tmp/crewship-review-2492-6fdc2cef`.

Verdikt: změny před merge. Níže je jeden reprodukovaný nález v publikační
cestě a jedna doložená chyba CI. Nejde o nový audit celého P0.

## R4: validace před withholding kontrolou stále prozrazuje panel a rutinu

`internal/api/pages_project_publish.go:365` volá `checkPageCandidate` ještě
před načtením authorizeru a transakční kontrolou `withheld.Changed` na řádku 482.
`checkPageCandidate` na řádku 236 volá `resolveReferences`, ten na řádku 1625
v `pages_handler.go` volá `resolveActionRoutines`. Chyba v
`internal/api/pages_actions.go:770` zapisuje ID panelu, akce a slug rutiny.

Reprodukováno přímým voláním skutečného HTTP handleru nad testovací SQLite,
Git checkpointem a testovacím builderem ze stávajícího withheldFixture:

1. MEMBER vlastní Page, patří do crew/engine, nepatří do crew/lookout.
2. Administrátor uloží a sestaví kandidáta měnícího skrytý panel sluzby.
3. Rutina ops-secret, kterou tento panel volá, je po buildu soft-deleted.
4. Vlastník publikuje s aktuálním digestem definice, správnou revizí,
   reviewed_code=true a prázdnou mapou viditelných rutin.
5. Server vrátí HTTP 400 s textem `panel "sluzby" action "a1" runs
   routine/ops-secret, and no such routine exists here …`.

Sonda očekávající 403 a neutrální tělo selhala na obou vlastnostech.
Publikace se neprovede; toto není prokázané obejití zákazu zápisu.
Je to porušení zatajení v samotné publikační odpovědi, navíc k již známému
nefiltrovanému draftu/historii #2502. Neoznačuji to za nově zavedený původní
endpointový únik: nová ochrana nepokrývá tuto existující validační větev.

Oprava musí chránit i chyby validace úplného kandidáta, nikoli pouze routine
fence uvnitř transakce. Zachovat úplnou validaci, ale skryté reference nesmějí
proniknout do odpovědi. Doplnit regresi pro chybějící skrytou rutinu a prověřit
stejnou větev pro chybějícího producenta, crew a gate. Tyto další varianty jsem
nespouštěl.

## CI: Shell je červený na této hlavě

Job https://github.com/crewship-ai/crewship/actions/runs/34601694015/job/103270352818
hlásí `144 t.Skip call(s) across 97 test file(s); baseline 143`.
Diff PR přidává `t.Skip` v `cmd/crewship/acceptance_page_projects_test.go:141`.
Nejde o plný disk. Náhrada tichého návratu explicitním skipem je poctivější,
ale musí být dokončena podle pravidel skip-budgetu; samotné zvýšení čísla bez
zdůvodnění a požadovaného waiveru nestačí.

Při poslední kontrole stále běžely Go, Go Race, Frontend Test, další CI joby
 a CodeRabbit. Poslední doložené obsahové CodeRabbit review bylo pro 8d9317f,
nikoli tuto hlavu. Stav CI uložen v `/tmp/pr2492-final-checks.json`.

## Co bylo skutečně ověřeno

- Stávající cílená sada prošla: `go test ./internal/api -run
  'TestPage(Publish.*(Withheld|Attestation|RoutineFence)|Review.*(Withheld|Routine|Definition))'
  -count=1`; balíček 4.316 s, log `/tmp/pr2492-targeted-go.log`.
- `go vet ./internal/api` prošlo, log `/tmp/pr2492-vet.log`.
- Nová adversariální sonda selhala očekávaným způsobem, 2.050 s;
  log `/tmp/pr2492-independent-probe.log`, zdroj
  `/tmp/crewship-review-2492-6fdc2cef/internal/api/pages_counter_6fdc2cef_test.go`.
- Produkční kód beze změn. Nebyl opakován celý Go suite, frontend, browser,
  mutační test opravy, screenshoty ani uživatelské měření. Žádné zápisy do dev3,
  komentáře na GitHub, merge či nasazení.

## Doporučení pro #2502

Podle existujícího kontraktu v `pages_authz.go` write opravňuje k uspořádání,
nikoli ke čtení skrytého obsahu; grant nemá rozšiřovat viditelnost panelů.
Doporučuji tento kontrakt zachovat, ne zpětně prohlásit únik za oprávnění.
Z variant v issue je nejsnáze auditovatelná oddělená úplná autorská cesta
vyžadující viditelnost všech panelů a filtrovaná čtecí cesta. Neprovádět prostý
filtr draftu bez bezpečného round-trip řešení. Jde o doporučení, nikoli schválenou
změnu produktu nebo implementované řešení.

Screenshoty nejsou důvod tohoto zamítnutí. Podstatné jsou reprodukovaný únik
v odpovědi a červené CI na posuzované hlavě.
