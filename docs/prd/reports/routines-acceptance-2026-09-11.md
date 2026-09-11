# Routines §9 — technická přejímka, 11. září 2026

Navazuje na [audit z 10. září](r7-preset-visibility-2026-09-10.md) a na
§9/§11/§12 PRD [ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08](../ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md).
Doplňuje scénáře, které dřívější důkazy neuzavřely, a zaznamenává tři
prokázané vady. **Lidská přejímka (§11, pět úloh bez výkladu) není součástí
tohoto protokolu a žádný agent ji nesmí odškrtnout.**

## Prostředí

| | |
|---|---|
| Výchozí kód | `ecab95ba44e0377064dd2eee18f32b828e2a67e9` (= `origin/main` v 08:20 UTC) |
| Větev změn | `fix/routines-cancel-classification-20260911` |
| Izolovaná instance | vlastní proces na `127.0.0.1:8099`, vlastní `CREWSHIP_DATA_DIR`, vlastní SQLite, vlastní IPC socket; binárka `sha256:16b2cdc9…` zkopírovaná z `/tmp/crewship-1-dev` |
| Řízená externí služba | lokální HTTP recorder na `127.0.0.1:8101`, zapisuje účinek **před** volbou odpovědi |
| Živé ověření po nasazení | dev1 (`localhost:8081`, `crewship-dev1.unifylab.cz`), binárka `sha256:14fae285…` |

Dev2, dev3 a stage nebyly použity. Databáze dev1 nebyla nulována, žádná cizí
rutina nebyla smazána, žádný cizí stash nebyl aplikován. Surové důkazy
(JSON snapshoty, screenshoty, logy) leží mimo repozitář v
`/srv/crewship/backups/crewship_1/acceptance-20260911/`.

### Proč izolovaná instance a ne dev1

Priorita 1 vyžaduje `SIGKILL`. Dev1 nese běhy jiných relací; zabít ho by
znamenalo zničit cizí práci. Izolovaná instance byla třikrát tvrdě zabita
(`kill -9`) a třikrát nastartována znovu.

## Nové důkazy

### P1 — Náhlý pád a obnova

Jeden běh, dva tvrdé pády. Rutina `acc-recovery` (agentless): `prepare`
(transform) → `announce` (notify, počitatelný trvalý účinek) → `decide`
(wait/approval) → `hold` (wait/datetime, čistě in-process) → `confirm`
(notify) → `finish`.

**Běh `run_cmtwp7vt700033e66cfc6`, verze v1, `definition_hash afa10a995ec7`.**

| Okamžik | Co se stalo |
|---|---|
| 08:32:49Z | start; `prepare` a `announce` dokončeny, běh zaparkován na `decide`, waitpoint `d65351e61b3347e3f4507d8a3e3770a3`, Inbox `ibx_waitpoint_d65351e6…` |
| 08:33:07Z | **`kill -9`** na čekajícím rozhodnutí |
| 08:33:09Z | restart; log: `resuming pipeline run from persisted step state … current_step_id=decide restored_steps=2`, `resumed=1 interrupted=0` |
| — | `prepare` a `announce` zůstaly na `attempt=1` (obnoveny, neopakovány). `decide` dostal nový řádek `attempt=2` na **témže** tokenu a témže Inbox id. Rozhodnutí bylo dál zodpověditelné. |
| 08:33:39Z | schváleno; běh vstoupil do `hold` |
| 08:33:51Z | **publikována v2** (`d72fc5043f0b`, mění titulek `confirm` a přidává krok `v2only`) zatímco v1 běh běžel |
| 08:33:55Z | **`kill -9`** uprostřed rozpracovaného kroku `hold` |
| 08:33:57Z | restart; `current_step_id=hold restored_steps=3`, `resumed=1 interrupted=0` |
| 08:40:53Z | běh dokončen |

Konečný stav exekucí:

```
prepare   completed  att=1     announce  completed  att=1
decide    waiting    att=1     decide    waiting    att=2     decide  completed att=3
hold      interrupted att=1    hold      completed  att=2
confirm   completed  att=1     finish    completed  att=1
```

Doložené:

- **Zachování přijaté verze a snapshotu po publikaci novější verze.** Běh
  skončil na v1 (`afa10a995ec7`). Účinek `confirm` nese v1 znění
  `EFFECT confirm P1-hardkill`, nikoli v2 `EFFECT v2-confirm`; krok `v2only`
  z v2 se nikdy neprovedl.
- **Zachování dokončených mezivýsledků.** `prepare`, `announce` i přijatý
  verdikt `decide` zůstaly na svých pokusech; nic se neopakovalo.
