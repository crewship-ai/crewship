# Agent access, Linux runtime a heartbeat — podklad pro Release 1.0

Datum: 2026-09-27. Výchozí kód Crewship: `884d5361954c082fcc5a8dcb2c071a5692a68950`, větev `feat/chat-workspace-preview-dev2` v instanci 1. Paperclip: `640dee18029f7651d56ae0f1828d04228f9ab044`. Jde o rešerši implementace a primárních zdrojů doplněnou cílenými testy (§12), nikoli penetrační test, benchmark nebo potvrzení produkční připravenosti. Nebyly změněny runtime, oprávnění, data ani konfigurace běžící instance. Starší handoffy jsou kontext; tvrzení o dnešním chování níže vycházejí z kódu uvedeného commitu.

Aktuální implementační stav a následné opravy: [delivery tracker](AGENT-ACCESS-RELEASE-1-IMPLEMENTATION-2026-09-27.md). Níže je zachován výchozí audit; není to stav po nasazení A1.

## Závěr a rozhodnutí pro 1.0

Nejvyšší hodnotu má sjednocení autorizace a skutečné hranice běhu. Heartbeat je doplněk nad již existující frontou a plánovačem. Nový časovač neřeší úniky dat, oprávnění ani spolehlivý provoz aplikací.

Produktový slib „klient smí komunikovat pouze s Mařenou a vidět svou práci“ dnes nelze odvodit z role VIEWER, adresáře agenta ani výběru agenta v UI. Je třeba oddělit:

1. Kterému člověku je přístupný konkrétní objekt a operace.
2. S jakým oprávněním agent vykonává právě tuto práci.
3. Jaké soubory, procesy, síť a přihlašovací údaje může runtime skutečně použít.
4. Komu lze vrátit výsledek, log, paměť nebo publikovanou aplikaci.

Pro 1.0 doporučuji dvě výslovné provozní hranice: důvěryhodná interní crew se sdíleným prostředím a omezený klientský přístup s izolovaným během/datovým prostorem. Druhou variantu nezpřístupnit jako bezpečnostní slib, dokud neprojde níže uvedenými negativními testy. To není důvod odložit veškeré 1.0: interní režim lze dodat s pravdivě popsanou hranicí. Pokud má být klientské sdílení součástí 1.0, jeho izolace je podmínkou vydání této funkce.

## 1. Ověřený současný stav

| Oblast | Zjištění | Důsledek |
|---|---|---|
| Agent Files | `AgentFiles` a download kontrolují workspace a `canRole(..., "read")`. `read` propouští VIEWER; nejde o grant na konkrétního agenta. | Cizí agent ve stejném workspace není chráněn pouhou volbou agenta v panelu. |
| Agentí výstupy | Listing přidává soubory z kořene crew. Resolver odmítá sourozenecký agentí namespace v této cestě, ale povoluje soubory kořene crew. | Existují kontroly cest; tvrzení „API dovolí jakýkoliv soubor kontejneru“ by bylo příliš široké. Sdílený kořen však záměrně není soukromý. |
| Container Files | `AgentContainerFiles` ověří workspace a crew, potom předá požadavek na crew container-files bez agentího filtru. | Samotný `agentId` v URL není izolace souborového pohledu. |
| Chat historie | `ListChats` filtruje agent/workspace/kind; uživatel slouží pro unread stav, nikoli vlastnický filtr dotazu. | Je nutné zvlášť definovat soukromí konverzací různých lidí se stejným agentem. |
| Linux | Crew runtime má standardně UID/GID `1001:1001`, odebrané capabilities, no-new-privileges, read-only rootfs a CPU/RAM/PID limity. Privileged konfigurace část ochrany mění. | Hardened crew kontejner existuje, ale sdílené UID není hranice mezi agenty uvnitř něj. |
| HOME a paměť | Trvalý stav `/crew/agents/<slug>`, HOME běhu `/crew/runs/<slug>/<runID>`, výstupy `/output/<slug>/runs/<runID>`. `.memory` odkazuje na trvalou paměť agenta. Episodic scope je own nebo crew_shared plus workspace. | Oddělené adresáře brání kolizím, ale samy neprokazují izolaci klientů ani autorizaci paměti podle člověka. |
| Terminál | Kontrola členství ve workspace crew, VIEWER odmítnut; MEMBER připuštěn. | Terminál je přístup k prostředí crew, nelze jej automaticky zahrnout do „smí chatovat s jedním agentem“. |
| Routines | Spuštění vyžaduje roli nebo `routine.run`, dále governance a další podmínky. Capability je na členství ve workspace, sama není jmenovitý grant jedné rutiny. Script kroky sdílejí prostředí autorovy crew. | Backend již obsahuje důležité brány. Je nutné doplnit rozsah konkrétní rutiny a navazující identity, nikoli je obejít novou univerzální rolí. |
| Pages | Mají vlastní granty, lidská složková ACL a kontroly agentích grantů včetně aktuálních oprávnění vydavatele. Složkové ACL nepřiděluje přístup agentům. | Znovu použít existující pravidla; nezavést vedle nich druhý nezávislý systém sdílení. |
| Pages Apps | Aktuální v1 je publikovaný frontend s omezeným SDK a deklarovanými akcemi. Není to libovolný per-Page backend/server. | „Mařena spustí Python server“ je samostatný runtime use case, ne současný kontrakt Pages Apps. |
| Exposed ports | `/exposed/{token}` používá token v URL jako přístupovou capability bez uživatelského přihlášení. | Sdílení odkazem není totožné s přístupem přihlášeného konkrétního klienta; revokace a volba režimu musí být výslovné. |

