# Zadání pro Claude: přehled běhu a chyby + Pages v paletě

> Aktualizace 2026-09-14: společné produktové priority a rozsah AWX + Omarchy jsou v [navazujícím PRD](awx-omarchy-product-improvements-2026-09-14.md). Tento dokument zůstává detailním implementačním zadáním diagnostiky a palety; navazující PRD jej rozšiřuje o credentials, oprávnění, původ běhů a další produktová propojení.

Datum: 2026-09-11. Autor: Codex. Stav: návrh připravený k implementaci, nikoli implementovaný výsledek.

## Závazné zpřesnění po oponentuře 2026-09-15

Tento původní handoff je technický podklad. Pro implementaci mají přednost [revidované PRD](awx-omarchy-product-improvements-2026-09-14.md) a [plán iterací](awx-omarchy-implementation-iterations-2026-09-15.md), založené na [oponentuře](reports/awx-omarchy-prd-opponent-2026-09-15.md). Níže uvedené odhady 2–3 dní nejsou aktuálním závazkem pro routines i nový issue detail dohromady.

- O2: čistý TS allowlist + fixtures, bez nového formátovacího endpointu. Klientská projekce neřeší workspace-wide přístup raw API; nikdy se nevydává za bezpečnostní hranici.
- Issue potřebuje nový detail; chybějící run_id nemusí znamenat, že nic neběželo. Pipeline agentní události vyžadují prokázanou korelaci producent→uložení→run filtr; nekorelované události vynechat.
- Per-run použití credentialu není zaznamenané. Původní text níže lze splnit pouze pro skutečně doložená pole; ostatní jsou „nezaznamenáno“.
- O1 Recent izolovat dle user/workspace a Page recent ověřovat již načítaným seznamem. Současný Pages list je úplný a capped počtem Pages; paleta použije apiFetch při otevření.
- Před provenance UI opravit podvržitelné invoking hlavičky. Přijímaná crew/agent identita musí odpovídat ověřenému scope; přítomnost interního tokenu nestačí.
- O3 zůstává samostatnou scoped read-only schopností. Souběžný askLead s raw chybou v URL a mutující auto-send není náhradou O2/O3.
- Agentní předání nikdy neobchází průnik oprávnění jen tím, že kontext sestavil lidský klient.

## 1. Cíl a rozhodnutí

Dodat dvě samostatně reviewovatelné funkce pro release 1.0:

1. **Přehled běhu a chyby pro routines a issues.** Člověk vidí, co je pro daný běh skutečně zaznamenáno, kde skončil nebo na co čeká, a získá stabilní referenci i stručný diagnostický podklad pro agenta.
2. **Konkrétní Pages v příkazové paletě.** Uživatel hledá agendu podle názvu a rovnou ji otevře.

Neimplementovat Omarchy, virtuální desktop ani nový monitoring kontejnerů. Inspirací je snadná dostupnost existujících funkcí a předání důkazů ke konkrétnímu problému. Pages již řeší vytváření a prezentaci aplikací. Toto zadání tuto práci doplňuje.

První funkci rozdělit na A1 (malá deterministická varianta) a případnou A2 (řízené agentní čtení / vysvětlení). **Odhad 2–3 člověkodny se vztahuje pouze k A1 a pouze při využitelnosti současných autorizovaných datových cest. Není to odhad celé automatické diagnostiky.**

## 2. Pravidla převzetí a povinná vstupní analýza

