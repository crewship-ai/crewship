# Crews, Agents a Memory — podrobný produktový a technický návrh

2026-09-08 · Dev2 · zdrojový stav `4456f2d9`.
Navazuje na [audit Crews & Agents](crews-agents-ux-audit-dev2-2026-09-08.md).
Jde o analýzu a implementační specifikaci; editor ani přehledy nejsou tímto
dokumentem nasazené. Barvy, typografii a základní navigační strom zachovat.

## 1. Hlavní rozhodnutí

Memory má být hlavní záložka detailu agenta i crew, dostupná jedním kliknutím.
Je to místo pro kontrolu znalostí a jejich použití. Edit obsahuje nastavení
chování, modelu a přístupů. Historie změn je detail konkrétní znalosti.
Technická diagnostika paměti je sekundární pohled pro správce.

Výchozí navigace agenta: **Overview · Work · Memory**; hlavička má Chat,
Files a Edit. Crew: **Overview · Team · Work · Memory**, Edit v hlavičce.
V Memory agenta výchozí pohled „Znalosti agenta“, přepnutí na „Sdílené
znalosti“ a „O mně“. Žádné implicitní načítání profilů dalších lidí.
Persona se upravuje v Edit → Instrukce; Memory pouze vysvětlí, jaký zdroj
instrukcí agent používá, a odkáže na stejný editor.

## 2. Co screenshot skutečně znamená

`MemoryTab` je otevřený přes nabídku `…` v agent canvasu. Jeho AGENT.md
a CREW.md čtou `/api/v1/memory/versions`, vyberou první záznam a načtou
uložený blob. Nečtou aktuální soubor používaný při běhu agenta.

Hláška na screenshotu odpovídá `ProjectionUnavailableReason`: není
nakonfigurovaný blob root pro verzování. `internal/server/server.go`
ho zapojuje pouze při neprázdném `cfg.Storage.MemoryRoot`. To vysvětluje
nedostupnost historie; neprokazuje prázdnou ani nefunkční paměť agenta.
Aktuální konfigurace služby nebyla v tomto auditu měněna ani vypisována.

Počítadlo `0/4000 B` zobrazuje výchozí nulu UI, i když obsah není známý.
Velké prázdné pole, žlutý odstavec a dlouhý seznam implementačních omezení
z toho dělají diagnostický dokument místo klientské funkce.

Správný stav pro současné možnosti API: **„Aktuální obsah zde zatím nelze
načíst. Historie změn není zapnutá.“** Po doplnění live read API lze
ukázat aktuální obsah a jen u záložky Historie uvést „Historie změn není
zapnutá“. Zapnutí historie nemá slíbit zpětné doplnění minulých verzí.

## 3. Skutečná mapa paměti a instrukcí

| Vrstva | Co představuje | Skutečný rozsah a použití | Dnešní klientská mezera |
|---|---|---|---|
| Historie konverzace | Co zaznělo v konkrétním vlákně | Samostatný chatový kontext, není totožný s Memory | Zachování starého chatu nevysvětlovat přepínačem dlouhodobé paměti. |
| AGENT.md | Dlouhodobé poznatky agenta | Agent, čtené při sestavení paměťového kontextu | UI nabízí pouze poslední zaznamenanou verzi. |
| BRIEF.md | Zadání od vedoucího pro najatého/pověřeného agenta | Agent; runtime ho čte vedle AGENT.md | V Memory není zastoupený. Patří primárně k aktuální práci. |
| Daily notes | Pracovní poznámky | Agent a crew, runtime umí poslední dostupný pracovní den a dnešek | Panel je pouze popisuje; neumí objevit existující data. |
| CREW.md | Sdílené znalosti týmu | Crew; více agentů čte stejnou vrstvu | UI pouze historie; nenabízí pohodlnou kontrolu využití. |
| Pins | Prioritní poznatky | Existují agentovy pins i snapshot připnutých journal událostí v crew topics | Nejsou jedním souborem ani jednou cestou. Sjednotit jejich prezentaci, zachovat původ. |
| Learned topics / lessons | Poznatky z konsolidace a zpětné vazby | Některé při startu, jiné přes vyhledávání; LEAD dostává digest výsledků crew | Vyhledatelný poznatek není automaticky součástí každého promptu. |
| Workspace knowledge | Znalosti celého workspace | Samostatný provider při sestavení kontextu | Ve čtyřech sub-tabs není vidět. |
| Episodic recall | Relevantní minulé události pro aktuální zadání | Samostatné vybavení přes recaller, best-effort, podle role/scope | Není kompletní kopií souborové paměti ani historie chatu. |
| Persona | Jak má agent komunikovat a vystupovat | Agent override → crew default → syntetický default | Aktuální panel ji směšuje se znalostmi a config zároveň upravuje system_prompt. |
| Peer card | Poznatky o spolupráci tohoto agenta s daným člověkem | Agent × user × workspace; runtime vybírá kartu zakladatele session | Existují storage/read/write primitiva, ale automatický extractor v produkčním startu zůstává no-op. |
| User model | Deklarované pracovní preference člověka napříč interakcemi | Logicky user × workspace; fyzicky soubor v shared memory nejaktivnější crew | Má skutečný extractor i vlastní API, ale Memory panel ani Settings Privacy ho nevykreslují. |

