# Routines — security, performance a PRD audit (20. září 2026)

## Shrnutí

Audit zdrojů vychází z `main` `8a1ca5fc3`; produktový kód odpovídá nasazenému
`00bc0cb50` a rozdíl je dokumentační merge #2625. Cílené bezpečnostní testy
prošly. V prohlédnutých cestách nebyla potvrzena kritická ani vysoká zranitelnost.
Toto není penetrační test ani opakovaná autentizovaná kontrola DEV1.

Implementační základ Routines je silný, ale plné naplnění PRD nelze označit za
uzavřené. §11 uživatelská přejímka neproběhla; R8 zůstává částečné; požadované
výkonové měření podle §8 nemá srovnatelný baseline. Nalezené škálovací mezery
jsou uvedeny níže.

## Rozsah a provedené ověření

- Ruční revize API rout, autorizace, běhu HTTP/script kroků, egress pravidel,
  práce s artefakty, seznamů plánů/běhů a příslušných limitů.
- Dřívější živá security matice v
  [akceptační zprávě z 11. září](routines-acceptance-2026-09-11.md): druhý
  uživatel, anonymní přístup, přímé odkazy přes hranici workspace, mutace,
  waitpointy, artefakty, stahování souborů a path traversal.
- Aktuálně spuštěno:
  - `go test ./internal/api -run 'TestRoutineArtifacts|TestRunEndpoint_|TestPipelineWrite_BodySizeCap|TestSecPipeMax_' -count=1` — PASS.
  - `go test ./internal/pipeline -run 'TestUncertainEffect|TestValidate_Egress_|TestRunScript_|TestScriptStep_|TestScriptAudit_|TestScriptProcess' -count=1` — PASS.
  - `go test ./internal/api -run 'TestRunExecutions_PaginationOutputAndWorkspaceBoundary|TestPipelineSchedules_.*(OtherWorkspace|List_Hides)|TestExportPipeline_CrossWorkspace_NotFound|TestPipelinesAPI_Delete_MEMBER_Forbidden' -count=1` — PASS.
- Pouze čtením prohlédnuta lokální DEV1 SQLite databáze: 193 rutin, 42 plánů,
  224 běhů, 27 700 journal záznamů. Jde o malý snapshot, ne zátěžový vzorek.
  `EXPLAIN QUERY PLAN` pro nepřefiltrovaný workspace feed běhů ukázal scan
  `pipeline_runs` a dočasné řazení. Tento plán při malé tabulce sám o sobě
  neprokazuje regresi při produkčním objemu.
- `./dev.sh status`: proces běží nad `00bc0cb5`; `web/out` také pochází z tohoto
  commitu. `main` je napřed jen dokumentačním commitem #2625. Neautentizovaný
  endpoint identity vrací 401, proto živou autentizovanou matici a běhovou
  identitu API tento audit znovu nezměřil.

## Bezpečnostní nálezy

### S1 — Credential reference může záměrně pokračovat bez credentialu (střední, návrhové riziko)

`runHTTPStep` injektuje credential jen při úspěšné resolve; chybějící typ nebo
chyba resolveru sama o sobě HTTP request nezastaví. `credentials_required` je
oddělená deklarace a gate při chybě DB/probe selhává otevřeně. Test
`TestWiredHTTPStep_CredentialInjection` výslovně očekává, že neznámý typ
neinjektuje hodnotu a request přesto proběhne.

Toto je zdokumentované chování, ne náhodné obejití autorizace. Pro rutinu, která
má autentizované volání považovat za povinné, však musí autor navíc správně
vyplnit `credentials_required`; jinak může endpoint dostat neautentizovaný
request. Doporučení: buď odvodit povinnost z `credential_ref`, nebo v UI/API
rozlišit `required` a `optional` a zobrazit následky. Při povinném credentialu
má chyba resolveru blokovat krok. Před změnou ověřit kompatibilitu existujících
rutin.

### S2 — Skripty sdílejí důvěrovou hranici crew (informativní)