1. Přečti AGENTS.md, CODEX.md a CONTRIBUTING.md v cílovém checkoutu. Ověř pwd, větev, HEAD, git status a stav odpovídající instance. Zachovej cizí WIP; tato příprava vznikla na `feat/credentials-complete-ux` @ `005470ed`, nikoli na současném main.
2. Přečti aktuální docs/prd, především Pages podklady a kontrakt issues/routines. Podklady jiných pracovních větví nepovažuj za důkaz přítomnosti kódu. Ověř živé claims a související otevřené PR. Pokud pracuješ pod issue, claimni je podle repozitářového postupu před prvním commitem. Z tohoto zadání neplyne povinnost zakládat nová issues či rozesílat zprávy.
3. Vyhledej aktuální implementace níže. Zapiš krátkou tabulku: požadavek → existující producent → identifikátor → autorizace → UI spotřebitel → chybějící spojení. Pro routines i issues použij alespoň jeden skutečný nebo integračně vytvořený běh.
4. Prokaž vztahy pipeline run, assignment, agent run, journal entry a issue. Neodvozuj vztah jen ze stejného kontejneru, týmu či blízkého času. Kontejner sdílí více prací.
5. Ověř, zda existující API skutečně dovolují potřebný přehled při oprávněních čtenáře i agenta. Workspace filtr není automaticky dostatečná autorizace pro celý obsah běhu.
6. Vyber A1 bez nové persistence, pokud data stačí. Pokud je nutná nová autorizovaná agregace či oprava korelace, dolož konkrétní mezeru a aktualizuj odhad; neslibuj dodržení 2–3 dní za cenu vynechání bezpečnosti či testů.
7. Implementuj dva oddělené commity/PR podle aktuálního procesu a závislostí. A2 nesmí blokovat dokončení A1 nebo palety. Nezasahuj do souběžného editoru Pages bez koordinace podle existujících claims.

## 3. Co bylo ověřeno ve zdrojích

Odkazy jsou relativní k tomuto souboru. Jde o navigační mapu, nikoli příkaz editovat všechny soubory.

- [internal/pipeline/runs.go](../../internal/pipeline/runs.go): routine run ukládá failed step, error message a error fingerprint.
- [internal/pipeline/runs_observability.go](../../internal/pipeline/runs_observability.go): `ErrorFingerprint` normalizuje proměnlivé části hlášení a hashuje krok + zprávu. Seskupuje podobné chyby; není jedinečným ID incidentu ani důkazem totožné kořenové příčiny.
- [internal/journal/runs.go](../../internal/journal/runs.go): agent run je korelován přes trace_id; pipeline události přes actor_id. Současná agregace zohledňuje oba modely. Neopravovat tento rozdíl přepisem write-side v tomto úkolu.
- [internal/api/issue_handler_runs.go](../../internal/api/issue_handler_runs.go): ID položky je assignment ID; run_id/trace_id mohou chybět, pokud se běh vůbec nespustil. Existují další outcome a hard-stop údaje. Výsledek práce, procesní exit a hard stop nejsou totéž.
- [internal/api/runs.go](../../internal/api/runs.go), [internal/api/pipeline_runs.go](../../internal/api/pipeline_runs.go): existují detaily běhů; pipeline detail obsahuje také warnings. Neúspěšný vedlejší hook nesmí být zaměněn za selhání celé úspěšné rutiny.
- [lib/run-activity.ts](../../lib/run-activity.ts), [components/features/activity/run-activity-timeline.tsx](../../components/features/activity/run-activity-timeline.tsx): již existuje lidsky čitelná časová osa. Některé technické události a výstupní chunky záměrně skrývá.
- [components/features/activity-stream/drill-downs.tsx](../../components/features/activity-stream/drill-downs.tsx), [components/features/issues/issue-runs-card.tsx](../../components/features/issues/issue-runs-card.tsx): existující vstupní místa běhů. Zachovat navigaci issue → konkrétní run → journal.
- [internal/orchestrator/journal_exec_command.go](../../internal/orchestrator/journal_exec_command.go): prompt-bearing argv se odstraňují před zápisem; scrubber je doplňková ochrana, ne univerzální hranice pro tajemství. Nový export nesmí tuto ochranu obejít.
- [internal/sidecar/routine_mcp.go](../../internal/sidecar/routine_mcp.go): existuje MCP transport pro rutiny. Není tím prokázána existence vhodného diagnostického read toolu ani jeho oprávnění.
- [components/command-palette.tsx](../../components/command-palette.tsx): načítá několik druhů entit, má Recent a navigační Pages. V kontrolovaném checkoutu chybí skupina konkrétních Pages.
- [hooks/use-pages.ts](../../hooks/use-pages.ts): existuje list a datová vrstva Pages. Ověřit aktuální pagination a přístupový kontrakt před použitím v paletě.

