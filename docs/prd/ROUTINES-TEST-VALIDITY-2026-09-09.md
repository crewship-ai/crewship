# Routines — důkazní hodnota testů, 9. 9. 2026

Evidence k [work orderu](WORK-ORDER-2026-09-09-ROUTINES-FIX-AND-UX.md) §9.
Otázka nezní „jsou testy zelené" — jsou, 692 frontendových a celá Go sada,
ověřeno. Otázka zní: **dokazují ty testy, že funkce fungují, nebo dokazují,
že se stuby chovají jako stuby?**

Kontext: code review právě našel dvě kritické chyby, které 692 zelených testů
minulo, a **obě jsou neshodou kontraktu mezi klientem a serverem.**

## Souhrn

| Funkce | Hloubka testu | Kontrakt klient↔server | Verdikt |
|---|---|---|---|
| Draft save/publish + CAS/ABA | reálný handler nad zmigrovanou DB | **žádný** — token si test podepíše serverovou funkcí nad serverovými bajty (`pipeline_drafts_test.go:67`) | otestováno / **proof jen stub** |
| Fixtures a izolace | čistá funkce + handler nad DB | **žádný** | otestováno, kontrakt ne |
| Typovaná rozhodnutí | handler nad DB, race i restart | **žádný** — FE testuje jen mock `onDecide` | server ano / **klient stub** |
| Vnořené vstupy | čistá funkce + reálný Executor | **žádný** + TS zrcadlo Go switche | Go ano / **FE zrcadlo ne** |
| Start intent / dvojklik | komponenta nad mockem `apiFetch` | **žádný** | **jen stub** |
| Replay integrita | reálný handler, negativní kontrola počtu řádků | n/a (serverová vlastnost) | **otestováno** |
| Porovnání verzí | čistá funkce + plně mockovaný `apiFetch`; CLI stub serializuje vlastní CLI typ | **žádný** | **jen stub** |
| Pinning verzí | handler nad DB + `fakeExecutor` | **žádný** | propojení ano / rearm ne |

**Napříč všemi osmi funkcemi: 0 řádků testu kříží hranici klient↔server.**

## Nejzávažnější mezera — nová, code review ji minul

`lib/api/waitpoints.ts:25` vyrábí tělo rozhodnutí jako
`JSON.stringify({ approved, ...answer })` — **plochý** objekt. Je to jediné
místo, které tvar očekávaný handlerem vyrábí, a netestuje ho nic: inbox testy
mockují přímo `waitpointDecide`.

Změň ten spread na `{ approved, answer }` a **všech 692 FE testů i celá Go sada
zůstane zelená**, zatímco každé typované rozhodnutí v produkci skončí na 400
`action_id and data required` (`internal/pipeline/decision_forms.go:83`).
Totéž platí pro `hooks/use-pending-approval.ts:131`.

## Adversariální mezery po funkcích

**Drafty.** Empiricky ověřeno: `{"name":"a & b"}` z prohlížeče a týž objekt po
Go marshalu mají odlišný hash (`2769a2b8…` vs `24e1899f…`). Testy to nemohou
zachytit, protože si token podepisují serverovou funkcí nad serverovými bajty.
Netestované také: FK `ON DELETE CASCADE` na `workspaces` —
`internal/pipeline/drafts_test.go:12` běží nad **ručně sestaveným** schématem
bez tabulky `workspaces`, tedy ne nad produkčním schématem. Přejmenování migrace
nebo drift `versioningSchemaSQL` nechá 6 draft testů zelených a API rozbité.

**Fixtures.** `FixtureStepInput.StepOutputs` je `map[string]string`
(`fixture_steps.go:18`). FE posílá hodnoty jako **řetězec**; přirozený refaktor
(poslat po parsování objekt) dá 400 a obě strany zůstanou zelené. Tvrzení
o izolaci navíc pokrývá jen `http` (`fixture_steps_test.go:32`) — přidání
dalšího spustitelného typu do switche na `:92` nezachytí žádná sonda.

**Vnořené vstupy.** `lib/routine-data-sources.ts:10–15` re-implementuje Go
scheduler switch. Změna pravidla na Go straně nechá TS zrcadlo nabízet kroky,
které v daném pořadí neběžely; editor zapíše `{{ steps.X.output }}` na
nedostupný krok a běh spadne až za běhu. Obě strany zelené, ani jedna o druhé neví.

**Start intent.** Tohle není hypotéza, už se to stalo:
`routine-comparison.tsx:53` vynechává `Prefer: respond-async` a jeho test
tvrdí jen `Idempotency-Key` (`routine-comparison.test.tsx:33`). Táž delece na
`routines-detail-panel.tsx:249` by způsobila, že **každý** ruční Run poběží
synchronně a umře na timeoutu — se 692 zelenými testy. Jediný test té hlavičky
v celém repu je `internal/api/pipeline_manual_async_test.go:26`, čistě serverový.

**Replay.** Všechny korupční fixtury jsou syntakticky vadné
(`pipeline_replay_input_integrity_test.go:48`). Netestované: syntakticky
**platné** `inputs_json`, jehož typy neodpovídají připnuté definici — např.
`{"count":"7"}` proti v2, kde je `count` integer. Replay dispatchne a spadne
uprostřed běhu místo odmítnutí před dispatchem.

**Porovnání.** `readComparisonResult` (`lib/routine-comparison.ts:32`) vyžaduje
`raw.pipeline_version`, ale `enrichRunDefinition` klíč **vynechá**, pokud se
nenajde archivní řádek (`internal/api/pipeline_run_definition.go:30`). Mock ho
dodává vždy → reálný běh bez archivní shody shodí frontu hláškou „recorded
recipe does not match" a testy zůstanou zelené. Větev `--version N`
(`cmd_eval_compare.go:140`) nemá **žádný** test.