- **Rozsah at-least-once.** Rozpracovaný krok `hold` se provedl znovu
  (`att=1 interrupted`, `att=2 completed`). Framework první pokus označí
  `interrupted`, nezahodí ho mlčky.
- **Počitatelné účinky.** Přesně dva notify účinky, každý jednou. Pozn.:
  notify je idempotentní per (run, step) — Inbox id je `…:announce` /
  `…:confirm` — takže počet Inbox řádků *neměří* at-least-once. To měří
  Go test níže.

Hranice: `hold` je in-process čekání bez vnějšího účinku. Počitatelný důkaz
„účinek se provedl dvakrát“ je v `TestUncertainEffect_ResumeReappliesTheInFlightStep`.

### P2 — Nejistý externí účinek

**Živá vrstva to nedokáže hostit.** `httpsafe` odmítá u `http` kroku každou
loopback i RFC1918 adresu a tento stroj nemá jinou (`192.168.1.201`, za NAT).
Řízený recorder je tedy z reálného crewshipd nedosažitelný. Pokus o `http`
krok na `127.0.0.1:8101` skončil doložitelně:

```
run_cmtwp2u9d00032f0d39c5  failed at "external"
http step "external": httpsafe: invalid outbound URL: literal private/internal IP 127.0.0.1 not allowed
```

To je správné chování SSRF ochrany, ne vada — a zároveň tvrdá hranice
přejímky. Scénář je proto uzavřen na úrovni, kde je měřitelný:
`internal/pipeline/uncertain_external_effect_test.go`. Recorder zapisuje
účinek **dřív**, než zvolí odpověď, takže aserce čtou jeho účetní knihu, ne
domněnku exekutoru. Skutečný runner, skutečné egress brány, skutečná
perzistence, skutečná resume cesta; `SetAllowPrivateHTTPForTesting` uvolňuje
výhradně SSRF kontrolu.

| Test | Co měří |
|---|---|
| `…_ResponseLostAfterWrite_FailsHonestly` | recorder 1× zasažen, běh `FAILED` na `apply`, `error_message` = `http step "apply" read body: unexpected EOF`, výstup `prepare` trvale uložen, `finish` **neproběhl** (0 zásahů) |
| `…_StepTimeout_LeavesTheEffectApplied` | timeout kroku: účinek 1×, běh selhal, následující krok 0× |
| `…_ResumeReappliesTheInFlightStep` | přes tvrdý pád: `/prepare` **1×**, `/apply` **2×**, `/finish` 1×; resume pokračuje na **témže** run id |
| `…_ReplayIsANewRunNotAResume` | dvě vědomá spuštění = dvě run id a každý endpoint 2× |
| `…_RecorderLedgerIsTheOnlyWitness` | premisa: recorder započítá požadavek, jehož odpověď volající nikdy nepřečetl |

Aplikace netvrdí „bezpečně pokračovat“: běh je `FAILED`, důvod je zachován
a krok za nejistým krokem se nespustí.

### P3 — Rozhodnutí, timeout a souběh

Vše živě na izolované instanci, přes skutečné HTTP.

**Timeout vs. opožděná odpověď.** `acc-decision-timeout`,
`timeout_seconds: 20`, běh `run_cmtwpgl3f00052bba32fb`.

- Sweeper verdikt uzavřel: `status=failed`, `error_message` =
  `wait step "decide" (approval) timed out`, Inbox karta `resolved`,
  následný krok `after` **neproběhl**.
- Opožděná odpověď přes API i CLI: **HTTP 409**
  `{"error":"waitpoint: already decided or expired"}`. Stav běhu se nezměnil.

**Dvě současné odpovědi.** `acc-decision-race`, čtyři nezávislé souběhy;
`approved:true` a `approved:false` odeslány ve stejném okamžiku na stejný
token.

| Běh | Výsledek |
|---|---|
| `run_cmtwpjpvv0007305cc63a` | reject 200 / approve 409 → `failed`, `wait step "decide" (approval) denied` |
| `run_cmtwpka4700087d343c72` | approve 200 / reject 409 → `completed` |
| `run_cmtwpkdk20009c8ec8b97` | approve 200 / reject 409 → `completed` |
| `run_cmtwpkguq000a5830472b` | approve 200 / reject 409 → `completed` |

Vždy přesně jedno 200 a jedno 409; výsledek běhu vždy následoval přijatý
verdikt. Krok `after` proběhl právě u tří dokončených běhů a u žádného
zamítnutého ani vypršelého — Inbox obsahuje přesně tři řádky
`EFFECT after-race`. Journal raced běhu ukazuje jediný `pipeline.run.started …
resumed after approval`.

