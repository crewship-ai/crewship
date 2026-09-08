# Crews & Agents — navigace editorů

Navazuje na vizuální sjednocení v PR #2464, issue #2463. Požadavek: zachovat barvy, fonty a komponenty, změnit uspořádání nepřehledných Edit formulářů.

## Rozdělení

- Agent Create/Edit: Identity; Model and execution; Instructions and persona; Permissions; Chat.
- Crew Edit: Identity; Environment; Tools; Tool versions; Network; Resource limits. Vytvoření crew si ponechává vedený výběr týmu; sdílí upravené síťové ovládání.
- Levé menu je stále dostupné; na telefonu jej nahrazuje označený select. Výška editoru je stabilní, posouvá se pouze obsah. Přepnutí sekce vrací obsah nahoru, nepřepisuje draft a nic neukládá. Footer a ochrana rozepsaných změn zůstávají společné.

## Model a spuštění

Poskytovatel, konkrétní model a CLI runner už nejsou schované v Advanced. Používají existující značky z registrů (Anthropic, OpenAI, Google, Cursor, Factory, Ollama; Claude Code, OpenCode, Codex CLI, Gemini CLI, Cursor CLI, Factory Droid). Přepnutí nativního runneru vybere odpovídajícího poskytovatele a nahradí nekompatibilní model výchozím modelem z katalogu. OpenCode umožňuje měnit poskytovatele při zachování runneru. Pouhé otevření formuláře zachovává i vlastní modelové identifikátory.

Timeout je Maximum run duration v minutách; API nadále dostává celé sekundy a stejný rozsah 60–7200. Tool profile je funkční nastavení runneru, nikoliv obsolete pole: Permissions ho vysvětluje jako Read and plan / Workspace work / Full tool access, s výslovným rozlišením od síťové politiky a individuálních grantů. Podpora omezení závisí na runneru. Lead mode zůstává dostupný leadům.

Instrukce, persona override a injekce paměti jsou vedle sebe; persona nemá vnořené duplicitní skládací hlavičky. Neexistující API pole (temperature, max_tokens, delegation caps) nejsou prezentována jako zákaznické nastavení. Billing account je v Permissions, se zachovanou informací o okamžité účinnosti. Při změně poskytovatele/runneru draftu se nejprve musí uložit tento výběr, aby account editor neměnil přístup pro starý runner.

## Prostředí a síť

Base image je samostatná oblast. Preinstalled tooling a přesné verze jazyků/nástrojů mají vlastní položky menu a zachovávají původní katalog, hledání i instalované výběry. RuntimeConfig zůstává jednou instancí a zachovává devcontainer passthrough a mise data. Změny nesmí vznikat pouhým procházením nastavení.

Síťové volby přímo mapují stávající API:

| UI | API | Význam |
|---|---|---|
| Provider APIs only | restricted + prázdné allowed_domains | Žádné přidané hosty; povinné provider/platform domény zůstávají povolené. Není to plné offline. |
| Selected hosts | restricted + allowed_domains | Povinné domény a hosty uvedené uživatelem; podporuje stávající wildcardy. |
| Open network | free | Internet i privátní síť/localhost; metadata a vyhrazené adresy nadále blokuje proxy. |

API `crews_create.go` / `crews_update.go` přijímá pouze free a restricted. `sidecar/server.go` přidává výchozí domény ke crew allowlistu; `proxy.go` u free dovoluje privátní adresy, nikoliv metadata. Nový skutečný offline režim není součástí změny UI a není předstírán. Hosty se při přepnutí na free zachovávají v neuloženém draftu, zatímco server zachovává své původní chování při uložení. Provider-only volba seznam výslovně vyprázdní.

## Ověření

- 69 frontendových souborů / 704 testů Crews prošlo; testy zahrnují navigaci, zachování draftu, výběr runner/provider/model, klávesové šipky a převod minut do uložených sekund.
- Celý lint: 0 chyb, 32 původních warnings. Produkční build a Go vet prošly.
- Cílené API testy agent tool profiles a crew update prošly; celý balík `internal/sidecar` prošel. Go zdroje nebyly měněny. Předchozí celkový Go běh a jeho 10min timeout API/databáze zůstává zaznamenaný v `crews-vivid-implementation-2026-09-08.md`; tento krok ho neoznačuje za zelený.
- Živý Dev2 prohlížeč: všechny sekce crew, image, katalog nástrojů a verze, síť, limity; procházení bez změn nevyvolá discard. Agent: model, pokyny, přístupy, přepínání se zachováním draftu, mobil 390 px bez overflow a potvrzené zahození draftu. API provoz pouze čtení, žádná zákaznická konfigurace nebyla v testu uložena.
- Logy `/tmp/crews-editor-final-tests.log`, `/tmp/crews-editor-submit-tests.log`, `/tmp/crews-editor-browser.log`, `/tmp/crews-editor-network-tests.log`, `/tmp/crews-editor-api.log`.

Závěrečná kontrola zachytává také `console.error`, nejen `pageerror`: odhalila opakované aktualizace RuntimeConfig při změně sousedních síťových polí. Stabilní callback ve StepContainer odstranil cyklus. Po opravě prošlo dalších 25 testů StepContainer a celý živý scénář včetně zadání hostu, přepnutí free → restricted se zachováním draftu a Cancel; konzole i API chyby jsou prázdné (`/tmp/crews-editor-console-verified.log`).
