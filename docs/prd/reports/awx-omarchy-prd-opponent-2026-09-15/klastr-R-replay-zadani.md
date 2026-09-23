# Klastr R: A4 (volba verze při opakování běhu) + O5/A5 (tři zadání pro jednu roli)

## 0. Ověření prostředí

Branch `main`, HEAD `5a0ad11f` (= origin/main), `git status` jen untracked docs/prd cizího WIP; nic tracked jsem neměnil, dočasný test `internal/api/zz_opp_R_test.go` smazán po běhu (zdroj + výstup v §7). Větve PR jsem jen fetchnul do `refs/remotes/origin/feat/routines-operator-console` (9b5e1388) a `origin/feat/routines-clarity-release-1` (1cefe100); #2556 je předkem #2562 (stack).

## 1. Verdikt per funkce

**A4 — upravit (ne implementovat znovu).** Na HEAD už existuje „Run again“ s volbou *Current recipe* / *Executed recipe · vN* a prefill uložených vstupů (`routine-run-detail.tsx:375-383, 547-571`), ale volba „Current“ nic nepřipíná: mezi otevřením dialogu a submitem tiše poběží jiná verze, `expected_*` v API neexistuje (sonda §7 T1). PRD se navíc mýlí v předpokladu, že UI staví na `/replay` — UI používá `/run` s `pinned_version`; `/replay` je jen CLI/backtest cesta a nemá capability `routine.run`. Minimální verze: UI pošle pin (nebo backend `expected_definition_hash`), label s číslem aktuální verze z čerstvého fetche, jeden Go test na posun HEAD, jeden Vitest. UI část koliduje s #2562 (oba soubory tam přepsané), backend část ne.

**O5/A5 — odložit jako kód, připravit jako obsah.** Všechny nosné mechanismy existují: ask_forms per agent (`internal/askforms`), suggested_prompts, vstupní formulář rutin se serverovou validací, seedovací balíčky rutin (`cmd/crewship/seeddata/packs`, `routines_packs.go`) a 422 s `missing_credentials`/`missing_integrations` při odmítnutí. Chybí datový zdroj „objednávek“ (žádný konektor; `query` step umí jen `pipeline_runs`) a kontrola připravenosti *před* spuštěním (jen při odmítnutí). Co je v O5 kód: (a) volitelný preflight endpoint (nová schopnost, 4 gate-y), (b) nic dalšího. Zbytek je YAML/JSON obsah + seed verify. STARTER_TEMPLATES v editoru mizí s #2562, takže obsah patří na server (seeddata), ne do FE.

## 2. Tabulka funkcí

