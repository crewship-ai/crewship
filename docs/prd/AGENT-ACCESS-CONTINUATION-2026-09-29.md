# Agent Access / Runtime — pokračování na dev1, 29. 9. 2026

**Aktualizace 30. 9., 07:15 UTC:** #2722 je sloučené jako `50a9ea900066144581e9ddfa6ad506cfca47ad6d`.
Finální head `5eacc19ba` schválil CodeRabbit bez připomínek. Vzdálené CI včetně
všech race jobů prošlo; Linux ARM64 vyžadoval jedno opakování kvůli desetisekundovému
timeoutu existujícího Pages collector testu. Kód ani timeout nebyl při opakování
změněn. Po přerušení sandboxem je přístup k dev1 opět funkční.
Navazuje aplikační provider binding ve větvi `feat/restricted-provider-binding-2711`.
Release 1.0 stále není přijatý a #2711 zůstává otevřené.

**Aktualizace 21:13 UTC:** #2721 je sloučené jako `272b43f58793872c6db9241f715cf33fd8a0cdf1`.
Finální head `939a30827` má skutečné schválení CodeRabbit, dokončený walkthrough
a úspěšné CI včetně všech Go/race/frontend jobů. Security i CodeQL prošly;
jejich anotace byly upozornění na verze Actions/runnerů, nikoli nález v tomto diffu.
#2711 zůstává otevřené. Navazuje [textový Responses adaptér brokeru](RESTRICTED-RESPONSES-ADAPTER-2026-09-29.md);
produkční restricted dispatch ani přejímka celého Release 1.0 zatím hotové nejsou.

**Aktualizace 21:24 UTC:** dev1 backend i web běží z čistého `d0e2023e64d660ced626145192dc18c0c38fa3df`
(nový broker adaptér, dosud nesloučená větev `feat/restricted-provider-2711`).
Celý Go průchod dokončil 150 balíčků, full vet, cílený race, invarianty a migration
lint prošly. Docker harness s race dokončil 14/14 testů včetně dvou klientů přes
`/v1/responses`, odmítnutí cizích modelů/resource referencí/tokenů a revokace A
bez zastavení B. Žádný skutečný placený model nebyl volán. Mutace odstranění
kontroly těla prokazatelně rozbila regresi. První Docker běh selhal kvůli chybě
fixture (nepřiznaný synthetic direct credential); opravený celý průchod je zelený.
Po reloadu byly ověřené skutečná binárka, webový marker, health/readiness,
oprávnění `/proc/<pid>/environ`, autentizované CLI a znovu pozitivní/negativní
dvouklientská policy akceptace. Vlastní testovací tmpfs byl normálně odpojen.

Důkazy: [Go](reports/restricted-responses-go-2026-09-29.txt),
[Docker](reports/restricted-responses-live-2026-09-29.txt),
[mutace](reports/restricted-responses-mutation-2026-09-29.txt),
[nasazení dev1](reports/restricted-responses-dev1-2026-09-29.txt).
Další integrační brána zůstává immutable provider/credential binding v aplikační
autoritě, scoped prompt/history/recall, accounting a dispatch/output. Textový
adaptér nepodporuje Codex tool loop ani ChatGPT subscription login. Nelze jej
označit za dokončený běžný omezený chat.

**Aktualizace 14:19 UTC:** #2720 je sloučené jako `51a931f423830bdb3ca6403b2fe27740e7bb8758`.
Finální head `5eb345326` má skutečné CodeRabbit review/schválení 5353361661 a
úspěšné CI, Security i CodeQL; CI run 36574189593 dokončil také všechny race joby.
#2711 se při merge #2717 uzavřelo, přestože aplikační integrace není hotová;
bylo znovu otevřené. Další větev přidává [správu grantů přes API a CLI](AGENT-ACCESS-POLICY-API-2026-09-29.md).

**Aktualizace 13:02 UTC:** #2717 je sloučené jako `7bbb09832`, po úspěchu celého
finálního CI a věcném review i schválení `56c103a98`. Starší věty o billing blokaci
nebo draftu níže jsou historický protokol. Release 1.0 stále není přijatý;
navazující runtime/provider/storage integrace pokračuje v oddělené větvi.