Script běží jako proces v autor crew kontejneru, se stejným sdíleným svazkem a
proxy/egress hranicí. Kontejner má dle implementace hardened profil (UID 1001,
odebrané capabilities, no-new-privileges, read-only rootfs), ale nejde o
izolovaný kontejner pro každý krok nebo rutinu. Rutiny stejné crew proto nesou
vzájemnou důvěru přes sdílené soubory a prostředí. To je architektonická hranice,
ne potvrzený únik mezi workspaces.

### Bezpečnostní kontroly, které jsou doložené

- API routy jsou přihlášené a workspace-scoped; citlivé operace mají role nebo
  capability gate. Run endpoint dále kontroluje governance, zdroje,
  integrace, credentials a rozpočet.
- Artefakty ověřují workspace přes run ID; blob stahování je lazy a soukromé.
  Zpráva z 11. září doložila 401/403/404 hranice a odmítnutí traversal.
- HTTP krok validuje URL proti SSRF, aplikuje crew a routine egress policy a
  používá redirect gate na každém přesměrování. Script env nesmí přepsat proxy.
- Ověřené limity zahrnují 1 MiB request body, 500 top-level kroků, 10 000
  položek foreach s nejvýše 12 souběžnými kroky, 1 MiB script stdout a limit
  velikosti HTTP odpovědi 50 MB.
- At-least-once obnova může znovu provést rozpracovaný krok. PRD to správně
  netvrdí jako exactly-once; nejistý externí účinek je třeba dál zobrazovat jako
  nejistý, ne jako bezpečný retry.

## Výkonové nálezy

### P1 — Seznamy plánů a rutin nemají stránkovací kontrakt

`PipelineHandler.List` nepřebírá `limit` z query; `Store.List` vrací nejvýše
500 řádků bez kurzoru. Klient tak nemá způsob, jak načíst rutiny nad tento limit.
`ScheduleStore.List` načítá všechny plány workspace bez limitu i kurzoru.
To nedodržuje obecný požadavek PRD §8 „seznamy stránkovat“ a při velkých
workspace současně zvětšuje čas, paměť i odpověď.

### P2 — Původní seznam a kalendář prováděly dotazy rostoucí s počtem plánů

`ListSchedules` původně volal `GetByID` pro každý unikátní cíl plánu a
`RoutineCalendar` volal `GetByID` uvnitř smyčky. Nový dávkový workspace-scoped
projection tento násobek DB dotazů odstraňuje. Kalendář ale stále načte všechny
plány workspace a až následně omezí výstup na 1 000 událostí; pro extrémně velké
workspace může narůstat CPU práce i při nízkém počtu výsledků. Samostatné
měření tohoto extrému zatím chybí.

### P2 — Workspace feed běhů nemá obecný index pro řazení

`ListWorkspaceRuns` při výchozím zobrazení filtruje podle workspace a řadí podle
`started_at`. Schéma má index workspace/status a částečný workspace/started_at
index jen pro aktivní běhy. V aktuálním malém DEV1 snapshotu plánovač zvolil scan
celé `pipeline_runs` a dočasné řazení. Při růstu historie může výchozí feed
opakovaně číst a řadit velké množství řádků. Před přidáním indexu změřit realistický
workspace a zkontrolovat dopad indexu na zápisovou zátěž SQLite.

### P1 — Benchmark PRD §8 není srovnatelný

Historický baseline uvádí 470 ms, 32 API požadavků a 158 433 přenesených bajtů.
Pozdější test uvádí první zobrazení 770 ms pro jinou velikost/detail dat a
stránkování. Není doloženo měření obou verzí nad totožným datasetem a stejným
postupem; chybí aktuální počet requestů/přenesených bytů, opakování a rozptyl.
To není důkaz zpomalení, ale ani splnění požadavku §8 „baseline a změnu na
stejných datech“.

Scénář §9 #16 doložil 100 kroků a 1 100 execution záznamů/stránkování a UI
lazy-load. Protokol výslovně upozorňuje, že šlo o foreach exekuce, nikoli 1 000
retry pokusů. Je to dobrý smoke test velkého výsledku, ale ne úplný zátěžový
benchmark ani test dlouhého běhu s tisícem opakovaných pokusů.

## Naplnění PRD

