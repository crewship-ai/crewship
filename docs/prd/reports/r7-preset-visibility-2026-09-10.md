# R7 — viditelnost presetu, dev1, 2026-09-10

Ověřen implementační commit `96e80d611865258e5105db6f9bcba8e76b84db55` na
https://crewship-dev1.unifylab.cz. Jde o část R7/§5.5 týkající se vstupních
presetů, nikoli potvrzení celého PRD.

## Reprodukce a výsledek

Přihlášený Chromium otevřel rutinu `work-order-inputs-mtvp2jz6`, vytvořil dvě
opakování a dva jednorázové starty na 11. září, tedy až po ověření. V detailu
byly bez otevření dialogu vidět `Inputs: message: R7 visible preset`,
`No inputs` a zkrácená dlouhá hodnota. Přes Edit inputs byla hodnota změněna
na `R7 updated preset`; detail i denní kalendář ukázaly shodný souhrn.
Kalendář zachoval rozdíl `Planned` / `Scheduled once` a vysvětlení, že
historie představuje skutečné běhy. Detail při 390 px nepřetékal přes viewport.
Žádná JavaScript pageerror. Všechny testovací plány byly zrušeny/odstraněny
ve finally; žádný nebyl spuštěn.

Aktuální reprodukční skript (rozšířen po review, viz níže): `/tmp/r7-browser.cjs`.
Poslední výsledky a ID: `/tmp/r7-browser-report.json`.
Prohlédnuté snímky: `/tmp/r7-detail.png`, `/tmp/r7-calendar.png`, `/tmp/r7-mobile.png`.

## Identita nasazení

Commit a `sudo systemctl reload crewship-ws@1` byly samostatné příkazy.
`rg -l 'Inputs unavailable' web/out/_next/static/chunks/` našel
`23iih3z_43qyp.js`, SHA-256
`5109aa1612ba53d8ecf48279fc3e2f0c63c7e9899be0191815eb0e423a44d953`.
Backend `/api/health` vrátil `{"status":"ok"}`. Čas souboru není důkazem nasazení.

## Regrese a rozsah

Na mainu `812ea8eff` nejprve selhaly oba nové komponentové scénáře i oba Go
API scénáře pro chybějící vstupy. Red logy: `/tmp/r7-red-vitest.log`,
`/tmp/r7-red-go.log`. Po opravě prošlo 321 testů komponent rutin/souhrnu a
Go scénáře `TestPlanPresetVisibility|TestRoutineCalendar`. Odebrání souhrnu
pomocí testového mocku znovu shodilo kalendářní regresi (`/tmp/r7-mutation.log`).

Samostatná čistá funkce zkracuje klíče i první neprázdný řádek hodnoty,
zobrazuje nejvýše dvě hodnoty/typy a ostatní počítá. Běžné citlivé názvy polí,
credential reference a souborové hodnoty skrývá nebo vypisuje jako typ/název;
nejde o obecnou detekci tajných údajů v libovolném textu. Celé hodnoty nejsou
v tooltipech. API doplňuje již uložené vstupy pending startů a plánovaných
kalendářních výskytů; není přidána migrace ani měněn CLI.

§9 (DST, souběžná rozhodnutí s run ID, restart u waitpointu) a lidské potvrzení
Edit/Test/Run zůstávají mimo toto ověření. PR/CI výsledek je nutné číst na
aktuálním headu PR; tento protokol dokládá pouze uvedené lokální a živé kontroly.

## Ověření po zapracování review

CodeRabbit skutečně zkontroloval `0383ac298` a vznesl čtyři připomínky.
V `ab5d7e240` je obnoven samostatný test plánu bez opakování a nové API vrací
read-only projekci: běžné primitivní hodnoty, typová označení credential/file/
redacted a prázdné kontejnery místo strukturovaného obsahu. Uložené vstupy pro
spuštění se nemění. Existující Go scrubber zachytává známé tokeny i pod neutrálním
klíčem; frontend je maskuje před zkrácením textu. Všechny čtyři připomínky mají
odpověď s důkazy a vyřešené vlákno. Finální head tím není automaticky nově
strojově zkontrolován; další review bylo vyžádáno.

325 testů rutin prošlo; Go regrese pro VIEWER ověřuje, že citlivé hodnoty nejsou
ani v JSON odpovědi. Původní redakční regrese nejprve spadly v UI i API.

Opakované živé ověření ab5d7e24063a17ce7648848e014f6799e848acbd zahrnulo
pět dočasných plánů. Tokenový vzor pod `message` vrátil pending endpoint jako
`{type: "redacted"}` a detail i kalendář zobrazily `Hidden`. Ostatní přejímací
kroky výše znovu prošly, bez JavaScript pageerror; všech pět plánů bylo uklizeno.

Finální lokální důkazy: `/tmp/r7-final-evidence.json` a
`/tmp/r7-browser-reviewed.log`. Chunk `web/out/_next/static/chunks/1jzb0u_44iwx-.js`
obsahuje nový string a má SHA-256 `533c8d9890cf7b1438b9de33971237ce3edab154461383d668f87aa93d2f9a41`;
bajty stažené z portu 8081 souhlasí se statickým exportem.


Final review follow-up (22:09 UTC): CodeRabbit reviewed head 8d46c8fe7 and found a file-prefix normalization bypass and incomplete sensitive-fixture presence assertions. New frontend and Go regressions each failed for data/file/blob prefixes with whitespace and Unicode format characters before the fix. Both display classifiers now normalize before prefix detection. API tests require the sensitive pending row and an actual planned occurrence, with redaction assertions for both. CI and deployment evidence below must be refreshed for this follow-up; earlier evidence remains attributed to its original commit.
