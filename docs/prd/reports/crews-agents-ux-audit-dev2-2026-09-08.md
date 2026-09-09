# Crews & Agents — audit a návrh sjednocení

Rozšíření o paměť, personalizaci a propojení napříč produktem:
[Memory — podrobný návrh a ověření](crews-memory-product-design-dev2-2026-09-08.md).
Toto rozšíření doplňuje Memory jako hlavní záložku agenta i crew a
zpřesňuje význam editoru instrukcí.

Datum: 2026-09-08. Instance: Dev2. Zdrojový stav: `4456f2d9`, větev
`fix/chat-workspace-foundations`. Výstup je návrh, nikoli implementace.

## Rozsah a ověření

Prostudovány dodané čtyři screenshoty, stránka `/crews`, explorer, výchozí
roster, agent/crew canvas a jejich záložky, vytváření agenta a čtyřkrokové
vytváření crew, související hooky, registrace API a klíčové handlery.
Kontext: `docs/prd/create-surface-parity.md`, zprávy o sjednocení chatu
ze 7.–8. září, aktuální dashboard komponenty a performance brief v
`.claude/context/prd/`. Historický parity audit není aktuální specifikace;
níže uvedená zjištění vycházejí z aktuálního kódu.

Veřejná stránka vrací HTTP 200, procesy Dev2 běží na 8082/3012.
Nepřihlášený požadavek na lokální API agentů vrací 401. Webový nástroj
veřejnou stránku neotevřel; HTTP dostupnost byla ověřena pomocí curl.
Neproběhl přihlášený průchod formulářů, měření v prohlížeči ani zápisy
do uživatelských dat. Rozlišuji tedy staticky potvrzené chování od scénářů,
které je třeba reprodukovat za běhu. Go/UI testy nejsou v tomto auditním
kroku spuštěny; aplikační kód se nemění.

## Produktový závěr

Zachovat vizuální základ a strom crew → agent. Sjednotit vytváření a úpravu
do společného editoru. Přehled má odpovídat na čtyři otázky:

1. Co právě dělá?
2. Co potřebuje ode mě?
3. Jaký poslední výsledek přinesl?
4. Kam mohu pokračovat?

Dnešní agent overview je převážně katalog vazeb na datové entity.
Devět základních karet má podobnou vizuální váhu; i prázdná karta má
nadpis, počet, filtry, prázdný stav a patičku. Crew overview naopak obsahuje
tři čísla, úzký výsek událostí a nepřiměřeně prominentní správu avatarů.
Stejný designový systém proto zatím neznamená stejný způsob používání.

## Zjištění podložená kódem