**Pinning.** `ON CONFLICT` na `internal/pipeline/store.go:426` nastaví
`status='pending'` vždy, když se liší `fire_at` — tedy i pro řádek se statusem
`cancelled`. Uživatel zruší jednorázový start, autor později uloží recept
s jiným `FireAt` a **zrušený start se tiše probudí**. Komentář na `:412–416`
tvrdí opak; SQL to nedělá a netvrdí to žádný test. Rozšiřuje N4 z code review.

## Migrace

- **Fresh install: pokryto implicitně** — `internal/api` testy jedou přes
  `testutil.MigratedSQLDB` (`router_test.go:36`), tedy plný migrační řetězec.
- **Upgrade ze starého schématu: žádný test.** Vzor
  `internal/database/migrate_v160_test.go` existuje, ale pro tyto tři migrace
  nebyl použit. Netestované zejména: trigger `pipelines_publication_revision`
  na existujících řádcích (všechny dostanou `publication_revision = 1`, takže
  dva recepty s různou historií mají po upgradu stejnou base revizi), a
  `decision_form_json NOT NULL DEFAULT ''` na rozhodnutých **i čekajících**
  waitpointech z doby před migrací.

## Fake timery

**Čisté.** Žádný z 20 nových frontendových test souborů nepoužívá
`vi.useFakeTimers` ani `setSystemTime`; testy používají `waitFor`/`act`
s reálnými promise. Dokumentovaná past zde nehrozí.

## Skladba nového testovacího kódu

| Kategorie | Řádky | % |
|---|---:|---:|
| Go — handler/DB (reálné SQL, reálný handler) | 1795 | 62 % |
| FE — komponenta/hook nad mockovanou sítí | 576 | 20 % |
| Go — čistá funkce | 165 | 6 % |
| FE — komponenta bez sítě | 157 | 5 % |
| FE — čistá funkce | 118 | 4 % |
| CLI — stub server | 64 | 2 % |
| **Celkem** | **2875** | |

62 % „handler/DB" vypadá dobře, ale 852 z těch 1795 řádků je
`routines_claims_test.go`, což s auditovanými funkcemi nesouvisí. Pro
auditovaných osm funkcí je poměr výrazně horší.

## Pět chybějících testů, seřazeno podle hodnoty

**T1 · Akceptační test draft/publish s bajty z prohlížeče.** Repo už má přesně
tu infrastrukturu, která by chybu s `&` chytila:
`cmd/crewship/acceptance_routine_trigger_test.go` staví reálnou zmigrovanou DB
+ `api.NewRouter` + `SetSaveTokenSecret`. Žádný nový endpoint ji nepoužívá.
Napsat `TestDraftPublishAcceptsBrowserSerializedDefinition`: definici
`{"name":"Sales & Ops","steps":[{"id":"a","type":"transform","transform":{"input":"a < b","expression":"."}}]}`
poslat jako **surové bajty** na `h.TestRun`, převzít vrácený `save_token`, ty
**samé bajty** poslat na `h.SaveDraft` a pak `h.PublishDraft` — očekávat 201.
Dnes vrátí 422.

**T2 · Test tvaru drátu pro rozhodnutí.** Vitest test, který volá
`waitpointDecide("ws","tok",true,{action_id:"go",data:{count:0,enabled:false}})`
s reálným `JSON.stringify` a asertuje **plochý** objekt
`{approved:true, action_id:"go", data:{…}}`, plus zrcadlový Go test, který
přesně ten řetězec pošle do `h.ApproveWaitpoint` a čeká 200. Totéž pro
`hooks/use-pending-approval.ts:131`.

**T3 · Guard test na `Prefer: respond-async` napříč všemi start cestami.**
Jeden vitest soubor, který vyrenderuje každou komponentu POSTující na `/run`
(`routines-detail-panel.tsx`, `routine-run-detail.tsx`, `routine-comparison.tsx`)
a pro každý zachycený POST asertuje
`new Headers(init.headers).get("Prefer") === "respond-async"` i neprázdný
`Idempotency-Key`. Doplnit statický grep-style test po vzoru `journal-groups`.
Tahle dvojice by zachytila N2.

**T4 · Upgrade test pro tři nové migrace.** Po vzoru `migrate_v160_test.go`:
zmigrovat na verzi před `20260908223757`, vložit dva `pipelines` řádky s různou
historií, `pending_runs` se statusem `cancelled` i `pending`, `pipeline_waitpoints`
se statusem `pending` i `approved` s legacy payloadem; dokončit migraci a ověřit
`publication_revision`, `pinned_version IS NULL`, `decision_form_json = ''`
a že `CompleteApproval` na legacy pending waitpointu **projde** s prázdným
payloadem (větev `formJSON != ""`, `internal/pipeline/waitpoints.go:641`).

**T5 · Zrušený one-time start nesmí ožít editací receptu.** Go test:
`SaveWithTrigger` s `TriggerKindOnce`, pak
`UPDATE pending_runs SET status='cancelled'`, pak znovu `SaveWithTrigger`
s jiným `FireAt` — asertovat `status = 'cancelled'`. Dnes projde jako `pending`.
Ve stejném souboru end-to-end dispatch přes **reálný** `Executor` (ne
`fakeExecutor`): pin na v1, publikovat v2 s jinými kroky, ověřit, že se
vykonala v1.