Zdroje: `internal/orchestrator/memory.go`, `memory_persona.go`,
`orchestrator_run.go`, `internal/consolidate/user_model_worker.go`,
`internal/usermodel/transcript.go`, `cmd/crewship/cmd_start.go`.

Historické PRD `agent-memory-on-wake.md` popisuje chybějící poslední pracovní
den; aktuální `resolveDailyWindow` už tuto mezeru řeší. Není správné znovu
prezentovat celý starý seznam problémů jako současný stav.

## 4. Personalizace: co platí a co nelze zatím slibovat

Pro uživatele A a B existuje samostatná identita profilů. Klíč není email
ani jméno, ale user ID se scope workspace; `memory.UserSlug` odvozuje
souborovou identitu. V UI se mají používat jména a avatary, technické ID
a hash patří do detailů.

Runtime helpery čtou pouze profil `OpenedByUserID`, bez známého uživatele
osobní blok nevloží. To je omezená vlastnost skládání promptu, nikoliv
záruka soukromí celého úložiště. Profil leží v crew filesystemu a agentové
API pro peers má širší workspace přístup.

### Staticky potvrzené nedostatky

1. **PeerCardSync není dokončená automatická personalizace.**
   `cmd_start.go:1252` předává jen BasePath. Worker volí `NoopExtractor`,
   který vrací prázdný obsah. Tvrzení prázdného stavu UI, že po 10 zprávách
   nebo 5 minutách vznikne karta, je bez další implementace zavádějící.
2. **UserModelSync už má skutečnou extrakci.** Start kolem řádku 1278
   zapojuje `usermodel.New`, ale výsledek závisí na profile settings,
   dostupném pomocném modelu, vhodných zdrojích a následném sweepu.
   Výchozí denní sweepy jsou rozložené kolem 04:00/05:00 UTC; nejde
   o okamžité zapamatování po každé zprávě.
3. **Obecný profil není dnes spolehlivě workspace-wide doručovaný.**
   Writer vybírá nejaktivnější crew, runtime čte shared soubor své crew.
   Jiný tým proto nemusí dostat aktuální profil. Je třeba sjednotit
   úložiště/doručení nebo v UI přiznat skutečný rozsah.
4. **Nové společné konverzace nejsou automaticky pokryté.** Extractor
   čte `conversation_messages JOIN chats`, omezené na chaty založené
   daným člověkem. Nepokrývá tím celý nový workspace conversation model.
   Upravovat zdrojová data až se správnou identitou autora a oprávněními.
5. **Zakladatel session není totéž co právě mluvící člověk.** Je třeba
   definovat chování skupiny, kanálu, delegace a autonomního běhu.
   Není podložené tvrdit, že současný runtime vždy přepne personalizaci
   na autora poslední zprávy. Výchozí návrh: osobní kontext pro soukromý
   přímý chat; sdílené konverzace používají týmový kontext, nikoliv
   automaticky soukromý profil zakladatele.
6. **Self-service existuje, ale UI ho využívá neúplně.** API umí vlastní
   user model zobrazit, smazat celý i odebrat jeden klíč. PrivacySection
   používá pouze peer consent a peer cards. „O mně“ má sjednotit oba
   zdroje, se srozumitelným označením rozsahu.