Pozn. k harnessu, ne k produktu: `/reject` jako route neexistuje — CLI
posílá `approved:false` na `…/approve`, a `…/approve` bez pole `approved`
**zamítá** (záměrný fail-closed, zdokumentováno v `pipelines_exec.go`).
První kolo měření na to narazilo a bylo zopakováno.

**Issue takeover vs. opožděná odpověď** — nedoplněno živě. Souběh
`answer` × `take_over` na Inbox kartě a souběh takeover × interní status
mají skutečné regresní testy
(`internal/api/issue_work_concurrency_test.go`: jeden vítěz, 200/1×, 409/1×,
poražená akce nezanechá účinek). Kombinace „takeover potvrzen, odpověď
dorazí až potom“ zůstává **NEOVĚŘENO**.

### P4 — Chybové stavy a oprávnění

**Serverová autorizace — úplná živá matice.** Druhý skutečný uživatel
(`outsider@tw.local`) s vlastním workspace, přímé API odkazy do cizího
workspace:

| Cesta | Anonym | Cizí uživatel |
|---|---|---|
| detail běhu, exekuce, artefakty, strom, logy | 401 | 403 |
| rutina, verze rutiny, waitpointy | 401 | 403 |
| Inbox, journal | 401 | 403 |
| rozhodnout waitpoint, zrušit běh, spustit rutinu, uložit, smazat, replay | — | **403** |
| náš běh/rutina pod *jejich* workspace id | — | 404, bez úniku |
| stažení souboru cizí party (`/crews/{id}/files/download`) | 401 | 403 |
| path traversal `../../../../etc/passwd`, absolutní cesta | — | 400 `Invalid file path` |

Po pokusech cizího uživatele zůstal waitpoint
`f2c5b027cdc5d7fa623ec0a98ab29e03` dál `pending` pro oprávněného vlastníka.
Tyto důkazy jsou serverové; fault injection v prohlížeči by je nenahradila.

**Chybějící historický archiv, bez tichého fallbacku na současný recept.**

```
GET …/pipelines/acc-recovery/versions/1   → 200, v1 definice
GET …/pipelines/acc-recovery/versions/99  → 404 {"error":"version not found"}
GET …/pipelines/acc-recovery/versions/0   → 404
GET …/pipelines/acc-recovery/versions/abc → 400 "version must be a positive integer"
```

v1 vrací `EFFECT confirm {{ inputs.label }}` a 6 kroků; v2 vrací
`EFFECT v2-confirm …` a 7 kroků. Archiv je skutečný, ne ukazatel na HEAD.

**Selhání načtení výstupů / journalu.** Detail, exekuce, logy, artefakty i
strom neexistujícího běhu vracejí **404 `run not found`** — chyba odlišená
od prázdna. `GET /journal?run_id=…` vrací 200 s nula položkami; jde o filtr
nad journalem, ne o fetch zdroje, a samotný běh 404 vrací.

Reakce UI na výpadek načítání (prázdno vs. chyba) je záležitost prohlížeče a
**NEOVĚŘENO** v tomto protokolu.

### P5 — Autorování, plány, zátěž

**Neprovedená větev, foreach a skutečné pokusy — živě, v jednom běhu.**
`acc-branch-foreach-retry`, `run_cmtwqaand001db3acdea1`:

```
branch  /branch              att=1  skipped    "Condition was false"
fan     /fan                 att=1  completed
each    /fan/items/0/each    att=1  completed
each    /fan/items/1/each    att=1  completed
each    /fan/items/2/each    att=1  completed
flaky   /flaky               att=1  failed
flaky   /flaky               att=2  failed
flaky   /flaky               att=3  failed
```

Skipped krok nese evidenci („Condition was false“), nemlčí. Položky foreach
mají **různé** `execution_path` a `attempt=1`; skutečné pokusy mají **tutéž**
cestu a `attempt` 1→3. Obojí je tedy rozlišitelné a ani jedno se nepočítá
jako definovaný krok (rutina má 3 kroky, exekucí je 8). Běh skončil
`failed` na `flaky` s čitelným důvodem; `step_outputs` drží `branch` a `fan`.

**Dva editoři, konflikt revizí.** Draft `acc-two-editors`
(`pln_cmtwqbkia0006ad593668`): oba editoři drží revizi 1, A ukládá → 200,
revize 2; B ukládá na revizi 1 → **409** „routine draft changed or its
published recipe changed; reload and review before publishing“. Po
znovunačtení B ukládá na revizi 2 → 200, revize 3, jeho text („B edited
after reload“) je uložen. Server nikdy nepřepsal A ani nezahodil B.
Zachování rozepsaného textu v *editoru* B je vlastnost klienta a je
**NEOVĚŘENO** v prohlížeči.

