# Routines — validace a stav uzavření, 18 September 2026

**Verdikt: integrace #2617 je technicky hotová a nasazená na DEV1 (binárka
`22ed292cd`). Oprava #2573 (PR #2620) v tomto verdiktu nasazená NENÍ — její
deploy a živé ověření kaskády teprve následují po mergi. Lidská přejímka §11
zůstává NOT VERIFIED.**
Tato zpráva ověřuje merge stav PR #2617/#2619, aktuální main, DEV1 nasazení
a R1–R10 proti čerstvým průchodům. Nenahrazuje uživatelskou přejímku.

Závazný rozsah: [ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md](../ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md)
včetně §11. Předchozí protokoly (10.–16. 9.) zůstávají archivem; níže je nové
měření, ne přejmenování starých důkazů.

## Merge stav integrace

- **PR #2617 je MERGED** (`a99a8212235a34f7996a2ebe620c3f57675eb81e`,
  2026-09-18T13:05:05Z). Hlava větve `22ed292cd` je obsahově totožná s main
  (`git diff 22ed292cd origin/main` je prázdný — merge commit nemění strom).
- **Merge ancestry všech 10 zdrojových PR ověřena**: hlavy `audit/routines-cli`,
  `audit/issues-docs`, `audit/credentials-docs`, `audit/memory-chat-cli`,
  `audit/openapi-schemas`, `audit/pages-cli`, `audit/journal-runs-docs`,
  `audit/gates`, `audit/inbox-backup-misc`, `fix/collector-process-diagnostics`
  jsou předci main. Hlava paralelní integrace **#2616** (`a29d5fc39`) je rovněž
  předkem main (merge parent `f86c63764`).
- **Obě dodatečné opravy z handoffu jsou v main**: odmítnutí
  `issue update --assignee-type` bez `--assignee`
  (`cmd/crewship/cmd_issue_lifecycle.go:58,138`) a nullable diagnostická pole
  backup OpenAPI schémat (`internal/api/openapi_backup_payload_test.go`).
- **Review #2617 byl skutečný**: CodeRabbit APPROVED s walkthrough headu
  `22ed292` (ne rate-limit notice); CI zelené včetně Go/Go Race/frontend.
- OpenAPI regenerace, changelog entry, docs-inventory strict, docs-surface-check,
  migrační lint (0 nových) a agents-invariants prošly při této validaci znovu.

## PR #2619 — nezávislý feature PR, žádný vztah k #2617

#2619 „feat(credentials): connect OpenCode Go and Zen" je otevřený PR
autora Srbino (člověk), issue #2618, větev `feat/opencode-go-zen`,
head `907c57e6`. Jde o novou funkci (OpenCode Go/Zen providery, credentials,
modely), **nerozšiřuje ani neopravuje obsah #2617** — diff se netýká Routines
PRD, CLI auditu ani dokumentačních gates z integrace. Issue #2618 má aktivní
claim clone `crewship_3` (2 minuty před kontrolou). **Není duplicitní ani
chybějící práce z #2617; nesahejte na něj v této relaci.**

## DEV1 identita a nasazení