7. **Vymazat nyní není zákaz znovu odvodit.** Per-fact delete odstraňuje
   textové pole, nikoliv zdrojové zprávy ani trvalý suppression záznam.
   Sweep ho může znovu extrahovat. UI musí oddělit „Odebrat“ a „Dále
   si o mně neukládat poznatky“. Trvalé zapomenutí konkrétního faktu
   potřebuje nový kontrakt a test proti opětovnému odvození.
8. **Původ jednotlivých faktů se neukládá.** Extrakce ověřuje evidenci
   proti vlastním výrokům člověka, ale per-fact message reference není
   persistovaná. UI nesmí vymyslet odkaz „řekl jste to v tomto chatu“.

### Oprávnění jako konkrétní implementační podmínka

`router_crews.go` registruje GET peers jako authenticated workspace read,
DELETE peers/{userId} jako `roleSelf`. `PeerCardHandler` kontroluje workspace
agenta, ale v těchto handlerech není kontrola, že cílový user je volající,
ani explicitní admin kontrola. `roleSelf` není automatické porovnání ID
uživatele v URL; správnou jemnou autorizaci musí provést handler.

To je nález k prioritnímu ověření a opravě před rozšířením zobrazení lidí.
Nebyl proveden živý pokus číst či mazat cizí karty. Výchozí návrh:
vlastní profil přes `/users/me`; cizí profily pouze v oddělené, oprávněním
řízené správě s konkrétním účelem a auditem. Samotné schování UI nepomůže.
Totéž prověřit pro search, export, Files, indexy a již sestavený kontext
běžící session. Neoznačovat data „vidíte pouze vy“, dokud to nevynucují
všechny relevantní cesty.

## 5. Přepínače musí říkat, co skutečně ovládají

| Mechanismus | Dnešní význam | Doporučení pro UI |
|---|---|---|
| `memory_enabled` | Gatuje `buildMemoryContext`, včetně persona/user/peer bloků uvnitř něj | Vysvětlit rozsah; před přejmenováním rozhodnout, zda instrukce opravdu mají záviset na paměti. |
| Episodic recall | V `assembleSystemPrompt` se volá mimo podmínku `MemoryEnabled` | Nevydávat memory off za úplný zákaz veškeré historie/recallu. |
| `self_learning_enabled` | Vlastní self-learning posture pro aplikování změn, nad ním další policy | Samostatné „Učení a schvalování“, nikoliv synonymum používání paměti. |
| Peer consent | Opt-out a purge osobních peer/user profilů | Zobrazit v „O mně“, zachovat oprávnění vlastníka a rozsah workspace. |
| Versioning | Ukládání auditních kopií | Samostatný stav historie, nezávislý na znalostech. |
| Konsolidace / návrhy | Zpracování zkušeností, případně schválení změn | Zobrazit poslední stav a pouze reálné čekající návrhy. |

Navržená budoucí semantika: vypnutí používání dlouhodobých znalostí
nevymaže obsah, historii ani instrukce. Tento kontrakt musí nejdřív
implementovat backend, včetně explicitního rozhodnutí pro episodic recall;
nelze ho docílit pouze změnou labelu.

## 6. Návrh Memory pro klienta

```text
Kodi · Memory                           Hledat ve znalostech

Znalosti agenta   Sdílené znalosti   O mně

Znalosti agenta
Co si Kodi uchovává pro další práci.

Připnuté poznatky
Pracovní poznámky a dlouhodobé znalosti
Poučení z dokončené práce

[Záznam]  obsah / čitelný název / rozsah / poslední změna
          otevření → celý obsah, původ, historie, dostupné akce

Návrhy ke schválení (pouze pokud existují)
```

Na běžné obrazovce se zobrazí obsah, nikoliv struktura adresářů. Markdown
renderovat jako dokument, zdrojový soubor jako sekundární volbu. Dnešní
AGENT.md je jeden dokument, nikoliv databáze jednotlivých faktů: v první
verzi ho tak prezentovat. Samostatné faktové karty používat pouze tam,
kde API poskytuje stabilní ID/klíč. Nedělit text podle odrážek a nevydávat
to za nezávislé záznamy s vlastní historií.

