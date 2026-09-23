# Iterace 8: vlastní přístup k rutině a credentialu

Větev `feat/access-me-routines-credentials`, issue #2667. Implementace je
rebasovaná na `origin/main` `42b4b9ac8` (2026-09-23).

## Kontrakt

- `GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/access/me` vrací
  `read`, `run`, `edit`, `approve`, `disable`, `replay` a `manage_schedules`.
  `run` sdílí s `/run` rozhodnutí role nebo `routine.run`, včetně kontroly
  membership při capability lookup. Jen aktivní rutina má podmíněné `run`.
- `GET /api/v1/credentials/{credentialId}/access/me` používá stejný SQL
  visibility filtr jako detail credentialu; skrytý i neexistující záznam má
  404. Vrací `read`, `edit`, `rotate`, `manage_bindings`, `reveal`, `delete`
  a `lower_sensitivity`. `rotate` sdílí role/capability rozhodnutí s akcí.
  Odpověď čte pouze metadata, nikdy hodnotu tajemství.
- Stav akce je `allowed`, `conditional` nebo `denied` s kódovaným důvodem.
  `conditional` znamená způsobilost k pokusu, ne záruku výsledku. Run stále
  provádí status, input, dependency, budget a concurrency preflight; reveal
  stále vyžaduje interaktivní a čerstvé přihlášení, důvod a auditní zápis.
  Mutace mohou selhat na aktuálním stavu a verzi mezi čtením a akcí.
- Oba detaily zobrazují společnou komponentu „Your access“. Tlačítko Run a
  credential mutační tlačítka používají serverový verdikt. Při načítání nebo
  chybě nejsou mutace nabídnuté; při změně URL hook zahodí starý verdikt.
  Pro přehled v terminálu slouží `crewship routine access` a
  `crewship credential access`.

## Ověření

Go test pro rutinu pokrývá 5 rolí × 3 varianty capability × 3 stavy rutiny a
kontroluje shodu s autorizační bránou skutečného `/run`. Credential testy
pokrývají viditelnost, SEALED, workspace switch, scope, capability, CLI token
a nepřítomnost tajemství v odpovědi. Frontend testy pokrývají success →
refetch failure → recovery, závod při změně URL, malformed response, neznámý
stav a serverový verdikt odlišný od lokální role. Cílené Go testy, Go vet,
TypeScript, lint, statický build, dokumentační gate a celá frontend sada
(`777` souborů, `9266` testů) prošly. Úplné `go test ./... -count=1 -p 2`
narazilo po deseti minutách na defaultní timeout obrovského balíku
`internal/api`; cílená matice a existující capability/reveal testy prošly.
Celá Go sada proto není prohlášena za zelenou. CI bude ověřeno samostatně.

## Zbývající limity pro iteraci 9

Přehled nepočítá per-routine granty ani credential dependents. Nedokáže
předpovědět živý preflight, přihlášení či úspěch auditu. Endpoint je okamžitý
snapshot: revokace po načtení stránky se prosadí při skutečné akci, ale UI
ji uvidí až při dalším načtení. CLI token nemůže odhalit secret.
