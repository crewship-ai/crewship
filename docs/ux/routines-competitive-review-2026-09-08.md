# Routines: konkurenční srovnání

Datum: 2026-09-08. Metoda: veřejná oficiální dokumentace a kontrola lokálního
kódu Crewship. Nejde o testování přihlášených konkurenčních aplikací ani
benchmark výkonu či uživatelskou studii. Níže jsou oddělené doložené funkce a
naše návrhy. Aplikace se při tomto průzkumu neměnila.

## Doložené rozdíly

- **Windmill:** z parametrů skriptu odvozuje JSON Schema a vstupní UI, které lze
  doplnit o názvy, popisy, výběry a omezení. Pro Crewship je podstatná plynulá
  cesta od implementace k nástroji pro klienta, nikoli další ruční opisování
  konfigurace. [Auto-generated UIs](https://www.windmill.dev/docs/core_concepts/auto_generated_uis).
  Umí test jednotlivého kroku a restart od kroku/iterace/větve; restart nasazených
  flows dokumentace váže na Cloud a Self-Hosted Enterprise.
  [Testing flows](https://www.windmill.dev/docs/flows/test_flows).
- **n8n:** minulý běh lze načíst do editoru a použít jeho data pro opravu.
  [Debug executions](https://docs.n8n.io/build/understand-workflows/understand-executions/debug-executions).
  Pinning znovu používá výstup uzlu při vývoji; není to produkční funkce a
  nepodporuje binární výstupy. Neznamená automatickou izolaci všech ostatních akcí.
  [Pin and mock data](https://docs.n8n.io/build/work-with-data/pin-and-mock-data).
- **Dify:** Human Input nabízí upravitelné předvyplněné podklady, vlastní rozhodovací
  tlačítka, navazující větve a timeout větev. Formulář může dorazit přes aplikaci
  nebo e-mail. Přínosem je lidský zásah jako součást práce, nejen ano/ne.
  [Human Input](https://dify.ai/blog/the-human-input-node-bringing-human-judgment-into-automated-workflows).
- **Make:** ukládá nedokončená provedení, umožňuje jejich řešení a opakování od
  chybujícího modulu. Ukládání musí být zapnuté. Je to konkrétní obslužný postup
  pro nápravu chyby, nikoli pouhý seznam červených běhů.
  [Incomplete executions](https://help.make.com/incomplete-executions),
  [Manage incomplete executions](https://help.make.com/manage-incomplete-executions).
- **Trigger.dev:** replay je nový běh se stejným payloadem proti nejnovější verzi
  prostředí, nikoli automatické pokračování starého běhu.
  [Replaying](https://trigger.dev/docs/replaying).
  Task idempotency vrací původní run handle při opakovaném klíči; samo o sobě to
  nedokazuje exactly-once účinky libovolné externí operace.
  [Idempotency](https://trigger.dev/docs/idempotency).
- **LangSmith:** propojuje datasety, experimenty, lidské hodnocení a evaluátory
  před nasazením i na produkčních bězích. Pro naši vizi různě silných modelů je
  relevantní měřit kvalitu, cenu a čas nad stejnými příklady.
  [Evaluation concepts](https://docs.langchain.com/langsmith/evaluation-concepts),
  [Evaluation types](https://docs.langchain.com/langsmith/evaluation-types).

## Co Crewship již má

Není správné tvrdit, že potřebujeme všechny tyto mechanismy teprve napsat:

- `internal/api/pipeline_runs_replay.go`: replay s původními vstupy a volitelnou
  připnutou verzí; také hromadný replay.
- `internal/pipeline/resume.go`: obnova po restartu, obnovené výstupy dokončených
  kroků; rozpracovaný krok má at-least-once semantiku. Není to obecný interaktivní
  restart libovolné větve po úpravě receptu.
- `internal/pipeline/idempotency.go`: deduplikace spuštění podle workspace,
  pipeline a klíče; výchozí TTL 24 hodin.
- `internal/pipeline/types.go`: retry, souběžnost, online eval konfigurace.
- `internal/quartermaster/regression.go`: porovnání eval metrik.
- `components/features/routines/routine-input-form-builder.tsx`: autorování
  vstupních polí; `routine-saved-inputs.tsx`: čitelné historické hodnoty.
- `routine-approval-banner.tsx`: schválit/zamítnout a komentář. To není ekvivalent
  obecného typovaného rozhodovacího formuláře Dify.

Existence kódu není potvrzením všech produkčních kombinací. Další audit má ověřit
propojení, oprávnění a skutečné chování těchto mechanismů před slibem v UI.

## Doporučení pro Crewship

1. Oddělit obsluhu rutiny od autorování, ale sdílet recept, identity a design.
   Obsluha: dodat vstupy, spustit, rozhodnout, převzít výsledky. Autor: upravit
   postup, mapovat data, testovat a publikovat.
2. Propojit selhání s opravou: otevřít problematický krok se zachycenými daty,
   bezpečně vyzkoušet změnu a jasně nabídnout nový běh versus podporované pokračování.
3. Rozšířit lidský krok o typovaná pole a pojmenované akce. V Routines a Inboxu
   musí jít o tentýž požadavek, rozhodnutí a auditní záznam.
4. Zpřístupnit srovnávací testy verzí/modelů nad sadou příkladů. Slabší model
   neslibuje stejný výsledek jen proto, že dostal stejný recept.
5. Náklady snížit opakovaným použitím testovacích dat a spuštěním potřebných kroků;
   skutečný dopad změřit. Kopírování canvasu či další karty nejsou náhradou za
   dobrý postup práce. Nepřebírat celý cizí engine bez konkrétního důvodu.

## Druhé kolo výzkumu: provoz, autorování a hranice testování

Další kontrola oficiálních zdrojů 2026-09-08:

| Doložené chování | Co z něj odvozujeme pro Crewship | Omezení srovnání |
| --- | --- | --- |
| Windmill rozlišuje Developer a Operator; operátor nemůže spouštět preview nepublikovaného kódu. | Oddělit možnost použít hotový recept od možnosti měnit jeho implementaci. | Operator není automaticky read-only pro všechny ostatní objekty; rozhodují také oprávnění k prostředkům. |
| n8n odděluje automaticky ukládaný draft a publikovanou verzi používanou produkčními triggery. | Editace musí mít předvídatelný vztah k budoucím spuštěním; samotné uložení nemá nepozorovaně přepnout živý recept. | Dokumentace uvádí automatické znovupublikování při změně workflow settings. Nepřebírat slepě tuto výjimku. |
| AWX 24.6.1 má Surveys: pojmenované otázky, proměnné, povinnost, výchozí hodnoty a výběrová pole. | Klient vyplňuje srozumitelnou objednávku práce; autor řeší mapování proměnných. Stejná pole používat pro plánování. | Jde o dokumentovanou konkrétní verzi, ne tvrzení o nejnovější edici. Je to automatizační systém, nikoli náhrada agentního uvažování. |
| Dify Human Input má adresáty a timeout větev. | Rozhodnutí potřebuje vlastníka, platnost a pokračování; pouhé tlačítko Approve nestačí pro všechny úlohy. | Převzít koncept, nikoli automaticky e-mailové kanály či celý Dify engine. |

Zdroje k tabulce: [Windmill roles](https://www.windmill.dev/docs/core_concepts/roles_and_permissions),
[n8n save and publish](https://docs.n8n.io/build/understand-workflows/save-and-publish-workflows),
[AWX Surveys](https://docs.ansible.com/projects/awx/en/24.6.1/userguide/job_templates.html#surveys),
[Dify Human Input](https://docs.dify.ai/en/cloud/use-dify/nodes/human-input).

Windmill navíc odlišuje autora spuštění (`created_by`) a identitu použitou pro
oprávnění (`permissioned_as`). Pro Crewship z toho plyne požadavek nezaměňovat
klienta, odpovědného agenta, crew a skutečnou autorizační identitu.
[Windmill jobs](https://www.windmill.dev/docs/core_concepts/jobs).

### Nově ověřené lokální skutečnosti

- `cmd/crewship/cmd_routine_step_run.go`: krok lze spustit s fixture a zachycenými
  upstream výstupy. Nápověda výslovně říká, že HTTP a skript mohou mít skutečné
  účinky; příkaz nevytváří plný záznam běhu. Podporované typy jsou omezené.
- `cmd/crewship/cmd_routine_backtest.go` a `internal/api/pipeline_runs_replay.go`:
  existuje porovnání kandidátní verze s minulými běhy. Komentář o read-only
  evaluaci není dokladem izolace: replay předává `pipeline.ModeRun`. Před nabídkou
  „bezpečného testu“ je nutné projít celou cestu, policy a dostupné nástroje.
- Výskyty `draft` u schedule approval nejsou důkaz obecného draft/publish
  životního cyklu receptu. Tento kontrakt musí implementující agent doložit.
- Během průzkumu HEAD přešel na `42aa533b3`: společný step spine, List/Map,
  odstranění druhé řady záložek běhu. Zkontrolován diff, nikoli nové browser
  ověření. Nové PRD nesmí tento commit nevědomky vrátit ani jej označit za
  uživatelsky přijatý.

### Co nekopírovat

Canvas jako povinnou vstupní obrazovku, další oddělené seznamy schválení,
technické názvy stavů bez vysvětlení, automatický replay externích zápisů ani
tvrzení, že jiný model poskytne stejnou kvalitu bez měření. Dokumentovaná funkce
konkurence není důkaz její lepší použitelnosti nebo rychlosti.

Výsledný návrh a implementační pořadí jsou v
[PRD](../prd/ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md).
