# Routines jako vykonatelný recept — audit enginu

2026-09-08; dev1, `dev1/issues-preview` @ `492c90ad9`. Navazuje na
`routines-audit-2026-09-08.md`. Tento dodatek je kontrola aktuálního kódu,
nikoli živé nasazení Terraformu, Ansible nebo porovnání modelů.

## Cílový kontrakt

Silný model připraví recept: definici kroků, skripty, závislosti, vstupy,
výstupní kontrakty a testovací příklady. Engine provádí mechanické kroky přímo.
Model se volá jen u kroků, které potřebují interpretaci nebo tvorbu obsahu.
Slabší model není zárukou stejného výsledku jako silnější; kvalitu je třeba
měřit na reprezentativních případech. Schéma JSON kontroluje strukturu,
nikoli pravdivost. Stejné deterministické výsledky vyžadují i stejné vstupy,
verze programů a stabilní stav externích systémů.

## Co dnes opravdu běží

Crewship má vlastní Go executor (`internal/pipeline/executor.go`, `dag.go`),
SQLite run store, dispatcher, retry a waitpointy. Cron parser je
`github.com/robfig/cron/v3` (`go.mod`). Zkoumaná execution cesta nevolá runtime
Trigger.dev ani n8n. `.claude/context/prd/PRD-ROUTINES-MAX-2026.md` výslovně
popisuje inspiraci Trigger.dev bez přepisu backendu. Komentáře o paritě
nejsou externí knihovna ani důkaz identických záruk.

| Typ kroku | Skutečný mechanismus |
| --- | --- |
| script | Soubor pod `/crew/shared`, exec v připraveném kontejneru týmu jako UID 1001; Python/Bash/Node/Go a další interpretry. Bez LLM. |
| code | Inline `expr` nebo `cel`; inline python/go/bash jsou rezervované, ale nepodporované. |
| transform, http, query | Přímá transformace, HTTP nebo čtení dat podle konkrétního runneru; bez nutnosti LLM. |
| agent_run | Model a nástroje agenta; kvalita závisí na zvoleném modelu i zadání. |
| wait | Čekání na schválení, datum nebo událost. |

Evidence: `runner_script.go`, `runner_code_multi.go`, `code_runtimes.go`,
`types.go`, jednotlivé runnery. Produkční ScriptRunner je zapojen v
`cmd/crewship/cmd_start.go`. Běh bez Docker runneru skripty neposkytuje.

Terraform/Ansible lze volat z připraveného shell skriptu. Musí být dostupné
binárky, runtime, pracovní soubory, credentials, oprávnění a připojení k cíli.
Nejde o slib, že libovolný CLI nástroj funguje v každém kontejneru: root,
host mounty, Docker socket a síťové protokoly nejsou automaticky k dispozici.
ScriptStep nemá vlastní workdir; wrapper musí zvolit správný adresář.

## Rozhodující mezery pro deklarovanou release ambici

### 1. Skutečné ukončení procesu — potvrzená mezera

`OrchestratorRunner.RunScript` při timeout/cancel zavře exec reader, ale
neukončí proces v kontejneru. Kód to výslovně přiznává v chybě
„in-container process may still be running“. `TestRunScript_TimeoutEnforced`
ověřuje návrat z čekání, nikoli smrt procesu. Pro důvěryhodné Stop u deploy
receptů je nutná správa jednotlivého procesu/skupiny a ověření ukončení,
bez zastavení jiných úloh ve stejném kontejneru. Zastavení nevrací už provedené
externí změny zpět; výsledek může potřebovat následnou kontrolu stavu.

### 2. Recept musí zahrnovat i konkrétní skript a závislosti

Verzuje se definition JSON. `ScriptStep` má Path, Interpreter, Args a Env;
execution cesta nenačítá ani neověřuje hash obsahu souboru. Změna souboru na
stejné cestě proto může změnit chování stejné verze definice. Pro opakovatelné
recepty potřebujeme neměnné balíčky/revize skriptů, ověřované hashe a připnuté
závislosti či image. Nestačí přidat hash do popisku; executor musí ověřit
vykonávaný obsah. Rozsah je nutné zvolit tak, aby nevyžadoval nový registr balíčků.

### 3. Obnova není záruka jediného vykonání externí akce

`resume.go` obnoví hotové kroky, rozpracovaný krok spouští znovu od začátku
(at-least-once). Deduplikace triggeru a běhu neřeší situaci „externí zápis
proběhl, ale výsledek se neuložil“. Pro mutující recepty explicitně určit
bezpečné opakování, identitu operace, zámek cíle a kontrolu skutečného stavu.
Při neznámém výsledku neprovádět slepý automatický replay. To je požadavek
na kontrakt receptu i runtime, nikoli důvod odstranit existující retry.

### 4. Kontrola receptu versus skutečná zkouška

Authoring `test_run` je ModeDryRun. Nestačí k prokázání přítomnosti a fungování
všech runtime závislostí nebo kvality výstupu. Release gate má zahrnout skutečný
Python/Go/shell běh ve studeném kontejneru, neplatný výstup, chybějící nástroj,
nedostupné credentials, chybu uprostřed, Stop/timeout/restart a dvojí trigger.
Pro nasazovací recept ověřit plán → rozhodnutí → provedení konkrétního plánu
→ kontrola cílového stavu. Testovat na izolovaných prostředcích.

### 5. Kalendář jako primární způsob plánování

Klient vybírá datum, čas, opakování a časové pásmo, ne cron. Náhled konkrétních
budoucích termínů ukazuje význam nastavení ještě před uložením. Týdenní či
měsíční kalendář může zobrazovat rutiny souhrnně, včetně pozastavených plánů.
Engine už má cron, timezone a endpoint pro náhled termínů; hlavní změna pro
opakování je UI. Jednorázové datum, začátek/konec série a výjimky nelze bez
ověření vydávat za hotové pole současného schedule API. Existující delayed
queue a datetime wait jsou primitiva, nikoli hotový klientský kalendář.
Zvlášť otestovat DST a měsíce bez zvoleného dne. Jednorázový termín se nesmí
nechtěně převést na každoroční cron.

## Doporučený příklad receptu

Připnutá konfigurace → kontrola nástrojů a vstupů → terraform plan → uložený
plán a čitelný souhrn → schválení konkrétního plánu → apply tohoto plánu →
Ansible playbook → automatická kontrola cílového stavu → výsledek do Issues.
Celý mechanický průchod může být bez LLM. AI má hodnotu při návrhu receptu,
vysvětlení změn a diagnostice, nikoli jako povinný prostředník každého příkazu.
Uložený plán je citlivý artefakt; klientský souhrn a přístup k plánu jsou různé věci.

Oficiální reference k plan/apply:
https://developer.hashicorp.com/terraform/tutorials/automation/automate-terraform
Inspirace k retry a idempotenci:
https://trigger.dev/docs/errors-retrying a https://trigger.dev/docs/idempotency

## Priorita

Před označením za spolehlivý runner infrastrukturních receptů uzavřít body
1–4 na konkrétních testech; kalendář je zásadní klientská část stejného releasu.
Není třeba přepisovat engine na Trigger.dev/n8n ani přidávat druhý Python
sandbox, když správně fungující ScriptRunner pokrývá požadovaný kontejnerový
model. Předchozí zelené pipeline/API testy nejsou důkaz odstranění těchto mezer.