| Priorita | Zjištění | Důkaz a důsledek |
|---|---|---|
| P1 | Historie komunikace se vydává za čekání na rozhodnutí. | `agent-canvas.tsx`: `total = escalations + assignments + approvals + peers.length`; overview z každého kladného total vykreslí „Waiting on your decision“ a tvrdí, že agent stojí. `/inbox` přitom vrací i queued/running assignments a posledních 20 peer konverzací oběma směry, bez omezení na nevyřešené. Oddělit skutečné lidské rozhodnutí, frontu práce a historii. |
| P1 | Chyba načítání může vypadat jako prázdný účet. | `canvas/use-agent-relations.ts` převádí neúspěšné odpovědi na `[]`; `use-agent-reach.ts` podobně degraduje chyby. Overview nevyužívá loading. Může zobrazit nulu a varování o credentials ještě před dokončením načítání nebo po chybě. |
| P1 | Některá čísla popisují pouze načtený vzorek. | Agent runs má pevný limit 100. Chats mají stránkování a total header, ale canvas čte jen tělo a používá délku. Crew issues se sčítají z jedné odpovědi. `/crews` načte jen 20 workspace missions a až pak filtruje podle crew. Pro metriky a celkové počty je nutný serverový součet nebo explicitní označení vzorku. |
| P1 | Create a edit nemají stejnou nabídku modelového nastavení. | Create má šest providerů včetně Cursor/Factory a vlastní `MODELS_BY_PROVIDER`; config má čtyři providery a `ConfigModel` čte `/api/v1/models`. Sdílet katalog, validaci a zachování uložených hodnot. |
| P2 | „From peers“ nereflektuje kontrakt odpovědi. | Overview používá `m.preview`, ale handler vrací `question`, `response`, `direction`. Navíc vždy ukazuje odesílatele, i když je jím právě vybraný agent. Zobrazit protistranu podle směru a skutečný text. |
| P2 | „Sessions“ směšují lidskou komunikaci a interní běhy. | Canvas volá `/agents/{id}/chats` bez `kind`; API již podporuje serverové filtrování druhů, stránkování, poslední aktivitu a unread. Overview ukazuje datum založení místo poslední aktivity. |
| P2 | „Tools 0“ neznamená, že agent nemá nástroje. | `useAgentReach` pro tuto kartu načítá pouze Composio bindings. Existují také resolved integrations, MCP a nástroje runtime. Přejmenovat na přesný rozsah nebo sestavit přehled efektivních přístupů. |
| P2 | „Channels“ není totéž co kanály v novém chatu. | Karta používá `notification-channels`. Oddělit chatové konverzace od externích oznámení. |
| P2 | Crew overview je nepřesný v označení stavů. | Není-li ERROR, hint počítá idle jako všichni minus RUNNING; může tím zahrnout čekající či jiné lifecycle stavy. Missions v hlavičce jsou seznam, karta ukazuje jen RUNNING/PENDING. Rozdíl „1 missions / 0 Missions“ není automaticky chyba dat, ale je špatně vysvětlený. |
| P2 | Feed není kompletní poslední aktivita. | `crew-activity-feed.tsx` filtruje pouze peer/escalation/assignment události, vynechává běžné chatové zprávy a další běhy. Assignment lifecycle vykresluje jako několik samostatných řádků s technickými ID. |
| P2 | Varování o přístupech je silnější než jeho důkaz. | Prázdné credentials znamenají text „its first run will fail“. Prázdný seznam ani HTTP chyba neprokazují konkrétní příčinu selhání. Ani `credential-readiness` není univerzální test spustitelnosti: kontroluje vazbu známých credentials na deklarované CLI nástroje. |
| P3 | Validace v create působí jako rozpor. | Na screenshotu je vidět Filip a současně chyba krátkého jména. Kód potvrzuje, že Filip je placeholder, nikoli zadaná hodnota. Použít „Např. Filip“ a chybu ukázat u pole po interakci/odeslání; předtím neutrální nápovědu. |
| P3 | Avatar a soubory mají více konkurenčních vstupů. | Crew overview nabízí Apply/Reset avatar vedle Open Files; stejné avatar akce existují v Settings a Files v hlavičce i záložce/spodním panelu. Vybrat primární vstup podle kontextu. |

Další staticky viditelný problém: `deriveTriggers` označuje počet historických
peer zpráv jako „waiting messages“ a z přítomnosti historie/role LEAD
odvozuje delegování. Historie není konfigurace schopností.

## Společný editor agenta

Tlačítko **Edit agent** s tužkou v hlavičce, vedle Chat a Files. Stejný
shell, rozměry, pole a komponenty jako **+ Agent**, režimy create/edit.
Po přesunu všech nastavení odstranit Configuration tab. Výchozí dialog
nepřevzít beze změny: dnes je sám příliš dlouhý a technický.

| Sekce | Obsah |
|---|---|
| Základní | Avatar, jméno, popis práce, crew, role; volitelná šablona při vytvoření. Slug automaticky, ruční změna v pokročilých. |
| Instrukce | Srozumitelně pojmenovaný editor chování, šablona a případné zděděné instrukce s uvedeným zdrojem. |
| Model | Jeden sdílený picker, dostupnost připojení; provider a adapter rozbalit podle potřeby. Nepřepisovat neznámý uložený model na výchozí. |
| Přístupy | Skills, připojené aplikace, přístupy zděděné z crew a externí oznámení. Děděné položky ukázat jako děděné. |
| Chat | Návrhy otázek a formuláře pro zadání; vizuální editor formuláře, JSON jako pokročilý režim. |
| Pokročilé | Tool profile, timeout, paměť, lead mode, technické identifikátory; existující legacy schedule musí jít vypnout. |

