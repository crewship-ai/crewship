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

```text
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

```text
run_cmtwp2u9d00032f0d39c5  failed at "external"
http step "external": httpsafe: invalid outbound URL: literal private/internal IP 127.0.0.1 not allowed
```

To je správné chování SSRF ochrany, ne vada — a zároveň tvrdá hranice
přejímky. Scénář je proto uzavřen na úrovni, kde je měřitelný:
`internal/pipeline/uncertain_external_effect_test.go`. Recorder zapisuje
účinek **dřív**, než zvolí odpověď, takže aserce čtou jeho účetní knihu, ne
domněnku exekutoru.

Rozsah té vrstvy přesně: produkční je `runHTTPStep`, run store i jeho
perzistence výstupů kroků, boot resume sken a jeho drift gate.
Produkční **není** crew network policy gate ani credential resolver — ty
`NewWiredExecutor` zapojuje a tyto rigy je nechávají nil, protože soubor měří,
co se stane *poté*, co byl požadavek povolen ven. Vrstvy rozhodující, zda smí
odejít, pokrývají `runner_http_test.go` a `http_egress_credentials_test.go`.
(Dřívější znění tohoto odstavce tvrdilo „skutečné egress brány“; to byla moje
chyba, na kterou upozornilo review PR #2494.)

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

```text
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