- Branch/checkout `fix/routine-delete-cascades-schedules` (= main + oprava
  #2573 z této relace, PR #2620); DEV1 binárka byla sestavena **před** touto
  opravou z `22ed292cd` (obsahově ≡ main po mergi #2617; build
  `2026-09-18T08:16:13Z`, služba aktivní od 08:16:31 UTC).
- `go version -m /proc/3984/exe`: `vcs.revision=22ed292cd…`, `modified=true` —
  dirty způsobují výhradně 17 chraněných WIP souborů (nezahltížené, SHA-256
  ověřeno na začátku i konci této relace; viz odstavec WIP níže). Žádná
  sledovaná modifikace: `git status` mimo WIP čistý.
- SHA-256 `/proc/3984/exe` = `/tmp/crewship-1-dev` =
  `a46146d35ac85cac1c6d59ef8b0a535bc8270fa85c93866d5fdb01250e3e4937`.
- API `/api/v1/system/version`: commit `22ed292cd`, schema `20260916090151`.
- DEV2/DEV3 nebyly nijak změněny.

## Nový nález a oprava — #2573 (R7 souvislost)

Otevřený produktový defekt bez claimu: **smazání rutiny nechalo její plány
povolené a osiřelé** — schedule zůstal enabled s `next_run_at`, v seznamu
plánů se řádek ukazoval s prázdnou rutinou a scheduler do něj opakovaně
failoval (alert + circuit breaker). Převzato claimem 2026-09-18T13:12Z.

Oprava `Store.SoftDelete` (PR #2620): kaskáda soft-delete + disable schedulů
**v téže transakci** (větví Delete i governance reject); `ScheduleStore.List`
skrývá řádky osiřelé před opravou (audit řádky zůstávají čitelné podle id);
wake probe reference záměrně dotčena není (má vlastní fail-open/closed
sémantiku). Obě regrese jsou doložené **red na starém kódu, green po opravě**
(schéma v PR). Plná sada `internal/pipeline` + `go vet` prošla; nasazení a živé
ověření kaskády na DEV1 proběhne po mergi #2620 (viz Zbývající kroky).

## Dnešní živé průchody DEV1 (API, přihlášený token)

- **R2**: `run_cmu6zm62r000523e4f8ea` — vstupy `count=0, enabled=false,
  note="zero and false"` uloženy přesně (`inputs` i výstup běhu odpovídají;
  `0`/`false` se nerozpadají do null/prázdná).
- **R5**: `validation-fail-mtw9f2k3` — běh `failed`; `failure` nese `step_id`,
  lidský `step_name` („Parse the message as JSON"), shrnutí v lidských slovech,
  `kept_step_ids`/`not_done`; exekuce ukazují selhaný krok. (Poznámka k sondě:
  identitní výraz `.` správně neselhává — selhání vyžaduje výraz `.count`;
  stejná sémantika jako v protokolu 14. 9.)
- **R7**: jednorázový odložený start `pnd_cmu6znh33026833149ce9` — 202,
  `pinned_version=1`, viditelný v pending seznamu **i v kalendářní projekci**
  (včetně input presetu); zrušení 200, z pending seznamu zmizel.
- **R1**: API seznam uvádí `description` a `step_count` pro všechny rutiny.
- Použity výhradně vlastní rutiny `validation-*-mtw9f2k3` (vytvořené přes
  test_run → save s save_token), cizí rutiny nedotčeny; úklid po sobě.

## Plná technická validace (main + oprava #2573)

- `go vet ./...`: prošel.
- Plný `go test ./... -count=1`: **145 balíků ok**; tři balíky padly na
  podmínkách prostředí, ne na asercích:
  - `cmd/crewship` — 4 testy `fork/exec /usr/bin/true: exec format error`.
    Příčina: **systémový `/usr/bin/true` byl 0-bajtový soubor** (mtime
    2026-09-17 08:43, `dpkg -V coreutils` potvrzen rozdíl; vznik mimo tuto
    relaci). Bash prázdný soubor zamaskuje, Go exec dostane ENOEXEC. Po
    `apt-get install --reinstall coreutils` (binárka obnovena, `dpkg -V` čisté)
    proběhlo kontrolované opakování celého balíku — viz výsledek dole.
  - `internal/api` (600.115s) a `internal/database` (600.066s) — přesně
    výchozí 10min limit balíku za souběhu s apt incidentem; stejný režim jako
    v protokolu 14. 9. Opakováno s limitem 25m jako v CI — výsledek dole.
- `scripts/agents-invariants`: 4 checkable položky drží.
- `scripts/lint-migrations`: ok (174 migrací, 0 nových).
- `docs-inventory -strict`: čisté (636 API operací / 878 CLI příkazů).
- `docs-surface-check`: 475 stránek, 0 nezabezpečených.
- Frontend lint/build/testy: změna #2620 je Go + changelog; frontendové sady
  netknuty (plné sady 9 159 testů / 767 souborů proběhly ve validacích
  #2556/#2562 na tomtéž UI stavu; CI #2620 je spustí znovu na aktuálním head).

## Kontrolovaná opakování

(výsledky doplněny po dokončení běhů — sekce Doplňky na konci)

## R1–R10 proti důkazům

Legenda: PASS = automaticky + živě doloženo v rozsahu Release 1.0 §11;
PARTIAL = funguje, ale s výslovně neověřenou částí; NOT VERIFIED = bez důkazu.

| ID | Stav | Důkaz a hranice |
|---|---|---|
| R1 | PASS | Živě 14. 9. (27 rutin/81 kroků, lidské názvy); aktuální UI ověřeno DEV1 akceptací #2562 (16. 9.); dnes API `step_count`+`description`. Porozumění člověka = §11. |
| R2 | PASS | Dnes živě 0/false přesně; 14. 9. browser odmítnutí neplatných, historie hodnot; #2556 desktop+mobile inline chyby a defaulty živě. File/credential pickery jsou výslovně mimo 1.0. |
| R3 | PASS | 14. 9. dva reálné editoři 200/409 se zachovaným textem, Publish preview/Confirm, odkládané běhy na v1; #2562 draft identity/stale 409/phone publish živě 16. 9.; V2/V3 opraveny #2553. |
| R4 | PASS | 14. 9. statická kontrola bez agentů/HTTP, fixture test kroku počítá, HTTP bez replacement odmítnuto, Run ukazuje účinky/hosta/credential typ před spuštěním. |
| R5 | PASS | Dnes živě failure s lidským názvem kroku + kept/not_done; 14. 9. rozlišení failed/cancelled/nepotvrzený výsledek, Run again = nová práce; #2556 retained output živě. Pokračování od kroku mimo 1.0. |
| R6 | PASS | 14. 9. totéž rozhodnutí z obou cest, reálné odeslání formulářem, souběh 200/409, opožděná 409; takeover serverovými testy (ne browser). |
| R7 | PASS | Dnes živě jednorázový pin/cancel/kalendář; 14. 9. recurrence + zóna + DST dispatch testy; preset viditelnost od 10. 9. Defekt #2573 opraven (#2620, nasazení po mergi). |
| R8 | PARTIAL | Typované otázky a pojmenované akce fungují (14. 9.); builder pokryt regresní sadou, ale samostatný živý průchod tvorby celého formuláře v aktuálním UI neproběhl. Návrhář větví mimo 1.0. |
| R9 | PASS | 14. 9. import zachyceného běhu do vzorku přes UI, výpočet kroku, původní běh nepřepsán; izolace fixture režimu kontraktně. Řízené pokračování odloženo v §11. |
| R10 | PASS | 14. 9. dvě publikované verze z Versions, export `complete=true` s oběma run IDs; bez hodnocení kvality modelu. Serverové datasety mimo 1.0. |

## §11 lidská přejímka

**NOT VERIFIED.** Materiál 5 úloh existuje od 11. 9.
([routines-human-gate-2026-09-11](routines-human-gate-2026-09-11.md), odkazy
přímo na dev1). Žádný průchod reprezentativními uživateli neproběhl, žádné
potvrzení uživatele neexistuje (kontrolováno v issue #2473 komentářích i
dokumentech). Cíl ≥4 z 5 uživatelů bez nápovědy zůstává nesplněný měřením.
Interní browser automatizace (14.–16. 9. i dnes) jej nenahrazuje.

## Známá omezení a otevřené položky

- #2620 (kaskáda #2573): před merge — čeká výhradně na rate-limited
  CodeRabbit re-review (viz Zbývající kroky).
- #2569 GitHub Review Pilot: záměrně neimplementován — draft #2572 byl
  odložen vlastníkem s třemi nevyřešenými připomínkami (reviewer-profile
  omezení, souběžná publikace, evidence historických statusů).
- #2609/#2610: flaky macOS watcher / Node collector v CI — nezávislé na
  Routines, sledované samostatně.
- #2612 backup export na zastaveném crew kontejneru (ops).
- #2619/#2622 (OpenCode Go/Zen, Z.AI GLM): cizí feature PR vlastněné
  crewship_3 — nejsou součástí Routines PRD ani této přejímky.
- Editor vazeb podřízené rutiny nenabízí pole dle schématu dítěte — odloženo
  v §11; runtime validace vstupů existuje.
- DEV1 hlásí `dirty=true` kvůli 17 chraněným WIP souborům (viz výše); čistý
  build pro releasovou evidenci vyžaduje build bez WIP (jako 15./16. 9.).

## Zbývající kroky (přesný stav na konci relace 18. 9.)

PR #2620 (head `d1d4fe1e0`): produkční kód i testy dokončeny, **CI plně zelené
na finálním headu** (34 kontrol pass, 0 fail, Go + tři Race sady, Frontend
Test, CodeQL, gitleaks, surface, changelog guard), review nález z `30fb9c18`
vypořádán v `d1d4fe1e0` (4řádková změna přesně v dotčeném souboru) s odpovědí
ve threadu. Merge blokuje výhradně rate-limit CodeRabbit (CHANGES_REQUESTED
stojí na předchozím commitu; 5 retriggerů za ~2,5 h bez výsledku, sloty
spotřebovávají souběžné PR #2619/#2622). Fallback dle CONTRIBUTING proběhl:
manuální review celého diffu zdokumentovaná v PR; admin merge ani dismissal
review se nepoužily.

Po mergi (kdokoli, jedním příkazem, až review proběhne):

1. `make build` + `sudo systemctl reload crewship-ws@1` na DEV1 (jen DEV1).
2. Živě: vytvořit cron schedule na vlastní rutině → smazat rutinu přes
   DELETE → ověřit 204, schedules list prázdný (kaskáda), pending seznam
   čistý; GET by id řádek vrátí (audit).
3. Smazat vlastní testovací rutiny `validation-inputs-mtw9f2k3`,
   `validation-fail-mtw9f2k3` (auditní historie běhů zůstává).
4. Znovu ověřit 17 WIP hashů; `dirty=true` je očekávaný (WIP soubory).


## Ochrana práce

17 původních WIP souborů ověřeno SHA-256 na začátku i na konci relace —
byte-identické, seznam hashů v protokolu relace. Cizí worktree/instance
nedotčeny; DEV2/DEV3 beze změn. Auditní historie vlastních testovacích běhů
zůstává; vlastní dočasné rutiny `validation-*-mtw9f2k3` budou smazány po
živém ověření kaskády #2573 na DEV1 (po nasazení opravy).

## Doplňky (výsledky kontrolovaných opakování)

- `cmd/crewship`: **PASS** (349.5 s) po opravě hostitele — potvrzeno, že původní
  selhání byla výhradně poškozená systémová binárka, ne produkt.
- `internal/database`: **PASS** (817.0 s, 25m limit).
- `internal/api`: první opakování **FAIL na 3 testech — reálný nález v mé opravě
  #2573** (nikoli flaky): API smoke rig neměl tabulku `pipeline_schedules`
  (kaskáda 500), a test z #2562 pinoval orphan viditelný v seznamu — přesně
  chování, které #2573 mění. Opraveno v commitu `a2771660e` (rig doplněn o
  tabulku; orphan kontrakt převeden na skrytý řádek; cílené testy
  `Schedule|Pipeline.*Delete` 7.6 s PASS, `internal/pipeline` 28.2 s PASS).
  Plný `internal/api` běh na novém headu proběhl znovu — viz další doplněk.
- `internal/api` (plný, head `a2771660e`): **PASS** (608.9 s, 25m limit).
  **Celý `go test ./...` je tímto zelený**: 145 balíků z prvního běhu +
  tři kontrolovaná opakování výše.
- CI #2620 na prvním commitu `7af373542` selhal (Go linux-arm64/macOS — též
  tři testy výše); head `a2771660e` dostává čerstvý běh; výsledek se čte až
  z něj, ne z zastaralého runu.
