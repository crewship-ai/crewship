# Crews — vizuální směr navazující na Routines

Samostatný [klikací prototyp](../wireframes/crews-vivid.html), modelová data, bez napojení na API a bez nasazení. Navazuje na crews-workspace-implementation-2026-09-08.md. Tento krok řeší uživatelem požadovaný vizuální návrh; neopravuje produkční Export, Refresh ani restart kontejneru.

## Směr

Tvář týmu tvoří současný AgentAvatar / CrewIcon, stručný účel a stav. Zachovat identitu existujících avatarů; jednoduché obličeje v prototypu jsou zástupné. Grafické akcenty soustředit do avatarů, ikon a skutečných dat. Jemně tónované pozadí zvýrazní aktuální práci. Hlavní přehled má tři malé metriky, aktuální práci, komunikaci a stručný souhrn úkolů, rutin a přístupů.

Při implementaci použít stejné DashboardCard, KpiCard a grafické tokeny jako components/features/routines/routines-overview.tsx. Ikony z existujícího lucide-react. Barva doplňuje text, nenahrazuje stav. Průběh kroků pouze tam, kde backend zná úplný počet kroků; u obecného agentového běhu poslední událost a indikátor běhu bez procent. Mini graf musí zobrazovat skutečná denní data a mít dostupný textový ekvivalent.

## Pohledy

- Katalog: znak crew, účel, avatarová skupina, stav; přepnutí karty/seznam a hledání. Pro 100 crew je nutné serverové hledání, řazení a stránkování; ukázka filtruje šest položek lokálně.
- Agent a crew: společná skladba. Crew přidává členy týmu. Přiřazené věci nejvýše tři na kategorii a odkaz na úplný filtrovaný seznam. U credentialů pouze název, dostupnost a původ přístupu.
- Memory: grafické rozcestí Znalosti agenta / Znalosti crew / O mně. Detail dokumentu zůstává dobře čitelný. Osobní profil je jen vlastní; cizí osobní profily nejsou součástí týmového přehledu. Persona patří do chování v editoru. Veškeré poznámky v ukázce jsou fikce.
- Edit: společný formulář pro Create/Edit, avatar a účel nahoře, ikonové sekce Identita / Chování / Model a běh / Schopnosti, pokročilé možnosti postupně rozbalovat. Prototyp ilustruje kompozici, ne úplný inventář či validaci produkčního editoru.

## Před implementací a nasazením

Opravit a ověřit na skutečném Dev2 API Export a Refresh (včetně osobní paměti), zjistit konkrétní příčinu chyby aktivity a přesunout restart kontejneru ke crew s průběhem a výsledkem. Souhrny rutin a přístupů musí respektovat přímé i zděděné vazby. Neznámé údaje nesmí být nuly. Prázdné znalosti mají jednu stručnou výzvu; modelovou paměť nevkládat do reálných uživatelských profilů.

## Ověření návrhu

Playwright/Chromium: všech pět pohledů bez JS chyb, hledání, přepnutí soukromého scope a stažení označené modelové poznámky. Hlavní pohledy bez horizontálního přetečení na šířce 390 px. Desktop 1440 px. Screenshoty crews-vivid-{agent,crew,fleet,memory,edit,mobile}.png ve wireframes. Produkční testovací smyčka se v tomto kroku nespouštěla, aplikační zdrojové soubory nebyly změněny.