Jeden draft a explicitní **Save changes / Cancel**, stejná ochrana neuložených
změn jako ve sdíleném CreateSurface. V editaci se slug nesmí regenerovat
při změně jména. Aplikace šablony nesmí bez jasného úmyslu přepsat vlastní
instrukce a přístupy. Technická pole nesmí ovládat autonomii crew implicitně:
tool profile, síť a pravidla schvalování jsou odlišná nastavení.

Sdílet formulář neznamená poslat stejný POST/PATCH payload. POST agenta
neobsahuje `suggested_prompts` a `ask_forms`; ty jsou v UPDATE. Přístupy
se dnes ukládají až po vytvoření. Agent může být úspěšně vytvořen a přidání
integrace selhat. Výsledek musí ponechat ID vytvořené entity a umožnit
opakovat jen neúspěšný krok. U editace odesílat změněná pole a zachovat
semantiku vymazání/null/absence i nezměněné přístupy.

Samostatně prověřit vztah `system_prompt` a Persona API před sloučením
editorů. Create používá `system_prompt`, existuje také `/persona`, historie
a runtime injekce persony. Přejmenování pole samo o sobě jejich zdroje
nesjednotí. Záložku Configuration odstranit až po inventuře parity včetně
lead/ephemeral akcí, legacy schedule, feature-gated webhook informací a
editace promptu. Zachovat serverové i UI kontroly oprávnění.

## Agent overview — navržené pořadí

```text
Avatar  Kodi · co má na starosti                    Chat  Files  Edit  …
Copy site · stav práce

[Vyžaduje váš zásah: konkrétní důvod a akce]         jen pokud existuje
[Právě pracuje na konkrétním úkolu]                 nebo klidový stav

Dokončené úkoly · období   |   Běhy s chybou · období   |   Náklady · měsíc

Poslední výsledky                         Poslední konverzace
názvy práce, výsledek, čas, odkaz          protistrana/kanál, ukázka, čas

Schopnosti a přístupy — krátký souhrn, odkaz do odpovídající sekce Edit
```

Tři krátké seznamy maximálně: aktuální práce, výsledky, komunikace. Ukázat
3–5 položek a „Zobrazit vše“ s přesným filtrem. U nového agenta jeden
klidný onboarding stav s akcí „Otevřít chat“ a případným konkrétním krokem
nastavení. Nevytvářet devět prázdných katalogů ani nápovědu obsahující CLI
příkazy v běžném přehledu.

Dokončený běh není automaticky vyřešený úkol. Čísla pojmenovat podle
skutečného zdroje. První verze může zobrazovat dokončené běhy; název
„Dokončené úkoly“ vyžaduje odpovídající issue/assignment metriku.

## Crew overview a vytváření

Stejná hlavička a vizuální pořadí jako u agenta: účel týmu, aktuální stav,
záležitosti vyžadující zásah, práce a výsledky, kompaktní roster.
Roster: avatar, jméno, role a aktuální úkol; otevření agenta nebo chatu.
Případné číselné karty: aktivní práce, dokončená práce v období, náklady.
Technický image, CPU a TTL přesunout z dlouhé hlavičky do Provoz/Pokročilé;
skutečnou chybu prostředí ponechat viditelnou s akcí.

Základní záložky: **Overview · Team · Work**. Files v hlavičce, Edit crew
pro nastavení. Pod Work dát související úkoly, mise a automatizace tak,
aby klient nemusel nejdříve pochopit interní rozdíl všech entit. Technický
Journal a terminál přístupné v sekundárních akcích. Provozní nástroje
neodstranit, pouze je nepovyšovat nad výsledky práce.

Create crew dnes: Identity → Lineup → Container → Review, s možností
výchozích nastavení, šablony a importu. Doporučený běžný tok:
**Název a účel → Složení týmu → Souhrn a vytvoření**. Prostředí dostupné
přes „Upravit prostředí“ s viditelným souhrnem výchozího nastavení sítě.
Šablona i prázdné crew zůstávají; po vytvoření jasně ukázat postup přípravy
a konkrétní další krok. Edit crew sdílí pole a sekce, ale nehraje znovu
wizard nasazení šablony, který by mohl vytvořit další agenty.

## Sidebar a návaznost na celý produkt

