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
- Nasazení nové větve a opakování živé přejímky zatím neproběhlo.
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
