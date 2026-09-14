# Technické ověření: River nad SQLite v Crewshipu

Stav: **provedeno 2026-09-10; výsledek reject.** Naměřené výsledky a rozhodnutí jsou v [ADR](ADR-QUEUE-RIVER-SQLITE-2026-09-10.md), surová data v [reports/spike-river-sqlite-results.json](reports/spike-river-sqlite-results.json), harness v [`tools/spike-river/`](../../tools/spike-river/). Zadání níže zůstává beze změny jako protokol, proti kterému se výsledek čte. Navazuje na [rozhodovací PRD](WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md) a [implementační kontrakt](WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md). Výchozí release cesta rozšiřuje existující SQLite primitives; pouze úspěšné ověření ji mění na River. Rozhodnutí musí padnout před implementací nové queue vrstvy. Časový rámec první iterace: jeden pracovní den; neuzavřené kritické garance znamenají, že River zatím není způsobilý pro release, nikoli že se automaticky považuje za funkční.

## Rozhodnutí, které má ověření přinést

Použít připnutou verzi `riverdriver/riversqlite`, nebo rozšířit stávající assignments/pending_runs. Nejde o výběr nového pipeline engine. Výsledek musí uvést verzi Riveru, modernc driveru a SQLite, hardware, DSN, velikost poolů a konfiguraci checkpointů. [Driver](https://raw.githubusercontent.com/riverqueue/river/master/riverdriver/riversqlite/river_sqlite_driver.go) deklaruje early testing; převzaté garance ověřit proti připnuté verzi.

## Protokol

Použít izolovanou testovací DB a mock worker bez credentials a externích efektů. Neměnit živou DB ani závislosti hlavní aplikace jen kvůli experimentu.

1. **Stejná transakce:** delivery a River enqueue v jednom existujícím `*sql.Tx`. Rollback nesmí nechat ani delivery, ani job. Commit vytvoří obojí; worker nesmí práci získat před commit. Prokázat také souběžné duplicate delivery a správný receipt.
2. **Kompatibilita:** modernc.org/sqlite, foreign keys, immediate transactions, migrace Riveru vedle Crewship migrací, managed WAL a FULL na každé autoritativní write connection. Popsat vlastnictví migrací a rollback/migration-upgrade omezení. Přiložit mapu požadovaných funkcí versus OSS/Pro licence připnuté verze; nespoléhat implicitně na placené global limits/dead-letter/workflow funkce.
3. **Restart a late completion:** ukončit worker po claimu a před potvrzením; obnovit práci a poté doručit completion starého pokusu. Zjistit, co River sám odmítne a kde Crewship musí přidat fencing generation. Knihovní job ID není automaticky run write capability.
4. **Integrace existujících front:** doložit cestu webhook/chat/assignment/pipeline step do jednoho admission kontraktu. Nesmí vzniknout dvojí vlastnictví práce, dvě neatomická enqueue nebo deadlock parent čekajícího na child při plné kapacitě.
5. **Reálná contention konfigurace:** porovnat pool 1 a 5, jeden i více poolů nad stejným souborem, `busy_timeout(30000)`, `_txlock=immediate`, souběžné API zápisy a checkpoint. Držet write transaction druhým klientem. Změřit p95/p99 acceptance, SQLITE_BUSY, cancellation latency a průchod po uvolnění locku. Odhalit opětovné získávání connection z poolu uvnitř již otevřené transakce.

## Úspěch a rozhodovací pravidlo

Žádný orphan delivery/job při rollback či pádu; žádný druhý logický job ze stejné delivery. Ztracený worker má vysvětlitelnou recovery a old-attempt completion nemůže přepsat nový výsledek přes podporované API. Konfigurace nesmí globálně překlopit živé API na jedinou connection bez změřeného dopadu.

Na referenčním stroji při 10 webhook requests/s, payloadu do 64 KiB a mock executor mají nekontendované acceptance cílit p95 do 500 ms a p99 do 2 s. Při úmyslně drženém DB locku endpoint do svého 2s acceptance budgetu buď commitne, nebo vrátí retryable chybu bez falešného `202`. Ověřit, zda driver cancellation skutečně přeruší 30s busy wait; nesplnění vyžaduje explicitní řešení.

Výstup: spustitelný izolovaný harness, naměřená tabulka, crash výsledky, seznam garancí knihovny versus Crewship a krátké ADR `adopt / reject / unresolved`. Adopt je přípustné jen při splnění correctness a contention kritérií. Unresolved není release approval pro River; pokračuje výchozí SQLite cesta. Fallback musí projít stejnými correctness testy; nepřebírat River schéma bez jeho související sémantiky.
