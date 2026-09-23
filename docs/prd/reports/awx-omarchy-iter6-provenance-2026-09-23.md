# Iterace 6 — původ běhu a historické deklarace

Navazuje na diagnostiku #2661. Implementace #2662 čte `definition` výhradně z workspace-scoped `GET /pipeline-runs/{id}`; server ji dohledává podle uloženého JSON/hash/verze. Dnešní HEAD se používá jen pro současnou identitu a srovnání verze, nikdy pro historické credential deklarace.

Detail běhu ukazuje trigger, dostupný zdroj, číslo verze a zkrácený hash. Automations uložené jako `triggered_via=schedule` se poznají podle `metadata.automation_name`, stejně jako seznam běhů a CLI. Pro exportovaný podklad jde ven jen pevná hodnota `automation`, nikoli volný název či metadata. Neznámý trigger zůstává neznámý.

`credentials_required` a `http.credential_ref.type` (i ve foreach a lifecycle hooks) jsou pouze deklarace použitého receptu. UI vždy říká, že skutečné použití credentialu v konkrétním běhu není zaznamenané. Při chybějícím historickém receptu je deklarace nedostupná. Nevyvozujeme ji z promptu, inputs, běžícího sidecaru ani dnešní verze.

Starší `invoking_*` pole nemají spolehlivý marker, že prošla opravou #2567, proto je UI neoznačuje za ověřeného lidského iniciátora. Existující cesta k inputs zůstává oddělená a exportovaný evidence JSON je neobsahuje.

Ověření: čistý extractor a test historické v2 proti odlišné v3, nested HTTP/hook, stará identita, chybějící archiv a schedule-vs-automation; cílené frontendové testy, tsc, eslint, build a Go pipeline/journal. Kompletní frontend suite s omezeným počtem workerů byla spuštěna samostatně; výsledek je nutné doplnit po dokončení. Živý průchod druhým účtem zatím neproběhl.

Další práce: iterace 7 (atomický očekávaný hash při Run again), 8–10 a celková akceptace. Tento report netvrdí dokončení celého PRD.