Navazuje na #2703/#2711 a předání uživatele. Rozsah není UX Routines.
Výchozí checkout i dev1: `07bfd2360` (#2716), čistý pracovní strom.
Uživatel povolil pokračování implementace, testování a nasazování na dev1.
Dev2/dev3 ani produkce nejsou cílem. Společný Docker daemon a host se nesmějí
restartovat při ověřování dev1: zasáhlo by to ostatní instance.

## Rozhodnutí pro společnou autoritu

- `workspace_members.access_mode` výslovně rozlišuje `trusted` a `restricted`.
  Migrace zachovává dosavadní členství jako trusted. Běžné přidání interního
  člena zachovává kompatibilitu; z role VIEWER se nestává klientský sandbox.
- `access_grants` váže jednotlivé operace na členství a skutečné FK agenta či
  projektu. Žádné wildcardy, oprávnění nad slugem nebo zadanou hostitelskou cestou.
  Chat/discover/run/delegate a list/read/write/delete jsou samostatné operace.
- Změnu politiky smí provést trusted OWNER/ADMIN, s očekávanou identitou členství a revizí.
  Restricted člen se ani změnou role nesmí vyhnout resource ceiling.
- Odstranění člena maže granty a pokusy. Změna role, capabilities, režimu nebo
  grantů posouvá revizi. Odebrání a opětovné přidání oprávnění neoživí starý pokus.
- `access_attempts` ukládá serverové identity, revizi, generaci a přesný rozsah.
  Klientský task JSON autoritu nevytváří. Opaque handle se ukládá pouze jako hash.
  Retry je nový pokus; scope zahrnuje člověka, konverzaci, členství a revizi.
- Delegace je průnik s uloženým rodičem a aktuálními granty. Změna cílového agenta
  nepřenáší jeho širší práva. Každé Resolve kontroluje celou rodičovskou větev.
- Aktuální implementace přijímá lidské chatové pokusy. Service principals,
  ostatní origins, provider/credential autorita a runtime launch adapter se
  tímto nepovažují za implementované.

## Připojené aplikační brány

RequireAuth kontroluje omezené členství i pro globální endpoint bez workspace.
Povolené jsou pouze explicitně připojené čtecí cesty a vlastní správa session;
nová neklasifikovaná route tedy nezdědí široký přístup. Pro účet se smíšenými
členstvími je tato počáteční brána konzervativní i v ostatních workspaces.
Před širším rolloutem je potřeba plně klasifikovat globální endpointy.

Seznam chatů a historie používají současně konkrétní agentí grant a publikum
konverzace. Restricted profil nemá operátorskou výjimku k cizím chatům.
Sdílený terminál a zatím nepřipojené WS kanály jsou uzavřené. Legacy resolver
odmítá materializovat širší kontext soukromého chatu restricted člověka.
Produkční routine invocation checker odmítá jeho starou lidskou autoritu,
včetně dříve zařazené práce s prázdným legacy označením.

WebSocket write a HTTP run stream nově kontrolují aktuální oprávnění před
každým rámcem, také při replay. Nedostupná DB není povolení pokračovat ve streamu.
Kontrola před zápisem je bod rozhodnutí; již přijaté/in-flight bajty nelze odvolat.

Restricted profil zatím nemá veřejný aktivační endpoint ani Settings přepínač.
Samotné brány a grant store nejsou důkaz pozitivního izolovaného chatového běhu.

## Obnova

Workspace backup zachovává režim člena a přesné granty; typed FK umožňují
přemapování agentů/projektů při fork restore. Pokusy a jejich capabilities se
do workspace bundle nepřenášejí. Import grantů posune revizi členství.
To nenahrazuje přejímku plného instance snapshotu, storage kvót a host rebootu.

## Ověření

- Cílené testy nad skutečně migrovaným SQLite: přesné operace, dva klienti
  stejného agenta, revokace/regrant, retry, odstranění/rejoin, odmítnutí rozšíření
  delegace a revokace rodiče.
- Router fixture s autentizovanými sessions: vlastní chat je vidět; cizí chat,
  nepovolený agent a nepřipojené Files/logs/credentials/run/WS cesty jsou odmítnuté.
- HTTP stream přes skutečný TCP server: pozitivní rámec, odebrání členství,
  následný canary rámec se nesmí doručit a spojení skončí.
- Samostatný runtime Docker harness byl spuštěn z dev1 s `-race`; všech 11
  původních živých testů prošlo. SIGKILL controlleru → nezávislé ukončení
  15 079 ms, sada 78,049 s. Jde stále o runtime harness, ne běžný chat.
- Celé balíčky pipeline a backup prošly (31,598 s / 135,613 s); backup/restore
  zachoval omezené granty a fork přemapoval skutečné FK. Starý pokus se neobnovil.
- Race testy access/WS a cílené API testy prošly. Frontend static export prošel.
- Mutation test vypnul pouze kontrolu doručení HTTP streamu: test správně selhal
  na doručeném canary po revokaci. Po obnovení kontroly je zelený.
- První celorepozitářový běh našel neaktualizované fixture/backup klasifikaci
  (opraveno), dva velké balíčky narazily na desetiminutový timeout. Nový celý
  běh s 30min timeoutem a omezenou paralelizací probíhá.
- Dev1 smoke na původním buildu ověřil dva samostatné soukromé chaty stejného
  agenta a zaznamenal baseline stream revokace. Vlastněný testovací workspace
  byl odstraněn přes CLI; testovací účty nemají přístup k workspace.
- První nasazení této větve na dev1: `ca379a58f4fce37c17ccc91018a919550bcb8961` (čistý build).
  Ověřena identita `/proc/<pid>/exe`, shodný web export, `/api/health`, `/healthz`,
  `/readyz` i autentizovaný CLI `whoami`. Nasazení pouze přes
  `sudo systemctl reload crewship-ws@1`.
- Dva živé WS průchody po nasazení: vlastní chat/list/count a pozitivní události
  2/2; cizí historie 404; po odebrání H1 další událost nedoručena a socket uzavřen
  (první průchod 103 ms). H2 událost dostal.
- Doplněn živý HTTP stream: oběma přišel `stream.open`, po odebrání H1 se jeho
  stream uzavřel bez dalšího rámce; H2 dostal heartbeat. Detekce nečinného HTTP
  streamu čekala na 20s heartbeat; není to 20s povolení dalších datových rámců.
  Testovací workspace odstraněn CLI, sessions odhlášeny. Syntetické účty zůstávají
  bez členství; netvrdíme odstranění všech user rows.
- GitHub CI na main `07bfd2360` stále provedlo nula kroků; annotation checku
  109298406651 uvádí „account is locked due to a billing issue“. Lokální testy
  tento vzdálený acceptance gate nenahrazují.

## Další implementační brány

1. Připojit serverovou autoritu k promptu/recallu, frontě a skutečnému runtime;
   zavést durable service origins, nikoli impersonaci posledního uživatele.
2. Vlastněný storage catalog se scope/provenance a vynucenými diskovými kvótami;
   Files, artefakty, soukromá paměť, logy a journal musejí mít pozitivní i negativní
   scénáře. Blokovaná route není splněný pozitivní scénář.
3. Model/provider stream a credential adapter nad omezeným brokerem; podpora
   musí být explicitní, nepodporovaný adaptér odmítnut bez crew fallbacku.
4. Settings/CLI a běžný chat/rutiny/Pages/Issues použijí tutéž autoritu.
5. Akceptace celé A2/B matice na konkrétním buildu, skutečné vzdálené CI a review.

**Release 1.0 není dokončený ani přijatý.**

## Nezávislé review #2717

Věcné review zdrojového commitu `ca379a58f` bylo dokončeno
([revize 5349737106](https://github.com/crewship-ai/crewship/pull/2717#pullrequestreview-5349737106)). Dva nálezy byly ověřeny:

- Mazání rodiče s delegovanými potomky vyvolalo FK chybu. Nový regresní test
  nejprve selhal na SQLite 787. Protože původní migrace již běžela na dev1,
  navazující migrace přidává mazání celé větve před odstraněním rodiče.
  Rekurzivní SQL odstraní i vnuky při `recursive_triggers=OFF`; neměníme
  historii nasazené migrace. Opravený test i celé access/WS balíčky prošly.
- Chatové rámce nyní vyhodnocují globální omezení i publikum jediným SQL
  dotazem ve stejném snapshotu. Žádná TTL cache povolení. Benchmark na dev1,
  20 000 rozhodnutí na variantu: dvě query 67,605 µs / 3 212 B / 54 alokací;
  jeden snapshot 61,574 µs / 1 329 B / 31 alokací. Jde o lokální mikrobenchmark,
  nikoli produkční propustnost či SLO. Existující indexy pokrývají user a chat ID.
- Upřesněna historická věta o zálohách: nepřenášejí `chat_read_shares`, ale
  zachovávají nové `access_grants`.

Následná revize oprav a finální ověření ještě probíhají.

## Obnovení práce po výpadku terminálu

Poslední nasazení před výpadkem: `cd67e695a1f4d92e552fcbd716f631c822002dae`.
Identita běžící binárky i webu, health/readiness a nový DB trigger byly ověřeny.
Živý test znovu prošel: soukromé chaty a počty oddělené, cizí historie 404,
WS H1 po odebrání uzavřen, HTTP H1 bez dalšího rámce, H2 dál dostává heartbeat.

Po obnovení terminálu byl přečten finální úplný testovací log. Všechny balíky
kromě `internal/database` prošly. Selhal skutečný schema invariant:
`TestForeignKeyIndexPolicy` našel šest neindexovaných FK v nových tabulkách
(40 místo původních 34). Nešlo pouze o pomalý disk. Nová append-only migrace
`20260929094200_access_authority_foreign_key_indexes.sql` přidává indexy;
limit testu zůstává 34. Cílený test již prošel, úplná sada se opakuje.

Pro úplnou sadu musí **TMPDIR i GOTMPDIR** směřovat do vlastněného tmpfs
adresáře. Pouhé TMPDIR nestačilo: Go testovací adresáře používaly GOTMPDIR.
Dva superseded mezilehlé databázové běhy byly přerušeny a nejsou green evidence.
Diskový běh API prošel (1468,164 s), database dosáhl 30min limitu. Korektní
úplný tmpfs běh odhalil výše uvedenou chybu za 91,222 s databázového balíku.
Živé aplikace a předchozí backup/restore testy používají normální úložiště.

## Uzavřené ověření indexované varianty

Zdrojový kód `7626636fde530aa148ba2b481824586fd8dfe70d`:

- `go test ./... -count=1 -timeout=30m -p=4`: **149 balíků prošlo**, Go exit 0.
  API 217,688 s, database 86,691 s. TMPDIR i GOTMPDIR ve vlastněném tmpfs.
  Následný Python cleanup narazil na Dockerem vlastněné fixture soubory;
  pouze tento konkrétní dočasný adresář byl odstraněn přes sudo. Není to
  selhání testů ani důvod označovat předchozí neúspěšné běhy za zelené.
- `go vet ./...`, migration lint a projektové invarianty prošly.
- Nasazeno výhradně na dev1; skutečná běžící binárka i nový web export odpovídají
  `7626636f`, čistý build. Health/readiness OK. Read-only kontrola sqlite_master
  potvrdila všech šest indexů; do živé DB nebylo ručně zapisováno.
- Živý WS/HTTP průchod znovu prošel na tomto buildu; vlastní chat/count, foreign
  history 404, revokovaný klient odpojen bez dalšího rámce, druhý klient funkční.
  Vlastněný workspace uklizen CLI a sessions odhlášeny.
- GitHub check 109348982150 opět potvrzuje billing blokaci bez spuštění jobu.
  CodeRabbit novou revizi zahájil; výsledek zatím není doložen v tomto záznamu.

Surové výsledky: `reports/agent-access-go-2026-09-29.txt` a
`reports/agent-access-dev1-app-live-2026-09-29.txt`.
Tato přejímka uzavírá dodanou dílčí opravu, nikoli celý Release 1.0.

## Další krok: lidská autorita před načtením kontextu

Po návratu prostředí pokračuje připojení skutečného odesílatele v chat bridge.
`HandleChatMessage` nyní vyžaduje `HumanChatResolver`; chybějící rozhraní nesmí
spadnout do starého resolveru. Produkční IPC předává autentizovaného odesílatele
ze spojení nebo serverem uložené odložené zprávy, nikoli user_id z metadat.

Nová host-only cesta `resolve-human` ověřuje současné publikum a absenci
restricted profilu jedním SQL snapshotem **před** legacy načtením promptu,
paměťové konfigurace a credentials. Workspace/crew token nesmí tvrdit lidskou
identitu. Člen odebraný po přijetí zprávy nebo účastník odebraný ze skupiny tak
nemůže při novém resolution použít dřívější rozhodnutí o přístupu.

Cílené router/bridge/IPC testy prošly: pozitivní kontext, cizí chat, odebraný
člen/účastník, restricted profil s platným chat grantem, forged metadata a
nižší interní tokeny. Bridge reproduktor na původní implementaci selhal,
protože místo lidské autority zavolal legacy resolver.

Tento krok stále **nespouští restricted runtime** a nenahrazuje durable pokus,
service origins ani grant-aware prompt/storage/provider adaptér. Již přijatý
běžící proces se touto admission kontrolou okamžitě neukončuje.

Revize zdrojového `7626636f` byla dokončena (5350699009); doporučení pro context
v benchmark seed a diagnostiku SQL chyby jsou zapracována. Navazující lidská
admission změna vyžaduje vlastní finální ověření a revizi.

Ověření nové lidské admission: celý `go test ./... -count=1 -timeout=30m -p=4`
prošel ve všech 149 testovaných balíčcích. `TMPDIR` i `GOTMPDIR` mířily do
vlastněného tmpfs. Go skončilo s 0; následný Python úklid selhal na Docker-owned
adresáři, který byl cíleně odstraněn. `go vet ./...`, migration lint a projektové
invarianty prošly. Surový Go výstup: `reports/agent-access-human-go-2026-09-29.txt`.

Nasazení lidské admission: dev1 `13cb8b29f79e5762c34f05b3789b5a4a21e4e957`,
clean, build 2026-09-29T10:05:43Z. Identita ověřena přímo přes `/proc/2147661/exe`
a vzdálené CLI version; shodný web export, health/readiness 200. Race testy
chatbridge/WS/API prošly (1,086 / 1,242 / 90,572 s).

Živý pozitivní průchod běžného skupinového chatu (druhý účastník, bez zmínky
agenta) přes nový human IPC resolver dokončil `done/no_reply`; podvržené user_id
v metadatech neměnilo přihlášenou identitu. Po odebrání účastníka historie 404.
Dva soukromé chaty téhož agenta zůstaly oddělené; po odebrání členství H1 jeho
WS uzavřen bez další události a HTTP stream bez rámce. H2 dostal událost i
heartbeat. Vlastněný workspace odstraněn, sessions odhlášeny; syntetičtí users
zůstávají bez členství. První dvě verze smoke měly chyby testovacího klienta
(očekávání 201 místo 204, nerozbalený `chat_event`); výsledky výše pocházejí
z opraveného úspěšného průchodu. Surový záznam:
`reports/agent-access-human-live-2026-09-29.txt`.

Finální diff `13cb8b29f` zatím nemá věcné nezávislé review: CodeRabbit ohlásil
limit s přibližně 38 minutami do dalšího review. Zelený bot status není review.
CI nadále blokuje účet/billing. PR #2717 zůstává draft, bez merge.


## Navazující implementace: autorita čekající lidské zprávy

Po obnovení GitHub billing prošly vzdálené funkční, race, frontend/browser,
build, Security a CodeQL joby původního headu. Jediná chyba byla tsformat lint;
`843dca032` ji opravil canonical timestamp formatterem. Dev1 na tomto buildu
prošlo dalších 149 Go balíčků, celý vet a živý dvouklientský smoke. Vzdálený
Go Lint nového headu je zelený; zbývající CI/review se ověřuje před merge.

Nová práce zachovává původní autoritu lidské zprávy během provisioning čekání:

- Host-only resolver vydá typed `human_authority` receipt z jednoho SQL snapshotu
  současného publika, identity členství/revize a generace/revize chatu.
- Receipt není bearer capability. Při obnově se znovu kontroluje současný přístup
  a přesná shoda původního receiptu, před materializací kontextu i před vydáním
  odpovědi resolveru. WS metadata jej nesmějí vytvářet.
- Nová generace chatu brání obnově po smazání a znovuvytvoření stejného ID.
  Změny a obnovení publika, agenta/crew nebo smazaného workspace posunou revizi.
  Změny členství a odstranění účastníka využívají existující členskou revizi.
- Bridge uchovává receipt v serverových options. Provisioning označí automatický
  resume; chybějící receipt nebo pokus o jeho nahrazení se odmítne. IPC resolver
  odmítne starou odpověď bez receiptu, bez legacy fallbacku.
- Coalescence i počítání opakovaného odložení používají chat, odesílatele a celý
  receipt. Druhý člověk či nová verze oprávnění nepřepíšou starou čekající práci.

Cílené testy ověřují pozitivní aktuální admission, odstranění/rejoin člena,
změnu/obnovení role a publika, odstranění/obnovu agenta či workspace, nové použití
chat ID, účastníka odstraněného a přidaného zpět, vadný receipt, přenos přes
host IPC a provisioning options i coalescenci. Mutation test vypnul pouze
porovnání původního receiptu: stará zpráva dostala 200 místo 404 a test selhal.
Kontrola je obnovena. První pokus reproduktoru selhal na krátkém syntetickém JWT
secretu fixture; tento pokus se nepovažuje za bezpečnostní reprodukci.

Tento krok nepřipojuje izolovaný runtime/provider/storage ani durable service
principals. Provisioning pending zprávy jsou stále v paměti; receipt nedělá
z této fronty durable queue a nezabíjí již spuštěný proces. Plná A2/B přejímka
zůstává otevřená. Nová implementace ještě potřebuje finální nasazení a review.


První celá sada nové queue změny našla dvě navazující testovací úpravy:
HTTP fixture bridge musí vydat nový receipt a chat generation se při restore
musí změnit. Round-trip test nyní výslovně požaduje novou neprázdnou generaci
každého obnoveného chatu; pouze tuto generaci vynechává z byte-for-byte hashe.
Obsah a ostatní sloupce zůstávají porovnané. Obě cílené regrese následně prošly;
celá sada běží znovu. Cílené race testy chatbridge/API prošly (1,070 / 72,094 s).


Navazující kontrola společného store našla opětovné použití soukromého datového
scope po smazání a znovuvytvoření téhož chat ID. Nový reproduktor nejprve selhal
(`recreated chat reused old private data scope`). Uložené pokusy nyní zmrazí
chat generation/revision při přijetí; Resolve porovnává tento stav s aktuálním
chatem a datový scope zahrnuje obě hodnoty. Ani vrácení agenta do původní crew
neobnoví starý pokus. Jde o další aditivní migraci, nikoli editaci už zapsané.


### Finální ověření queue/scope změn na dev1

- Zdroj `16e46f9eaecaf4f5c1ff0512940c9c99d932794a`, clean build
  `2026-09-29T12:12:04Z`; shodné lokální/vzdálené CLI version, skutečný
  `/proc/2539054/exe` a web export. Schema `20260929120500`.
- Celý Go průchod po scope změně: 149 testovaných balíčků, exit 0; celý vet,
  migration/tsformat lint, projektové invarianty a commit lint prošly.
  Cílené race: chatbridge/API 1,070 / 72,094 s, celý access 69,146 s.
  Předchozí full Go průchod queue změny také prošel po opravení fixture.
- Živý host IPC resolver: vlastní receipt pozitivní; odstranění člena přes CLI
  → původní receipt 404; přidání téhož uživatele zpět přes CLI → starý receipt
  stále 404, nový receipt 200 s novou identitou členství. Žádné přímé DB zápisy.
- Živý WS group send prošel skutečným human resolverem a skončil `no_reply`.
  Dva klienti stejného agenta viděli jen své soukromé chaty; cizí historie 404.
  WS po revokaci uzavřen bez další události, HTTP bez dalšího rámce, druhý klient
  dále dostával událost i heartbeat. Vlastněný workspace odstraněn, sessions
  odhlášeny; syntetičtí users zůstávají bez workspace členství.
- Health/readiness 200. Linux `/proc/<pid>/environ` má mode 0400, vlastník UID
  1000; skutečná hodnota host tokenu nebyla tisknuta ani ukládána do reportu.
  Tato kontrola host procesu nenahrazuje již dříve provedený Docker harness
  pro UID 1001/1002 a není důkaz připojeného izolovaného běhu.
- Surové výsledky: `reports/agent-access-queue-live-2026-09-29.txt` a
  `reports/agent-access-authority-final-go-2026-09-29.txt`.

Živý revoke/rejoin průchod ověřil resolver použitý obnovou zpráv; nevytvářel
čekající reálný modelový běh. Přenos přes provisioning resume a odmítnutí změny
receiptu pokrývají automatické bridge/handler/IPC testy. Nepovažujeme to za
akceptaci celé durable queue ani restricted runtime.

Vzdálené CI `843dca032` nakonec celé prošlo. Novější kód vyžaduje vlastní CI;
finální nezávislé review dosud chybí (poslední věcná revize `7626636f`). Další
žádost o review byla odeslána 29. 9. v 12:12 UTC. PR #2717 není sloučené.

## Propojení aplikačního store a runtime, další větev

Finální věcné review #2717 nad `56c103a98` dorazilo jako revize 5352626632.
Nemá nové actionable inline nálezy. Dvě drobné připomínky jsou zapracované
na navazující větvi: kontextové SQL a defer Close v backup testu a dodatečná
kontrola receiptu před načtením konfigurace. Původní vstupní i závěrečná kontrola
zůstávají. #2717 je ready; při tomto zápisu čeká na dva vzdálené race joby.

`internal/restricteddispatch` nyní implementuje runtime Authority skutečným
`access.Store`. Příkaz sestavuje důvěryhodný server až po admission; následná
kontrola revokace předchází uložení. Aditivní `restricted_launches` sváže neměnný
příkaz s durable pokusem, bez uložení capability handle nebo credentials.
Lease, vydání výstupu a nový start používají aktuální databázovou autoritu.
Neúspěšná či zrušená příprava pokus odvolá. Workspace bundle nové launch payloady
neobnovuje. Mounty jsou odmítnuté, credentials a síť nejsou v tomto adaptéru
povolené. Nejde o veřejný execution endpoint ani zapnutí restricted profilu.

Ověření na dev1:

- Celý Go průchod: **150 balíků, exit 0**, celý vet, migration lint a invarianty.
- Skutečný runtime + migrovaná aplikační DB, nikoli fixture Authority: dva lidé
  stejného agenta běželi v oddělených kontejnerech pod UID 1001. H2 neviděl H1
  soubor v `/tmp`, Docker socket ani `/data`. Po odvolání grantu se H1 výstup
  odmítl a celý kontejner skončil za 4,776 s; H2 zůstal funkční.
- Všech 11 původních Docker testů také prošlo s race. Pád controlleru → nezávislé
  ukončení 14,983 s. Jde o jedno syntetické měření, nikoli produkční SLO.
- Mutation test přes Go source overlay odstranil jen kontrolu po sestavení
  příkazu a správně selhal. Pracovní zdroj se při mutaci neměnil.
- Reporty: `reports/agent-access-dispatch-go-2026-09-29.txt`,
  `reports/agent-access-dispatch-live-2026-09-29.txt`,
  `reports/agent-access-dispatch-mutation-2026-09-29.txt`.

**Stále nejde o běžný chat s modelem.** Test používá vlastněnou migrovanou fixture
DB a skutečný Docker na dev1, nikoli veřejnou chat route živé aplikace. Produkční
provider/prompt/recall, quota storage a chat/CLI/routine adapter nad touto hranicí
zůstávají další implementační brány. Celé PRD není uzavřené.

Nasazení této návaznosti: dev1 `7d86c9583c1911d0b75cf27fb3ed9cd0ac0d662e`,
clean build `2026-09-29T12:48:32Z`, shodný skutečný proces a web export.
Schema `20260929123723`; read-only kontrola potvrdila launch tabulku i immutable
trigger. Health/readiness 200. Živý group human resolver a dva soukromí klienti
stejného agenta prošli; starý receipt po odebrání/rejoin zůstal odmítnutý, nový
fungoval. WS/HTTP revokace fungovala a druhý klient dostával heartbeat. Fixture
workspace odstraněn přes CLI, účty zůstávají bez členství. Report:
`reports/agent-access-dispatch-app-live-2026-09-29.txt`.

GitHub automatický merge není v tomto repozitáři povolen; pokus o jeho nastavení
byl odmítnut, žádná ochrana nebyla vypnuta. #2717 již má finální APPROVED review,
ale při tomto zápisu stále čeká na poslední Go Race (internal/api) job.

## Broker v2: omezený SSE transport

Navazující větev doplňuje explicitní profil `brokered-http-v2`, network version 2
a grant `ResponseMode=sse`. V1 zůstává pouze bufferovaný; prázdná nová pole se
nepřidávají do jeho wire formátu. Cíl, metoda a účet jsou nadále pevné, bez
redirectů či obecného proxy. Limit je nejvýše 1 MiB přijatých i doručených dat
a pět minut. Každý vydaný frame ověřuje aktuální autoritu i expiraci vydaného
credential; odvolaný nebo nedokončený stream skončí přerušením spojení. Tajemství
rozdělené přes hranice čtení se zadržuje a rediguje, s lineárně omezeným hledáním
prefixu. Čas čekání na modelové hlavičky zůstává omezený na nejvýše 30 sekund.

Ověření finálního kódu: všech **150 Go balíků**, celý vet, cílené runtime race
testy a **13/13 živých Docker případů**. UID 1001 obdržel první SSE event ještě
před dokončením syntetického TLS upstreamu, broker secret se neobjevil v odpovědi
a po revokaci nepřišel následující canary; kontejner skončil. Samostatný test se
skutečným aplikačním grant store znovu zastavil H1 za 4,717 s při funkčním H2.
Mutation overlay odstranil pouze per-frame broker authority check a test správně
selhal doručením `REVOKED_CANARY`. Výsledky:

- `reports/agent-access-broker-stream-go-2026-09-29.txt`
- `reports/agent-access-broker-stream-live-2026-09-29.txt`
- `reports/agent-access-broker-stream-mutation-2026-09-29.txt`

Aplikační store adaptér a síťový SSE profil jsou zatím ověřené **odděleně**.
`restricteddispatch.Authority` dosud nevydává síťové/credential granty a nemá
produkční provider adapter. SSE transport sám neřeší modelovou sémantiku dokončení,
ceny, scoped prompt/recall, storage ani veřejný chatový dispatch. Veřejná aktivace
restricted profilu zůstává nedostupná. Celé PRD se tím neuzavírá.

Finální nasazení větve po SSE změnách: dev1
`f084be41aeeddd64b0f2033de02852cdaeeb5ec1`, clean, build
`2026-09-29T13:17:30Z`. Skutečný `/proc/2745490/exe` a web export jsou shodné;
autentizovaný CLI smoke i health/readiness prošly, schema `20260929123723`,
Linux environ 0400/UID 1000. Nasazení nemění stav veřejné aktivace restricted
profilu. Navazující PR musí ještě projít vzdáleným CI a nezávislou revizí.

Další provider adapter musí kromě streamu svázat aktuální credential/account,
model, rozpočet a povolený tvar požadavku s pokusem. Pevná HTTPS adresa sama
o sobě neopravňuje číst libovolnou upstream konverzaci či soubor z téhož účtu.
SDK route mapování, výběr scoped promptu/recallu a výstupní audience proto nesmějí
převzít široký legacy resolver.


## Správa grantů přes API/CLI a živá revokace

Větev `feat/access-policy-api-2711` přidává GET/PUT politiky členství a
`workspace member access get/set`. Identitu operátora určuje autentizace;
aktuální trusted OWNER/ADMIN se kontroluje v transakci. CLI token navíc potřebuje
workspace:admin. Dokument musí obsahovat membership_id, revision, mode a výslovné
rights; prázdné restricted rights znamenají deny-all. Starý zápis vrací 409,
CLI si nesmí tiše načíst novou revizi a opakovat ho. Čtení politiky používá
společný snapshot členství a grantů. Kontrakt: AGENT-ACCESS-POLICY-API-2026-09-29.md.

Dev1 nasazeno z čistého `976dacddc6abf4bb4b07b0c8d5bb9b07e0afd374`, build
2026-09-29T14:28:25Z. Skutečná binárka i web export odpovídají; health/readiness
200, environ 0400 / UID 1000. Dva syntetičtí klienti stejného agenta dostali
konkrétní chat grant přes nové CLI. Viděli vlastní chaty, cizí historii nikoli;
unintegrované files/WS-token cesty zůstaly odmítnuté. CLI deny-all prvnímu odebral
seznam i historii, druhý dál četl. Stará politika nemohla revokaci přepsat.
Fixture workspace odstraněny a sessions odhlášeny. Report:
`reports/agent-access-policy-live-2026-09-29.txt`. Kontrolní mutace odstranila pouze
operator check a test zachytil neoprávněné čtení politiky:
`reports/agent-access-policy-mutation-2026-09-29.txt`.

Doplněná akceptace B4 na předchozím stejném aplikačním chatu (`f084be41a`):
anonymní prohlížeč zobrazil neprázdný syntetický přepis, po odvolání tokenu jej
API i browser odmítly. Token nebyl v URL/localStorage/sessionStorage. První
harness potřeboval správně rozbalit chat_event; poté celý scénář prošel.
Reprodukce a výstup jsou na dev1 v `/tmp/crewship-1-shared-nonempty-live.py`
a stejnojmenném `.log`; detail také v těle #2720.

Provider průzkum: skutečný dev1 image má Codex 0.157.0. V network-none kontejnerech
s umělými credentials obě konfigurace custom provider (env-key a ChatGPT) poslaly
/v1/responses na lokální mock a dokončily syntetické SSE s exit 0. ChatGPT navíc
zkoušel automatické MCP síťové volání, které network-none zablokovalo. Vlastní
base URL tedy sama nestačí; provider adapter musí omezit také pomocné cesty.
Nešlo o skutečný model ani finální runtime/provider integraci. Reprodukce:
`/tmp/crewship-1-codex-provider-success.py`, detail v těle #2720.

Celé PRD zůstává otevřené. Správa grantů neaktivuje izolovaný modelový běh ani
neřeší provider/account adapter, scoped prompt/recall, storage kvóty, Settings
editor či produkční chat/CLI/routine dispatch. Sdílený host/Docker nebyl restartován.


Ověření při zápisu této části: nové API/CLI/store testy i cílený race prošly,
celý `go vet ./...`, migration lint a agent invariants prošly. Celý Go průchod
běží jako `/tmp/crewship-1-policy-full.log`; konečný stav a CI/review navazujícího
PR je nutné ověřit v jeho těle/checks. Na dev1 běží kód výše uvedeného čistého
commitu; následné změny této zprávy jsou jen dokumentace.


### Finální ověření policy API a review oprav (#2721)

Kód `4e21bba970e77232dbd2964c98048e564f2cdd7d` po CodeRabbit připomínkách
navíc kontroluje identitu členství těsně před vydáním dokumentu: odebrání/rejoin
mezi lookupem a snapshotem nesmí vrátit novou politiku přes staré ID. Regrese
ověřuje tento případ a mutation overlay odstraněním samotné kontroly správně
selhal. CLI get nápověda nyní výslovně popisuje --format json.

API má explicitní schéma povinného CAS dokumentu a chybového 409 kontraktu;
veřejné API/CLI reference i číselný přehled OpenAPI jsou aktualizované. Strict
inventory je čistý. Source guard čtení workspaceId z path má úzkou výjimku s
odůvodněním: hodnota se používá výhradně k odmítnutí neshody, všechny DB dotazy
používají ověřený kontext. Nové autentizované GET/PUT regrese pro podvrženou
kombinaci path/query kontrolu dokazují; žádná query nesměřuje podle path.

**Finální celá Go sada: 150 testovaných balíků, exit 0**, celý vet, cílený race,
OpenAPI consistency a strict docs inventory prošly. Report:
`reports/agent-access-policy-go-2026-09-29.txt`. Pomalý původní diskový běh byl
ukončen, není green evidence. Mezilehlý tmpfs běh odhalil uvedené dva invarianty;
po opravách a poslední review změně celá sada proběhla znovu. Vždy používat
TMPDIR **i** GOTMPDIR ve vlastněném exec-enabled tmpfs.

Finální dev1 clean build `4e21bba97`, 2026-09-29T14:52:36Z, PID 2980119:
identita skutečné binárky, environ 0400/UID 1000, health/readiness 200. Živé
OpenAPI skutečně vydává povinné typované schéma i 409. Znovu prošel celý vlastní
CLI/API scénář obou klientů a revokace; testovací workspaces uklizeny. Reporty
live/mutation výše obsahují i závěrečné opakování. Staré CI na bdbe74a9b se známou
chybou dokumentace bylo zrušeno kvůli nákladům; nový finální head musí dostat nové
CI a review. Původní review #2721 obsahovalo 4 nálezy; všechny jsou zapracované.
#2721 není důkazem hotového provider/runtime rollout. #2703 a #2711 zůstávají otevřené.