Zachovat strom, avatary, vyhledávání, výběr a mobilní sbalení. Klidový stav
stačí decentní tečkou s dostupným textovým popisem; výrazné štítky věnovat
práci, chybě a skutečnému čekání. Součty neopakovat v toolbaru i sidebaru.
Zvážit připnutí oblíbených a přepínač „Vše / Vyžaduje pozornost“ až po
ověření většího rosteru. Dnešní seskupování podle stavu může při práci
přesouvat celé crew; zachovat stabilní polohu vybraného uzlu.

Ověřit vyhledávání při více než jedné stránce: aktuálně filtruje načtené
entity. Načtených 500 není totéž co celý workspace. Stejně tak přímý odkaz
na entitu mimo načtenou stránku nesmí skončit hláškou „not found“.

Celoproduktové rozdělení:

- Home/dashboard: co potřebuje člověka napříč workspace, výsledky a provozní stav.
- Crews & Agents bez výběru: orientace v týmech a lidech/agentech, s účelem a stavem.
- Crew/agent detail: práce a výsledky dané entity.
- Chat: čtení a pokračování v konkrétní konverzaci.
- Edit: změny chování, přístupů a nastavení.
- Journal/Provoz: podrobné události, diagnostika, technické nástroje.

Dashboard již obsahuje užitečné rozlišení „data neznáme“ versus „vše je
v pořádku“ (`attentionState`, `capacitySignal`). Tuto logiku přenést
do crew/agent přehledů. Pro chat používat společné link helpery a nový
kanonický vstup `/chat?conversation=…`; staré `/chat/{slug}?session=…`
odkazy zachovat kompatibilní. Stejné soubory otevírat společným Files
rozhraním s explicitním rozsahem agent/crew.

## API: co využít a co doplnit

Všechny uvedené zdroje musí zachovat workspace a oprávnění volajícího.

| Potřeba | Existující API | Omezení / další práce |
|---|---|---|
| Základní identita a edit | `/api/v1/agents`, `/agents/{id}`, `/crews`, `/crews/{id}` | POST/PATCH mají různé kontrakty; jeden editor potřebuje různé převody draftu. |
| Modely | `/api/v1/models?provider=…` | Sdílet create/edit katalog; zachovat vlastní modely a rozlišit nedostupný katalog. |
| Aktuální práce a poslední běhy | `/api/v1/runs?agent_id=…`, `/agents/{id}/runs`, `/issues?assignee_id=…&crew_id=…` | Agent runs je omezený seznam. Neodvozovat z něj historické total ani přesnou metriku za období. |
| Rozhodnutí a fronta | `/api/v1/agents/{id}/inbox`, `/api/v1/inbox` a approvals | Rozlišit lidské rozhodnutí, agentovu frontu, eskalaci a historii. Banner potřebuje konkrétní actionable položky. |
| Měsíční náklady agenta | `/api/v1/agents/{id}/inbox` | Vrací USD, LLM calls a tokeny za aktuální kalendářní měsíc UTC. Není to libovolné období ani garance úplného vyúčtování; chybějící ledger může skončit nulou. |
| Poslední přímé chaty | `/api/v1/agents/{id}/chats?kind=direct&limit=5` | Použít poslední aktivitu, unread a total; pro textový preview ověřit/doplnit vhodný souhrnný kontrakt. |
| Agent–agent komunikace | `peer_messages` z `/agents/{id}/inbox` | Již má otázku, odpověď, směr, protistrany, čas a status; vrací nejvýše 20. |
| Sdílené konverzace | `/api/v1/conversations`, `/{id}/agents`, `/{id}/activity`, `/{id}/messages` | Neexistuje zde hotový univerzální přehled komunikace jednoho agenta. Doplnit agregaci se zachováním viditelnosti; neprocházet všechny transcriptové endpointy na dashboardu. |
| Aktivita a výsledky | `/api/v1/journal` s agent/crew filtry | Projekce událostí do lidského shrnutí, deduplikace lifecycle, přesné odkazy na výsledek. |
| Metriky běhů a nákladů | `/api/v1/runs/insights?window=…`, `/api/v1/journal/spend?window=…` | Workspace agregace; insights má by-crew a omezené top-agents. Handler nečte agent/crew filtr. Pro přesný profil doplnit scope, neinterpretovat workspace totals jako agentovy. |
| Schopnosti a přístupy | `/agents/{id}/skills`, `/credentials`, `/integrations/resolved`, Composio bind, `/notification-channels` | Sjednotit efektivní rozsah, dědění a názvy. Nezobrazovat počet Composio toolkitů jako všechny nástroje. |
| Stav prostředí crew | `/crews/{id}/container-status`, `/capabilities`, `/credential-readiness` | Částečné signály, nikoliv kompletní test spustitelnosti. Ukázat zjištěnou příčinu a její rozsah. |

