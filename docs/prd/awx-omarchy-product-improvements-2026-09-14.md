# PRD: zlepšení Crewshipu inspirovaná AWX a Omarchy

Datum: 2026-09-14; revize po oponentuře: 2026-09-15; stav implementace aktualizován 2026-09-24. Tento dokument zachovává původní zadání a odhady. Priority níže jsou doporučeným pořadím, nikoli příslibem, že všechny funkce blokují release 1.0.

**Stav k 2026-09-24:** všech deset plánovaných iterací je sloučených do `main`: [#2567](https://github.com/crewship-ai/crewship/pull/2567), [#2571](https://github.com/crewship-ai/crewship/pull/2571), [#2661](https://github.com/crewship-ai/crewship/pull/2661), [#2663](https://github.com/crewship-ai/crewship/pull/2663), [#2666](https://github.com/crewship-ai/crewship/pull/2666), [#2669](https://github.com/crewship-ai/crewship/pull/2669), [#2670](https://github.com/crewship-ai/crewship/pull/2670), [#2673](https://github.com/crewship-ai/crewship/pull/2673), [#2677](https://github.com/crewship-ai/crewship/pull/2677) a [#2675](https://github.com/crewship-ai/crewship/pull/2675). [Integrační testy](reports/awx-omarchy-integration-2026-09-23.md) a [živé ověření dev3](reports/awx-omarchy-dev3-deployment-2026-09-23.md) pokrývají konkrétní části cílového průchodu, nikoli jeho úplnou akceptaci. Na dev3 prošla Page action → rutina → výsledek/journal i připnutá verze a hash; úspěšný agentní běh blokovalo `401 authentication_failed` u Anthropic API. Druhý účet prošel read/write/revoke na izolované instanci, ne na dev3. Rozšíření `routine.run` na Page action/replay/schedule-run zůstává produktovým rozhodnutím; O3 je Later a O5 čeká na cílovou roli a skutečný datový zdroj.

## Revize 2026-09-15: stav rozhodnutí a zdroj důkazů

Zohledněna [integrovaná oponentura](reports/awx-omarchy-prd-opponent-2026-09-15.md) proti main @ 5a0ad11f a čtyři přiložené klastrové reporty. Jejich reprodukce jsou důkazy oponenta; autor této revize znovu přečetl relevantní kód, ale neopakoval všechny testy. Stav souběžných PR v reportu je datovaný snapshot, před implementací jej ověřit.

Prováděcí pořadí a prompty: [10 iterací po oponentuře](awx-omarchy-implementation-iterations-2026-09-15.md). Tento revidovaný PRD a plán mají přednost před původními odhady a domněnkami handoffu z 2026-09-11.

Potvrzené mezery zahrnují podvržitelný invoking původ, chybějící issue run detail, chybějící per-run credential evidenci, rozdílné spouštěcí autorizace, nedostatečnou korelaci některých journal událostí a závod verze při Run again. To nejsou všechno nové produktové funkce; bezpečnostní opravy mají samostatnou první iteraci.

**Nevyřešená produktová rozhodnutí:** rozšíření `routine.run` na Page action/replay/schedule-run a zákaznická role/datový zdroj O5. Page handoff nyní znovu ověřuje přístup člověka i vybraného agenta při odeslání a předává pouze omezenou identitu Page, ne její datový payload. Multi-crew credential scope používá `credential_crews` po sloučení #2677. Doporučení oponenta sama o sobě nejsou autorizací rozšířit přístup.

Zachované požadavky: žádný raw error v URL, žádné mutující auto-send pod označením diagnostika, žádné automatické rozšíření lidských práv na agenta. Kolize s #2562 se musí vypořádat v jeho review; tato revize nic neposílá jeho autorovi a nic nemerguje. Per-rutinové execute granty i desktopy zůstávají mimo 1.0. O5 je obsahový balík, nikoli nový formulářový systém.

## 1. Problém a zamýšlený výsledek

Crewship již má agenty, credentials, policy, routines, historii práce a Pages. Uživatel však potřebuje snadno najít svou agendu, pochopit oprávnění a stav práce a předat konkrétní problém agentovi bez ručního skládání kontextu.

AWX přináší inspiraci v delegovaném spouštění automatizace, oddělení oprávnění, vazbách credentials a dohledatelnosti běhů. Omarchy přináší inspiraci v dostupnosti funkcí, promyšlených výchozích volbách a předávání kontextu diagnostice. Nepřebíráme jejich rozhraní ani provozní infrastrukturu.

Cílový průchod: člověk najde Page své agendy → vyplní několik pochopitelných vstupů → spustí povolenou rutinu → vidí výsledek či důvod čekání → může předat agentovi přesný podklad. Správce současně zjistí, která rutina používá který přístup a co jeho změna ovlivní.

## 2. Vztah k dosavadním dokumentům

Tento dokument je společný produktový vstup pro všechny níže uvedené návrhy.

- [Detailní implementační zadání diagnostiky a palety](release-1-0-run-diagnostics-pages-palette-handoff-2026-09-11.md) zůstává prováděcím zadáním funkcí O1/O2/O3. O3 je tam označena A2; zde neměníme její rozsah ani odhad.
- [Původní výzkum Omarchy](omarchy-crewship-research-2026-09-11.md) zůstává zdrojem rozboru kódu, desktopových variant a ekonomického modelu. Jeho dřívější pořadí „nejprve desktopový konektor“ není prioritou release 1.0: pozdější uživatelské upřesnění upřednostňuje levná produktová propojení a existující Pages.
- [Pages Apps v1](pages-apps-v1.md) vlastní požadavky tvorby aplikací, runtime, publikování, oprávnění a vzhledu. Nepřidávat paralelní aplikace, témata, katalog ani nový publish proces.
- [Kontrakt issues a lidské/agentní práce](issues-human-agent-work-contract-2026-09-07.md) a stávající policy zůstávají zdrojem pravdy pro význam outcome, schvalování a zastavení.

Produktové požadavky tohoto PRD nemění existující bezpečnostní kontrakty. Pokud vstupní audit odhalí rozpor či chybějící backendovou schopnost, implementující agent jej zaznamená a odhadne samostatně.

## 3. Inventura: co nevytvářet podruhé

Aktualizováno podle oponentury checkoutu 5a0ad11f dne 2026-09-15; před implementací ověřit skutečný aktuální stav a souběžné PR.

| Oblast | Existující základ | Zamýšlená změna |
|---|---|---|
| Rychlá navigace | CommandPalette, Recent, položka sekce Pages | Konkrétní Pages jako výsledky hledání |
| Credentials | Used by, přiřazení, poslední použití, readiness, přístup/reveal | Doplnit dohledatelné vazby na rutiny a dopad změny |
| Oprávnění | RBAC, policy, sidecar, autorizační cesty akcí Pages | Srozumitelně vysvětlit oddělená práva a ověřit delegované spuštění |
| Běhy | Journal, časová osa, routine versions, replay, outcomes | Zpřístupnit důkazy, původ běhu a sémantiku opakování |
| Zadání práce | Vstupní formuláře routines, ask_forms, doporučené otázky | Promyšlené první agendy a konkrétní nabídky |
| Aplikace | Pages a rozpracované Apps v1 | Propojit aplikaci s chatem; respektovat její současný runtime |
| Schvalování a upozornění | Inbox, waitpoints, notifikace | Využít existující cesty, nevytvářet druhý inbox |
| Paměť | Stávající memory/episodic/recall | Nezaměňovat filesystem desktopu za paměť agenta |

## 4. Funkce a priority

P1 = doporučený první balík pro 1.0, P2 = navazující doplnění podle kapacity, Later = samostatné rozhodnutí. Odhady jsou pro úzké varianty a obsahují cílené testy; závisí na existenci bezpečných datových cest.

| ID | Funkce | Inspirace | Priorita | Odhad |
|---|---|---|---|---|
| O1 | Pages v paletě + Recent podle identity/workspace | Omarchy | první malé UI rozšíření | 0,5–1 dne |
| O2 | Kontrakt, podklad rutin a nový detail issue runu | Obě | po bezpečnostní/korelační kontrole | 3,5–4,5 dne |
| A1a | Opravy důvěryhodnosti identity; matice spouštěcích cest | AWX | první bezpečnostní iterace | 1–1,5 dne, širší nálezy zvlášť |
| A1b | access/me rutiny a credentialu + Your access | AWX | po A1a a rozhodnutí matice | 3–5 dní |
| A2 | Závislosti podle typu a současného resolveru | AWX | po rozhodnutí crew scope; #2675 stojí na #2677 | 2,5–3,5 dne |
| A3 | Původ běhu; credential použití „nezaznamenáno“ | AWX | po O2 a A1a | 1 den |
| A4 | Verze při Run again, očekávaný hash a CLI | AWX | backend samostatně, UI dle souběžných PR | 1,5–2,5 dne |
| O4 | Nová session, draft, metadata Page kontextu | Omarchy | po vyřešení identity a přístupu cílového agenta | 2–3,5 dne bez nové agentní read cesty |
| O5/A5 | Parametrizovaný obsahový pack pro jednu roli | Obě | mimo desítku, po volbě role a zdroje | 1–1,5 dne + akceptace |
| O3 | Scoped evidence tool + řízené vysvětlení | Omarchy | Later | +3–6 dní, ověřit podle budoucího scope |

O2 a A3 sdílejí data a UI: jejich odhady nesčítat automaticky. O4 a O3 mohou sdílet předání do chatu, ale mají odlišné podmínky oprávnění a nesmějí se zaměnit.

## 5. O1 — Pages v command palette

Použít stávající apiFetch fan-out při otevření palety a autorizovaný seznam metadat Pages, ne trvale aktivní usePages hook. Na auditovaném HEAD je list nestránkovaný, úplný a omezený limitem počtu Pages/workspace; nese i metadata aplikací. Tyto vlastnosti zamknout testem, před změnou ověřit současný kontrakt. Neposílat neexistující parametr limit.

Přidat výsledky konkrétních Pages s názvem, PageGlyph a oprávněně dostupným sekundárním popisem; navigace přes existující helper. Zachovat obecnou položku Pages.

Recent dnes není izolované. O1 oddělí klíč podle uživatele i workspace pro všechny skupiny, starou nescopeovanou historii nepřenese (jednorázový reset, uvést v handoffu). **Nepřebíráme návrh oponenta neověřovat dostupnost Page v Recent:** při otevření porovnat Page recent s právě načteným autorizovaným seznamem; při chybě načtení je neprezentovat jako dostupné. 404 až po kliknutí nezabrání úniku starého názvu po revokaci. Nejde o jednotlivé dotazy pro každou položku.

Místa: components/command-palette.tsx a existující palette testy. Akceptace: klik/Enter, workspace switch s pending requestem, ztráta workspace, logout/login jiného uživatele, revokace/smazání a chyba listu. Staré výsledky se vyčistí ihned, pozdní odpověď je neobnoví; ostatní skupiny při výpadku Pages fungují.

## 6. O2 + A3 — přehled běhu, reference a původ

Společný přehled v existujícím detailu routines a novém lehkém detailu agentního běhu issue ukáže stav, poslední zaznamenanou činnost, čas, známý důvod čekání/selhání, zdrojové odkazy a stabilní referenci. Akce Kopírovat podklad pro agenta vytvoří omezený, předem viditelný souhrn. Tato verze nevolá LLM.

A3 přidává na stejné místo původ: iniciátor nebo trigger, historická verze rutiny, dostupné necitlivé vstupy, reference týmu/agentního běhu. Per-run důkaz použití credentialu nyní neexistuje pro žádný engine; v 1.0 zobrazovat „Deklarováno v použité verzi: …“ ze snapshotu a „Použití: nezaznamenáno“. Historická fakta se nesmějí odvozovat z dnešní konfigurace. Neukládat tajemství kvůli reprodukovatelnosti. Pokud údaj chybí, napsat „nezaznamenáno“. Audit posledního sidecar fetch není důkazem použití v daném běhu. Nový zápis credential.resolved je volitelná samostatná write-side změna, nikoli podmínka dokončení 1.0.

Implementační místa: `internal/api/issue_handler_runs.go`, `internal/api/runs.go`, `internal/api/pipeline_runs.go`, `internal/journal/runs.go`, `lib/run-activity.ts`, `components/features/issues/issue-runs-card.tsx`, `components/features/routines/routine-run-detail.tsx` a stávající activity detail.

O2 začne čistou TS funkcí `lib/run-evidence.ts` a sdílenými vstup/výstup JSON fixtures; nový endpoint jen pro formátování nepřidávat. Budoucí O3 musí mít serverový builder a scoped autorizaci, fixtures ověří shodu projekcí. **Klientský allowlist není bezpečnostní hranice API.** Dnešní workspace-wide čtení raw dat tím není vyřešené a nesmí se označit za crew izolaci. Nepřidávat nové read plochy nebo agentní čtení s odkazem na to, že data již někde unikají.

Lehký issue detail musí skutečně načíst agentní run (`GET /runs/{id}`) místo předpokladu, že existující routine detail umí oba enginy. Rozlišit 404 typu běhu od 403/500; po obecné chybě nezpřístupňovat alternativní širší cestu.

Export allowlist: ověřené reference, dostupné sourceLinks, status/outcome/hard-stop, časy, známý krok, strukturovaná failure klasifikace a omezené schválené zprávy, trigger a snapshot verze/hash. Error max. 1 KiB je pouze velikostní limit, nikoli sanitace: raw volný text může obsahovat tajemství; převzít existující bezpečnou projekci, jinak obsah vynechat a přiznat. Vyloučit task/prompt, inputs, metadata, output, step_outputs, definition, sub_spans, result_summary, session_id a obecné journal payloady. U humanizace neznámého typu nepřebírat volný fallback text. Limit celého podkladu 16 KiB UTF-8 a 20 událostí platí po finálním sestavení; označit oříznutí a zachovat terminální důkaz.

Důležité invarianty:

- Oponentura zjistila chybějící run context v agentních krocích rutiny. Korelační opravu provést samostatně a testovat producent→uložená událost→run filtrovaný dotaz; samotné přidání context hodnoty není důkaz kompletní korelace. Sidecar/LLM události bez ověřené per-run vazby vynechat, uvést neúplnost a nepoužívat agent/mission/container fallback.
- Autoritativní řádky pipeline_runs/assignments mohou nesouhlasit s journal agregátem. Rozpor přiznat. Bez run_id není automaticky prokázáno „nikdy se nespustil“: mohl selhat emit; bez důkazu uvést „běh není dohledatelný“.
- Agent run je korelován přes trace_id; pipeline události používají actor_id. Assignment ID není automaticky run ID. Nevytvářet vztahy podle společného kontejneru nebo blízkého času.
- Fingerprint seskupuje podobné chyby; není jedinečným identifikátorem výskytu. Pro reference použít existující plná ID, volitelně event ID a step ID.
- Poslední záznam není tvrzení o aktuálním procesu. Exit 137 není sám důkaz OOM. Exit 0 není důkaz splnění úkolu. Oddělit outcome, procesní stav a potvrzené externí účinky.
- Zachovat bounded export, allowlist polí, scope a sanitaci dle detailního handoffu. Žádné celé prompty, env, raw payload dump ani celokontejnerový sběr.

Akceptace: dva souběžné běhy se nesmíchají; odkaz/reload otevře správný běh; assignment bez runu, timeout, cancellation, NEEDS_HUMAN, stará data a výpadek API mají odlišné pravdivé zobrazení. Export neobsahuje cizí ani zakázané údaje. Otevření a kopírování nic nespouští.

## 7. A1 — delegované spuštění a vysvětlení přístupu

Dnešní operátor je člen workspace s routine.run; capability platí workspace-wide, nikoli jen pro Import objednávek, a může ji mít i VIEWER. Příklad „smí jen jednu rutinu“ není v 1.0 implementovaným kontraktem. Operátor nemá tímto grantem právo editovat kód či odhalit credential. UI vysvětlí nezávislé možnosti: zobrazit, spustit schválenou definici, upravit definici, přiřadit/použít credential, odhalit secret a spravovat přístup. Použít aktuální názvosloví produktu, nepřidávat kopii rolí AWX.

Backend zůstává autoritou. Výpis vychází z autorizačních rozhodnutí, ne z odhadů podle názvu role v prohlížeči. Pokud již existuje ekvivalent, pouze jej propojit. Úvodní audit již proběhl: přímý run a slash capability respektují, Page action, replay a schedule-run ne. Zda ji mají respektovat všechny tyto cesty, je rozhodnutí R2; run_batch doporučujeme ponechat zvlášť kvůli rozsahu a nákladům. Nové endpointy access/me jsou aditivní práce v A1b, ne existující schopnost. Jejich verdikt musí sdílet funkce se skutečnými handlery, včetně omezení, která lze vyhodnotit až při akci (fresh login, reason, governance).

A1a: potvrzené podvržení X-Crewship-Invoking-Crew/-Agent přes JWT opravit před přidáváním provenance UI. Identitu odvozovat z ověřeného auth contextu. **Pouhá přítomnost libovolného crew-bound tokenu nestačí k důvěře v hodnotu hlavičky:** crew se musí rovnat scope tokenu, agent patřit do tohoto crew/workspace a identity být oprávněné pro danou cestu. JWT hlavičky ignorovat nebo odmítnout podle kontraktu. Testovat také škodlivý interní caller, ne jen JWT.

Oponentura prokázala zápis podvrženého původu, nikoli živé obejití trust grantu. Trust cesta podvrženou crew používá; přidat samostatný test, že oprava zabrání použití cizího trust grantu, a ověřit i legitimní fallback autor crew. Historické invoking údaje z doby před opravou nezískají opravou zpětně důvěryhodnost.

Oddělit oprávnění člověka spustit konkrétní automatizaci a runtime oprávnění její vykonávající identity. Execute nesmí otevřít libovolný příkaz či endpoint se stejným credentialem. Editace privilegované rutiny nesmí obcházet právo použít změněné zdroje. Vstupy formuláře musí být omezené schématem a serverovou validací.

Místa pro audit: `internal/api/pipelines_exec.go`, `internal/api/pages_actions.go`, `internal/policy/`, `internal/sidecar/`, `lib/permissions/`, credential detail a spouštěcí formulář rutiny. Nekopírovat pouze frontendový canRun boolean.

Akceptace: čtenář bez routine.run nemůže spustit; operátor spustí povolenou rutinu, ale neupraví definici/credential; modifikované HTTP tělo ani hlavičky nejsou eskalace; po revokaci další spuštění selže podle kontraktu. Odebrání oprávnění během běhu musí mít zdokumentovanou současnou sémantiku, nikoli slib okamžitého zastavení, které runtime neumí. Lidská práva nepřenášet automaticky na agenta.

## 8. A2 — závislosti credentialu a dopad změny

Rozšířit existující Used by o strukturovaně dohledatelné rutiny s odkazy. Oddělit „nakonfigurováno k použití“ a „zaznamenané skutečné použití“. Před odebráním či relevantní změnou nabídnout přehled známých závislostí; nezahlcovat univerzálním potvrzením každé editace.

Stávající `components/features/credentials/credential-detail-sheet.tsx` již má Used by, assignment coverage, Your access a readiness. Nový přehled staví na nich. Vazby nejsou přímo uložené; odvodit sjednocením credentials_required, credential_ref.type a šablon secrets podle typu. Ověřený resolver preferuje crew-specific kandidáta před workspace fallbackem a teprve v této prioritě vybírá nejnovější ACTIVE; nekopírovat zjednodušení „nejnovější ve workspace“. Sdílet stejnou rozhodovací logiku, ale pro náhled nedekryptovat hodnoty. Rozlišit configured, would-resolve (odhad při současné konfiguraci) a recorded pouze s jeho skutečnou úrovní důkazu, dnes bez run attribution.

Audit odhalil rozdíl `credential_crews` versus legacy `crew_id`. R3 v #2677 volí junction jako pravdu pro výběr rutiny; parity test na migrované databázi porovnává resolver, probe, viditelnost člena a doručení agentovi. #2675 na něm stojí a používá stejný výběr pro náhled závislostí. Dokud oba PR nejsou mergnuté, není tato mapa chováním `main`. A2 zahrne error≠empty i v auditu/fields credential sheetu. Pokud runtime vybírá credential dynamicky, uvést neúplnost; nepředstírat statickou analýzu libovolných skriptů.

Akceptace: konfigurační vazba bez běhu se nezobrazuje jako skutečné použití; nedostupné zdroje či omezená oprávnění nejsou „nikdo nepoužívá“. Názvy skrytých rutin se neprozradí. Rotace stejné identity zachová vazby, zrušené vazby zmizí po obnově. Chyba refetche označí údaje jako neověřené, i když cache zachovala předchozí hodnoty.

## 9. A4 — opakovat aktuální nebo původní verzi

Před opakováním musí člověk vědět, jaká verze skutečně poběží. Příklad: „Původní běh v4. Spustíte aktuální v6 se stejnými uloženými vstupy.“ Pokud podporovaná cesta dovolí původní verzi, nabídnout ji explicitně. Neoznačovat ani tuto volbu za identickou reprodukci externího světa či původních secrets.

UI Run again ve skutečnosti používá `/run` s volitelným pinned_version, nikoli `/replay`; zůstane na této cestě. Replay API má vlastní odlišné auth/idempotency chování; jeho změny neposouvat skrytě do A4. Nevyžadovat replay_of od cesty, která jej nepodporuje.

Doplnit expected_definition_hash a čitelný 409 při změně; UI musí použít čerstvě načtený hash verze, kterou člověku ukazuje. **Kontrola a přijetí snapshotu musí být svázané:** zabránit posunu HEAD po porovnání hashe, ale před výběrem skutečné definice; executor musí dostat právě ověřený snapshot/pin. U delayed/debounce cest výslovně vymezit podporu; nepřijmout parametr, jehož slib pozdější dispatch nedodrží. CLI doplnit odpovídající --version a případný očekávaný hash dle existujícího kontraktu. Zachovat vstupní validaci, aktuální autorizaci a pravidla pro vedlejší účinky. Pokud se mezi dialogem a submit změní HEAD, nesmí být nepozorovaně provedena jiná verze než ta, kterou UI slíbilo; použít podporované připnutí/verzovní kontrolu, jinak přiznat nutnost backendového doplnění.

Akceptace: v4→v6 jasně rozlišeno, nepřístupná/smazaná verze má čitelnou chybu, změna HEAD je pokryta testem, do URL nesmí přijít tajemství. Současné inputs se archivují doslova a nemají secret typ; UI to musí sdělit. Tento úkol nezavádí bezpečné archivování secrets a textové upozornění není jeho technickou náhradou. Opakování není automatický retry po chybě spojení; existující idempotency a schvalování zůstávají platné.

## 10. O4 — Zeptat se na tuto Page

Akce v hostitelské hlavičce Page otevře rozepsanou zprávu s viditelným odstranitelným kontextem stránky. Úzká verze pracuje s celou Page; výběr libovolných řádků uvnitř generované aplikace je další rozsah.

Předávat stabilní referenci a čas, případně omezená autorizovaná data. Člověk i cílový agent musejí mít odpovídající přístup; lidská metadata nejsou automaticky oprávněním agenta. Kontext je nedůvěryhodný obsah, nikoli nástrojové instrukce. Nezachycovat DOM celé aplikace ani skryté panely.

Tlačítko patří do SubBar v `components/features/pages/pages-layout.tsx`, nikoli page-view. Využít `app/(dashboard)/chat/chat-client.tsx`, composer a stávající message metadata/provenance. Současné `?prompt=` auto-odesílá: pro tento scénář vyžadovat režim draft. Citlivá data nevkládat do URL. Současné ?prompt= míří na nejnovější existující vlákno. O4 vyžaduje explicitní novou session bez autoSendInitial a nesmí přepsat uložený draft. Server persistuje jen ask_submission, proto přidat validovanou whitelistovanou provenance page_context; metadata z klienta nejsou důkazem přístupu ani autentickým serverovým snapshotem.

Úzký návrh kontextu = identifikace a oprávněná metadata/stavy panelů, nikoli payloady aplikace. I názvy a owner/stavy mohou být citlivé: povolený cílový agent a rozsah musí projít autorizační kontrolou. Pokud tuto kontrolu stávající cesty neumí, dokončit nejprve obecný draft handoff; tlačítko s automatickým kontextem odložit nebo odhadnout novou serverovou schopnost. Klientské DTO autorizované pro člověka samo nesplňuje průnik lidských a agentních práv. Serverové znovunačtení pro agenta je samostatné rozšíření, v odhadu úzké varianty není.

Akceptace: správná Page a agent, odebrání kontextu před odesláním, žádné nechtěné auto-send či duplikace při změně session, revokace přístupu, obnova chatu se zachovaným původem zprávy.

## 11. O5/A5 — první užitečné agendy

Pro jednu vybranou zákaznickou roli připravit tři konkrétní zadání na existujících formulářích: například přehled objednávek po termínu, porovnání týdnů a návrh odpovědi zákazníkovi. Výběr role ověřit s aktuálním obchodním scénářem; bez tohoto rozhodnutí nepřidávat generické šablony do všech agentů.

Využít ask_forms, doporučené otázky, `components/features/chat/chat-empty-state.tsx` a `components/features/routines/routine-run-inputs-dialog.tsx`. Současné obecné Try it u dovednosti není nutné rušit; pro podporovaný scénář doplnit konkrétní zadání a jasný výsledek. Názvy pro člověka, vstupy se serverovou validací, žádný druhý formulářový framework.

Akceptace: každé zadání funguje s dostupnými daty; chybějící připojení je srozumitelné před spuštěním nebo při odmítnutí; formulář neslibuje neexistující schopnost. Rutinní sběr dat zůstává deterministickým skriptem/rutinou, nepřidávat pravidelné LLM volání jen pro zobrazení dashboardu.

## 12. O3 — agentní vysvětlení chyby, oddělené rozšíření

Navazuje na O2. Jeden serverový evidence builder a oprávněný read tool, pokud vhodný již neexistuje. Explicitní spuštění, časový/tokenový limit, existující paymaster a audit. Read-only vynutit dostupnými nástroji a credential scope, nikoli pouze instrukcí v promptu. Výsledek oddělí fakta, hypotézy a chybějící důkazy, odkáže na zdroje a navrhne další krok.

Automatické opravy, restarty, opakování úloh a neomezený sběr logů nejsou součástí. Změna oprávnění, prompt injection v logu a limit podkladu musejí být testovány. Kompletní postup je v navázaném handoffu A2.

## 13. Omarchy: zachované pozdější směry

Aby „zapracovat vše“ neznamenalo ztratit původní výzkum, tyto varianty zůstávají evidované, ale mimo release 1.0:

- Desktopový konektor k současnému počítači: explicitní předání souboru/odkazu a schválení. Nevyžaduje migraci zaměstnance na Omarchy.
- Browser/desktop pro konkrétní úlohu agenta, s možností lidského převzetí; životnost výpočetního prostředí oddělit od identity, souborů a paměti.
- Omarchy jako experimentální VM profil; nenahrazuje CLI adapter ani orchestrátor. Port celého desktopového OS do privilegovaného Dockeru není výchozí řešení.
- Firemní vzdálené desktopy: samostatný produktový pilot se souběžností relací, latencí, kompatibilitou, obnovou, podporou a ekonomikou. Výkon/hustota relací nejsou dosud změřené.
- Jednotné pluginy a metadata příkazů jako inspirace pro konzistenci; nepřebírat libovolné nesandboxované shell akce, auto-approve agentů ani mutabilní desktopové aktualizace jako model spravované služby.
- Témata, aplikace, usage panel a notifikace navazují na Pages, paymaster, journal a Inbox, ne na nové konkurenční zdroje pravdy.

Původní odhady desktopového výzkumu: pilot 10–15 dní, omezený agentní desktop 55–90 dní kumulativně, plná desktopová služba 120–220 dní kumulativně. Jde o scénáře z 2026-09-11, nikoli aktuální nabídku; ceny infrastruktury a modelů před investicí znovu ověřit. Ekonomický model, recenze a zdrojový audit zůstávají v původním výzkumu.

## 14. Realizace, rozpočet a měření

Pořadí je nyní v [plánu deseti iterací](awx-omarchy-implementation-iterations-2026-09-15.md). Bezpečnostní oprava předchází provenance UI; O1 je nezávislé. O5 obsah a závěrečná integrovaná akceptace jsou mimo deset vývojových balíků, ale závěrečná akceptace zůstává povinná před tvrzením, že je hotový celý scénář.

Oponent odhaduje deset úzkých balíků na přibližně 15–22 člověkodnů včetně cílených testů a repo gates, bez čekání na review. Jde o odhad, nikoli rozpočet schválený uživatelem. Při ilustrativní sazbě 10–15 tisíc Kč/den je to 150–330 tisíc Kč. Dodatečné bezpečnostní mezery, nové agentní auth schopnosti, O5 a závěrečná akceptace mohou přidat práci; neskrývat je v původním balíku 3–6 dní. Tento starší odhad platil pro menší rozsah a neplatí pro všechny iterace po oponentuře.

Měřit před a po: čas najít konkrétní agendu, čas určit běh a připravit podklad pro podporu, schopnost operátora spustit práci bez správcovských práv a schopnost správce určit známé závislosti credentialu. Na nejméně třech reprezentativních uživatelích projít stejné úkoly; nejde o statistický důkaz, ale o vstup pro další prioritizaci. Nepřidávat telemetrii citlivých vstupů kvůli měření.

## 15. Společné podmínky přijetí

- Před změnou dodržet AGENTS.md/CODEX.md/CONTRIBUTING, ověřit současný HEAD, WIP, claims a souběžné PR. Nevytvářet duplicitní implementaci podle zastaralých podkladů.
- Ke každému balíku doložit producenty dat, identity, autorizaci a aktuální vs historické údaje. Unknown, loading, error, stale a empty mají odlišnou sémantiku. Refetch error se nesmí ztratit v cached datech ani přes prop předaný jiné komponentě.
- Testovat skutečné integrační cesty, tenant/crew izolaci, revokaci, časové závody, clipboard chyby, dlouhé hodnoty a klávesnici. Neomezit testy na očekávání přesné formulace helperu.
- Podle změny cílené frontendové/Go testy a repozitářová verifikace: go test ./... -count=1, go vet ./..., pnpm lint a pnpm build pro UI/cross-cutting implementaci. U tohoto dokumentačního PRD se ověřují odkazy a konzistence, nikoli znovu celý produktový suite.
- Živý read/write/revoke průchod druhým účtem je samostatná akceptace oprávnění, serverové testy ho nenahrazují. Nevydávat založení testovacího účtu za důvod plošně seedovat celé demo.
- Odevzdat změny, důkazy testů, známé limity, aktualizovaný handoff a skutečný review status podle repo procesu. Toto PRD samo neautorizuje merge, nasazení nebo změny zákaznických oprávnění.

## 16. Zdroje inspirace

AWX dokumentace byla konzultována jako konkrétní verzovaná dokumentace 24.6.1, nikoli jako tvrzení o nejnovější vydané verzi:

- [AWX Credentials](https://docs.ansible.com/projects/awx/en/24.6.1/userguide/credentials.html): delegované použití bez odhalení tajemství, vazby na job templates a běhy.
- [AWX RBAC](https://docs.ansible.com/projects/awx/en/24.6.1/userguide/rbac.html): Execute/Use/Admin a práva při změně zdrojů šablony.
- [AWX Jobs](https://docs.ansible.com/projects/awx/en/24.6.1/userguide/jobs.html): původ, prostředí a strukturované události běhu.
- [AWX Job Templates](https://docs.ansible.com/projects/awx/en/24.6.1/userguide/job_templates.html): formuláře Surveys a sémantika relaunch; relaunch není doslovné přehrání původního běhu.
- [Omarchy příkazový vstup, auditovaný commit](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/bin/omarchy).
- [Omarchy diagnose-crash](https://github.com/omacom/omarchy/blob/0534987009061cbe2dacdde4ad564092ab698d12/default/agents/skills/diagnose-crash/SKILL.md): důkazy, přiznaná nejistota a oddělení diagnostiky od oprav.
- Další zdroje, recenze a ekonomické předpoklady: [výzkum Omarchy](omarchy-crewship-research-2026-09-11.md).
