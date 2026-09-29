# Agent Access / Runtime — pokračování na dev1, 29. 9. 2026

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