**DST na skutečné dispatch cestě.** Dosud existovala jen projekce (kalendář).
`internal/pipeline/schedules_dst_dispatch_test.go` žene `fireOne` s
připnutými hodinami; hostitelský čas se nemění.

| Přechod | Doložené |
|---|---|
| 2026-10-25, `30 2 * * *` Europe/Prague | zdvojený místní čas 02:30 **odpálí dvakrát** — 00:30Z (CEST) a 01:30Z (CET) — jako dva samostatné běhy; další due bar je 26. 10. 01:30Z |
| 2027-03-28 | neexistující místní 02:30 **neodpálí vůbec**, due bar postoupí na 29. 3. 00:30Z; plán se nezasekne a 29. 3. skutečně běží |
| hodinový plán přes oba přechody | kontrolní vzorek: rozestup zůstane hodinu, nic navíc ani nic navíc chybějícího |

Projekce a skutečné spuštění jsou tím vykázány odděleně.

**Typované vstupy, defaulty a neplatné hodnoty.** UI: `InputsForm`
(`routine-run-inputs-dialog.tsx`) kontroluje povinnost (`isMissingRequired`)
i typ (`routineInputsFromValues` vyhodí `RoutineInputError` a chyba se
vykreslí **u pole**, `data-testid="routine-input-error-<name>"`). Ručně i v
plánu jde o **stejnou komponentu** — preset formulář, kalendářní plán i
fixture test ji importují. Požadavek §11 R2 („uvidí neplatnou hodnotu u
pole“) je tedy splněn na vrstvě, kterou pojmenovává. Serverová vrstva žádnou
z těchto kontrol neprovádí — viz nález **N3**.

**Změna schématu s nekompatibilním presetem.** Brána existuje a má skutečné
regresní testy na obou vrstvách:
`internal/pipeline/drafts_test.go:TestDraftPublicationChecksSchedulePresetsAtomically`
(pojmenuje plán, zachová draft, nezmění živý recept, připnutý plán je
vyňat) a `internal/api/pipeline_drafts_test.go` (HTTP 409 s
`schedule_conflict` nesoucím `schedule_id`, `name` a `reason`). Živý průchod
přes Edit → Publish se v tomto protokolu nepodařil — můj harness
nereprodukoval kanonický `definition_hash`, který podepisuje `save_token`;
jde o chybu harnessu, ne o produkt. Živě je naopak doložen **obchvat**
brány, viz nález **N2**.

### Co tento protokol nedoplnil

| Scénář | Stav |
|---|---|
| Issue takeover × opožděná odpověď | NEOVĚŘENO (souběžná varianta má testy) |
| UI chování při selhání načtení outputs/journalu | NEOVĚŘENO (server ověřen) |
| „Výsledek existuje, chybí completion signal“ jako živý průchod | NEOVĚŘENO; kontrakt existuje (`routineRunExplanation`, `no outcome reported`), živý stav se na tomto kódu nepodařilo vyrobit — recovery ho buď obnoví, nebo výslovně označí `interrupted` |
| Dlouhý journal, lazy loading | NEOVĚŘENO nad rámec důkazů z 10. 9. (100 kroků / 1 100 exekucí / 11 stran) |
| Zbývající klávesnicové interakce, úplný accessibility audit | NEOVĚŘENO |
| Zachování rozepsaného textu ve dvou prohlížečích | NEOVĚŘENO (serverový konflikt ověřen) |
| Živý Edit → Publish s nekompatibilním presetem | NEOVĚŘENO (pokryto testy na obou vrstvách) |

## Nálezy

### N1 — Zrušený běh se zapisoval jako selhaný · OPRAVENO

Klasifikace zrušení se ptala výhradně `RunRegistry`, tedy tlačítka Cancel.
Každá jiná cesta, kterou kontext běhu skončí, nechala nálepku `FAILED`,
kterou napsala kroková smyčka — a ty ostatní cesty jsou ty běžné: výchozí
`POST …/run` předává exekutoru kontext HTTP požadavku, takže zavřená karta,
timeout proxy, deadline CLI i řádné vypnutí končí běh kontextem, který
registry nikdy neviděl.

Reprodukce na izolované instanci (před opravou):

```
curl -m 5 -X POST …/pipelines/acc-nosignal/run -d '{"inputs":{…}}'   # klient odpadne po 5 s
→ run_cmtwpqgl6000fc1fbf43b  status=failed  outcome=FAILED
   error_message="context canceled"  error_fingerprint minted
   journal pipeline.run.failed  payload.status="CANCELLED"
```

