# Předání čistému agentovi: Routines

**Aktualizace 9. září:** začni [závěrečným přehledem pro revizi](ENDING-2026-09-09-ROUTINES-REVIEW.md).
Obsahuje současný stav implementace, testy, nasazení a otevřené body R1–R10.
Níže uvedené tvrzení o čistě výzkumné relaci je historické. Implementace mezitím
proběhla; uživatel ale výslovně odmítl srozumitelnost Edit/Test. Celé PRD ani UX
nejsou uzavřené. Nový UX agent má řešit orientaci klienta, ne další přidávání funkcí.

## Původní předání z 8. září (historický kontext)

Datum: 2026-09-08. Zadáním této relace byl další výzkum a PRD, nikoli implementace.
Nové dokumenty jsou v pracovním adresáři; nejsou tímto předáním automaticky
commitnuté, publikované ani nasazené. Při předání mimo tento workspace je přilož.

## Zpráva k předání

Pokračuj na Crewship Release 1.0, pouze `/srv/crewship/crewship_1` a dev1
https://crewship-dev1.unifylab.cz. Přečti AGENTS.md, CODEX.md a tyto dokumenty:

1. [Nové PRD](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md) — hlavní zadání a P0/P1.
2. [Výzkum konkurence](../ux/routines-competitive-review-2026-09-08.md) — dvě kola,
   oficiální zdroje, omezení a konkrétní lokální evidence.
3. [Dosavadní implementace Routines](HANDOFF-2026-09-08-ROUTINES-WORKSPACE.md).
4. [Kontrakt Issues](HANDOFF-2026-09-08-ISSUES-EXECUTION.md).

Uživatel opakovaně odmítl dosavadní UX. Technické testy nejsou schválení designu.
Zachovej původní grafiku aplikace, barvy, ikony, avatary, levý explorer a plný
kalendář. Inspirace hustotou a formuláři: Credentials popup a New Project.
Nezačínej další sadou karet a tabů. Cíl je oddělit jednoduchou obsluhu od
autorování, se společnou identitou a stejnými kroky receptu a běhu.

Při výzkumu byl HEAD `42aa533b3` na `dev1/issues-preview`. Tento nový commit již
přidává společný step spine a List/Map, odstraňuje druhou řadu run záložek.
Byl přečten jeho diff, nebyl zde nově ověřen v browseru. Zjisti skutečný aktuální
stav; nezačínej podle zastaralého screenshotu ani starého handoff úvodu.

Existující feature větev `feat/routines-workspace-v2`, worktree
`.claude/worktrees/routines-workspace`, PR #2460. Součástí je nesloučený Issues
kontrakt z PR #2448. Claim #2459 byl v předchozím předání uvolněn; znovu ověř
aktuální claims před implementací/commitem. Stav review ověř z obsahu review,
zelený rate-limited check nestačí. Toto zadání není pokyn k merge.

Nejdřív udělej inventuru P0 proti aktuálnímu kódu. Replay, pinned version,
boot resume, start dedup, krokové testy a eval mechanismy již existují.
Neoznač je za chybějící a nepřepisuj engine. Zásadní nejasnosti k ověření:

- Obecný draft/published-version kontrakt; draft schedule approval není totéž.
- Testy mohou mít reálné účinky. `step-run` to uvádí; replay předává ModeRun.
  Komentář „read-only backtest“ není důkaz izolace.
- Resume po restartu má at-least-once rozpracovaný krok; neznamená obecný retry
  libovolného kroku po editaci ani exactly-once externí zápisy.
- Výsledek, dokončení runtime a dostupnost souboru jsou rozdílné skutečnosti.

Následně vytvoř konkrétní klikací prototyp jednoho směru a projdi pět úloh z PRD.
Pak realizuj P0 po vertikálních celcích. Rich human forms, rozsáhlý recovery editor
a eval dashboard jsou P1, nikoli předpoklad prvního použitelného výsledku.
Průběžně informuj stručně česky. Dodávej konkrétní dev1 odkazy a ověření chyb,
čekání a přerušení, nejen úspěšné spuštění. UI copy zůstává anglicky.

## Pracovní adresář a ochrana cizí práce

Při začátku této dokumentační relace byly cizí tracked změny v AGENTS.md,
`internal/api/pipeline_artifacts.go`, `internal/api/pipelines_exec.go`,
`internal/pipeline/runs.go`, `internal/pipeline/store.go`; dále řada untracked
testů, PRD, auditů a HTML designů. Zachovej je. Nepoužívej plošné git add,
reset ani checkout. Aktuální status je autoritativní, seznam rychle stárne.

Nové soubory tohoto zadání: tento handoff, nové PRD a doplnění výzkumu v docs/ux.
Žádné změny API, DB, runtime či nasazení touto relací. Ověření dokumentace není
nové potvrzení funkčnosti aplikace. Před aplikačním releasem proveď předepsané
Go/vet/frontend/build kontroly a browser scénáře podle AGENTS a PRD.


## Navazující implementace na dev1