Podklady v kódu: [proxy_files.go](../../internal/api/proxy_files.go), [helpers.go](../../internal/api/helpers.go), [routes_files.go](../../internal/server/routes_files.go), [agent_chats.go](../../internal/api/agent_chats.go), [docker_container.go](../../internal/provider/docker/docker_container.go), [run_paths.go](../../internal/orchestrator/run_paths.go), [episodic/recall.go](../../internal/episodic/recall.go), [terminal/handler.go](../../internal/terminal/handler.go), [capabilities.go](../../internal/api/capabilities.go), [pipelines_exec.go](../../internal/api/pipelines_exec.go), [pages_grants_authz.go](../../internal/api/pages_grants_authz.go), [port_expose_list_revoke_serve.go](../../internal/api/port_expose_list_revoke_serve.go).

Navazující kontrakty: [chat workspace](chat-workspace-dev2-2026-09-25.md), [děděná Pages oprávnění](pages-folder-permissions-linux-model-2026-09-13.md), [Pages Apps v1](pages-apps-v1.md), [Routines audit 20. září](reports/routines-security-performance-audit-2026-09-20.md). Nálezy ze starého auditu se tímto automaticky neprohlašují za stále otevřené.

## 2. Oprávnění napříč produktem

RBAC role je základ, ale samotná role nestačí. Doporučený kontrakt doplňuje konkrétní resource grant a kontext delegace. Výchozí chování pro omezeného klienta je odmítnutí; kontrola musí fungovat na serveru při každé cestě k objektu. Odpovídá to [OWASP Authorization Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html).

Následující názvy jsou návrh významu oprávnění, nikoliv existující API:

| Objekt | Samostatně rozhodované operace |
|---|---|
| Agent | objevit v seznamu, chatovat, konfigurovat |
| Konverzace | číst historii, přispívat, spravovat účastníky |
| Soubory / projekt | list/read, write, případně delete a sdílení |
| Artefakt / výstup | zobrazit, stáhnout, zveřejnit |
| Issue | zobrazit, komentovat, upravit, přiřadit práci |
| Routine | zobrazit, spustit konkrétní schválenou definici, upravit, schválit, plánovat |
| Page / aplikace | použít UI/data, upravit zdroj, build, publikovat, spustit deklarovanou akci |
| Služba | použít, zobrazit provozní stav/logy, start/stop, nasadit verzi, spravovat oprávnění |

„Smí chatovat“ nesmí automaticky dávat historii jiných klientů, všechny soubory agenta, crew shell ani agentovy credentials. „Smí aplikaci používat“ nedává zdrojové kódy. „Smí upravovat zdroj“ nesmí bez dalšího měnit právě běžící privilegovanou službu: změna skriptu, který se následně spustí s vyšší autoritou, je nepřímé oprávnění vykonávat kód. Publikace/spuštění má proto používat schválenou neměnnou verzi nebo při změně znovu vyžadovat příslušnou autoritu.

Je nutné rozhodovat zvlášť o lidském žadateli, agentovi, vykonávajícím běhu a případné službě. `created_by` nebo `opened_by` je auditní stopa, nikoli samo oprávnění. Kontext běhu má serverově ověřitelné workspace, agent, run, původ požadavku, žadatele a omezení delegace. Agent nesmí autoritu rozšířit změnou hlavičky, task argumentu nebo založením podúkolu.

Pro interaktivní práci jménem klienta je výchozí přístup průnik toho, co může klient delegovat, co může agent a co dovoluje konkrétní běh. Schválená služba může legitimně používat oprávnění, která klient nemá (např. vystavit účetní doklad bez zpřístupnění celé databáze); musí jít o omezenou operaci se svým servisním oprávněním, kontrolou vstupů a výstupů, nikoli obecný shell s autoritou vlastníka. Plánovaná rutina potřebuje trvalou deklarovanou autoritu a vlastní pravidla revokace, ne token posledního člověka v chatu.

Stejná pravidla musí pokrýt REST, WebSocket/SSE, CLI, sidecar IPC/MCP, hledání, počty, strom souborů, náhledy, exporty, run logy, notifikace a odkazy z Work. Nestačí filtrovat detail: název souboru nebo náhled zprávy už může být citlivý údaj. UI čerpá efektivní oprávnění ze serveru.

Revokace má jasné hranice: nové požadavky/claimy již odmítnout, odpojit nebo znovu autorizovat streamy, před citlivým krokem ověřit aktuální autoritu; u běžících procesů určit stop/reconcile postup. Již přečtená data nelze vzít zpět a `chmod` automaticky neodvolá otevřený file descriptor. Neměnit mlčky existující Pages kontrakt, kde autorizovaný požadavek může doběhnout; přísnější runtime revokaci definovat explicitně.

## 3. Linuxová hranice a vlastní adresář