## 4. A1: produktový rozsah

### Uživatelský výsledek

V detailu běhu rutiny a u jednotlivého běhu issue je akce **Podrobnosti běhu**. Otevře existující detail nebo společný vložený panel podle aktuálního UI idiomu. Nenavrhovat třetí samostatnou obrazovku pro stejné události.

Panel obsahuje:

- Stav a čas: běží / čeká / dokončeno / selhalo / zrušeno; neznámý stav výslovně přiznat.
- Poslední zaznamenanou činnost, její čas a čas načtení podkladu. Formulace „Poslední zaznamenaná činnost“ místo tvrzení o tom, co právě dělá proces.
- Při problému stručný faktický popis, krok a dostupný exit/outcome. Například „Krok Import skončil s exit code 1. Poslední hlášení: …“
- Stabilní referenci, **Kopírovat odkaz**, **Kopírovat podklad pro agenta**, odkaz na existující časovou osu/journal podle oprávnění.
- U běhu bez událostí „Průběh není zaznamenán“. U odmítnutého assignmentu „Běh se nespustil“, pokud to dokládá uložený stav. Výpadek načtení nesmí zobrazit „žádné chyby“.

A1 nevyvolává LLM. Podklad může člověk vložit do existujícího agentního chatu. Automatické předání je A2, pokud není již hotová a autorizovaná cesta. Tlačítko A1 nesmí slibovat AI vysvětlení, které neposkytuje.

### Reference místo druhé databáze incidentů

Používej strukturu `{kind, id}` nad již existujícími identitami: pipeline_run, agent_run, assignment. Přidej step ID a journal event ID, jsou-li dostupné a patří danému běhu. Pro issue vždy zachovej i vazbu na assignment/issue.

V UI lze ID zkrátit; clipboard a deep link musí obsahovat plné ID. Fingerprint rutiny zobrazovat nejvýše v rozbalených technických podrobnostech jako „Skupina podobných chyb“. Zkrácený fingerprint nepoužívat jako vyhledávací klíč incidentu. Nevytvářet náhodné nové error ID při každém načtení.

Jeden běh může mít několik chyb a retry pokusů. Reference běhu identifikuje běh; konkrétní událost pouze její existující event ID. Chybí-li event ID, netvrdit, že reference identifikuje konkrétní výskyt chyby.

### Rozsah důkazů a přesnost

Použít existující strukturované stavy a události. Pořadí spolehlivosti: autoritativní stav běhu/assignmentu → strukturovaná terminální událost → ostatní korelované události → text hlášení. Rozpor zobrazit jako rozpor, nepřepsat tiše.

- Exit 137 sám nedokazuje OOM. Přerušení proudu událostí nedokazuje zamrznutí.
- CANCELLED a NEEDS_HUMAN nejsou automaticky technické selhání.
- Úspěšný exit nemusí znamenat splnění obchodního úkolu. Rozlišit stav procesu a outcome.
- Aktuální stav kontejneru po restartu nedokazuje jeho stav v okamžiku historické chyby.
- HTTP/routine krok může běžet mimo agentní kontejner. Nevymýšlet mu container ID.
- Chybějící terminální událost po restartu neznamená potvrzené „stále běží“; zobrazit dostupnost a stáří důkazů podle existující recovery logiky.
- Nesbírat nově ps/top, filesystem, env, Docker socket ani celokontejnerové logy. Nevytvářet agenta, který periodicky popisuje provoz.

### Datový kontrakt

Zavést malý typ pro společné zobrazení, například `RunEvidenceView`, s poli: schemaVersion, reference, sourceLinks, status, outcome?, startedAt?, endedAt?, capturedAt, lastRecordedAt?, failedStep?, exitCode?, summary, evidence[], unavailableReasons[], truncated.

