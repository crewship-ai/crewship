# Crews & Agents — vizuální sjednocení a provozní opravy

Navazuje na [schválený vizuální návrh](crews-vivid-design-2026-09-08.md) a první implementaci Crews workspace. Práce pouze v instanci Dev2, issue #2463, PR #2464.

## Výsledné chování

- Agent a crew používají DashboardCard, KpiCard a StatusDonut ze stejného základu jako Routines. Ikonové hlavičky, barevné akcenty, větší původní avatar agenta, avatarové členství týmu, graf rozdělení skutečných výsledků běhů. Graf se nezobrazuje bez dat; zrušené běhy jsou výslovně vyjmuté. Nevznikly umělé časové řady ani procenta průběhu obecného agenta.
- Katalog má původní CrewIcon, původní avatary agentů, účel, karty/seznam, serverové hledání názvu/slugu/účelu, řazení nově vytvořené nebo A–Z a stránky po 24. Stav práce se odvozuje jen ze známých agentů; neúplný vzorek nemůže prohlásit celou crew za nečinnou.
- Souhrn Assigned & connected ukazuje poslední přiřazené issues/missions, rutiny vlastněné crew, dostupné credentialy a efektivní integrace agenta nebo integrace crew. Rutiny nejsou nepravdivě vydávány za přímé přiřazení agentovi. Metadata credentialů neobsahují jejich hodnoty. Úplné seznamy zůstávají v původních modulech. Samostatné opakování načtení při výpadku, cache 30 s omezuje počet požadavků při procházení agentů.
- Memory obsahuje vizuální rozcestí znalostí agenta, crew a workspace, automaticky otevřený první dokument vybraného scope a oddělené About me. Prázdný stav nabízí chat a výslovně označenou ukázku, která se nikam nezapisuje. Osobnost a instrukce zůstávají v Edit.
- Refresh obnovuje aktuální soubory, historii i všechny tři osobní zdroje a hlásí dokončení/selhání. Export jmenuje vybraný scope (agent/crew), předává workspace v query i hlavičce, rozlišuje prázdný scope, odmítnutí a skutečnou chybu; po předání souboru potvrdí zahájení stahování.
- Společné Create/Edit formuláře mají ikonové navigační zkratky, oddělené skupiny polí a viditelnou identitu. Zkratky posouvají stávající formulář, jeho draft zůstává zachovaný; pokročilé nastavení lze rozbalit přímo. Crew používá stejný vizuální rytmus a ikonový vstup do prostředí.
- Neimplementovaný restart zmizel z agenta. Crew administration obsahuje skutečný POST restart-agents, potvrzení sdíleného dopadu, průběh a výsledek. API recykluje kontejner a nový vzniká až při dalším agentovém běhu; UI netvrdí, že jej okamžitě spustilo.

## Opravy ověřené proti skutečnému API

Před nasazením backendové opravy vracel Kodi inbox `unavailable:["cost"]`. Dotaz používal neexistující `cost_ledger.created_at`; používá nyní `ts` a přesný měsíční začátek ve formátu ledgeru. Regresní test zahrnuje první okamžik měsíce a vyloučení starých záznamů.

Živé inventory Kodiho a Copy site vracelo prázdnou agent/crew paměť a nedostupný workspace scope. Export odpověděl 404 s `this scope holds no memory yet`. Nešlo o ztracený stažený soubor, ale o chybějící obsah. Ukázka v UI řeší prohlédnutí designu bez vytváření smyšlených osobních profilů.

Reálné kliknutí na kartu crew odhalilo neaktualizovaný výběr při Next Link navigaci: existující výběr používá shallow history a vlastní lokální stav. Karty a týmové odkazy nyní volají stejné výběrové callbacky jako sidebar a zachovávají odkaz pro otevření v novém panelu.

## Review první fáze

Opraveny platné připomínky: cache po invalidaci nepřepisuje novější data starším požadavkem, relations zachovávají známou cache při chybě, historická peer komunikace už netvoří automatický delegation trigger, osobní seznamy tolerují chybějící pole, původní PersonaPanel má klíč identity bránící přenosu pozdní odpovědi, /issues má explicitní Suspense.

Status counts nové API čte EntityWork pro plné počty příslušných seznamů; změna celého obecného Issues dashboardu není součástí tohoto vizuálního kroku. Refaktor všech stávajících přístupů na filesystem za provider a přesun filesystem fixture setupu z API testů jsou architektonické připomínky, nikoliv nové funkce této dodávky; nejsou zde vydávány za dokončené.

## Ověření

Samostatný browser běží s reálnými API odpověďmi Dev2 přes již přihlášený CLI účet. Jediný adaptovaný endpoint je NextAuth session wrapper pro browserové přihlášení CLI identity; produktová data nejsou nahrazena fixture daty. Zápisy jsou při browserovém průchodu blokované. Ověřen agent, Memory, prázdný export, osobní refresh, editor, katalog, hledání, kompaktní seznam, crew a šířka 390 px; bez JS chyb. Skutečný restart živého kontejneru není součástí read-only průchodu; vazba na API a úspěch/selhání jsou testovány automaticky.

Ověřeno:

- `go test ./... -count=1 -timeout=30m`: všechny balíčky prošly kromě jednoho 15s timeoutu existujícího `TestRunMissionLoop_DispatchesLeadPlanningWhenNoTasks` při souběžném zatížení. Celý `go test ./internal/orchestrator -count=1` následně prošel (22.8 s); plný původní příkaz měl exit 1, opakování balíčku exit 0. API 954.2 s a databáze 1067.5 s prošly již v plné sadě.
- Samostatný test nového stránkovaného hledání crew a ledger timestampu: prošel.
- `go vet ./...`: prošel.
- Crews a cache: 68 souborů, 709 testů prošlo. Dodatečný průchod Memory/editor/katalog/Settings privacy: 43 testů prošlo, včetně osmi testů sdílené osobní paměti. Tím je opraven předchozí CI pád šesti zastaralých Settings testů.
- `pnpm lint`: 0 chyb, 32 existujících varování. `pnpm build`: statický export prošel.
- `agents-invariants` a `docs-inventory -strict`: prošly.
- Živý browser: všechny uvedené pohledy prošly. Jediná odpověď 404 byla očekávaný prázdný export; UI ji správně vysvětlilo. Backendová chyba nákladů se ověří po reloadu Dev2.
