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

Lokální reprodukční skript: `/tmp/r7-browser.cjs`.
Výsledky a ID: `/tmp/r7-browser-report.json`.
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