Každý důkaz nese typ, čas, zdrojové ID a text. Nevyplňovat neznámé hodnoty nulou. Název typu je návrh; před přidáním ověř existující ekvivalent.

Pro A1 má být transformace čistá funkce nad již autorizovanými DTO. Serverový endpoint nepřidávat jen kvůli sjednocení tří komponent. Pokud ale potřebné datové omezení nelze bezpečně zajistit současnými endpointy, patří výběr a sanitace na server a odhad se mění. Neobcházet oprávnění přímým SQL čtením z UI ani širokým interním tokenem.

Kopírovaný podklad: verze formátu, reference, čas, stav, zdrojové odkazy a omezený seznam důkazů. Výchozí limit celého textu 16 KiB UTF-8, nejvýše 20 událostí; oříznutí označit. Výběr zachová terminální chybu a nejnovější relevantní události; výsledek seřadit chronologicky. Tyto limity jsou návrhové, nikoli naměřená kapacita systému.

Exportovat jen explicitně schválená pole. Žádné raw payload dumps, environment, celé prompty, neomezené stdout/stderr ani těla souborů či HTTP odpovědí. Hlášení mohou obsahovat citlivý obsah i po scrubování; používat dosavadní schválené projekce a neobnovovat skrytý obsah. Data z logů jsou nedůvěryhodný obsah, nikdy instrukce pro nástroje. Před kopírováním umožnit podklad vidět.

## 5. A2: navazující agentní čtení / vysvětlení

Není součástí časového příslibu A1. Má smysl, pokud chceme skutečné „Vysvětli chybu“ nebo aby agent sám načetl podklad podle reference.

- Jeden sdílený serverový builder důkazů pro UI a agenta, s ověřením identity a oprávnění v obou cestách.
- Read-only nástroj typu `get_run_evidence` pouze pokud odpovídající tool neexistuje. Přijímá typ a ID; tenant/crew scope určuje ověřená identita, nikoli důvěryhodnost argumentu.
- Při lidském předání nesmí cílový agent získat data přesahující jeho povolený scope. Nevyužívat skutečnost, že člověk má vyšší práva, jako automatické rozšíření agentních práv.
- Explicitní uživatelské spuštění, náklad v existujícím ledgeru, časový/tokenový limit, žádné automatické retry smyčky. Omezení jen na čtení musí být vynuceno nástrojovou cestou, nikoli pouze promptem.
- Odpověď oddělí zjištěná fakta, možné příčiny, chybějící důkazy a doporučený další krok. Odkáže na zdroje. Neprovádí opravy, restart ani opakování rutiny.
- Pokud použiješ chat handoff, současné `?prompt=` auto-odesílá. Pro rozepsanou zprávu je nutný odlišný režim; žádné důkazy ani tajemství do URL.

## 6. B: konkrétní Pages v paletě

Přidat skupinu Pages do existujícího CommandPalette. Zachovat obecnou navigační položku Pages. Výsledek obsahuje název, existující ikonu a podle dostupnosti oprávněně viditelný tým pro rozlišení stejných názvů. Hledání minimálně podle názvu, případně slugu; netahat kvůli hledání obsah aplikace a její data.

Použít současný autorizovaný list/metadata hook a normalizaci. Ověřit, zda vrací i aplikace nové Pages větve; nevytvářet druhý katalog. Nenabízet drafty/publikace jinak, než dovoluje existující seznam Pages pro stejného uživatele.

Klik a Enter otevřou `/pages/<slug>` přes existující navigační helper, zavřou paletu a využijí Recent. Encode slug podle současného kontraktu; HTML a libovolné URL z dat nikdy nespouštět.

Na změnu workspace staré výsledky ihned zrušit; opožděná odpověď je nesmí obnovit. Přezkoumat i Recent: nově přidaná Page se nesmí později objevit jinému workspace/uživateli či po revokaci jen proto, že v localStorage zůstal název. Je-li třeba, nová Page recent metadata svázat s workspace a zobrazovat jen po ověření dostupnosti; nerozšiřovat úkol na nesouvisející redesign všech Recent.