„Sdílené znalosti“ ukáže crew a workspace s jasným štítkem původu.
Přechod na crew zachová vybraný dokument. Agentovo sdílené zobrazení
nesmí být druhým nezávislým editorem stejných dat.

„O mně“ ukáže **Pracovní preference** a **Spolupráce s Kodi**. Každá
část má vlastní dostupnost, datum aktualizace a rozsah použití. Chybějící
PeerCard extractor se označí jako nedostupná funkce, ne jako nedostatek
uživatelových interakcí. Vysvětlení nemá obsahovat psychologické úsudky;
usermodel profil aktuálně přijímá deklarované pracovní informace.

Detail znalosti: obsah → rozsah a zdroj → historie → dostupné akce.
Akce vrací backend: read, propose correction, edit, restore, forget,
export. Existující AGENT/CREW panel je read-only; nové „Opravit poznatek“
potřebuje auditovaný mutation/proposal kontrakt. Nevystavit libovolný
zápis do souboru jen proto, že UI přidalo tlačítko.

Příklad srozumitelného sdělení: „Tento poznatek je dostupný agentům týmu
Copy site.“ Bez důkazu o skutečném vstupu do konkrétního běhu nepsat
„Kodi tento poznatek použil“. To vyžaduje záznam použití v kontextu.

## 7. Chování pro neúplná a nedostupná data

| Situace | Prezentace |
|---|---|
| Obsah se načítá | Skeleton; žádný nulový byte counter. |
| Obsah je známý a prázdný | „Zatím žádné uložené znalosti“ s vhodným dalším krokem. |
| Historie je vypnutá, live obsah dostupný | Obsah normálně; upozornění jen v Historii. |
| Dostupná je jen historická kopie | „Poslední zaznamenaná verze z …; aktuální obsah neověřen“. |
| Načtení selhalo | Lokální chyba a Opakovat; ostatní části zůstávají použitelné. |
| Nedostatečné oprávnění | Nezobrazovat ani počty či náhled cizích osobních dat. |
| Agent nemá crew | Agentovy dostupné znalosti; sdílený týmový scope není k dispozici. |
| Osobní profil je vypnutý uživatelem | „Ukládání poznatků o vás je vypnuté“ a správná správa preference. |
| Generátor není zapojený nebo nemá model | „Automatické vytváření není dostupné“, správci konkrétní náprava. |
| Znalosti jsou vypnuté pro použití | Uchovaný obsah lze prohlížet; pravdivá informace o omezení použití. |

I `projection.state=recorded` říká pouze, že cesta může být verzovaná.
Nedokazuje, že watcher skutečně běžel od počátku existence souboru.
Text „žádná verze znamená, že nic nebylo napsáno“ je příliš silný.

Persona panel navíc při `from_default=true` zahodí `content`, ačkoli hint
tvrdí, že syntetický obsah zobrazuje. Při crew fallback ignoruje `layer`
a může crew obsah ukázat jako agent override. Použít explicitní původ
agent/crew/default a odlišit efektivní text od editovaného override.
`save` při HTTP chybě pouze nastaví error a nevyhodí výjimku; nadřazený
editor poté zavře editaci. Opravit, zachovat draft a zůstat v editaci.

## 8. Integrace napříč funkcemi Crewship

| Funkce | Propojení v klientském produktu |
|---|---|
| Chat | Otevřít Memory konkrétního agenta a vlastní personalizaci bez ztráty rozepsané zprávy. Zpětný odkaz na zdroj jen s doloženým ID a oprávněním. |
| Issues/assignments/missions | Výsledek práce → související poučení; BRIEF jako zadání. Aktuální úkol nesmí vypadat jako trvalý fakt. |
| Routines | Stav poslední konsolidace, zdroj automaticky vzniklých poznatků; žádný dojem nekonečného samoučení. |
| Inbox/approvals | Návrhy změn paměti s explain/diff a approve/reject, jedna společná rozhodovací fronta. |
| Skills | Stabilní poučení může vytvořit návrh skillu; ukázat původ, schválení a následné použití existujícího importeru. |
| Journal | Audit změn, obnovení verze a připnutí poznatků; z detailu znalosti filtrovaný odkaz. |
| Files | Čtení souvisejících dokumentů přes sdílené Files rozhraní; `.memory` není neřízená cesta k obejití pravidel editace a osobních profilů. |
| Crew policy | Kdo může měnit týmové znalosti, co se aplikuje automaticky a co čeká na schválení. |
| Settings Privacy | Stejná self-service správa jako „O mně“, žádné další nezávislé přepínače. |
| Admin/provoz | Blob storage, dostupnost extrakce, poslední úspěšný sweep, chyby indexace; technické detaily mimo hlavní Memory obsah. |
| Export/import/backup | Jasný rozsah souborů a osobních dat, oprávnění, přehled kolizí a skutečný výsledek. Export není automaticky kompletní paměť ani důkaz zapomenutí. |