[Implementační inventura a testovací evidence](ROUTINES-RELEASE-IMPLEMENTATION-2026-09-08.md)
rozlišuje stav všech 17 hodnocených principů, konkrétní opravy start identity,
pravdivých stavů a testovací semantiky a otevřený návrh durable draft/publish.
Není potvrzením dokončeného P0 ani akceptace UX.


Navazující relace přidala durable draft/publish API, CLI a editor. Přesné
kontrakty, testy a stále otevřený rozsah jsou v
[implementačním protokolu](ROUTINES-RELEASE-IMPLEMENTATION-2026-09-08.md).
Nepřebírat původní tvrzení „editor má pouze lokální buffer“ jako aktuální stav.


9. září: Chat nyní používá stejný draft jako UI (nové MCP get/save draft,
crew-scoped API, hluboký odkaz s draft ID). Přibyly odkazy na konfliktní
presety, typované formuláře pro opakované plány a jejich opravu, explicitní
live execution u step-run a první stránka pokusů na požádání. Aktuální
ověření a zbývající R2/R6/P1 rozsah jsou v poslední sekci inventury výše.


## Aktualizace 9. září — pokračování implementace

Nezačínat znovu od původní tabulky chybí/existuje. Aktuální stav, testy,
konkrétní soubory a omezení jsou v posledních oddílech
[implementačního protokolu](ROUTINES-RELEASE-IMPLEMENTATION-2026-09-08.md).
Dev1 je nasazené včetně durable draft/publish, pinned schedules, společného
run/recipe UI, fixtures bez externích akcí, rich human decision forms,
importu historických fixture dat a klientských dataset/tier/version comparison.
CLI eval compare nyní opravdu připíná stejnou archive version pro obě strany.

Důležité nové body: migration `20260909091500_waitpoint_decision_forms.sql`,
`pipeline/decision_forms.go`, `HumanDecisionForm`, `RoutineFixtureImport`
a `RoutineComparison`. Rich denied action jde přes stávající OnFail, alternativní
pokračující větev používá approved=true a JSON action_id/data. Porovnání exact
text/status není semantická kvalita; klientská queue není durable scheduler.

Finální kontrola: 651 frontend testů, lint/build/vet prošly; jediný CLI YAML
parity fail celé Go sady opraven a celá CLI sada znovu prošla. Lokální/veřejný
health OK, browser pouze nepřihlášený redirect. CLI profile dev1 bez loginu.
Autentizované run/approval/compare, načtení MCP v existujících crew a uživatelský
UX protokol nejsou ověřené. File/credential input reference, širší typed mapping
a řízená obnova z vybraného podporovaného kroku zůstávají dalšími oblastmi;
nevydávat tuto etapu za dokončení celého PRD. Žádné commity ani PR.

Navazující dávka 2026-09-09 je rovněž nasazená na dev1 (PID2554511):
zdroje dat pro více kroků bez změny sekvenčního pořadí, blokování neznámých
formulářových widgetů a přísná kontrola JSON projekcí ve fixtures. Celá Go
sada prošla; po poslední katalogové změně opakovaná API sada a vet prošly.
Frontend 78 sad / 658 testů, následně 71 routine-inputs testů; poslední lint
bez chyb a build prošly. Podrobnosti/logy v implementačním dokumentu.
Skutečný nasazený CLI ověřil chybějící projekci exit1 a platnou0 exit0;
/api/health lokálně i veřejně ok. Autentizované E2E stále čeká na login.

Další nasazená dávka 2026-09-09 (dev1 PID2683601): typované whole-reference
inputs pro call_pipeline, validace před child dispatch a UI změny existujících
child input vazeb. Legacy string/mixed text/literal map chování zachováno.
Celé Go testy a vet, lint/build, 5 UI testů kroků a invariants prošly.
Nasazený CLI ověřil platný/neplatný rodičovský recept; /api/health obě adresy ok.
Detaily v implementačním dokumentu. Profil dev1 stále nepřihlášený; live E2E
nedokládat těmito offline/testovými výsledky. Ostatní popsané mezery PRD trvají.

Klientská frontendová etapa nyní nasazená na dev1 PID2792510:
- Test workspace (definice/sample/live comparison), čitelný Publish diff,
  vizuální human decision builder+náhled, per-step sample data editor.
- Skutečné komponenty ověřené v browseru včetně mobilu, keyboard tabs,
  zachování hodnot a dlouhých názvů. Rozsah/evidence viz
  docs/ux/routines-client-frontend-2026-09-09.md.
-77sad/692UItestů, lint/build/vet/invariants prošly. Go všechny balíky
  ověřené: jediný nedostatek místa v RAM TMPDIR u devcontainer vyřešen
  opakováním celého tohoto balíku s TMPDIR=/tmp (25,982s,exit0).
- Public/local API health ok. Public browser login bez page errors,
  dev1 whoami nepřihlášený; full-app E2E a UX s uživateli stále otevřené.
  Žádný commit/PR/merge, backendové zbývající mezery PRD trvají.