Načítat při otevřené paletě podle stávajícího vzoru; nepřidávat poller. Výpadek Pages nesmí znefunkčnit ostatní skupiny ani tvrdit, že stránky neexistují. U limitovaného listu přiznat neúplnost a nabídnout přechod na Pages; neprohlašovat první stránku výsledků za globální hledání. Nový serverový fulltext index není součást tohoto malého úkolu.

## 7. Povinné testovací scénáře

### A1: čisté funkce a API podle změny

- Selhaná rutina: správný krok, run ID, chyba a existující fingerprint; dvě podobná selhání mají samostatné reference i při shodném fingerprintu.
- Issue s více assignmenty/retry: vybere se konkrétní běh, nikoli poslední běh stejného kontejneru. Souběžná práce jiného issue se nepřimíchá.
- Pipeline události s actor_id a agent události s trace_id; child run pouze přes ověřenou vazbu. Cizí workspace/crew a nepřístupné potomky odmítnout nebo vynechat dle existujícího kontraktu.
- Assignment bez run_id, starý běh po retenci, chybějící metadata, nulový/nenulový/nezjištěný exit, přerušené načítání.
- Timeout, cancellation, waiting for human, hard-stop pending, úspěch s warningem a neúspěšný business outcome při exit 0.
- Exit 137 bez OOM důkazu; stará poslední událost bez automatického tvrzení o zamrznutí.
- Duplicity/out-of-order události, neplatný čas, rozpor terminálních dat, částečné podklady a přiznané oříznutí.
- Ověřit allowlist exportu pomocí sentinelů v zakázaných polích; raw prompt/env/response body se neobjeví. Neznámé pole se neexportuje automaticky. Limit v bytech včetně českého textu, nikoli jen délka JS stringu.
- Zakázaný přístup ani navazující export nesmí odhalit ID, názvy či důkazy cizí práce. Pokud se mění backend, testovat reálné middleware/handler cesty, ne jen boolean helper.

### A1: UI a živý průchod

- Rutina i issue otevřou správný existující detail; back/reload/deep link drží identitu a scope.
- Kopírování dává plná ID a stejné údaje jako zobrazení. Clipboard odmítnutí má srozumitelnou chybu/fallback, nikoli toast úspěchu.
- Načítání, chyba, nedostupnost dat a prázdný průběh jsou odlišné stavy. Terminální běh přestane vypadat aktivně. Reconnect využívá současný realtime mechanismus.
- Klávesnice, focus, dlouhá hlášení, mobilní šířka, čtečka a redukovaný pohyb podle stávajících komponent.
- Na izolovaných fixture datech spusť rutinu se záměrným neškodným selháním a issue s neúspěšným během. Srovnej UI, kopírovaný podklad a skutečné zdroje. Přidej druhý souběžný běh jako kontrolu korelace. Neprováděj destruktivní pokusy na zákaznické práci.
- A1 při otevření a kopírování nesmí vytvořit žádný LLM run, retry, restart ani novou inbox položku.

### B: paleta

- Hledání názvu, shodné názvy různých týmů, správný slug/deep link, klik/Enter/Escape a Recent.
- Metadata nových typů Pages podle skutečného list kontraktu; viditelnost odpovídá Pages overview.
- Workspace switch během pending requestu, odhlášení/přihlášení jiného uživatele, odebrání přístupu, smazaná stránka a staré Recent.
- API 403/500, malformed response, prázdný list, dlouhý název, limitovaný list; ostatní skupiny fungují.
- Zavřená paleta nespouští nový periodický fetch, rychlé otevření/zavření nemíchá odpovědi.

### A2 navíc, pokud se implementuje

Prokázat autentizaci agenta, scope a revokaci, shodný evidence kontrakt, odmítnutí mutací, limity a ledger; vložené instrukce v logu nesmějí vyvolat nástrojovou akci. Bez těchto testů neoznačovat diagnostiku za bezpečně read-only.