- **R1–R7:** report z 18. září uvádí PASS s kombinací živých, browser a
  serverových testů. Živá oprava kaskády #2573 je doložena protokolem po merge
  #2620. Bez přihlášeného přístupu ji tento audit znovu nereprodukoval.
- **R8:** PARTIAL — builder formulářů má regresní testy, ale samostatný živý
  průchod vytvořením formuláře v aktuálním UI chybí.
- **R9–R10:** report uvádí PASS na historických průchodech ze 14. září; nejde o
  nové měření v tomto auditu.
- **§9 scénář 14 (oprávnění):** doložen úplnou živou maticí s druhým uživatelem
  11. září a aktuálními cílenými serverovými testy. Opakovaný živý pokus 20. 9.
  chybí.
- **§9 scénář 16 / §8 výkon:** stránkovací smoke test existuje; srovnatelný
  baseline benchmark chybí.
- **§11:** NOT VERIFIED. Pět uživatelských úloh nebylo provedeno s
  reprezentativními uživateli; pouze oni mohou potvrdit cíl ≥4/5 bez nápovědy.

## Doporučené pořadí uzavření

1. Zpřesnit kontrakt povinných a nepovinných credentialů; přidat fail-closed
   test povinného typu při chybě resolveru.
2. Stránkování routin a plánů, O(N) lookupy v API listu/kalendáři a feed index
   jsou doplněny na auditní pracovní větvi a čekají na celou ověřovací sadu.
3. Změřit aktuální a baseline UI nad stejným datasetem, 100 kroků a 1 000+
   execution záznamy; zaznamenat počet requestů, bajty a opakovaný čas prvního
   použitelného detailu. Samostatně testovat dlouhou historii a 1 000 retry.
4. Spustit pět uživatelských úloh včetně samostatného autorování formuláře a
   zaznamenat úspěch/nápovědu pro každou úlohu. Dokud to neproběhne, PRD a
   Release 1.0 zůstávají z hlediska uživatelského přijetí otevřené.

## Následné technické opravy (20. září)

Na větvi `fix/routines-security-performance-prd` byly po auditu provedeny:

- Seznam plánů a kalendář načtou slug, název, stav a verzi rutin dávkovým
  workspace-scoped dotazem; dotazy se už nezvyšují o jeden pro každý plán.
- Seznam rutin i plánů podporuje volitelné `limit`/`offset` a vrací
  `X-Next-Offset`. Výchozí JSON pole zůstává stejné a starší API klienti
  zachovají původní chování. Frontendové hooky stahují všechny stránky.
- Pořadí seznamů má stabilní ID tie-breaker. Přidaný index pro workspace feed
  běhů a plánů má EXPLAIN regresní test. OpenAPI a API reference jsou upravené.

Zelené cílené kontroly: API stránkování, hranice workspace, efektivní verze,
kalendářní projekce a EXPLAIN; tři klientské regresní testy; migration lint.
`go vet ./...`, `pnpm lint` (0 chyb; 30 varování mimo změněné soubory),
`pnpm build`, `pnpm exec vitest run` (9 216 testů / 773 souborů) a strict
docs-inventory prošly. `go test ./... -count=1` ani následný běh
`go test ./internal/api ./internal/pipeline ./internal/database -count=1`
neprošly do konce: API a database balíky vypršely po 10 minutách v
nesouvisejících `TestMemoryVersions_Restore_HappyPath` a
`TestCheckpointerBoundsWAL`. V tu dobu na stejném hostu běžel ještě jiný plný
Go test proces s 40min timeoutem; příčinu timeoutu nelze bezpečně připsat pouze
této konkurenci, ale log neukazuje pád nových rutina testů. Cílené testy
změněných cest jsou zelené.

Tyto změny napravují konkrétní statické škálovací mezery. PRD §8 však dál
vyžaduje srovnatelné měření baseline a současné verze na totožném datasetu,
včetně počtu požadavků, přenesených bytů a času do použitelného detailu. To
dosud nebylo provedeno. Stejně tak živá autentizovaná security matice je v tomto
auditu historickým důkazem, nikoli novým měřením z 20. září.