## 9. API: zachovat, doplnit, nezdvojovat

Existující building blocks:

- `GET /agents/{id}/persona`, `PUT/DELETE`, `GET /persona/history`; crew persona obdobně.
- `GET /agents/{id}/peers`, `GET/DELETE /peers/{userId}` — nejprve revidovat autorizaci.
- `GET/PUT /users/me/peer-consent`, `GET/DELETE /users/me/peer-cards`.
- `GET/DELETE /users/me/user-model`, `DELETE /users/me/user-model/facts/{key}`.
- `GET /memory/versions`, `GET /memory/versions/{sha}`, `POST /…/{sha}/restore`.
- `POST /memory/search/hybrid`, `GET /memory/health?crew_id=…`.
- `POST /consolidate/run`, proposed explain/diff/approve/reject; `/skills/proposed`.
- `GET/PATCH /agents/{id}/learning`; export/import a admin memory config/stats.

Vše pod `/api/v1`, s workspace a autentizací podle registrovaných kontraktů.
Memory health je agregát stavu paměťového systému, nikoliv hodnocení
inteligence, pravdivosti znalostí či jistoty konkrétního agenta.

### Nové potřebné kontrakty — návrh, zatím neexistují

**Memory overview:** vrátí dostupné scopes, explicitní capabilities,
stavy používání/zapisování/indexace/verzování, poslední úspěšné zpracování
a návrhy. Endpoint agreguje existující zdroje, netvoří další autoritativní
databázi znalostí. Nezjišťovat stav LLM tím, že se na každé otevření zavolá.

**Inventory a live content:** stránkovaný seznam povolených dokumentů
podle entity, stabilní opaque ID a autorizované čtení aktuálního obsahu.
Použít kanonický resolver cest, kontrolu workspace a scope; žádný libovolný
absolute path z klienta. Pokud storage jde číst ze správného host mountu,
otevření stránky nesmí vyžadovat start agentova kontejneru. Jiné providery
musí mít explicitní read capability a dostupnost.

**Obsah není historie:** odpověď má `content_state`, `content_source`,
`revision`, `updated_at`, `scope`, `owner`, `history_state`, `allowed_actions`.
Neznámé bytes a čas = null. Doplňkový hash neznamená potvrzení kvality.
History API ponechat jako timeline téhož dokumentu.

**Autorizované změny:** upravit/propose correction podle skutečné vrstvy,
podmíněné očekávanou revision; při souběžné změně vrátit konflikt místo
přepsání agentovy novější práce. Poskytnout diff a zachovat draft.

**Původ a použití:** per-fact source references ukládat vedle textu profilu,
nikoliv uměle vyrábět v UI. Pro „použito při běhu“ evidovat run ID,
zdrojovou revision a důvod zahrnutí/omezení. Není nutné ukládat celý
system prompt ani zpřístupňovat jiné osobní profily.

**Zapomenutí:** definovat rozdíl aktuální dokument / odvozené indexy /
verze / běžící session / zálohy. Nevracet obecné „vše zapomenuto“, pokud
byl odstraněn jediný soubor. Trvalé potlačení konkrétního faktu musí
respektovat budoucí extractor i restore/import; je to dodatečná funkce.

## 10. Plán dodání

1. **Pravdivé stavy a oprávnění:** opravit nulové počty při chybě, texty
   o generování peer karet, persona source/save, scope cizích peer dat.
2. **Jeden editor a hlavní Memory tab:** společný AgentEditor; explicitní
   mapa memory/personalization/self-learning/versioning; zachovat parity.