```text
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

### P6 — Druhé kolo: browser, dlouhý journal a publikační průchod

Doplněno poté, co první verze tohoto protokolu označila tyto scénáře za
NEOVĚŘENO.

**Dva skutečné browser kontexty nad jedním draftem.** Dvě nezávislé
`browserContext` instance, obě přihlášené, obě v editoru téže rutiny. A napsal
„EDITOR A WAS HERE“ a uložil; B napsal „EDITOR B WAS HERE“ a uložil na téže
revizi. B dostal viditelně:

```text
Save failed — routine draft changed or its published recipe changed;
reload and review before publishing
```

a **jeho text zůstal v editoru**. To je ta část řádku 5, kterou serverový
409 dokázat nemohl. Screenshot `B-editor-B-after-conflict.png`.

**Selhání načtení, s Retry a zachovaným kontextem.** Nejdřív bylo změřeno,
které zdroje detail běhu skutečně volá (jinak by „injektoval jsem 500 a nic
se nezměnilo“ nešlo odlišit od „pohled se na to nikdy neptal“). Volá run,
`executions`, `artifacts`, `waitpoints` a `journal/lookup`. Nevolá
`/api/v1/journal` ani `/logs` ani archiv verzí — na těch injekce nic
nedokazuje.

| Injekce | Co UI řekne |
|---|---|
| run 500 | „Could not load this run. **Try again**“ |
| executions 500 | „Recorded step executions could not be loaded, so per-step state is missing below. **Try again**“ — a netvrdí nula kroků |
| artifacts 500 | „Results could not be loaded. **Retry**“ |
| journal/lookup 500 | nic viditelného (dekorace odkazu, ne výsledková plocha) |

Ve všech případech zůstal blok RESULTS s výsledkem běhu vykreslený — chyba se
nezobrazila jako prázdná data. Screenshoty `D-*.png`.

**Výsledek bez completion signal.** Vyrobeno bez agenta: rutina s jediným
`call_pipeline` krokem je „outcome-capable“, dítě vrátí výsledek bez
CHECKPOINT/HANDOFF bloku. Běh `run_cmtwt2dfj000560dd6f13`:
`status=completed`, `outcome=FAILED`, `error_message="no outcome reported"`,
výstup přítomen. V prohlížeči pilulka **„Result failed“**, nadpis **„A result
was recorded, but completion was not confirmed“**, důvod „no outcome
reported“ a RESULTS dál ukazuje `{"result":"a real answer with no handoff
block"}`. Nikde v panelu není falešný úspěch. Screenshot
`E-no-completion-signal.png`.

**Dlouhý journal.** Rutina o 121 krocích (120 transform + jeden, který
skutečně třikrát opakuje), běh `run_cmtwt67ne000fedede3b3`:

- **245 journal záznamů** pro jeden běh (121 `step.started`, 120
  `step.completed`, 2 `step.retrying`, 2 run-level), přečtené průchodem přes
  kurzor po 200; **166 exekucí**, API stránkuje po 100 s `next_cursor`.
- Browser: první zobrazení seznamu kroků **770 ms**, vykresleno **12 ze 121**
  kroků s ovládáním pro rozbalení; po rozbalení 121 kroků, bez horizontálního
  přetečení na 1440 px.
- Klíčové pro poctivost: pod seznamem stojí **„Only the first recorded
  executions were loaded for this run. Later attempts are not shown here.
  Load more executions“** — UI neříká, že vidíte všechno.
- Panel „Run activity“ hlásí 124 událostí s vlastním stránkováním.

770 ms je jedno měření na jednom stroji, ne benchmark.

Pozorování k zaznamenání, nikoli nález: `GET /api/v1/journal?run_id=<run_…>`
vrací pro běh rutiny nula záznamů — ty nesou identitu běhu v `actor_id`, a
`run_id` je sloupec pro běhy agentů. UI tuto cestu nepoužívá (čte
`/pipelines/{slug}/runs` a `/journal/lookup`). Zamýšlenou sémantiku toho
sloupce jsem neověřoval.

**Publikační průchod a oprava vlastního harnessu.** První verze protokolu
uvedla, že živý Edit → Publish s nekompatibilním presetem se nepodařil kvůli
`save_token`. To byla chyba harnessu, ne produktu, a je opravená.
`DefinitionHash` je prosté SHA-256 nad *přesnými bajty* definice; draft je
ukládá verbatim (`json.RawMessage`). Můj skript definici mezitím
přeserializoval v Pythonu, takže podepsal jiné bajty. Po vložení jednoho
literálu do obou těl beze změny:

```text
POST .../pipelines/drafts     → 200   (uložené bajty == můj literál, ověřeno SHA-256)
POST .../pipelines/test_run   → 200   DRY_RUN_OK, save_token vydán
POST .../pipelines/acc-schema2/publish → 409
  {"error":"publication blocked by schedule nightly-who2: Input recipient is required by the draft",
   "schedule_conflict":{"schedule_id":"psched_cmtwt3yna0001cd0e0145","name":"nightly-who2",
                        "reason":"Input recipient is required by the draft"}}
```

Draft zůstal (revize 1), živý recept zůstal na `['who']`, head_version 1.
Po opravě presetu plánu (`PATCH … {"recipient":"alice"}` → 200) a novém
tokenu nad týmiž bajty: **publish 201**, živý recept `['recipient']`, draft
spotřebován. Token byl vždy vydán skutečným `/test_run`; žádná serverová
privátní funkce, žádné obejití podpisu.

**Issue takeover versus opožděná odpověď.** Souběžnou variantu pokrývaly
testy; sekvenční ne, a přitom je to ta, která se stává.
`internal/api/inbox_takeover_late_answer_test.go` pokrývá oba pořadí —
takeover a pak odpověď, odpověď a pak takeover — obojí **409**, přijatý
verdikt na kartě beze změny, a žádný komentář ani obnovené přiřazení po
odmítnuté akci. Opakovaná opožděná odpověď dostane stejné odmítnutí a
payload karty nenaroste.

### P7 — Živé ověření obou oprav proti skutečnému serveru

Izolovaná instance na binárce sestavené z **kombinace** obou větví
(`sha256:5ed54d20…`), skutečné HTTP, skutečně serializovaná těla, žádné mocky.

**#2496 — plán se nedá uložit s presetem, který jeho rutina odmítá.** Rutina
`acc-live-gate` s povinným `select` a volitelným `boolean`, oba s widgetem:

```text
POST /pipeline-schedules  inputs {}                                → 400 input "region" is required
                          inputs {"region":"antarctica"}           → 400 input "region": choose one of the available answers
                          inputs {"region":"eu","dry_run":"yes"}   → 400 input "dry_run": expected true or false
                          inputs {"region":"eu","dry_run":false}   → 201
```

Uloženy **jedna** z těch čtyř — `false` je odpověď, ne nepřítomnost.

**#2495 — přímé uložení už plán tiše nerozbije.** Táž rutina, v2 přejmenuje
povinný vstup:

```text
crewship routine save --definition v2   → API error (409): publication blocked by
                                          schedule p-ok: Input zone is required by the new recipe
POST /pipelines/save (totéž tělo)       → 409 {"error": …,
                                            "schedule_conflict":{"schedule_id":"psched_cmtwtyo0n0001a88675d1",
                                                                 "name":"p-ok","reason":"Input zone is required by the new recipe"},
                                            "hint": "Update this plan's inputs in the same save …"}
```

Po odmítnutí: živý recept dál `['region','dry_run']`, preset plánu dál
`{"dry_run":false,"region":"eu"}`. Nic částečně zapsaného.

**Deadlock, který to živé ověření odhalilo, a cesta ven.** První pokus o
nápravu skončil takto:

```text
PATCH plán {"inputs":{"zone":"eu"}}  → 400  (nesplňuje recept, který je stále publikovaný)
POST  /pipelines/save v2             → 409  (uložený preset nesplňuje nový recept)
```

Přejmenování povinného vstupu vyžaduje, aby se recept a plán pohnuly
**společně**, a ani jeden nemohl jít první. Produkt ten atomický krok už měl —
uložení rutiny může nést `trigger` a `upsertTriggerSchedule` vlastní nejvýš
jeden plán na (workspace, rutina) — jen brána běžela dřív, než ho viděla.
Posunuta za `createTriggerTx`:

```text
POST /pipelines/save  {definition: v2, trigger:{cron, inputs:{"zone":"eu"}}}  → 201
   živé vstupy: ['zone']     plán: enabled, {"zone":"eu"}
```

Recept sám dál 409. Trigger nesoucí preset, který **nový** recept odmítá,
také 409 a recept se nepohne — posunutí brány není cesta okolo ní.

Pozn.: formulace důvodu se opravou mění z „is required by the draft“ na
„is required by the new recipe“, protože brána už nefiří jen při publikaci
draftu. Citace „by the draft“ v oddílu P6 pochází z průchodu na binárce před
touto opravou.

## Závěrečná tabulka §9 — všech 16 řádků

Rozsah Release 1.0 podle §11. **PASS znamená „doložené v pojmenovaném
rozsahu“, ne „bez vad“.** Smíšený scénář není celý PASS proto, že prošla
jedna jeho část — sloupec Rozsah říká přesně která.

Vrstvy se v protokolu nezaměňují: *server* = test nebo HTTP proti skutečnému
handleru; *živě* = běžící crewshipd a skutečné run/decision identity;
*browser* = přihlášený Chromium proti nasazenému buildu; *fault injection* =
vynucená chyba v prohlížeči, která dokazuje reakci UI a nikdy ne serverovou
autorizaci; *lidské porozumění* = §11, neodškrtnuto.

Verdikty v tabulce **nestojí na nesloučených opravách N2 a N3**. Řádek 6 je
doložen publikační cestou, která bránu měla už před nimi; řádek 2 vrstvami,
které existovaly předtím. Co ty dvě opravy přidávají, je uzavření nálezů, ne
změna některého PASS.

| # | Scénář §9 | Stav | Rozsah důkazu | Zbývající omezení |
|---|---|---|---|---|
| 1 | Recept bez vstupů, jednoduchý úspěch | PASS | §14 (10. 9.), interní browser walkthrough pěti úloh | Není uživatelská studie; §11 lidská brána otevřená |
| 2 | Typované vstupy, defaulty, neplatné hodnoty | PASS | Server: `ValidateFormInputs` v handleru i v exekutoru, `TestPresetValidation_RunPathAlreadyRejects`. UI: jediná sdílená `InputsForm` pro ruční start i plán. Živě: `run_cmtvp6yn4001555f2552a` (§14) | **Vstup s `type` bez `widget` se netypuje nikde** — jedno sdílené pravidlo (`hasInputForm`), většina existujících rutin má tento tvar. Viz N3 |
| 3 | Dvojklik / opakovaný request | PASS | Živě: dva požadavky se stejným idempotency klíčem → jediný `run_cmtvp9dkr001eaf494dda` (§14) | Není to přejímka všech gest v UI |
| 4 | Edit během běhu, publish během čekání ve frontě | **PASS pro běžící a čekající, FAIL pro zařazený** | Živě 11. 9.: v2 publikována uprostřed běhu `run_cmtwp7vt700033e66cfc6`, běh doběhl na v1 (`afa10a995ec7`), v1 znění účinku, krok `v2only` neproveden. **Ale** `--delay` běh (`pnd_cmtwvbad3000108716b4d`, 202 SCHEDULED, `pinned_version:null`) po publikaci v2 odpálil **v2** — `run_cmtwvcb6c00052baa2423`, `pipeline_version 2`, výstup `{"ran":"v2"}` | Nález **N4**, oprava v [PR #2501](https://github.com/crewship-ai/crewship/pull/2501), **nesloučeno**. Doslovné znění §9 („publish během čekání ve frontě“) tento tvar pojmenovává, takže řádek není celý PASS, dokud oprava nepřistane |
| 5 | Dva editoři | PASS | Server: 200/409 nad stejnou revizí. **Browser 11. 9.: dva skutečné kontexty**, B dostal „Save failed — routine draft changed…“ a **podržel si svůj text** | — |
| 6 | Změna schématu s existujícími plány | PASS | Živě 11. 9. celý průchod Edit → Test → Publish: 409 `schedule_conflict` (`psched_cmtwt3yna0001cd0e0145`, „Input recipient is required by the draft“), draft zachován, živý recept nezměněn → oprava presetu → publish 201. Testy na obou vrstvách | Brána platí pro **povolené a nepřipnuté** plány; vypnuté a připnuté jsou vyňaté záměrně |
| 7 | Větev neprovedena, foreach, více pokusů | PASS | Živě `run_cmtwqaand001db3acdea1`: skipped s důvodem „Condition was false“; foreach položky `/fan/items/N/each att=1`; skutečné pokusy `/flaky att=1..3` — rozlišené cestou, ne počtem | — |
| 8 | HTTP chyba po možném externím zápisu | PASS na měřitelné vrstvě | `uncertain_external_effect_test.go` s recorderem: účinek 1×, běh FAILED, důvod zachován, následující krok 0× | **Živě nelze**: SSRF ochrana odmítá každou dosažitelnou adresu. Egress brány v tom rigu nejsou zapojené |
| 9 | Restart u waitpointu a rozpracovaného kroku | PASS | Živě: dva `kill -9` nad `run_cmtwp7vt700033e66cfc6`; obnovené rozhodnutí na témže tokenu; `hold att=1 interrupted → att=2 completed`. Počitatelné at-least-once (1× vs 2×) v `…_ResumeReappliesTheInFlightStep` | **Exactly-once se netvrdí.** Počitatelný důkaz dvojího účinku je testový, ne živý — viz řádek 8 |
| 10 | Dvě rozhodnutí / timeout / Issue takeover | PASS | Živě: timeout → 409 na opožděnou odpověď, stav nezměněn; čtyři souběhy, vždy jedno 200 a jedno 409, výsledek následoval verdikt. Server: `inbox_takeover_late_answer_test.go` — takeover→opožděná odpověď a odpověď→opožděný takeover, obojí 409 bez vedlejších účinků | Takeover × opožděná odpověď je doložen na serverové vrstvě, ne živým průchodem UI |
| 11 | Výsledek existuje, chybí completion signal | PASS | Živě `run_cmtwt2dfj000560dd6f13`: `status=completed`, `outcome=FAILED`, `error="no outcome reported"`, výstup přítomen. **Browser:** pilulka „Result failed“, nadpis „A result was recorded, but completion was not confirmed“, RESULTS dál zobrazen | Vyrobeno `call_pipeline` krokem (token-zero); s agentem nevyzkoušeno |
| 12 | Načtení outputs/journalu selže, archiv chybí | PASS částečně | **Browser fault injection:** run 500 → „Could not load this run. Try again“; executions 500 → „Recorded step executions could not be loaded… Try again“ a netvrdí nula kroků; artifacts 500 → „Results could not be loaded. Retry“. **Server:** 404 pro neexistující běh i verzi, žádný fallback na současný recept (v1 a v2 se prokazatelně liší) | `/journal/lookup` selže **beze stopy v UI** (dekorace odkazu, ne výsledková plocha). Stránka Journal a záložka Versions nebyly fault-injectované |
| 13 | Jednorázový start + recurrence + DST | PASS | Projekce (API + browser, 10.–11. 9.) **a nově skutečná dispatch cesta**: `schedules_dst_dispatch_test.go` — 25. 10. 2026 dvě odpálení (00:30Z, 01:30Z), 28. 3. 2027 žádné a due bar postoupí; hodinový kontrolní vzorek | Řízené hodiny v testu; hostitelský čas se nikdy neměnil |
| 14 | Neoprávněný uživatel, cizí soubor | PASS | Živě, úplná matice se **skutečným druhým uživatelem**: 401 anonym, 403 čtení i všechny mutace, 404 při záměně workspace id, 403/400 na stažení souboru a path traversal; waitpoint zůstal pending pro vlastníka | Serverová vrstva; browser by ji nenahradil |
| 15 | Klávesnice, úzký displej, reduced motion | PASS v rozsahu §9 | Browser: 390 px bez horizontálního přetečení (`scrollWidth == 390`), `/` fokusuje hledání (desktop), Escape čistí a nechá fokus v poli, Tab dosáhne akčních prvků s viditelným fokusem, reduced-motion → nula běžících animací | **Není to certifikace přístupnosti.** `/` na 390 px nefokusuje — vstup není vykreslen ve sbalené liště |
| 16 | 100 kroků, 1 000 pokusů, dlouhý journal | PASS | 10. 9.: 100 kroků, 1 100 exekucí, 11 stran. 11. 9. **dlouhý journal**: 245 journal záznamů a 166 exekucí pro jeden běh; API stránkuje kurzorem; browser vykreslí 12 ze 121 kroků a **řekne** „Only the first recorded executions were loaded… Load more executions“; první zobrazení 770 ms | 770 ms je jedno měření, ne benchmark. „1 000 pokusů“ je doloženo jako 1 100 foreach exekucí, ne jako 1 000 retry pokusů |

### Co zůstává NEOVĚŘENO

| Položka | Proč |
|---|---|
| Issue takeover × opožděná odpověď **živým průchodem UI** | Serverová vrstva doložena testy |
| Selhání načtení journalu na stránce Journal a archivu na záložce Versions | Run detail tyto zdroje nevolá; ověřeno, které volá |
| `/journal/lookup` selže tiše | Pozorováno, nezměřen dopad; dekorace odkazu |
| Úplný accessibility audit | Mimo rozsah §9 |
| Retry pokusy v řádu tisíců | Doloženy 3 skutečné pokusy a 1 100 foreach exekucí |

## Nálezy

### N1 — Zrušený běh se zapisoval jako selhaný · OPRAVENO

Klasifikace zrušení se ptala výhradně `RunRegistry`, tedy tlačítka Cancel.
Každá jiná cesta, kterou kontext běhu skončí, nechala nálepku `FAILED`,
kterou napsala kroková smyčka — a ty ostatní cesty jsou ty běžné: výchozí
`POST …/run` předává exekutoru kontext HTTP požadavku, takže zavřená karta,
timeout proxy, deadline CLI i řádné vypnutí končí běh kontextem, který
registry nikdy neviděl.

Reprodukce na izolované instanci (před opravou):

```text
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

### N2 — Kontrola presetů se obchází přes `routine save` · OPRAVA V REVIEW

Brána kompatibility plánů žije v `consumeDraftTx`, tedy výhradně na cestě
draft → publish, a jen pro **povolené** a **nepřipnuté** plány. Přímé
`crewship routine save` (dveře CLI a agentů) změní schéma pod plánem bez
varování:

```text
plán "nightly-who" (enabled, nepřipnutý), preset {"who":"alice"}
crewship routine save …  # v2 přejmenuje povinný vstup who → recipient
→ Saved routine acc-schema (hash=eb481348ceeb)
plán po změně: stále enabled, stále {"who":"alice"}
HEAD vstupy: ["recipient"]
```

§9 to zakazuje slovy „žádný tichý rozbitý plán“. Oprava přesouvá bránu z
větve `Publication != nil` do `Store.save`, kudy prochází **každá** cesta
měnící aktivní recept — publish, přímé uložení, agentní/interní uložení,
import i manifest apply. Vyloučení zůstávají a jsou připnutá testy: vypnutý
plán, připnutý plán, legacy netypovaný vstup a uložení, které definici nemění.
Konflikt se nově mapuje na 409 se strukturovaným `schedule_conflict` i na
importní a agentní dveře — tam by dosud i plně akční odmítnutí skončilo jako
500. Issue [#2495](https://github.com/crewship-ai/crewship/issues/2495),
oprava v [PR #2497](https://github.com/crewship-ai/crewship/pull/2497).

**Stav: nesloučeno.** Dokud ten PR není v `main` se zelenou požadovanou CI,
je tento řádek doložená oprava v review, ne uzavřená vada.

### N3 — Plán lze uložit s presetem, který jeho rutina odmítá · OPRAVA V REVIEW

**Oprava původní formulace.** Toto zjištění jsem nejdřív zapsal jako „server
nevaliduje typované vstupy běhu“. To je nesprávné. `pipeline.ValidateFormInputs`
existuje a je na cestě běhu zapojený dvakrát: `PipelineHandler.Run` ho volá před
dispatchem a exekutor znovu před prvním krokem; stejnou funkci používají
rozhodovací formuláře i vazba vstupů vnořené rutiny.

Moje původní sonda prošla proto, že rozhoduje `hasInputForm`:

```go
func hasInputForm(in InputSpec) bool { return in.Widget != "" || len(in.Options) > 0 || in.AllowCustom }
```

Vstup nese formulářový kontrakt jen když deklaruje `widget`, `options` nebo
`allow_custom`. Sonda deklarovala `type` a `required`, ale žádný `widget`, takže
všechny její vstupy byly podle tohoto pravidla legacy a všechny kontroly se
přeskočily. Naměřená matice je pravdivá, její vysvětlení nebylo.

**Co je skutečně vada:** preset plánu se ověřuje jen tehdy, když se později
změní *rutina* (brána z N2). Nic ho neověřuje ve chvíli, kdy se zakládá nebo
edituje **samotný plán**. Plán tak může vzniknout s presetem, který jeho cílová
rutina už teď odmítá, sedět v kalendáři jako zdravý a selhat poprvé ve 02:30 —
bez run řádku k prohlédnutí, protože exekutor odmítne dřív, než nějaký vznikne.

Ověřeno proti rutině, jejíž vstupy formulářový kontrakt **nesou**:

| `POST /pipeline-schedules`, inputs | před | po |
|---|---|---|
| `{}` — chybí povinný `region` (select) | **201** | 400 s názvem `region` |
| `{"region":null}` | **201** | 400 |
| `{"region":"antarctica"}` — mimo `options` | **201** | 400 |
| `{"region":"eu","dry_run":"yes"}` — string za boolean | **201** | 400 |
| `{"retries":9}` — nad deklarovaným `max` | **201** | 400 |
| `PATCH` na `{"region":"antarctica"}` | **200** | 400, uložený preset beze změny |

Beze změny a připnuto testy: `{"region":"eu"}`, `dry_run:false`, `retries:0`,
neznámý vstup navíc i legacy netypovaná rutina s prázdným presetem se ukládají
dál. Oprava volá `pipeline.ValidateFormInputs` — tedy tutéž funkci jako cesta
běhu, ne druhý názor na to, co je platný vstup.

**Vědomě mimo rozsah:** `type` bez `widget` se netypuje nikde. Je to jedno
sdílené pravidlo napříč branou běhu, rozhodovacími formuláři, vazbou vnořených
vstupů i presetovou branou z N2, a takový tvar má většina existujících rutin.
Zpřísnění je samostatné rozhodnutí o legacy kontraktu, ne něco, co by měla
propašovat oprava validace presetů.

Oprava je v [PR #2498](https://github.com/crewship-ai/crewship/pull/2498).
**Stav: nesloučeno.** Dokud ten PR není v `main` se zelenou požadovanou CI,
je tento řádek doložená oprava v review, ne uzavřená vada.

### N4 — Zařazený běh není připnutý, takže publikace změní, co odpálí · OPRAVA V REVIEW

`POST …/run` s `delay_seconds` zaparkuje spouštěč v `pending_runs` a odpoví
`202 SCHEDULED` s handlem — běh je přijatý. Nebyl ale připnutý, takže
publikace během čekání změnila, co se spustí.

```text
POST …/run {"inputs":{},"delay_seconds":45}
→ 202 {"pending_id":"pnd_cmtwvbad3000108716b4d","status":"SCHEDULED","pinned_version":null}

crewship routine save --definition v2        # publikováno, zatímco běh čeká

o 45 s: run_cmtwvcb6c00052baa2423  pipeline_version=2  hash=b9ad38ef6dc0  output={"ran":"v2"}
```

Připnutí v `enqueueDeferredRun` bylo podmíněné `body.FireAt != ""`, tedy
tvarem jednorázového plánovaného startu. `delay_seconds` i debounce jdou
toutéž cestou, projdou toutéž preflight kontrolou a dostanou účtenku;
komentář u té větve — „pin the definition that passed preflight, not a
concurrently changed HEAD“ — nikdy neříkal, kterým tvarem odkladu.

Druhá polovina §9 řádku 4 je v pořádku a zůstává doložená: běh, který už
`waiting` nebo `running` je, si snapshot podrží — `run_cmtwp7vt700033e66cfc6`
přežil publikaci v2 i dva `kill -9` a doběhl na v1.

Issue [#2500](https://github.com/crewship-ai/crewship/issues/2500), oprava v
[PR #2501](https://github.com/crewship-ai/crewship/pull/2501).
**Stav: nesloučeno.**

Živě ověřeno na binárce s opravou (`sha256:4eb6625c…`), týž scénář, skutečná
těla:

```text
POST …/run {"inputs":{},"delay_seconds":45}
→ 202 {"pending_id":"pnd_cmtww3usu0001426ab256","status":"SCHEDULED","pinned_version":3}

crewship routine save --definition v4        # publikováno, zatímco běh čeká

o 45 s: run_cmtww4whc0005a29bde22  status=completed  pipeline_version=3  output={"ran":"v3"}
```

Účtenka nese připnutou verzi a běh odpálil recept, proti kterému byl přijatý.

Vědomě beze změny: volající, který `pinned_version` uvede, dostane svůj;
jednorázový start si nechává i své 409; a rutina bez archivované verze se dá
odložit dál — poběží nepřipnutá a účtenka to řekne, místo aby o běh přišla.
Plány a webhooky zůstávají opačně: plán je stálý pokyn, odložený běh je jedno
spuštění, které už člověk udělal.

## Úklid

Izolovaná instance i recorder zastaveny, jejich data zůstávají mimo repozitář
ve scratchpadu relace a v soukromé záloze
`/srv/crewship/backups/crewship_1/acceptance-20260911/`.

Na dev1 byly založeny **dvě** dočasné rutiny — `zz-acceptance-cancel-probe`
(ověření opravy N1 v prohlížeči) a `zz-acceptance-ui-probe` (browser scénáře
řádků 5, 12 a 15) — obě po přejímce smazané, spolu s běhy, které k nim
patřily. Žádná původní rutina, žádný cizí běh a žádný plán na dev1 nebyl
změněn a databáze nebyla nulována. Pět testovacích plánů na izolované
instanci bylo smazáno hned po měření. Pomocné Playwright skripty žily v
`e2e/` jen po dobu běhu a do repozitáře se necommitovaly.

Práce na opravách probíhala ve vlastních worktree
(`.claude/worktrees/wt-2495`, `wt-2496`, `wt-2500`), aby hlavní klon zůstal
volný pro nasazení; po sloučení se odstraní. Hlavní klon byl přepnut z větve
`dev1/routines-legibility-20260910` na větev této přejímky — obě vycházejí
z téhož commitu a jediný rozdíl jsou commity odsud.

Poznámka k hostiteli, ne k produktu: disk crewship-dev byl během relace na
99 %. Uvolnil jsem, co je bezpečné (`docker builder prune`, dangling images;
431 MB). Sdílenou Go cache (50 GB) a 35 obrazů `crewship-cache:*` /
`crewship-feat:*` jsem **nemazal** — na cache v každý okamžik staví jiné
relace, a „orphan“ verdikt u obrazů pocházel z throwaway instance, která
nevlastní žádnou partu, takže nic neznamená.