| ID | Současný stav (HEAD) | Souběžné PR | Doporučený rozsah (min. verze) | Závislosti | Odhad | Důkaz v kódu (soubor:řádek) |
|---|---|---|---|---|---|---|
| A4 | Run again = `POST /pipelines/{slug}/run` s `inputs` + volitelným `pinned_version`; volba „Current recipe“ pin neposílá; run detail odvozuje `pipeline_version` z hashe; `/replay` (+ CLI `routine replay --version`) je oddělená cesta bez `routine.run` capability a bez `invoking_user_id` | #2556 (MERGEABLE): `initialInputs` oddělené od defaultů, hint „Changed from the recipe default“. #2562 (CONFLICTING, stack na #2556): dialog dostane `headVersion`/`draft` → text „Uses vN — draft rM is not used“, chip „Version vN“; ale `headVersion` je ze stavu načteného při otevření stránky, ne z fetche v `prepareAgain` → může být zastaralý | (1) backend: pole `expected_definition_hash` v `runRequestBody` → 409 při neshodě (1 compare); (2) UI: `prepareAgain("current")` si uloží `head_version`+`definition_hash` z čerstvé odpovědi, pošle pin/hash, label „Current recipe (v6)“; (3) 404 „recipe version not found“ přeložit na čitelnou hlášku; (4) CLI `routine run --version` (parita s API, které pin už přijímá); (5) testy | UI část až po merge #2562 (nebo na jeho větvi); backend + CLI nezávisle hned | 1,5–2,5 čd (backend 0,5 vč. gen-openapi + docs + CLI flag + docs/cli tabulka + CHANGELOG; UI 0,5–1; testy 0,5) | `internal/api/pipelines_exec.go:32,139-158`; `routine-run-detail.tsx:182-189,220-243,547-571`; `routine-run-inputs-dialog.tsx:88-110`; `internal/api/pipeline_runs_replay.go:46-112,115`; `internal/api/pipeline_run_definition.go:18-32`; `internal/pipeline/executor.go:724,756-768,2543-2546`; `cmd/crewship/cmd_pipeline.go:1147-1162` (bez `--version`); `cmd/crewship/cmd_routine_replay.go:18-70` |
| O5/A5 | Mechanismy hotové: ask_forms (4 formuláře × 6 polí, validace při uložení), suggested_prompts s role-packy, ChatEmptyState „Try it“ ze skills, routine inputs form + `ValidateFormInputs` na serveru, seed knihovna rutin + packs (script+agent+notify+Page), 422 `missing_credentials`/`missing_integrations` s toastem „Manage credentials“. Chybí: datový konektor objednávek, readiness před spuštěním, konkrétní obsah | #2562 maže `routine-create-dialog.tsx` vč. `STARTER_TEMPLATES` a jejich testu; #2556 přidává `format`, hinty a chybové souhrny do formuláře vstupů | Obsahový pack `seeddata/packs/<role>` = 2 rutiny (deterministický script/http + transform + Page panel; volitelný agent summary) + 1 ask form + `suggested_prompts` pro jednoho agenta; parametrizace `orders_source_url` + `credentials_required: [{type: api_key, scope: orders}]`; `crewship seed verify` průchod | Rozhodnutí role a datového zdroje (§6); #2562 kvůli zmizení FE šablon; #2556 kvůli formátu vstupů | obsah 1–1,5 čd; E2E průchod 0,5–1 čd; preflight endpoint (volitelně) +1 čd | `internal/askforms/forms.go:38-58,93-148`; `internal/api/ask_forms.go:34`; `lib/agent-suggestions.ts:11-56,88-99`; `chat-empty-state.tsx:29-32,48-51,108-113`; `chat-panel.tsx:147-149,928`; `internal/pipeline/dsl_validate_inputs.go:83-100`; `cmd/crewship/seeddata/routines_packs.go:22-60`; `seeddata/askforms/casey.json`; `internal/api/pipeline_credentials_gate.go:35-58`; `routines-detail-panel.tsx:270-320`; `routine-card-detail.tsx:677-730`; `internal/pipeline/runner_query.go:34`; `internal/pipeline/types.go:469-496` |

## 3. Nálezy podle závažnosti

### Vysoké