3. **Inventory + live read:** zpřístupnit skutečné znalosti i bez historie,
   oddělit snapshots, stránkování a chybové stavy, používat společné Files.
4. **O mně:** propojit již existující self-service user model/peer API,
   určit doručení napříč crew a kontrakt pro shared chat. Teprve pak
   dokončovat PeerCardSync a rozšiřovat zdroje personalizace.
5. **Výsledky → znalosti → skills:** návrhy, diff a schválení; provenance
   a konkrétní odkazy do Work/Chat/Journal, následně run usage evidence.

Nejprve kvalitní výchozí pohled. Až poté volitelná personalizace layoutu;
nedostupnost a skutečná rozhodnutí nelze trvale schovat. Struktura se má
ověřit na novém účtu, aktivním týmu, týmu s problémem a větším rosteru.

## 11. Přijímací scénáře

- A/B uživatelé stejného workspace, stejný agent: read/search/export/delete
  neodhalí ani nezmění cizí osobní profil podle schválené access policy.
- Přímý chat A, zpráva B ve skupině, kanál, routine a delegovaný běh:
  v každém explicitně ověřit, čí osobní kontext se smí vložit.
- Vypnutá historie + neprázdný live soubor: obsah dostupný, žádná falešná nula.
- Prázdný/no-op/disabled/failed extractor: různé stavy, žádný slib budoucí karty.
- Profil existuje v jiné crew: skutečný rozsah nebo správné doručení bez stale kopie.
- Memory off: ověřit samostatně soubory, Persona, recall, chat historii a učení.
- Default/crew/agent Persona: přesný účinný zdroj, žádné kopírování fallback do override bez úmyslu.
- Chyba uložení a souběžný zápis: draft zůstane, změna se nepřepíše potichu.
- Delete fact → další sweep; opt-out → další běh; import/restore po opt-out:
  výsledek odpovídá přesně deklarovanému rozsahu akce.
- Proposal schválený/zamítnutý z Inbox i Memory: jeden konzistentní stav.
- Mobil 390 px, klávesnice, návrat z Chat/Work, rychlé přepnutí entity/workspace.
- Poznatky za týden neaktivity: správný dostupný daily window, ne datum včerejška naslepo.

## 12. Rozsah důkazů

Klikací návrh: [crews-memory.html](../wireframes/crews-memory.html).
Obrazové náhledy: [desktop](../wireframes/crews-memory-desktop.png),
[mobil](../wireframes/crews-memory-mobile.png). Offline ukázka obsahuje
modelová data, přepínání agent/crew, Overview/Work/Memory/Team, osobní
pohled, detail znalosti, editor a pět scénářů dostupnosti. Je to návrh
informační struktury, nikoliv hotový sdílený editor s persistencí draftu.

Analyzován zdrojový kód, nový screenshot a historické PRD s kontrolou
aktuální implementace. Nebyl čten obsah osobních profilů živých uživatelů,
nebyla zapnuta extrakce, verzování ani provedena migrace. Případné příklady
v návrzích jsou modelová data.

Cílené existující Go testy prošly:

```sh
go test ./internal/orchestrator ./internal/api \
  -run 'Test(BuildPersonaBlock|BuildPeerCardBlock|BuildUserModelBlock|GetMyUserModel|ForgetUserModelFact|DeleteMyUserModel|PutConsent_OptOut|UserModelPrivacy|Peers_|Privacy_)' \
  -count=1 -timeout=5m
```

Oba balíčky PASS (orchestrator 0.015 s, API 5.247 s). Tyto testy pokrývají
již existující izolované mechanismy, nikoliv úplnou autorizaci všech cest
nebo živou konfiguraci Dev2. Nepotvrzují všechny nové návrhy tohoto auditu.

Offline wireframe prošel Playwright/Chromium kontrolou osobního pohledu,
detailu, otevření editoru, vypnuté historie, současného omezení, opakování
po chybě, crew/Team navigace, hledání a šířky 390 px bez horizontálního
overflow včetně editoru. Bez uncaught JavaScript errors. Desktopový
screenshot byl také vizuálně zkontrolován. Plné produkční testy/build se
nespouštěly; aplikační kód se nezměnil.