První krok může znovu využít existující API. Pro finální rychlé přehledy
navrhuji souhrnný kontrakt pro agent/crew overview: scope, období, čas
aktualizace, aktuální práce, lidská rozhodnutí, bounded seznam výsledků,
komunikace a metriky. Nový endpoint není v této chvíli implementovaný.
Jednotlivé sekce potřebují status dostupnosti, chybu a údaj o úplnosti,
aby dílčí výpadek nebyl „0“ a současně neshodil celý detail. Nezavádět
nový zdroj pravdy vedle journalu, assignments a konverzací.

## Metriky a přizpůsobení

Výchozí maximum tři čísla. Každé s obdobím a odkazem na zdrojový seznam.
Vhodné: dokončená práce, chyby vyžadující řešení, evidované náklady.
Technická úspěšnost běhů může být doplňková, ale není hodnocením kvality
výstupů. Bez běhů zobrazit „Zatím bez dat“, nikdy 100 %.

Odložit obecné skóre inteligence, odhady ušetřených hodin a žebříčky podle
tokenů/počtu zpráv: současná data jejich význam neprokazují. Délku běhu
nezaměňovat s dobou odpovědi v chatu. Náklady nerozmnožovat současným
sčítáním ledgeru, run metadat a journalu.

Přizpůsobení nabídnout lehké: zapnout náklady/automatizace/komunikaci,
pořadí volitelných sekcí a obnovit výchozí. Preference per-user/workspace;
neukládat je jako vlastnosti agenta. Důležitou chybu nebo rozhodnutí nesmí
jít trvale skrýt. Volný drag-and-drop builder není potřeba pro první verzi.

## Doporučené pořadí práce a akceptace

1. Opravit sémantiku stavů, chyby versus prázdná data, limity a scope počtů.
2. Společný AgentEditor a model picker, kompletní parity inventura; odstranit Configuration.
3. Nový agent overview, výsledky a komunikace napojené na společný Chat/Files.
4. Crew overview, Team/Work a sdílená editace; zjednodušení create crew.
5. Jemné úpravy sidebaru, volitelné metriky a preference.

Před implementací konkrétního issue zkontrolovat a získat claim dle
CONTRIBUTING. Audit nic neclaimuje, necommitoval ani nepublikoval.

Akceptační scénáře pro implementaci:

- Nový agent bez historie: jeden použitelný prázdný stav, žádná falešná chyba při loading.
- Běžící assignment a vyřešená peer konverzace: žádný banner vyžadující lidské rozhodnutí.
- Skutečné schválení: konkrétní důvod, správná akce, po vyřízení banner zmizí.
- Chyba API/403: „Nelze načíst“ či odpovídající dostupnost, nikoliv falešná nula.
- Více než 100 běhů/chatů a více než 20 missions: pravdivé total a scope.
- Create/Edit parity včetně vlastního modelu, Cursor/Factory, promptu, lead role a přístupů.
- Cancel a zavření editoru neuloží draft; přejmenování nezmění automaticky existující slug.
- Dílčí selhání přístupů po create nevytvoří duplicitního agenta při opakování.
- Kliknutí na konkrétní konverzaci otevře její správné ID, respektuje přístup a nevytvoří nový chat/běh.
- Přepnutí agent/crew/workspace a realtime refresh nezobrazí data předchozí entity.
- Desktop, 390px a tablet: klávesnice, focus, mobilní navigace, žádný horizontální overflow.
- Povinné Go testy a vet, frontend lint, relevantní regresní testy a produkční build;
  CodeRabbit review před případným mergem dle repo pravidel.