Řádek a journal si tedy protiřečily *konstrukcí* (`emitRunFailed` klasifikuje
podle `ctx.Err()`), a `docs/guides/routines.mdx` popisoval chování řádku
slovy journalu. Navíc padly ochrany, které #1426 bod 2.1 pro zrušení
výslovně vyloučil: minted error fingerprint v errors view, odpálená failure
notifikace, spuštěný `on_failure` hook.

Oprava: `runWasCancelled` přijme svědectví od kteréhokoli zdroje —
registry, nebo `context.Canceled` na kontextu běhu. `DeadlineExceeded` je
záměrně vyňat: běh, který vyčerpal vlastní limit, selhal po právu.
`cancelledRunMessage` zachová jakýkoli důvod, který kroková smyčka složila,
a holý Go sentinel nahradí větou, která pojmenuje krok.

Regresní test `internal/pipeline/executor_ctx_cancel_classify_test.go` padá
na předchozím kódu na všech pěti asercích.

Živě po nasazení na dev1 (`run_cmtwqn4ab00035c82ccbc`):
`status=cancelled`, `outcome=CANCELLED`,
`error_message="run cancelled at step hold"`, `error_fingerprint` prázdný.

Doprovodná oprava: detail běhu psal nad důvodem „Failed step: …“ i u běhu,
který v témže panelu hlásí „Run stopped“. `routineStoppingPointLabel` odvodí
uvození ze stejného zaznamenaného stavu jako pilulka a nadpis.

**Neopraveno a nezměněno:** samotná životnost synchronního běhu. Výchozí
`POST …/run` běh k požadavku váže dál; `Prefer: respond-async` ho od něj
odváže a je nyní zdokumentován v průvodci. Živě ověřeno:
`run_cmtwprucs0010063ab752` s `Prefer: respond-async` přežil odchod klienta
a doběhl. `crewship routine run` async cestu nepoužívá.

### N2 — Kontrola presetů se obchází přes `routine save` · NEOPRAVENO

Brána kompatibility plánů žije v `consumeDraftTx`, tedy výhradně na cestě
draft → publish, a jen pro **povolené** a **nepřipnuté** plány. Přímé
`crewship routine save` (dveře CLI a agentů) změní schéma pod plánem bez
varování:

```
plán "nightly-who" (enabled, nepřipnutý), preset {"who":"alice"}
crewship routine save …  # v2 přejmenuje povinný vstup who → recipient
→ Saved routine acc-schema (hash=eb481348ceeb)
plán po změně: stále enabled, stále {"who":"alice"}
HEAD vstupy: ["recipient"]
```

§9 to zakazuje slovy „žádný tichý rozbitý plán“. Oprava znamená přesunout
bránu na všechny zápisové cesty, což by začalo odmítat dnes fungující
uložení od agentů a CLI — patří do vlastního PR s vlastním rozhodnutím.

### N3 — Server nevaliduje typované vstupy běhu · NEOPRAVENO

`validateFormValue` existuje a je úplný (povinnost, typ, meze, volby), ale
volá se jen pro odpovědi rozhodovacích formulářů a pro vstupy **vnořené**
rutiny (`nested_inputs.go`). Vstupy běhu nejvyšší úrovně neprochází ničím.
Živě, obě cesty stejně:

| Vstup | `POST …/run` | `POST …/pipeline-schedules` |
|---|---|---|
| chybí povinný `who` | **200, běh COMPLETED** s `who=""` | **201** |
| `count: "seven"` u `type: number` | **200, COMPLETED**, výstup `"count":seven` (nevalidní JSON) | **201** |
| `dry_run: "yes"` u `type: boolean` | 200 | 201 |
| pole jako objekt, objekt jako pole | 200 | 201 |
| neznámý vstup | 200 | 201 |

„Shodná validace ručně i v plánu“ tedy platí — obě cesty validují stejně,
totiž nijak. Požadavek §11 R2 se týká toho, co vidí klient, a UI ho plní;
tvrdší serverová brána je změna kontraktu pro agenty i webhooky a patří do
vlastního PR.

## Úklid

Izolovaná instance i recorder zastaveny, jejich data zůstávají mimo repozitář
ve scratchpadu relace. Na dev1 byla založena jediná dočasná rutina
`zz-acceptance-cancel-probe` (a jeden běh k ní) — po přejímce smazána; žádná
původní rutina, žádný cizí běh a žádný plán nebyl změněn. Pět testovacích
plánů na izolované instanci bylo smazáno hned po měření.