`HOME` a pracovní adresář určují prostředí a relativní cesty. Neomezují proces na podstrom. Proces se stejným UID může používat další dostupné soubory stejného vlastníka; `0700` mezi dvěma procesy se stejným UID takové oddělení nevytvoří. POSIX `x` na adresáři znamená průchod, nikoli produktové právo „spustit aplikaci“. [Linux path resolution](https://www.man7.org/linux/man-pages/man7/path_resolution.7.html)

| Varianta | Bezpečnost a vhodnost |
|---|---|
| Současná crew, stejné UID, více HOME | Vhodné jako sdílená důvěryhodná dílna. Nevhodné jako vzájemná izolace agentů/klientů. |
| Různá UID a ACL ve společném kontejneru | Umí oddělit DAC přístup, ale vyžaduje správné identity, skupiny, procesní/síťovou izolaci a rozsáhlou migraci. Navíc mění zdejší invariant UID 1001/1002. Nedoporučeno jako rychlá oprava. |
| Oddělený runtime pro odlišnou autoritu | Doporučeno pro omezené klientské běhy: vlastní viditelné mounty, identity/capabilities, paměťový kontext a síťová pravidla. Uvnitř lze zachovat UID 1001. |

Samostatný kontejner sám nestačí, pokud opět připojíme celé `/crew`, společné secrets, Docker socket nebo síť ke všem interním službám. Mountovat jen konkrétní oprávněné zdroje, read-only kde stačí; oddělit zapisovatelné výstupy a servisní data. Read-only mount zakazuje zápis touto cestou, nikoli čtení a odeslání ven. Když se mountuje stejný hostitelský strom podruhé zapisovatelně, ochrana se obchází touto druhou cestou. [Docker bind mounts](https://docs.docker.com/engine/storage/bind-mounts/)

Kontejnery sdílejí kernel; nejsou absolutní izolace nepřátelských tenantů. Pro vyšší nároky vyhodnotit další sandbox/VM. Rootless nebo user namespaces chrání hostitelskou hranici, ale neřeší objektová oprávnění Crewshipu ani společně připojená data. [Docker security](https://docs.docker.com/engine/security/), [rootless](https://docs.docker.com/engine/security/rootless/)

Důležité je oddělit i dvě konverzace se stejným agentem, pokud mají různé čtenářské oprávnění. Session ani agentí paměť nesmí přenést data klienta A ke klientovi B. Auditovat recall, konsolidaci, checkpointy, přílohy a cache; model už načtená data nedokáže bezpečnostně „zapomenout“ pouhým systémovým pokynem. V tomto průchodu je doložen agent/crew scope recallu, ne kompletní důkaz úniku ani kompletní klientská izolace.

## 4. Agent spí, služba běží 24/7

Tři odlišné stavy: agent právě nevolá model; kontejner je stále running; aplikační služba je running a zdravá. Spánek agenta nevyžaduje zastavení služby. Služba potřebuje procesní supervision, nikoli LLM heartbeat.

Současný [idle reaper](../../internal/orchestrator/orchestrator_lifecycle.go) chrání agentí běhy, připojený terminál a script kroky pomocí holds; před stopem navíc kontroluje aktivní port exposure a tmux. Hodnota crew TTL <= 0 znamená nezastavovat. Libovolný `nohup script &` bez těchto vazeb není v doloženém modelu obsazenosti registrován; po expiraci TTL proto může přijít o proces. Jde o závěr z podmínek reaperu, nikoli živě provedený kill test.

Deklarované crew services už běží v samostatných kontejnerech a idle reaper je nezastavuje. Existuje konfigurace image/command, volumes, env refs, healthcheck a živý inventory. Současná restart policy je `on-failure` s nejvýše třemi pokusy; tato Docker policy sama nezaručuje návrat služby po restartu Docker daemonu. Host reboot a obnovení služby je nutné ověřit v celém startup/reconcile toku. [Crew services decoder](../../internal/crewstart/services.go), [service provider](../../internal/provider/docker/sidecar.go), [Docker restart policies](https://docs.docker.com/engine/containers/start-containers-automatically/)

Doporučení: rozšířit existující service model o explicitní vlastnictví, grants, požadovaný stav a schválenou verzi aplikace. Zdrojový projekt může patřit agentovi; běžící služba má vlastní lifecycle, omezené credentials a persistentní data. Nesmí záviset na dočasném HOME nebo tokenu konkrétního agentího běhu. Nezavádět druhý konkurenční mechanismus start/stop vedle provideru.

Minimum provozního kontraktu: start/stop, health a restart/backoff, rozlišení crash/OOM, CPU/RAM/PID a log/disk limity, trvalá data a obnova ze zálohy, restart hosta, audit nasazení, aktualizace a rollback, autentizovaný ingress a revokace. Healthcheck sám nerestartuje každý nezdravý Docker kontejner; musí být určeno, kdo reaguje. „24/7“ na jednom hostu není vysoká dostupnost při výpadku hosta. [Docker resource constraints](https://docs.docker.com/engine/containers/resource_constraints/)

Do 1.0: využít existující deklarované služby a doložit provozní kontrakt; samoobslužný hosting libovolných agentem generovaných backendů ponechat za touto hranicí, dokud nemá stejnou izolaci a autorizaci. Statické Pages Apps tím nejsou blokovány a zůstávají ve vlastním kontraktu.

## 5. Co převzít z Paperclipu

Paperclip heartbeat je pracovní běh: čas/událost → oprávnění a budget → adapter → převzetí úkolu → práce/delegace → výsledek/session. Má také volbu přeskočit časový wake bez vykonatelné práce, slučování některých probuzení a omezená pokračování pro určité výsledky bez pokroku. [Protokol](https://github.com/paperclipai/paperclip/blob/640dee18029f7651d56ae0f1828d04228f9ab044/docs/guides/agent-developer/heartbeat-protocol.md), [implementace](https://github.com/paperclipai/paperclip/blob/640dee18029f7651d56ae0f1828d04228f9ab044/server/src/services/heartbeat.ts)

Crewship už má plánovač, durable dispatch pro schedules/webhooky, assignments a mentions, checkpointy a outcome kontrakt. Není prokázáno, že všechny zdroje práce sdílejí jeden dispatcher; nerozšiřovat tento slib z jedné cesty na všechny. [Scheduler](../../internal/scheduler/durable_fire.go), [dispatch wiring](../../internal/api/webhook_dispatcher_wiring.go), [outcomes](../../internal/orchestrator/outcome.go)

Převzít zejména kontrolu před voláním modelu: je nová vykonatelná práce, platná autorita, budget a volná kapacita? Události obsluhovat primárně přímo, časový sweep použít jako pojistku přehlédnuté práce. Sloučení musí respektovat workspace, agenta, zdrojový úkol i autoritu; nikdy nesmí spojit požadavky dvou klientů do společného kontextu. Blokovaný úkol bez změny podmínek nemá opakovaně budit model.

Sémantické pokračování není retry procesu ani obnovení lease. Navázat na stávající outcome kontrakt; omezený počet pokusů, budget, backoff a viditelná eskalace. Úspěšný exit procesu ještě není dokončení úkolu. Automaticky neopakovat akci s nejistým externím účinkem (např. odeslání nebo platba) bez idempotency/reconciliation. Rozšíření autonomních pokračování má přijít až po auditovatelné autoritě.

Existující [`dispatch.heartbeat`](../../internal/dispatch/dispatcher.go) obnovuje lease běhu. Není důvod přejmenováním či novým tickerem zavést druhý vlastnický protokol.

## 6. Váha, náročnost a dopad

Skóre je odborná prioritizace 1–5 (5 = zásadní), nikoli naměřené zrychlení. Náročnost M/L/XL je relativní implementační rozsah včetně migrace a ověření, ne závazný termín.

| Práce | Bezpečnost | Spolehlivost | Náročnost | Očekávaný provozní dopad | Doporučení |
|---|---:|---:|---|---|---|
| Objektové granty a úplné pokrytí všech čtecích/výkonných cest | 5 | 4 | XL | Další DB autorizace; u seznamů nutné dávkové dotazy a indexy | P0 pro omezené klienty |
| Autorita konkrétního běhu, delegace a revokace | 5 | 5 | XL | Kontroly při přijetí, spuštění a citlivých operacích | P0 pro omezené klienty |
| Izolace runtime, dat a paměti různých autorit | 5 | 5 | XL | Více kontejnerů a cold starts; lepší omezení dopadu havárie | P0 pro omezené klienty |
| Negativní E2E matice, audit a náhled efektivních práv | 5 | 5 | L | Auditní zápisy/retence; delší CI | P0 release gate |
| Oddělený lifecycle služeb a bezpečné uspávání | 4 | 5 | L | Trvalá RAM/CPU pro skutečně běžící služby | P0, pokud slibujeme služby 24/7 |
| Kontrola práce před LLM wake, dedupe a recovery sweep | 2 | 4 | M | Potenciálně výrazně méně prázdných LLM běhů; levné DB čtení | P1 pro 1.0 |
| Automatická pokračování podle kvality výsledku | 2 | 4 | L | Může zvýšit spotřebu tokenů i počet externích akcí | P1 úzce a s limity; širší autonomii později |
| Obecný hosting libovolných generovaných backendů | 3 | 3 | XL | Nové runtime, síťové i provozní náklady | P2 po 1.0 |

Nejlepší poměr hodnoty a zásahu má z heartbeat části levná předběžná kontrola. Největší hodnotu pro firemní důvěru mají P0, i když jsou náročnější a samy nezrychlí produkt. Bez čísel z benchmarku nelze poctivě tvrdit „RBAC zpomalí jen o 2 %“ nebo „kontejner na agenta stojí X MB“.

Výpočty pro plánování měření, nikoli výsledky benchmarku:

- 100 agentů buzených každých 5 minut znamená až 28 800 plánovaných příležitostí k běhu za den. Každých 30 minut je 4 800. Rozdíl 83,3 % je pouze počet ticků; úspora tokenů závisí na skutečné práci, délce kontextu a eventových bězích.
- Lease interval 10 s znamená při 100 aktivních pokusech přibližně 10 obnov za sekundu, před retry a dalšími zápisy. Měřit SQLite contention; neopravovat výkon oslabením fencing/recovery.
- UI polling po 5 s při 100 aktivních pohledech je přibližně 20 požadavků za sekundu pro jednu takovou cestu. Autorizace seznamu po jednom objektu by zavedla N+1; ověřit konstantní počet dotazů, autorizovanou pagination a ETag/invalidation.

Měřit stejný workload před/po: 10/50/100 agentů, neaktivní i aktivní stav, více klientů, souběh Pages/rutin/služeb. Zaznamenat p50/p95/p99 API a wake-to-start, cold/warm start, RSS a CPU včetně Docker daemonu, DB writes/lock waits, počet docker exec, tokeny na dokončený úkol a prázdné běhy. Rozlišit hard limit, skutečnou spotřebu a rezervovanou kapacitu. Číselné SLO a budget regresí stanovit nad baseline; nejsou v této rešerši naměřeny.

## 7. Pořadí dodávky a akceptační brány

1. Uzavřít význam klientského přístupu a vytvořit inventář všech cest k objektům včetně background work. Neoznačovat VIEWER za „jen jeden agent“. Pro migraci zachovat interní režim výslovně, nové omezené účty nechat bez implicitních workspace grantů.
2. Zavést sdílený rozhodovací kontrakt oprávnění, navázat stávající Pages/routine pravidla a propagovat omezenou autoritu až do runtime. Současně připravit izolovaný runtime/datový prostor pro omezené běhy; samotné API brány nepovažovat za hotový celek.
3. Ověřit tok člověk → chat/issue/Page/routine → queue → agent/script → soubor/služba → výstup včetně změny oprávnění během čekání. Kontrolovat source/code drift před spuštěním a publikací.
4. Doložit samostatný service lifecycle a idle/restart kontrakt. Potom přidat levnou kontrolu práce před heartbeat a měřit přínos.

Povinné scénáře před slibem omezeného klientského přístupu:

- Dva lidé A/B, agenti X/Y v jedné crew a další workspace. A má pouze chat s X a vlastní konverzaci. Přímé API i UI nesmí vydat B/Y obsah, názvy, počty, přílohy, logy, Work odkazy, notifikace ani paměť.
- Agent X na pokyn A nesmí obejít omezení přes shell, absolutní cestu, symlink, další mount, lokální službu, alternativní API ani delegaci na Y. Zvlášť otestovat souběžnou výměnu cest a citlivé runtime konfigurace.
- Read-only sdílený projekt je čitelný a nezapisovatelný i z procesu; nepovolený projekt není namountovaný. Sdílený zápis nesmí nepozorovaně změnit kód privilegované živé služby.
- Odebrání oprávnění během queued/running práce, otevřeného streamu, cachovaného náhledu a background služby. Ověřit přesně slíbenou hranici revokace a audit důvodu odmítnutí.
- Page read neumožní publish ani spuštění cizí rutiny. Routine run neumožní změnit script/credentials, eskalovat autoritu nebo získat neautorizované logy.
- Agent neaktivní + služba aktivní + TTL. Služba pokračuje; po crash/OOM, restartu Crewshipu a restartu hosta vznikne slíbený stav bez nekontrolované duplicitní instance. Stop je konečný, ne automatický nekonečný restart.
- Ztracený wake hint, opakovaná událost, server restart, stale completion a lease loss; žádná ztracená přijatá práce ani nekontrolované dvojí vykonání. Nejasný externí účinek se nezmění na slepý retry.
- Naplnění CPU/RAM/PID/disku/logů jedné služby nezpůsobí neomezené vyčerpání ostatních. Obnova ze zálohy zachová vlastnictví, grants a vazby služby na verzi/data.

## 8. Prohloubení: existující Settings a model sdílení

Doplněno na základě druhého zadání: cílem je od začátku správný datový a provozní model, nikoli dokončení všech úrovní sandboxingu před jakýmkoliv vydáním. Postupné dodání je přijatelné, pokud UI pravdivě rozlišuje nastavené pravidlo od skutečně vynucené hranice.

### Co Settings dnes opravdu dělají

V tomto checkoutu existuje Settings → **Crew links** v [connections-section.tsx](../../components/features/settings/sections/connections-section.tsx). Ukládá směrované/obousměrné propojení crews; auditní pohled je v [crew-audit-section.tsx](../../components/features/settings/sections/crew-audit-section.tsx). `crew_connections` je trvalý model. Směr `A → B` dává A možnost komunikovat s B, předávat práci a využít příslušné file API. Nejde o Linuxový mount ani změnu vlastníka souborů.

Backend [crew_messaging.go](../../internal/api/crew_messaging.go) používá pro `ReadFile` i `WriteFile` stejnou `canCommunicate` podmínku. Čtení míří do sdíleného stromu cílové crew; upload standardně konstruuje cestu `incoming/<requesterCrewID>/<destPath>` uvnitř něj. To není totéž jako obecné oprávnění přepisovat všechny projekty cílové crew. Rozlišení workspace, vazby tokenu, směr propojení a ochrana cest již existují; nesmí se při rozšíření zahodit.

Samostatný agent→agent file ACL editor s nezávislou volbou read/write jsem v prohledaných Settings a agent/crew konfiguraci tohoto commitu nedoložil. To nevylučuje novější práci na jiné větvi nebo jiný UI povrch. Proto nelze uživatelem zmíněné nastavení automaticky prohlásit za neexistující, ale ani vydávat Crew links za kompletní agentí file ACL. Před implementací porovnat přesnou obrazovku a její API s aktuální integrační větví. Cizí instance v této rešerši nebyly měněny ani auditovány.

### Doporučená rozšířená obrazovka

Zachovat Crew links jako místo vztahů, přidat samostatný význam **Sdílení dat**. Formulář bez Linuxových pojmů:

- **Kdo:** konkrétní agent / členové vybrané crew / konkrétní člověk. Lidské členství a agentí členství jsou odlišné subjekty, ne jedna zaměnitelná skupina.
- **K čemu:** vybraný projekt, sdílená složka nebo publikované výstupy vlastníka. Nenabízet implicitně celý agentí HOME, runtime konfiguraci ani secrets.
- **Co smí:** zobrazit / upravovat; případné přidávání do doručené složky je samostatná omezená operace. Použití aplikace, její nasazení, přidělení credentials a delegování práce se nepřidají automaticky.
- **Proč má přístup:** přímý grant nebo dědění z crew, kdo jej nastavil, případná expirace. Náhled efektivního přístupu konkrétního agenta.
- **Účinnost:** uloženo / vynuceno pro nové běhy / probíhá aktualizace / chyba. Při sdíleném interním runtime uvést, že přímý shell má širší crew přístup; nepoužívat zavádějící zelený stav „izolováno“.

Příklad: „Agent Mařena může zobrazit projekt Faktury crew Finance. Nemůže jej upravovat, spouštět jeho rutiny ani používat jeho přihlašovací údaje.“ Změna na „Může upravovat“ ukáže, zda jsou ve zdroji spustitelné skripty a kdo následně schvaluje nasazení.

### Datový kontrakt pro stabilní základ

Nezavádět zatím závazný název SQL tabulky. Minimální logický grant: `workspace_id`, stabilní ID subjektu a typu, stabilní ID zdroje a typu, povolené operace, vydavatel, vlastník politiky, verze, čas vytvoření/odvolání a volitelná expirace. Cesta je vlastnost spravovaného zdroje, nikoli jeho identita. Přejmenování agenta/projektu nesmí změnit oprávnění; odebraný a znovu vytvořený slug nesmí zdědit cizí grant.

Výchozí doporučení je aditivní model bez obecných deny pravidel, kompatibilní s Pages: přímý grant rozšiřuje děděná práva. Pokud crew grant dává write, agentí read jej nezúží. UI to musí říct a nabídnout úpravu širšího grantu; pokud produkt potřebuje výjimky, nejprve určit precedence a dopad, ne přidat skrytý deny. Grant není tranzitivní: A čte B a B čte C neznamená, že A čte C. Schopnost číst však umožňuje data zkopírovat; zákaz dalšího sdílení by vyžadoval navíc datovou politiku, ne jen ACL.

API a runtime používají jedno rozhodnutí o přístupu. Pro API-only operace stačí ověření při požadavku. Pro přímý shell se z rozhodnutí vytvoří mount/runtime plán; změna DB sama neodebere již existující writable mount. Odebrání práv musí stav běhu buď změnit podporovaným mechanismem, nebo jej zastavit a znovu vytvořit. Do té doby lze pravidlo označit jako vynucované pouze API, nikoli jako plnou izolaci procesu.

Migrace současných Crew links zachová jejich doložené chování explicitním legacy rozsahem (komunikace, sdílené čtení, doručení souboru). Nesmí je tiše přeložit na obecné read/write všech dat. Vazby Pages a credentials si ponechají vlastní sémantiku a v přehledu se sjednotí jejich vysvětlení, nikoli bez rozmyslu jejich úložné tabulky.

## 9. Restart Crewshipu, Dockeru a celého hosta

### Co je již doložené

- [server_lifecycle.go](../../internal/server/server_lifecycle.go), `rehydrateContainers`: při bootu vyhledá existující běžící crew kontejnery, zaregistruje statistiky, file watcher a idle clock. Zastavené kontejnery výslovně přeskočí; tato funkce není automatické spuštění všech služeb.
- [port_expose_registry.go](../../internal/api/port_expose_registry.go): port exposures se obnovují z aktivních neexpirovaných DB záznamů. Token je v DB hashovaný; aktuální IP se při obsluze ověřuje. Rekonstrukce kontejneru s novým ID vyžaduje zvláštní reconciliaci identity, ne pouhé přepočtení IP starého ID.
- [run_registry.go](../../internal/sidecar/run_registry.go): sidecar obnovuje run start/end journal a ověřuje běhy proti hostitelské autoritě. Lokálně ukončený běh se po restartu nemá znovu autorizovat.
- Crew runtime i deklarované service kontejnery mají `on-failure`, nejvýše 3 pokusy. Tato politika sama nespouští kontejnery po restartu Docker daemonu. Startup služby Docker/Crewship a jejich pořadí na konkrétním hostu zde nebylo ověřeno.
- Schedule/webhook dispatcher má durable práci, lease, recovery a fencing. Nelze z toho odvodit automatické zopakování každé rozpracované rutiny či bezpečné přehrání externích účinků.

### Navržený kontrakt obnovy

Databáze drží **požadovaný stav**, provider hlásí **pozorovaný stav**. Pro každou spravovanou službu: stabilní resource ID, workspace/owner, revision image/source/config, požadováno running/stopped, režim automatic/on-demand, data volumes a credential reference. PID, container ID a IP jsou dočasné runtime údaje. Samotné `docker ps` není zdroj produktového záměru.

Jeden logický lifecycle controller obnoví a průběžně porovnává stav přes existující provider: počká na DB/šifrovací klíč/storage/Docker, ověří vlastnictví a labels, připojí živé instance, chybějící automatické služby spustí, zastavené na žádost uživatele zachová zastavené. Operace musí být idempotentní, serializované per resource a v případě více serverů pod leader/lease pravidly. Nejasně vlastněný proces nejprve reconciliovat; nevytvořit vedle něj druhý.

Na hostu zajistit startup Crewshipu a Dockeru přes správce služeb. Běžné restarty konkrétního kontejneru může řešit Docker; produktový controller řeší požadovaný stav, verze, bindings a dlouhodobý drift. Nepřidávat druhý hostitelský supervisor, který soutěží s Dockerem o restart téhož kontejneru. Volbu `unless-stopped`/`always` určit podle trvalého Stop kontraktu; ani jedna nenahrazuje reconciliaci. [Docker restart policies](https://docs.docker.com/engine/containers/start-containers-automatically/)

Při rebootu se znovu připojí persistentní data, ale `/secrets` tmpfs a procesní env musí vzniknout z nového autorizovaného doručení. Neukládat plaintext kvůli pohodlnému restartu do aplikace, image nebo běžné zálohy. Nejsou-li credentials dostupné nebo platné, služba přejde do viditelného čekajícího/chybového stavu; nepoužije jiný účet jako fallback.

Obnova má testovací matici: restart pouze Crewshipu; restart sidecaru; restart Dockeru; reboot hosta; ztráta kontejneru při zachovaných volumes; chybějící image/volume/klíč; rotace/odebrání credentials během výpadku; ruční Stop; obsazený port; současný start dvou controllerů. U všech ověřit identitu, žádnou duplicitní službu, trvalá data, platnost grantů, audit a srozumitelný stav v UI. Nezaměňovat resume konverzace, retry úlohy a restart služby.

## 10. Credentials: co chrání sidecar a kde zůstává mezera

### Doložený řetězec oprávnění

[credential_delivery.go](../../internal/api/credential_delivery.go) sjednocuje přímá agentí přiřazení, applicable bindings a přiřazení přes crew, stav ACTIVE a lease podmínky. [agent_config.go](../../internal/api/agent_config.go) z této množiny vytváří runtime doručení. Přiřadit credential celé crew záměrně rozšiřuje dosah na její agenty; není to izolované přiřazení jedinému agentovi. V UI musí být vysvětleno, odkud efektivní přiřazení pochází.

Sidecar [CredStore](../../internal/sidecar/credstore.go) vybírá provider credential podle ověřeného acting agent a `AgentIDs`, priority a lease. Prázdné `AgentIDs` znamená crew-wide. [proxy.go](../../internal/sidecar/proxy.go) odmítá neplatnou identitu a nesouhlas credential-config fingerprintu v konfiguraci, která identitu vyžaduje. [identity.go](../../internal/sidecar/identity.go) podporuje tokeny konkrétního běhu a kontrolu jeho aktuálnosti; existují také legacy agentí/tokenless kompatibilní cesty, jejichž dostupnost se řídí konfigurací.

Proxy klíče jsou předávány do sidecar procesu přes stdin, ne jako secret v argv; proces používá UID 1002, agent UID 1001. To je skutečný ochranný základ. Není to důkaz, že libovolná hodnota kdykoliv použitá agentem žije pouze tam. Podklad: [exec_sidecar.go](../../internal/orchestrator/exec_sidecar.go).

### Způsob doručení je rozhodující

| Kategorie | Dnešní chování a význam |
|---|---|
| Podporovaný LLM reverse proxy tok | Reálný klíč používá sidecar, agent typicky dostává placeholder a route token. Přístup k účtu stále existuje jako možnost volat povolenou službu. |
| CLI/login/OAuth | Některé adaptéry potřebují access token přímo v env či auth souboru. Provider-login refresh token má podle kontraktu zůstat na serveru. Přesný tok ověřovat pro konkrétní adaptér, ne pouze podle typu credential. |
| CLI_TOKEN, GENERIC_SECRET, USERPASS, SSH_KEY, CERTIFICATE | Mají file delivery; některé i env cestu. Agent, který hodnotu potřebuje přímo používat, ji může také přečíst. |
| SECRET s Keeper enabled | Hodnota se předběžně zadržuje; povolení jde přes Keeper. Není to automatická vlastnost všech typů hesel. |
| Handle-only | Resolver nevydává plaintext ani při vypnutém Keeperu; navazující operace vyžaduje odpovídající zprostředkovanou cestu. |
| Neznámý credential typ | `DeliveryNone`; výchozí doručení je odmítnuto. |

Autoritativní tabulka je [credpolicy.go](../../internal/credpolicy/credpolicy.go), výjimky/konkrétní adaptéry v [exec_env.go](../../internal/orchestrator/exec_env.go). Aktuálně je z pojmenovaných známých typů Keeper-gated jen `SECRET`; `GENERIC_SECRET` není totéž. Settings „kdo smí odhalit heslo člověku“ z [access-secrets-section.tsx](../../components/features/settings/sections/access-secrets-section.tsx) je jiná kontrola než doručení hodnoty agentímu procesu.

### Odpověď na „neuvidí agent jiné heslo?“

Na proxy cestě existuje konkrétní per-agent ochrana výběru. U plaintext doručeného souborovým/env kanálem nelze uvnitř dnešní společné UID-1001 crew tvrdit vzájemnou izolaci agentů: adresáře `/secrets/<agent>/<run>` a HOME mají chránit jiné identity, ale sibling agent má stejnou OS identitu. Agentí bearer/route token uložený v takovém prostředí je sám citlivá capability; ověření tokenu neřeší jeho odcizení ze společně dostupného prostředí.

To je architektonické riziko doložené modelem identity a delivery, nikoli zde provedené čtení skutečného cizího hesla. U `/proc/<pid>/environ` závisí přístup navíc na ptrace/DAC/LSM a dumpability; samotné shodné UID bez ověření runtime nestačí k tvrzení, že každý env je vždy čitelný. File delivery však již neumožňuje slib „všechny secrets chrání pouze sidecar“. [Linux proc_pid_environ](https://man7.org/linux/man-pages/man5/proc_pid_environ.5.html)

Šifrování v databázi chrání at-rest úložiště, nikoli hodnotu po doručení do procesu. Log scrubber a zákaz downloadu známých auth souborů přes Files jsou užitečné vrstvy, ale nebrání agentovi s OS přístupem hodnotu načíst nebo zakódovat. Odebrání souboru ani expirace Crewship lease nezneplatní u poskytovatele kopii dlouhodobého hesla, kterou proces už získal.

### Revokace a výpadek

[credstore_reap.go](../../internal/sidecar/credstore_reap.go) obnovuje metadata živých credential IDs každých 60 s. Při chybě ponechá stávající hodnoty kvůli dostupnosti. Expirované lokálně známé lease odmítá samotný Select, nezávisle na pollu. Metadata reaper neobnovuje per-agent grantee množinu ani plaintext; odebrání jediného agentího grantu, zatímco credential zůstává aktivní pro jiné, proto nelze považovat za vyřešené pouze tímto reaperem. Ověřit celý řetězec invalidate/config refresh a souběh rozdílných lease pro jeden credential — toto je otevřená akceptační otázka, ne tvrzení, že všechny revokace selhávají.

Run registry má vlastní dostupnostní politiku: lokálně ukončený běh zůstává odmítnutý po restartu; známý živý běh může při nedostupnosti autority pokračovat neomezeně dlouho, neznámý má grace pravidlo. To není univerzální okamžitá revokace při výpadku hosta. Budoucí omezený klientský režim má definovat maximální offline dobu a tradeoff; interní režim může vědomě ponechat dostupnostní chování.

### Doporučený základ a postupné posílení

1. Jedno efektivní přiřazení a vysvětlení jeho původu; rozlišit **smí použít**, **dostane hodnotu**, **smí spravovat**. File share nikdy implicitně nesdílí credentials.
2. Preferovat proxy/handle-only, pokud integrace dovoluje zprostředkovanou operaci. Kde CLI potřebuje plaintext, ukázat „token bude dostupný běžícímu procesu“ a používat oddělený credential pro službu nebo běh.
3. Krátkodobé a omezené upstream tokeny, pokud je provider umí; zúžení účtu i egress, zákaz náhradního účtu při chybě. Nevydávat obecný HTTP proxy s credential injection na libovolnou adresu.
4. Izolovaný secrets mount a identita běhu pro prostředí, kde si agenti nemají věřit. Doručit jen aktuálně přidělené hodnoty, uklidit je po skončení, vyřešit orphan procesy a restart. Neměnit UID invariant bez architektonického návrhu.
5. Audit bez plaintextu, rotace/revokace u upstream služby i Crewshipu, jasné chování během výpadku. Export/zálohy zahrnují potřebné šifrované záznamy a bezpečnou obnovu klíče, ne náhodné runtime secret soubory.

Směr odpovídá doporučením pro minimální oprávnění, řízený životní cyklus a krátkodobé/dynamické secrets v [OWASP Secrets Management](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html). U již odhaleného dlouhodobého tajemství je rozhodující odvolání/rotace u poskytovatele.

## 11. Co musí být pevné hned a co lze dodat postupně

**Základ pro 1.0:** stabilní resource identity, explicitní vlastník, oddělená oprávnění k datům/kódu/spuštění/credentials, jeden serverový rozhodovací kontrakt, požadovaný versus pozorovaný lifecycle stav, idempotentní obnova, verze politiky, audit, efektivní přístup v UI a pravdivé označení úrovně izolace. Existující crew links, delivery resolver, run registry a provider použít jako stavební části.

**Postupné rozšíření:** nejprve interní důvěryhodná crew, poté vynucení file grantů ve všech API, potom izolované runtime podle autority pro přímý shell; nakonec jemnější live revokace mountů/credentials, širší automatizace a další sandboxy. Agent→agent read/write v UI lze navrhnout již teď, ale dokud sdílený shell obchází stejné pravidlo, nesmí takové nastavení deklarovat technický zákaz přístupu. Nezpřístupnit nedodělaný omezený režim cizím klientům pod označením bezpečné izolace.

Průběžně musí jít odpovědět: kdo smí zdroj použít, proč, co skutečně vidí proces, co se stane po odebrání práva, co se obnoví po rebootu a kdo za obnovu odpovídá. To je architektonická připravenost; počet přepínačů není její měřítko.

## 12. Doplňující akceptace a ověření rešerše

Nové povinné scénáře: A→B read bez uploadu; upload jen do určeného doručeného prostoru; agentí grant proti crew dědění; odstranění a opětovné vytvoření stejného slugu; restart s novým container ID; odebrání grantu jedinému agentovi při zachování credential pro druhého; dvě rozdílné lease téhož credential; ukradený token ukončeného běhu; Keeper on/off pro každý delivery typ; stale sidecar configuration; nemožnost přečíst syntetické sousední secrets z izolovaného runtime. Testovat s umělými hodnotami, nikdy sběrem skutečných zákaznických hesel.

V tomto doplňujícím průchodu byly spuštěny existující cílené testy: `go test ./internal/credpolicy -count=1` a `go test ./internal/sidecar -run 'Test(CredStore_|LLMRoute_)' -count=1 -timeout=3m`; obě sady prošly. První společný příkaz s tímto filtrem nevybral testy credpolicy, proto byl tento balík spuštěn znovu bez filtru. Dále prošlo `go test ./internal/sidecar -run 'Test(RunAuthorityRealHost_|ActingIdentity|HandleEscalate_.*Token)' -count=1 -timeout=3m` (2,003 s). To doplňuje ověření identity, revokace po restartu registru a chování při nedostupné hostitelské autoritě v testovacím harnessu. Tyto testy dokládají policy tabulku, výběr/lease a LLM route kontroly, nikoli izolaci procesů v reálném Docker kontejneru ani reboot operačního systému.

## Meze této rešerše

Kód a dokumentace byly čteny; nebyly spouštěny modely, zákaznické rutiny, kontejnery ani destruktivní scénáře. Nebyl proveden živý dvouuživatelský průchod, úplný audit všech endpointů, load test ani test rebootu. Doložené cílené testy jsou výše; ostatní testové soubory nejsou důkaz jejich aktuálního průchodu. Tato změna upravuje pouze rešeršní dokument; celý Go/UI verification loop nebyl spuštěn. Implementace musí projít běžným repo verification loopem a výše uvedenou maticí. Stav běžícího exportu může být starší než zkoumaný checkout; závěry zde nejsou atestace nasazené instance ani tvrzení, že další výzkum nemůže odhalit nové nálezy.
