# Agent access / runtime — implementace Release 1.0

Navazuje na [rešerši](RESEARCH-AGENT-ACCESS-RUNTIME-HEARTBEAT-2026-09-27.md).
Tracking: [#2703](https://github.com/crewship-ai/crewship/issues/2703).
Uživatel autorizoval vývoj, testy a nasazení na dev1. Základ implementace je
`8dc421fdb` na main; jiné instance ani produkce nejsou cílem.

## Dodávky a hranice

| Balík | Stav | Akceptace |
|---|---|---|
| A1: směrované oprávnění sdílených souborů mezi crews | Implementováno a nasazeno na dev1, PR review/CI probíhá | Settings/API/CLI, none/read/read+delivery, stale update 409, role/workspace, odebrání dalšího requestu |
| A2: agent→agent / projektové granty | Připravený návrh, neimplementováno | stabilní resource ID, efektivní dědění, všechny čtecí cesty, shell hranice |
| B: omezený klientský běh a konverzace | Neimplementováno | žadatel→běh→výstup, historie, paměť a artefakty dvou klientů |
| C: service desired state / obnova po rebootu | Nasazeno na dev1; Docker ztráta kontejneru/data a server restart ověřeny; host reboot otevřený | durable running/stopped, rekonciliace, data/identity, žádná duplicita |
| D: credentials / revokace konkrétního grantu | D1: per-agent proxy grant snapshot/refresh implementován; přímá delivery a izolace dále otevřené | rozdílné lease, odebrání jedinému agentovi, výpadek autority, izolovaná delivery |
| E: Chat / Issues / Routines / Pages | E1: crew-bound chat/config IPC opraveno; společná omezená autorita nedokončena | negativní end-to-end matice včetně logů/streamů/delegace |
| F: levná kontrola práce před heartbeat | F1 nasazeno a otestováno na dev1; autoritní dedupe čeká na A2/B | žádné prázdné LLM wake, budget/dedupe/recovery bez oslabení lease |

Tabulka není prohlášení, že celý Release 1.0 je připraven. Každý další balík musí
mít vlastní reproduktor a testovací bránu. Sdílené UID crew zůstává důvěrovou
hranicí: A1 neizoluje libovolný shell a nepřidává klientovi právo jen na jednoho agenta.

## A1 — konečný kontrakt

- Link nadále určuje směr komunikace/delegace. `forward_file_access` a
  `reverse_file_access` zvlášť zužují shared-file API v každém směru.
- `none` nic; `read` výpis/čtení; `read_write` čtení a doručení souboru.
  Doručení není obecná editace celého projektu. Žádné implicitní credentials
  nebo soukromé HOME. Směr bez komunikačního linku zůstává nepřístupný.
- Migrace zachovává dosavadní `read_write` existujících linků. Nové linky ze
  Settings začínají `none`; staré API/CLI vytvoření bez nových polí zachovává
  historický default. POST existujícího linku nemění file permissions.
- MANAGER+ používá verzovaný PUT. Souběžná stará změna dostane 409; role,
  workspace a requester membership se kontrolují serverově. Nové čtení/zápis
  používá aktuální DB pravidlo. Již přijatý požadavek může doběhnout.
- Změna směru v existující UI cestě zachovává směrová file permissions i při
  nahrazení řádku a při rollbacku. Chyba načítání nehlásí prázdný workspace;
  neověřená nastavení nelze měnit. MEMBER vidí hodnoty bez editačních ovladačů.
- CLI: `crew file-access <connection> <requester> <level> <version>`, pozorovaná
  verze je v `crew connections --format json`.

## Nově potvrzený nález a oprava

`TestCrewFileAccess_DeliveryCannotOverwriteSharedRoot` před opravou vrátil 201
a přepsal umělý `shared/project.sh` uploadem na `../../project.sh`. `filepath.Join`
smazal traversal ještě před kontrolou cíle. Oprava validuje cestu před spojením
s `incoming/<sender>`. Po opravě je požadavek 400 a původní obsah zůstává zachován.
Toto je potvrzená chyba request path; netvrdí úplnou procesní/symlink izolaci crew.

`TestCrewFileAccess_CannotImpersonateSiblingRequester` před opravou prokázal
čtení (200) i upload (201) s tokenem crew C, ale `requester_crew_id` crew A.
Workspace kontrola sama nestačila. Obě file operace nyní používají také
`assertBoundCrewWorkspaceDB` na requester; cíl zůstává legitimně cizí crew.
Workspace-bound/master kompatibilita zůstává, nejde o novou agentí identitu.

Verzovaný DELETE chrání UI změnu směru před obnovením oprávnění ze staré
otevřené obrazovky. Neshoda vrátí 409 a UI znovu načte aktuální stav.

## Doložený experiment Linux

Na dev1 byl spuštěn pouze vlastní jednorázový Docker kontejner z již přítomného
image, bez sítě, s read-only rootfs, `cap-drop ALL`, `no-new-privileges`, UID 1001
a samostatným tmpfs `/probe`. Umělé agentí adresáře měly 0700, soubor obsahoval
pouze `synthetic-only` a měl 0400. Proces s HOME agent-b přečetl soubor agent-a.
Kontejner se po testu odstranil (`--rm`). Nebyla čtena skutečná hesla ani data
existujících crews. Výsledek potvrzuje omezení DAC při společném UID; nejde o
test všech ochranných vrstev živé Crewship instalace.

## Ověření

- Cílené backend testy A1, stávajících connections/messaging a traversal prošly.
- Settings: 20 testů prošlo; přidána změna přesného směru/verze a chyba načítání.
- Kompletní `pnpm test`: 790 souborů, 9 361 testů prošlo; `pnpm test:types`: clean.
- CLI: nový příkaz a předání chyby stale verze prošly.
- CI test-typecheck našel nepodporovaný parametr `exact` v Testing Library testu; nahrazen přesným regulárním výrazem, bez změny produktového kódu.
- TypeScript, ESLint (0 chyb, 30 existujících varování), produkční Next export
  a Go vet prošly v průběžném ověření.
- Finální `go test ./... -count=1`: exit 0, 146 testovaných balíků; `go vet ./...`: exit 0.
  Průběžné kontroly našly chybějící registraci a povinná pole OpenAPI; vše opraveno
  a ověřeno finálním celým průchodem. Strict docs inventory, invariants a migration lint prošly.
- Dev1 restart přes `systemctl reload crewship-ws@1` nasadil A1 a migraci. Živý
  API smoke se dvěma novými crews ověřil none/none, read, stale PUT a DELETE 409,
  revoke, aktuální listing i versioned delete 204. Obě dočasné crews odstraněny.
- Draft PR: [#2704](https://github.com/crewship-ai/crewship/pull/2704); vzdálené
  CI a review jsou samostatná zbývající brána, žádný merge nebyl proveden.
- Reálný reboot hosta, úplná izolace klientských běhů a všechny balíky B–F nejsou
  tímto ověřeny. Browser smoke prošel: Settings render i změna dropdownu A→B na read; B→A zůstalo none. Dočasná UI data odstraněna.

## Navazující implementační pořadí a release brány

1. **A2 + B: objektová autorita a klientský kontext.** Inventář entrypoints
   (HTTP, WS, download/export, proxy, interní tool, fronta) musí uvést konkrétní
   enforcement místo. Stabilní ID subjektu a resource; oprávnění read, edit,
   execute, publish a credential-use se nesmějí slít do write. Omezený běh
   uchovává žadatele, scope a verzi politiky; delegace scope jen zužuje.
   Spouštění z chatu/rutiny/issue musí používat stejný kontrakt. Test dvou lidí
   u stejného agenta musí pokrýt historii, soubory, paměť, log i stream.
2. **Procesní hranice A2.** Zachovat UID 1001/1002, zavést samostatný runtime
   podle důvěrové autority místo změn UID v existující crew. Read-only sdílení
   připojit jen do povoleného prostoru, write projekt má konkrétního vlastníka.
   Bezpečnostní test používá syntetická secrets a pokusy ze shellu; průchod
   souborovým API sám o sobě nestačí. Migrace interních crews je explicitní,
   nepřeruší dosavadní sdílenou práci ani ji nepřejmenuje na izolovanou.
3. **D: autorita credentials.** Nejdříve reproduktor odebrání jednoho agentího
   grantu při zachování jiného, dvě lease a stale sidecar. Potom versioned
   grant snapshot/refresh a definovaný limit zastarání pro omezený runtime.
   Proxy-only preference nezmění fakt, že plaintext doručený do procesu je
   pro tento proces čitelný. Každý delivery typ má vlastní negativní test.
4. **C: spravované služby.** Durable desired state a controller s lease;
   idempotentní adopt/create/start/stop; manuální Stop se po restartu zachová.
   Proces 24/7 patří do samostatné deklarované služby, ne do nehlídaného
   background procesu agenta. Nejdříve testy na izolovaném provideru, potom
   disposable kontejnery. Restart celého sdíleného Dockeru/hosta nesmí být
   součástí dev1 smoke; reboot brána vyžaduje samostatnou testovací instanci.
5. **E: sjednocení cest.** Pages zachovají vlastní existující granty; napojení
   na společný rozhodovací kontrakt má test změny identity publikujícího,
   spouštění akce, čtení výsledku a revokace. Routines/Issues zkontrolují
   oprávnění při přijetí i skutečném spuštění, včetně retry a recovery.
6. **F: heartbeat preflight.** Použít existující queue/trigger/lease. Levně
   zjistit vykonatelnou práci, změnu vstupů, budget a kapacitu; nebudit LLM
   bez práce. Dedupe zahrnuje autoritu, ne pouze agenta. Recovery nesmí
   slepě opakovat externí účinek. Měřit počet probuzení bez práce, tokeny,
   čekání na práci a duplicity proti stejnému vzorku událostí.

Každá dodávka má migraci/kompatibilitu, Settings/API/CLI, audit, pozitivní i
negativní test a dev1 smoke. Výkon měřit odděleně: p50/p95 autorizace a seznamů,
počet SQL dotazů na request, cold/warm start, RAM izolovaného runtime a služby,
čas zotavení, tokeny na dokončený úkol. Bez měření uvádět pouze očekávaný směr
dopadu. A1 nepřidává background loop; nahrazuje kontrolu linku jedním dotazem
na link a jeho file permission. Výkonový benchmark zatím nebyl proveden.

## Dodávka C a D1 — pokračování 27. 9. 2026

C ukládá opt-in `running/stopped` do `service_runtime_intents`. Controller
běží mimo agentí run, používá dvouminutovou DB lease a verzované potvrzení výsledku.
Kontroluje až 100 splatných záznamů, nejvýše čtyři souběžné operace; zdravý stav
obnovuje po 30 sekundách, chybu po minutě, polling každých 15 sekund. Agentí
startup předává spravované služby výhradně controlleru. Ruční Stop celé crew
ukládá Stop jejích spravovaných služeb. Legacy služby bez záznamu zůstávají
on-demand. Settings rozlišují požadovaný a naposledy potvrzený stav; změnu
smí MANAGER+, stará verze dostane 409. CLI a OpenAPI pokrývají stejný kontrakt.

Při chybě konfigurace/credentials se controller pokusí službu zastavit, aby
neponechal běžet proces se starým env. Do API ukládá pouze bezpečný kód chyby.
Chyba jedné deklarace neblokuje ostatní. Jde o eventual reconciliation, nikoli
atomickou revokaci právě běžícího procesu. Nedostupný Docker může zastavení
odložit; UI musí dál ukazovat error. Proces může po odebrání oprávnění běžet do
další kontroly a dokončení Stop. Záloha zahrnuje i durable service intent;
obnovená nevypršená lease může rekonciliaci odložit nejvýše o původní TTL při
synchronizovaných hodinách. Reálný host reboot vyžaduje izolované prostředí.

D1 přidává do boot payloadu sidecaru explicitní mapu credential→agent→lease.
Snapshot používá existující delivery SQL v jedné read transakci, včetně
precedence explicitního grantu nad bindingem. Metadata endpoint je vázaný na
crew/workspace, nedoručuje plaintext. Refresh každou minutu; snapshot starší
než dvě minuty zakáže nové použití. Chybějící credential/agent je deny,
rotace grace při retry respektuje aktuální autoritu stejného agenta.
Odlišná lease agenta A již neexpiruje credential agenta B. Payload je vedený
přes agent-config, assignment/query, chatbridge a orchestrator.

Kompatibilita D1: crewless a staré payloady zachovávají legacy chování;
existující runtime potřebuje obnovení sidecaru s novým payloadem/binary.
Refresh nepřidává nové hodnoty credentials, pouze mění autoritu již doručených.
Klíč může zůstat v paměti sidecaru, i když jej konkrétní agent už nesmí použít.
Přímé env/file credentials a shared-UID shell tím nejsou izolované.
Snapshot nyní dělá jeden delivery dotaz na každého člena crew; benchmark pro
velké crews ještě není proveden. Žádné naměřené zlepšení výkonu netvrdíme.

První celý Go průchod odhalil chybějící registraci nové tabulky v backup
kontraktu a YAML tagy CLI; obojí doplněno. Cílené testy ověřují dvě instance
controlleru, obnovu intentu, Stop při startu agenta, izolované chyby credentials,
role/workspace/stale update a bezpečné chyby. Finální gate a dev1 evidence se
zapisují až po skutečném dokončení. A2/B/E/F zůstávají otevřené.

### Dokončená verifikace C/D1 (`292a1c570`)

- Celý Go průchod: 146 balíků, exit 0; go vet exit 0; race testy
  `TestCredentialGrants*` exit 0. Celý frontend: 791 souborů, 9 365 testů.
- Test types, lint (0 chyb/30 existujících varování), Next static export,
  strict docs inventory, migration lint a agents-invariants prošly.
- Dev1 nasazen přes `systemctl reload crewship-ws@1`, build identity ověřena.
- Vlastní dočasná crew se službou alpine:3: běh bez agenta, odstranění
  kontejneru, automatická obnova do nového ID a zachování `synthetic-stable`
  v pojmenovaném svazku. Poté verzovaný Stop a další restart dev1; služba
  zůstala zastavená. Host/Docker daemon reboot nebyl proveden.
- Browser ověřil Crew administration → Services, skutečný Stop a oba
  ovladače. Dočasná crew, kontejnery a označené svazky odstraněny.
- Remote CI/review nového commitu je samostatná neuzavřená brána; draft PR
  se nemerguje podle starého zeleného CodeRabbit statusu.


## F1 — levná kontrola práce v Routines

Nový deterministický query source `assigned_issues` používá uloženého autora
rutiny a jeho crew/workspace. Vrací jen `has_work`, žádný obsah cizích issues.
TODO kandidát musí být delegovaný autorovi, v agentím režimu, bez otevřeného
blockeru a bez aktivní assignment/routine execution. Nepřítomná/smazaná nebo
cizí identita a DB chyba jsou error, nikoliv false. Save gate požaduje autora.
Query má dvousekundový timeout a nevolá model. Podmíněné runtime kroky již
nevyvolají předběžný prewarm; kontejner se spouští až po splnění podmínky. To šetří prázdné wake, ale
kladný výsledek může zaplatit cold-start latenci až na agentím kroku; benchmark
2,33 ms měří pouze query, nikoliv tuto latenci.

Jde o signál, ne rezervaci práce. Pozitivní výsledek může během čekání/resume
zestárnout. Agent musí použít existující atomický issue start, budget a aktuální
autoritu. F1 nezavádí další timer, neslučuje klientské autority a nedokončuje
A2/B. Běžné denní reporty zůstávají nezměněné, protože gate je opt-in v DSL.
Úplný příklad a limity jsou v Routines guide/cookbook.

Cílené testy: prázdná fronta = 0 agentích volání; vlastní TODO = 1; cizí
workspace/crew/agent, human mode, blockers (včetně legacy opačného směru),
živá assignment/rutina, smazání autora, DB failure, chybějící autor při save,
žádný prewarm podmíněného agenta. Query ověřená i na plném migrovaném schématu.
Lokální `BenchmarkAssignedIssuesPreflight`, 5 000 historických DONE issues,
Intel i7-12700, SQLite fixture v tmpfs: 2 329 127 ns/op, 3 236 B/op,
39 allocs/op (2s benchmark). Toto je testovací průměr, nikoliv produkční p95.

Vzdálený CodeQL označil sčítání délek při rekonstrukci ciphertext+tag ve starším
Decrypt. Nová alokace používá `len(data)-len(iv)` po existující kontrole délky;
copy zachovává GCM layout. Celý encryption test balík včetně layout/TS
kompatibility prošel. Z nálezu samotného netvrdíme prokázaný exploit.


### Dokončená verifikace F1 (`990bfee8c`)

Celý `go test ./... -count=1` znovu exit 0 (146 balíků), `go vet ./...`
exit 0, strict docs inventory a diff-scoped timestamp lint prošly. Po malé
změně alokace v encryption samostatně prošly všechny encryption testy.
Dev1 znovu nasazen přes systemd reload, build identity ověřena.

Živý CLI smoke vytvořil vlastní crew a autora, uložil rutinu s query a
podmíněným `agent_run`, poté ji spustil. Výsledek: COMPLETED, `has_work=false`,
`work=<skipped>`, 61 ms za celou rutinu a $0.0000. Jde o jeden lokální smoke,
nikoliv výkonový percentil. Dočasná rutina/agent/crew odstraněny; auditní
historie přirozeně zůstává podle běžné retence. První harness předpokládal JSON
stdout z `routine run -f json`; tento existující příkaz vypisuje text. Harness
byl upraven na skutečný výstup, samotný run proběhl už při prvním pokusu.

Remote CI na `990bfee8c` běží znovu; výsledek CodeQL po úpravě alokace je
teprve potřeba potvrdit. CodeRabbit review je při zápisu pending; draft není mergovaný. Celek Release 1.0 není
akceptovaný: otevřené jsou A2/B, návazná matice E, přímé credentials a izolovaná
host reboot akceptace. Žádná současná workspace role se neslibuje jako přístup
pouze ke konkrétnímu agentovi.


### Linux UID hranice pro sidecar — další živý experiment

Na dev1 jednorázový alpine:3 kontejner, bez sítě, read-only rootfs,
cap-drop ALL, no-new-privileges, 64 MiB/0,25 CPU/16 PID. UID 1002 vytvořilo
v soukromém tmpfs `/secrets` (0700) soubor pod umask 077 se syntetickou
hodnotou. Kontrolní čtení UID 1002 uspělo. `docker exec --user 1001:1001`
obdržel Permission denied při čtení souboru i `/proc/1/environ` procesu UID
1002. Kontejner byl odstraněn. To dokládá tuto konkrétní Linux DAC/proc hranici;
není to test všech sidecar endpointů ani důkaz agent→agent izolace (ti sdílí
UID 1001). Nebyla použita ani přečtena reálná credentials.

Doplňující race gate `go test -race ./internal/api -run TestManagedServices
-count=1` prošla (46,726 s): DB lease dvou controllerů, obnova, role/CAS,
manuální Stop i ztráta credentials. Pracovní strom po dodávce obsahuje pouze
committed změny této větve. Živý dev1 produktový build je `990bfee8c`; pozdější
commity přidávají pouze dokumentační akceptaci.

## E1 — interní chat a konfigurace respektují crew token

Regresní reproduktor `TestInternalChatCrewBoundary` před opravou doložil:
crew-bound token vytvořil chat pro sousedního/cizího agenta (201), resolve
vrátil konfiguraci sousední crew (200) a změny názvu/počítadla jejího chatu
uspěly (200). Samotný middleware vkládající workspace do query tyto cesty
nechránil. Test používá dvě workspace a dvě crews v jedné workspace, reálné
odvozené tokeny a autentizační middleware; vše nad syntetickou testovací DB.

Create nyní porovnává workspace s tokenem a ověřuje živého agenta ve zvolené
workspace i crew. Opakované chat ID uspěje pouze při shodě agenta, workspace,
routine run/step a zakladatele; jiná identita je konflikt 409. Čtení a změny
chatu filtrují crew přímo v SQL. Resolve konfigurace preferuje scope z
ověřeného kontextu před URL a filtruje crew ještě před sestavením konfigurace
či delivery credentials. Host a workspace tokeny si zachovávají dosavadní
rozsah; testy to výslovně ověřují.

E1 opravuje hranici crew IPC. Nezavádí identitu klienta napříč delegací, nové
agent→agent granty ani izolaci procesů sdílejících UID 1001. B, A2 a zbytek E
zůstávají otevřené. Celý `go test ./... -count=1` i `go vet ./...` prošly; cílené
race testy (52,905 s), doplňující test idempotence routine kroku a agentí
invarianty také. Předchozí CodeQL kontrola PR již hlásí SUCCESS, CodeRabbit
nových změn zůstává pending. Živá dev1 akceptace této změny následuje.

### Živá akceptace E1 — dev1 `4a0edba80`

Nasazeno přes `systemctl reload crewship-ws@1`; stav potvrdil nový build a
běžící API/Next. Smoke přes skutečné interní HTTP API použil crew-bound
tokeny dvou nově vytvořených syntetických crews. Vlastní vytvoření chatu 201,
retry 200, resolve 200 a změna počítadla 200; cizí agent při create 404,
cizí chat/agent resolve 404, změna cizího názvu/počítadla 404, pokus použít
cizí chat ID pro vlastní identitu 409. Test nevypsal tokeny ani obsah
konfigurací. Všechny testovací chaty, agenty a crews odstranil veřejným API/CLI.

Na stejném buildu znovu prošly živé smoke A1 (směry, revoke, stale PUT/DELETE)
a F1 (uložená rutina, prázdná fronta, dokončení s přeskočeným agentím krokem).
Jejich dočasné zdroje také uklizeny. Lokální plná Go brána: 146 balíků,
exit 0; `go vet` exit 0; pre-commit golangci-lint a secret scanner prošly.
UI kód se v E1 neměnil; nasazení vytvořilo nový web export. Vzdálené CI a
review commitu E1 je nutné posoudit samostatně; draft PR se nemerguje.

## E2 — autorita při evidenci běhu; opravy review C/D1

`TestInternalRunCrewScope` před opravou prokázal vytvoření běhu sousední crew,
přijetí cizí workspace v těle, podvržení vazby na cizí chat (201) i přijetí
všech změn stavu cizího běhu (200). Create nyní kontroluje workspace tokenu,
crew agenta a případnou vazbu chat→agent→workspace před zápisem do journalu.
Update kontroluje crew vlastníka běhu před terminal eventy i před odpovědí
na neterminální RUNNING. Neautorizovaný request nesmí změnit stav agenta ani
zapsat událost. Test zachovává pozitivní vytvoření a dokončení vlastního běhu.
Toto je další uzavřená IPC cesta, ne dokončení klientské autority balíku B.

Review předchozího commitu upozornilo na dvě potvrzené provozní chyby:

- Změny grantů/lease měnily fingerprint sidecaru. Fingerprint nyní obsahuje
  režim grantů, nikoliv jejich proměnlivý obsah nebo nepoužitý legacy seznam
  příjemců. Startup payload zůstává úplný; změna skutečného tokenu se stále
  rozlišuje. Test chrání refresh, revokaci, legacy režim i nezměněný payload.
- Automatické zastavení při změně síťové politiky používalo explicitní Stop,
  čímž přepisovalo službám durable intent na stopped. Samostatná IPC cesta
  recycle zastaví runtime a služby, ale zachová desired state; controller je
  obnoví. Uživatelský Stop nadále ukládá stopped. Testy kontrolují obě cesty
  i volání z network-policy handleru.

Doplněna diagnostika DB chyb controlleru a přechodu výpadek/obnova grant
refresh. Refresh neloguje každou minutu další stejnou chybu; logy neobsahují
response body, URL chyby ani tajné hodnoty. Test ověřuje omezení opakovaných
logů i obnovení hlášení po novém výpadku. Výsledek plné brány a živá dev1
akceptace budou doplněny po doběhnutí.