**V1 [POTVRZENO] Volba „Current recipe“ neslibuje nic — po posunu HEAD tiše poběží nová verze; neznámé pole v těle se ignoruje.**
`routine-run-detail.tsx:182-189` posílá `pinned_version` jen pro ne-„current“; `pipelines_exec.go` nemá `expected_*`/If-Match; executor si pipeline znovu načte (`executor.go:724`). Sonda T1 (§7): seed head=v2, „kolega“ publikuje v3, `POST /run {"inputs":{},"expected_head_version":2}` → 200, `pipeline_version=<nil>`, výstupy `v3step` — tj. přesně hazard z PRD §9. Sonda T2: `pinned_version:2` po posunu HEAD → běží v2, řádek má `pipeline_version=2` — připnutí na zobrazenou verzi funguje bez změny backendu. Precedens už v repu: deferred start pinuje „definition that passed preflight, not a concurrently changed HEAD“ (`pipeline_deferred.go:85-107`, #2500); okamžitý `/run` tento princip nemá. Dopad na PRD: akceptace „změna HEAD je pokryta testem“ dnes nemá červený test; #2562 to nezavírá (jen zobrazuje `headVersion` ze stavu při otevření stránky, `routine-run-detail.tsx@2562:437`).

**V2 [POTVRZENO] Dvě autorizační cesty pro „opakovat“: `/run` zná `routine.run` capability, `/replay` ne; UI nabízí „Run again“ podle role, ne podle capability.**
`pipelines_exec.go:88-97` (`requireRoleOrCapabilityOrForbid`), `pipeline_runs_replay.go:115` (`requireRole "create"` = MANAGER+, `helpers.go:541-553`), `routine-run-detail.tsx:375` (`roleAtLeast(role,"MEMBER")`). Sonda T6: MEMBER bez grantu → 403 na `/run` i `/replay`. Dopad: delegovaný operátor (A1 „Martička“) vidí tlačítko, vyplní formulář a dostane „Forbidden“; kdyby A4 přesunulo UI na `/replay` (jak PRD §9 předpokládá), operátor s capability by opakovat nemohl vůbec. Stejný vzor má `pages_actions.go:503` (jen role) — předat klastru A1.

**V3 [POTVRZENO] Uložené vstupy jsou archivovány a vraceny doslova; typ „secret“ pro vstup rutiny neexistuje.**
`dsl_validate_inputs.go:30-45` povoluje widgety text/select/multiselect/boolean/number/textarea, jiné = „unknown widget“; `pipeline_runs.go:268` vrací `inputs` každému, kdo smí číst běh. Sonda T4: `inputs.password="hunter2"` → GET run detail jako VIEWER vrátí `password:hunter2`. Do URL se nic nepřenáší (`router.push` jen `slug`+`run`, `routine-run-detail.tsx:207-209`). Dopad na PRD §9 „tajemství se nepřenášejí … jako archivní vstupy“: splnitelné jen prohlášením „vstupy rutin nejsou místem pro tajemství“ (a případně lint na název pole `password|token|secret` v definici), ne kódem replaye.

### Střední

**S1 [POTVRZENO] `/replay` nezaznamená, kdo opakoval.** `pipeline_runs_replay.go:79-91`: `TriggeredByID: runID`, `InvokingUserID` se nenastaví (na rozdíl od `/run`, `pipelines_exec.go:253`). Sonda T5: `invoking_user_id=""`, `triggered_via=manual`, `triggered_by_id=<zdrojový run>`. `notify to: trigger` pak padá na workspace-wide; A3 „iniciátor“ pro replay = „nezaznamenáno“. Oprava = 3 řádky, pokud se `/replay` má vůbec používat z UI.

**S2 [POTVRZENO] Metadata a vstupy se při replayi kopírují bez validace vůči cílové verzi nad rámec `ValidateFormInputs`.** `pipeline_runs_replay.go:70-72,87`: `orig.MetadataJSON` a `inputs` jdou beze změny; executor validuje jen deklarované formuláře (`executor.go:774-778`). Při pinu na starší verzi s jinými vstupy projdou navíc pole tiše. Nízké riziko, ale PRD „vstupní validace“ = jen to, co je deklarované.

**S3 [POTVRZENO] `/replay` a `bulk_replay` jsou synchronní (drží spojení po celý běh, `pipeline_runs_replay.go:78-91,215`), bez `Prefer: respond-async` (jen `/run`, `pipelines_exec.go:308`) a bez Idempotency-Key.** CLI `routine replay` čeká 10 min (`evalRunTimeout`). Dvojí odeslání = dva běhy. UI „Run again“ na `/run` má idempotency (test `routine-run-detail-claims.test.tsx:192-233`). Závěr: A4 má zůstat na `/run`; PRD větu „zachovat replay_of“ zúžit (viz §4).

**S4 [POTVRZENO] CLI parita: `/run` přijímá `pinned_version`, `crewship routine run` nemá `--version`** (`cmd_pipeline.go:1147-1162`), a `docs/api-reference/pipelines.mdx:154-170` pole neuvádí (jen `openapi.gen.json`). Porušuje „každý endpoint má CLI příkaz“ ve smyslu flagů; A4 by to mělo dorovnat (docs-inventory chce řádek v docs/cli tabulce).

**S5 [POTVRZENO] Komentář `RunRecord.PipelineVersion` tvrdí „mirrors pipelines.head_version at insert time“ (`runs.go:83`), skutečně se ukládá jen explicitní pin (`executor.go:2546`); u HEAD běhů je NULL a číslo verze se dopočítává z hashe (`pipeline_run_definition.go:18-22`).** Důsledek pro A4: „Původní běh v4“ je dostupné jen pokud hash odpovídá řádku v `pipeline_versions`; u běhů před v114 UI správně říká „recipe version unavailable“ (`routine-run-detail.tsx:410`) a volba „Executed recipe“ se nenabídne. Join podle hashe není omezen na jeden řádek (`QueryRow` bere první) — u duplicitních hashů před #996 nedeterministické číslo.

**S6 [POTVRZENO] Readiness „před spuštěním“ pro rutinu neexistuje; existuje jen odmítnutí 422.** `pipeline_credentials_gate.go:35-58` (+ sourozenci integrations/resources) vrací Problem Details s `missing_credentials`; `routines-detail-panel.tsx:270-320` to překládá na toast s „Manage credentials“. Access karta ukazuje požadavky, ne stav („accounts are resolved when the run starts“, `routine-card-detail.tsx:706-712`). `GET /crews/{id}/credential-readiness` je o nástrojích v kontejneru, ne o vaultu. Cesta „Run again“ v run detailu 422 zobrazí jen `detail` text bez odkazu (`routine-run-detail.tsx:203-204`). Pro O5 tedy platí „srozumitelné při odmítnutí“; „před spuštěním“ = nový endpoint.

**S7 [POTVRZENO] Pro „objednávky“ není žádný datový konektor.** Integrace v seedu jen Linear/GitHub MCP (`seeddata/builtin/integrations.yaml`), `query` step jen `pipeline_runs` (`runner_query.go:34`), obecné: `http` step s `credential_ref` bearer/header/query (`types.go:469-496`) a `script` step nad crew share. O5 zadání 1 a 2 tedy vyžadují buď HTTP API zákazníka + `api_key` credential, nebo export souboru na share. Bez tohoto rozhodnutí lze připravit jen parametrizovaný pack.

### Nízké

**N1 [POTVRZENO] Nález z 12. 9. (rollback/re-enable obcházejí preset gate) je na HEAD opraven.** `versions.go:236-262` dělá `checkSchedulePresetsTx` v téže transakci; `TestSchedulePresetGate_RollbackDoor` a `TestPresetValidation_ReenableRequiresCompatiblePreset` PASS (§7). V #2562 web rollback mizí a nahrazuje ho „Restore as draft“ přes drafts API (`routine-versions-tab.tsx@2562`), takže UI-cesta pro rollback z A4 pohledu odpadá.

**N2 [POTVRZENO] Replay běžícího/čekajícího runu API nebrání** (`pipeline_runs_replay.go:50-60` kontroluje jen existenci a workspace); UI tlačítko skrývá (`!active`). Schvalování: nový běh vytvoří nový waitpoint (žádná vazba na `is_replay`, `runner_wait*.go` bez `IsReplay`), původní approval zůstává — odpovídá PRD „existující schvalování zůstává platné“, ale PRD by mělo říct výslovně „schválení se vyžádá znovu“.

**N3 [POTVRZENO] Chyba nedostupné verze v UI je jednotná „The selected recipe version is unavailable.“** (`routine-run-detail.tsx:228`) pro 404/403/500/síť; backend rozlišuje: `/run` 404 „recipe version not found“, `/replay` 409 s názvem rutiny a číslem verze (sonda T3, žádný run se nevytvoří), `GET /versions/{n}` 404 „version not found“ (`pipelines_crud.go:472`).

**N4 [POTVRZENO] Page action spouští rutinu jako pending run bez pinu** (`pages_actions.go:410-430`, `pending_runs.go:16` „nil is the legacy live-at-dispatch policy“) — u scénáře „Page → spustit rutinu“ z PRD §1 neplatí ani „published at start“, které má deferred `/run`. Předat O1/A1.

**N5 [NÁVRHOVÉ RIZIKO] `head_version` v detailu rutiny je sloupec `pipelines.head_version`** (`pipeline_recorded_state.go:19,41-43`), zatímco deferred cesta pinuje podle hashe kvůli „pre-#996 rows where a dedup'd save left head_version stale“. Pokud UI připne „Current“ na `head_version`, u takové legacy rutiny by běžela starší definice než živá. Proto doporučuji backend `expected_definition_hash` (nebo pin po hashi na serveru), ne čistě UI pin.

**N6 [NEOVĚŘENO] ChatEmptyState „Try it“ nemá vazbu na `suggested_prompts` ani `ask_forms`** — bere jen skills (`chat-empty-state.tsx:48-51`); rail s formuláři a otázkami je v `chat-panel.tsx:928`. Zda uživatel v prázdném chatu uvidí formuláře před prvním odesláním, jsem v prohlížeči neověřil.

## 4. Navržené přesné změny PRD (PRD nepřepisuji)

1. **§9, odst. 2, věta „`internal/api/pipeline_runs_replay.go` již podporuje pinned_version; běžný replay používá HEAD. Rozšířit současné UI, nikoli nový executor. Zachovat replay_of…“** → nahradit: „UI „Run again“ používá `POST /pipelines/{slug}/run` s `pinned_version` (`routine-run-detail.tsx`), nikoli `/replay`; `/replay` zůstává CLI/backtest cestou (MANAGER+, bez capability `routine.run`, bez záznamu iniciátora). A4 rozšiřuje cestu `/run`; `replay_of` se u UI opakování nezaznamenává a není součástí akceptace. Pokud se `/replay` má z UI použít, musí nejdřív získat capability cestu a `invoking_user_id`.“
2. **§9 „použít podporované připnutí/verzovní kontrolu, jinak přiznat nutnost backendového doplnění“** → konkretizovat: „Backend doplní `expected_definition_hash` (volitelné pole `runRequestBody`; 409 při neshodě s `pipelines.definition_hash`). UI pošle hash zobrazené verze; při 409 zobrazí „Rutina byla mezitím publikována jako vN — zkontrolujte vstupy a spusťte znovu“. Připnutí přes `pinned_version = head_version` z UI není dostačující kvůli možnému zastaralému `head_version` (viz deferred cesta #2500).“
3. **§9 akceptace „tajemství se nepřenášejí v URL ani jako archivní vstupy“** → „Vstupy rutin nemají typ secret; jsou archivovány doslova a viditelné každému, kdo smí číst běh. A4 to v dialogu přizná („Uložené vstupy jsou součástí historie běhu“) a nepřidává redakci; pole s názvem odpovídajícím `password|token|secret|key` ve formuláři rutiny odmítne validace definice (rozhodnout, zda do 1.0).“
4. **§9 akceptace doplnit:** „Opakování pod schvalovací branou vyžádá schválení znovu (nový waitpoint); původní rozhodnutí se nepřenáší.“ a „`crewship routine run --version N` dorovná CLI k API.“
5. **§9 „nepřístupná/smazaná verze má čitelnou chybu“** → „UI rozliší 404 (verze neexistuje) od chyby sítě; verze se nemažou (žádné `DELETE FROM pipeline_versions`), „smazaná“ znamená soft-deleted rutina → 404 `pipeline not found`.“
6. **§11 „Využít ask_forms, doporučené otázky, chat-empty-state.tsx a routine-run-inputs-dialog.tsx“** → doplnit: „Obsah žije v `cmd/crewship/seeddata/packs/<role>` (rutiny, skripty, ask_forms JSON, suggested_prompts) a ověřuje se `crewship seed verify`; FE `STARTER_TEMPLATES` odcházejí s #2562 a nejsou nosičem obsahu.“
7. **§11 akceptace „chybějící připojení je srozumitelné před spuštěním nebo při odmítnutí“** → „Při odmítnutí: existující 422 `missing_credentials`/`missing_integrations` (hotovo v detail panelu; doplnit stejný toast do cesty Run again). Před spuštěním: pouze pokud vznikne `GET /pipelines/{slug}/preflight` nad `findMissing*` — nová schopnost, samostatný odhad 1 čd, do 1.0 volitelné.“
8. **§11 „pro jednu vybranou zákaznickou roli … bez tohoto rozhodnutí nepřidávat generické šablony“** → doplnit výchozí technickou přípravu: „Pack je parametrizovaný (`orders_source_url`, `credentials_required: [{type: api_key, scope: orders}]`, `egress_targets`), takže volba role/zdroje mění jen YAML a texty, ne kód.“
9. **§3 řádek „Běhy … replay“** → „Run again (`/run` + pinned_version) a replay (`/replay`, CLI) jsou dvě cesty s odlišnou autorizací; nesjednocovat bez rozhodnutí o capability.“
10. **§14 pořadí** → A4 backend+CLI lze udělat před O2/A3 (nezávislé); A4 UI až po merge #2562.

## 5. Návrh iterací pro tento klastr

### Iterace 6 — A4: verze při opakování je slib, který server drží

- **Vstupy:** #2556 merged (formulář vstupů), #2562 merged nebo rozhodnutí implementovat na jeho větvi; tento report; `TestOppR_*` z §7 jako výchozí červené testy.
- **Rozsah:**
  1. Backend: `expected_definition_hash` v `runRequestBody` (`pipelines_exec.go`), 409 `{"error":"routine was published since you opened this form","head_version":N}` při neshodě; nepinovat implicitně (změnilo by chování webhooků/CLI). Regenerovat `openapi.gen.json`, doplnit `pipelines.mdx` (pole `pinned_version` i `expected_definition_hash` u `/run`), CHANGELOG.
  2. CLI: `crewship routine run --version N` a `--expect-hash` (nebo jen `--version`), řádek v `docs/cli/routine.mdx` tabulce flagů (docs-inventory).
  3. UI (`routine-run-detail.tsx`, `routine-run-inputs-dialog.tsx`): `prepareAgain` si z čerstvého GET uloží `{head_version, definition_hash}`; volby „Current recipe (v6)“ / „Executed recipe · v4“; při „Current“ posílat `expected_definition_hash`; při ne-current `pinned_version`; 409 → zpráva + tlačítko „Načíst znovu“; 404 verze → „Verze vN už není v archivu“; 422 `missing_*` → stejný toast jako v detail panelu.
  4. Text v dialogu: „Původní běh v4 · Spustíte aktuální v6 se stejnými uloženými vstupy. Vstupy jsou součástí historie běhu; nepoužívejte je pro hesla.“
- **Nepatří sem:** přesun UI na `/replay`, capability pro `/replay`, redakce vstupů, preflight endpoint, změna pending/page-action pinování (N4), rollback UI.
- **Testy:** Go `internal/api -run 'TestManualRun_ExpectedHash|TestManualRun_Pinned'` (409 při posunu HEAD, 200 při shodě, pin drží — z §7 T1/T2 udělat trvalé testy); Vitest `routine-run-detail-claims.test.tsx` (nový: HEAD posun → 409 → hláška, žádný `router.push`; label s číslem verze), `routine-run-inputs-dialog.test.tsx`; držet zelené: `TestReplayRun_*`, `TestReplayPreservesPin*`, `TestReplayRejectsCorrupt*`, `TestManualRun_PinnedVersion*`, `TestSchedulePresetGate_RollbackDoor`, `routine-run-detail-claims` „Run again retries an uncertain start with the same key“.
- **Podmínky dokončení:** v4→v6 rozlišeno textem s čísly; posun HEAD = 409 pokrytý Go i Vitest testem; CLI flag zdokumentován; `scripts/docs_audit.sh`/docs-inventory strict zelené; CodeRabbit review skutečně proběhl (ne rate-limited).
- **Handoff:** stav merge #2562; zda `expected_definition_hash` zůstal volitelný; seznam míst, kde se `/run` volá bez hashe (slash command, pages action, inbox retry) a rozhodnutí, že tam pin nepatří.

### Iterace 10a — O5 obsah: pack pro jednu roli (bez E2E)

- **Vstupy:** rozhodnutí §6 (role + zdroj dat); #2562 merged (jinak by se šablony psaly do mizícího editoru).
- **Rozsah:** `cmd/crewship/seeddata/packs/<role>/` se skripty a testy jako `ci-watch`; 2 `RoutineDef` v `routines_packs.go` (po termínu; porovnání týdnů) s `inputs` (widgety podle `dsl_validate_inputs.go`), `credentials_required`, `egress_targets`, Page panel `status.v1`; 1 ask form („návrh odpovědi zákazníkovi“: order_id, zpráva zákazníka, tón, do 6 polí) + 4 `suggested_prompts` pro jednoho agenta; `seed verify` scénář; dokumentace v `docs/guides/routines-cookbook.mdx`.
- **Nepatří sem:** nový konektor, preflight endpoint, LLM v deterministickém sběru, šablony pro všechny agenty.
- **Testy:** `cmd/crewship -run 'Seed.*Routines|Packs|AskForms'`, `internal/askforms` schema testy, `seed_routines_validate_test.go`; skript testy v packu.
- **Podmínky dokončení:** `./dev.sh seed` na izolované instanci vytvoří rutiny+form; běh s chybějícím `api_key` vrátí 422 s `missing_credentials` a UI toast; s credentialem doběhne a Page panel ukáže data.

### Iterace 10b — E2E průchod scénáře PRD §1

- **Vstupy:** 10a, O1 (Pages v paletě), O2/A3 (přehled běhu), A4.
- **Rozsah:** Playwright/`.mjs` průchod: paleta → Page role → formulář → Run (s vN v dialogu) → výsledek/čekání → „Kopírovat podklad“ → Run again s volbou verze; druhý účet s `routine.run` capability (Martička) — spustí, neupraví.
- **Nepatří sem:** obsah, backend.
- **Testy:** nový `e2e/first-agenda-<role>.mjs`; měření z §14 PRD ručně na 3 uživatelích.
- **Podmínky dokončení:** průchod prochází na dev instanci z čerstvého seedu; zápis limitů.

**Rozdělit 10?** Ano — 10a je obsah bez závislosti na O1/O2/A4 a může jít paralelně po rozhodnutí §6; 10b je závěrečná integrace celého balíku a bez O1/O2 nemá smysl. **Závisí A4 na #2562?** UI část ano (oba soubory přepsané, #2562 je CONFLICTING s main, tj. další rebase); backend + CLI + Go testy ne — doporučuji je jako samostatný malý PR hned.

## 6. Rozhodnutí vyžadující produktový vstup

1. **Role a datový zdroj pro O5** (jediné skutečné): kdo je ta jedna zákaznická role a odkud čte objednávky — HTTP API (typ credentialu `api_key`, host do `egress_targets`) nebo soubor na crew share. Technický default bez rozhodnutí: parametrizovaný pack s `orders_source_url` + `api_key` scope `orders`, skript čte JSON pole `[{id, customer, due_at, status}]`; texty v CZ/EN.
2. **Má `/replay` zůstat jen CLI/backtest cestou?** Pokud ano, PRD §9 se přepíše na `/run` (technicky rozhodnuto výše, ale mění text PRD, který uživatel schválil). Pokud ne, `/replay` potřebuje capability `routine.run` + `invoking_user_id` (+0,5 čd) — vazba na A1.
3. **Zákaz „citlivých“ názvů polí ve vstupech rutin** (V3): technicky triviální lint v `validateInputForms`, ale je to produktové pravidlo („vstupy nejsou pro hesla“) a může rozbít existující definice; navrhuji jen text v dialogu do 1.0.

Technicky rozhodnuto mnou: `expected_definition_hash` místo `expected_head_version` (N5); A4 zůstává na `/run`; preflight endpoint mimo minimální verzi; obsah O5 na serveru v seeddata.

## 7. Příloha: dočasné testy a spuštěné příkazy

### Spuštěné příkazy (výběr)
```
go test ./internal/api -count=1 -timeout 8m -v -run 'TestReplayRun_|TestReplayPreservesPin|TestReplayRejectsCorrupt|TestSchedulePresetGate_RollbackDoor|TestPresetValidation_ReenableRequiresCompatiblePreset'
--- PASS: TestReplayPreservesPinWithoutContentLength (6.87s)
--- PASS: TestReplayRejectsCorruptInputsAndAmbiguousRequestBeforeDispatch (1.94s)
--- PASS: TestReplayRun_PinnedVersion_ExecutesPinned_NotHead (0.29s)
--- PASS: TestReplayRun_PinnedVersionMissing_FailsLegibly_NoRunCreated (0.49s)
--- PASS: TestReplayRun_NoPinnedVersion_ExecutesHead_Unchanged (0.69s)
--- PASS: TestPresetValidation_ReenableRequiresCompatiblePreset (0.30s)
--- PASS: TestSchedulePresetGate_RollbackDoor (0.33s)
ok  	github.com/crewship-ai/crewship/internal/api	11.949s

pnpm exec vitest run components/features/routines/__tests__/routine-run-detail-claims.test.tsx components/features/routines/__tests__/routine-run-inputs-dialog.test.tsx
 Test Files  2 passed (2) · Tests  29 passed (29)

gh pr view 2562 --json ... → state OPEN, mergeable CONFLICTING, base main, head feat/routines-operator-console (9b5e1388), 121 files
gh pr view 2556 --json ... → state OPEN, mergeable MERGEABLE, head feat/routines-clarity-release-1 (1cefe100); `git merge-base --is-ancestor` → 2556 je předkem 2562
gh pr diff 2562 → HTTP 406 (diff > 20000 řádků); použit `git diff 0122583b origin/feat/routines-operator-console -- <soubory>`
```

### Dočasný test `internal/api/zz_opp_R_test.go` (smazán; kopie ve scratchpadu `zz_opp_R_test.go.txt`)

První běh s rolí MEMBER: všech 5 sond 403 Forbidden (→ nález V2); poté role MANAGER + přidána sonda T6.

```
go test ./internal/api -count=1 -timeout 8m -v -run 'TestOppR_'
=== RUN   TestOppR_HeadMovesBetweenDialogAndSubmit_UnpinnedRunsNewHeadSilently
    zz_opp_R_test.go:76: unpinned submit after head moved: pipeline_version=<nil> executed v3step=true outputs=map[v3step:v3-out]
--- PASS (4.91s)
=== RUN   TestOppR_PinToDisplayedHead_ExecutesPromisedVersion
    zz_opp_R_test.go:100: pinned submit: pipeline_version=2 outputs=map[v2step:v2-out]
--- PASS (0.20s)
=== RUN   TestOppR_MissingVersion_ErrorShapes
    zz_opp_R_test.go:112: /run pinned_version=9 → 404 {"error":"recipe version not found"}
    zz_opp_R_test.go:117: /replay pinned_version=9 → 409 {"error":"pipeline: pinned routine version no longer exists: routine \"opp-missing\" has no version 9 (was it deleted? update or unpin the trigger)"}
--- PASS (0.11s)
=== RUN   TestOppR_RunDetailEchoesInputsVerbatim
    zz_opp_R_test.go:152: GET run as VIEWER → 200 inputs=map[customer:acme password:hunter2] pipeline_version=2
--- PASS (0.15s)
=== RUN   TestOppR_ReplayDropsInvokingUser
    zz_opp_R_test.go:176: replay: invoking_user_id="" triggered_via="manual" triggered_by_id="run_opp_prov" is_replay=true replay_of="run_opp_prov" pipeline_version=<nil>
--- PASS (0.14s)
=== RUN   TestOppR_MemberWithoutCapability_403OnRunAndReplay
    zz_opp_R_test.go:200: MEMBER /run → 403 {"error":"Forbidden"}; /replay → 403 {"detail":"Forbidden",...,"status":403,"title":"Forbidden"}
--- PASS (0.16s)
PASS
ok  	github.com/crewship-ai/crewship/internal/api	5.859s
```

Zdroj (zkráceně; helpery `runsHandlerRig`, `seedVersionedPipeline` (head=v2, verze 1+2), `seedRunRow`, `stubRunner` jsou existující testovací fixtury v `internal/api`):

```go
const oppV3DSL = `{"dsl_version":"1.0","name":"pin-hook","steps":[{"id":"v3step","type":"transform","transform":{"input":"v3-out","expression":"."}}]}`

func oppPublishV3(t *testing.T, h *PipelineHandler, id string) { // kolega publikuje v3 mezi dialogem a submitem
	h.db.Exec(`INSERT INTO pipeline_versions (id, pipeline_id, version, definition_json, definition_hash, author_type, author_id, created_at) VALUES (?, ?, 3, ?, 'h3', 'user', 'u_other', datetime('now'))`, "plnv_"+id+"_v3", id, oppV3DSL)
	h.db.Exec(`UPDATE pipelines SET head_version=3, definition_json=?, definition_hash='h3' WHERE id=?`, oppV3DSL, id)
}
func oppRun(t *testing.T, h *PipelineHandler, user, ws, slug, body string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(body)), user, ws, "MANAGER")
	req.SetPathValue("slug", slug); rr := httptest.NewRecorder(); h.Run(rr, req); return rr
}
// T1: head v2→v3, body {"inputs":{},"expected_head_version":2} → 200, výstup v3step, pipeline_version nil
// T2: head v2→v3, body {"inputs":{},"pinned_version":2} → 200, výstup v2step, pipeline_version 2
// T3: pinned_version 9 → /run 404, /replay 409, COUNT(pipeline_runs)=1 (žádný nový run)
// T4: /run inputs {"customer":"acme","password":"hunter2"} → GetRun jako VIEWER vrací inputs doslova
// T5: /replay {} → RunRecord.InvokingUserID == "", TriggeredByID == zdrojový run
// T6: role MEMBER bez capability → /run 403 i /replay 403
```
Plný zdroj: `/tmp/claude-1000/-srv-crewship-crewship-3/a0261745-a8be-4057-9c64-1286f3bee302/scratchpad/zz_opp_R_test.go.txt`.