## 8. Verifikační a review postup

Průběžně spouštěj cílené Vitest/Go testy změněných cest. Před dokončením implementace podle AGENTS.md: `go test ./... -count=1`, `go vet ./...`, při UI změnách `pnpm lint`, pro tyto zásahy také `pnpm build`. Relevantní frontend testy přes `pnpm exec vitest run <soubory>`; nepřidávat npm/yarn lockfile. Docker skip explicitně přiznat; skip není živá akceptace. Dodrž web/out placeholder pravidla.

Pokud vznikne migrace, zdůvodni ji oproti původnímu cíli bez persistence, přidej nový časový stamp a spusť migration lint. Změna rout/API vyžaduje odpovídající kontrakt a generování dle aktuálního repo procesu. Vhodné repo invarianty spustit podle CONTRIBUTING/CI.

Před merge ověř skutečně dokončené CodeRabbit review podle scripts/review-status.sh, nikoli pouze zelený rate-limit check. Tento dokument sám neautorizuje publikování, merge ani restart živé instance nad rámec pokynů implementační relace.

## 9. Odhad práce a náklady

Odhady jsou plánovací, ne naměřená produktivita ani nabídka dodavatele. Člověkoden = soustředěná práce jednoho vývojáře; zahrnuje cílené testy, nezahrnuje čekání na externí review.

| Varianta | Odhad | Modelová cena při 10–15 tis. Kč/den |
|---|---:|---:|
| A1, současné autorizované DTO stačí, bez nové agentní cesty | 2–3 dny | 20–45 tis. Kč |
| B, list a Pages kontrakt stabilní | 0,5–1,5 dne | 5–22,5 tis. Kč |
| Společná akceptace, integrace do aktuální větve a rezerva | 0,5–1,5 dne | 5–22,5 tis. Kč |
| Doporučený balík A1+B celkem | 3–6 dní | 30–90 tis. Kč |
| A2 navíc: serverový evidence builder + agentní read cesta / vysvětlení | +3–6 dní | +30–90 tis. Kč |

Pokud úvodní audit objeví chybějící korelaci nebo autentizační mezeru, není automaticky zahrnuta do těchto čísel; nejprve vyčíslit konkrétní opravu. Neplánovat nový sběr container telemetry v rámci rezervy.

A1+B nepotřebují nový server ani placenou službu, nepřidávají automatickou tokenovou spotřebu. Využijí současná data a malé dotazy při otevření. „Bez nového serveru“ neznamená nulovou spotřebu CPU; ověřit bounded queries, payload a počet requestů. A2 spotřebovává tokeny při použití; model/cenu a limity určit až podle skutečně zvoleného provideru. Volné tokeny v Claude snižují přímý náklad na implementaci, ne potřebu review a akceptace.

## 10. Co implementující Claude odevzdá

1. Krátký výsledek vstupní analýzy včetně HEAD a skutečných producentů/autorizace; seznam rozdílů proti tomuto zadání.
2. A1 a B jako konkrétní reviewovatelné změny; A2 jen jako explicitně oddělený rozsah.
3. Příklad výsledné reference a anonymizovaného podkladu, screenshot rutiny, issue a hledané Page; žádná skutečná tajemství.
4. Tabulku testů se skutečnými výsledky, commit/PR odkazy, aktuální stav review a přesné limity. Oddělit testované fixture chování, živé ověření a neověřené předpoklady.
5. Aktualizovaný handoff v docs/prd a release claimu podle repo pravidel. Zachovat cizí změny.

Hotovo znamená: člověk bez ručního hledání logů otevře správný běh, rozpozná známý stav a získá použitelný podklad; agent dostane v A1 stejná fakta prostřednictvím tohoto podkladu; paleta otevře konkrétní oprávněně viditelnou Page. Žádné nové automatické opravy či monitoring se za dokončení nepočítají.
