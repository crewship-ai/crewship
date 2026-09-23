# Klastr P: O1 (konkrétní Pages v command palette) a O4 („Zeptat se na tuto Page“)

## 0. Ověření prostředí
Větev `main`, HEAD `5a0ad11f` (= origin/main), `git status` obsahuje jen untracked `docs/prd/**` cizího WIP; žádný tracked soubor jsem nezměnil, dočasný test `components/__tests__/zz-opp-P.test.tsx` byl po běhu smazán (zdroj v §7). Otevřené PR #2562, #2558, #2556 nesahají na `command-palette.tsx`, `use-pages.ts`, `page-view.tsx`, `pages-layout.tsx`, `chat-client.tsx`, composer ani `internal/api/pages*.go` [POTVRZENO — `gh pr view <n> --json files`, průnik prázdný; jediné společné soubory jsou `components/features/chat/asks/form-field.tsx` a `app/(dashboard)/integrations/page.tsx`].

## 1. Verdikt per funkce

**O1 — implementovat, jako iterace 1, v úzké verzi „další skupina ve fan-outu palety“.** Na HEAD paleta konkrétní Pages neobsahuje (jen navigační řádek `Pages`, `components/command-palette.tsx:147`) a nikdy nevolá `/api/v1/pages` [POTVRZENO testem §7]. Server vrací kompletní, autorizovaný, nestránkovaný list (max 100 stránek na workspace), takže funkce je čistá změna rozhraní nad existujícím DTO: žádný endpoint, žádná migrace, žádná CLI/OpenAPI brána. Jediná skutečná mezera vůči akceptaci PRD je Recent, který dnes není vázán na uživatele ani workspace — to platí pro všechny skupiny, ne jen pro Pages, a PRD to musí říct.

**O4 — upravit zadání, teprve pak implementovat (iterace 9 sedí, ale jako dvě půlky).** PRD staví na „stávajících message metadata/provenance“ a na režimu draft; obojí na HEAD existuje jen z půlky: `ChatPanel` umí prefill bez odeslání, ale `chat-client` nemá žádný vstup, který by ho zapnul, a server zahazuje veškerá metadata zprávy kromě `ask_submission`. `?prompt=` navíc neposílá do „nové session“, ale do nejnovějšího existujícího vlákna agenta. Úzká verze O4 je proveditelná bez nové agentní čtecí cesty (kontext se skládá na klientu z DTO, ke kterému člověk má přístup, a jde do textu zprávy), ale provenance po obnovení chatu vyžaduje malé rozšíření serverového whitelistu metadat. Serverové znovunačtení kontextu pro agenta je samostatná backendová schopnost a do úzké verze nepatří.

## 2. Tabulka funkcí

| ID | Současný stav (HEAD) | Souběžné PR | Doporučený rozsah (min. verze) | Závislosti | Odhad | Důkaz v kódu (soubor:řádek) |
|---|---|---|---|---|---|---|
| O1 | Paleta má 9 fan-out skupin + Recent + navigační řádek `Pages`; skupina konkrétních Pages chybí; `/api/v1/pages` se nevolá. List je nestránkovaný, RBAC-filtrovaný (reach), vrací i stránky s aplikací i s draftem projektu (`has_application`, `has_project`). Recent = `localStorage["crewship.palette.recent"]` bez userId/workspaceId, nikdy nečištěn. | žádné | 1) Desátý `apiFetch("/api/v1/pages?workspace_id=…")` ve stejném `Promise.allSettled`, parsování `normalizePageList`+`toPageView` z `hooks/use-pages.ts`; 2) skupina „Pages“ s `PageGlyph` (icon/color), value `${name} ${slug} page`, href `/pages/${encodeURIComponent(slug)}` přes `go()`; sekundární popisek = název crew z již načteného `crews` (list dává jen `owner_crew_slug`) nebo název složky; 3) Recent klíčovat `[userId, workspaceId]` (jedna změna `RECENT_KEY` + `useSessionSafe`), zbytek Recent neměnit; 4) Vitest + 1 Playwright case + odstavec v `docs/guides/pages.mdx` + CHANGELOG. | Žádné produktové. Technicky: `useSessionSafe` (hooks/use-auth.tsx:322) pro klíč Recent. | 0,5–1 den (z toho ~0,25 dne Recent scoping a testy závodů). Bez gate gen-openapi/CLI/docs-inventory (žádný nový endpoint ani flag). | `components/command-palette.tsx:139-155` (NAV), `:252-293` (Recent), `:419-467` (fan-out + abort), `internal/api/pages_handler.go:461-484` (List), `:514-604` (loadPageIndex bez LIMIT, reach filtr), `:312-352` (pageListWire), `internal/pages/pages.go:45` (cap 100), `hooks/use-pages.ts:640-650, 554`, `lib/pages/editor-contract.ts:130-132` (href helper), `components/features/dashboard/pages-strip.tsx:117` (stejný encode) |
| O4 | Hostitelská hlavička s akcemi (Application/Panels, Share, Edit, Import, New) je `SubBar` v `pages-layout.tsx`, ne `page-view.tsx` (ten má jen breadcrumb + owner + LiveIndicator). `?prompt=` → `autoSendInitial` → auto-odeslání do nejnovějšího existujícího vlákna agenta. `ChatPanel.initialInput` bez `autoSendInitial` = prefill composeru (draft), ale `chat-client` ho nikdy nezapne. Server persistuje z metadat zprávy jen `ask_submission`. Agent nemá žádný read tool na Page; `agentViewer` existuje bez volajícího; `workspace_overview` vrací názvy+slugy VŠECH stránek workspace bez ohledu na reach. | žádné | 9a) „draft handoff“ v `chat-client`: druhý, ne-URL nebo referenční vstup (např. `?page=<slug>` čtený jednou jako `?prompt`), který nastaví `initialInput` bez `autoSendInitial` a připne se k první session; explicitní volba nové vs. existující session. 9b) tlačítko „Ask about this Page“ v `pages-layout` SubBar → `/chat/<agent>?page=<slug>`; chat načte `usePage` (lidská autorizace), zobrazí odstranitelný chip; při odeslání vloží do `content` ohraničený textový blok (název, slug, id, workspace, čas snapshotu, stavy panelů; žádný DOM, žádná data aplikace) a do `metadata.page_context` stabilní referenci; server whitelistuje `page_context` vedle `ask_submission` a UI ho vykreslí jako provenance chip (vzor `askProvenanceForTurn`). | Produktově: který agent je cíl (owner crew lead? výběr?). Technicky: 9a je předpoklad 9b a hodí se i pro O3. Serverové znovunačtení pro agenta = mimo rozsah (nová interní cesta + `agentViewer`/`canSeePage` skládání + obrácení importu api→chatbridge). | 9a 0,5–1 den; 9b 1,5–2,5 dne (FE 1–1,5, Go whitelist+testy 0,5, docs/CHANGELOG 0,25). Celkem 2–3,5 dne = uvnitř 2–4 dnů PRD. Bez nového endpointu; nový metadata klíč = doc v `docs/guides/chat-sessions.mdx` nebo conversations reference. | `components/features/pages/pages-layout.tsx:276-345` (SubBar akce), `components/features/pages/page-view.tsx:325-384` (breadcrumb), `app/(dashboard)/chat/chat-client.tsx:294-330, 776` (`?prompt`, `autoSendInitial`), `:540-575` (výběr nejnovějšího vlákna), `:377-385` (writeUrl maže `prompt`), `components/features/chat/chat-panel.tsx:594-612`, `components/features/chat/composer/chat-composer.tsx:121-138`, `hooks/use-chat.ts:1371-1407`, `internal/ws/client.go:428-560`, `internal/chatbridge/bridge.go:533-566`, `internal/askforms/envelope.go:50,166`, `internal/api/pages_grants_authz.go:413-447`, `internal/api/internal_status.go:150-158` |

## 3. Nálezy podle závažnosti

### Kritické
Žádné.

### Vysoké

**V1 [POTVRZENO] Recent palety není vázán na uživatele ani workspace a nikdy se nečistí.**
`components/command-palette.tsx:252` `RECENT_KEY = "crewship.palette.recent"`; `readRecent()`/`pushRecent()` (`:261-293`) neznají `workspaceId` ani `userId`; jediné `localStorage.removeItem` v kódu jsou pro jiné klíče (`hooks/use-workspace.ts:72`, onboarding, welcome). Reprodukce: dočasný test §7, případ „Recent is not scoped“ — položka zapsaná pod `ws-A` se nabídne pod `ws-B` (PASS = chování potvrzeno). Dopad: akceptace O1 „přepnutí workspace a odhlášení nezanechají cizí názvy“ dnes neplatí pro ŽÁDNOU skupinu (Issues, Agents…); pro Pages by navíc název stránky, ke které byl odebrán přístup, zůstal v Recent a klik by skončil na 404-stavu (`pages_handler.go:704-714` odpovídá stejným 404 pro nedosažitelnou i neexistující). Nejmenší oprava: klíč `crewship.palette.recent:<userId>:<workspaceId>` (fallback: bez userId → nepamatovat), zbytek Recent beze změny. Ověření dostupnosti stránky před zobrazením Recent řádku PRD dovoluje vynechat („Je-li třeba“); s klíčem per user+workspace je zbytkové riziko jen revokace v témže workspace → 404 stav s jasnou větou, což je přijatelné a levné.

**V2 [POTVRZENO] `?prompt=` auto-odesílá do NEJNOVĚJŠÍHO existujícího vlákna agenta, ne do nové session.**
`chat-client.tsx:540-561`: bez `?session=` se vybere `freshestOf(threadsByAgent[named.id])`, draft se mintuje jen když agent žádné vlákno nemá (`:566`). Auto-send: `:776` `autoSendInitial={handoffForThisSession}` → `chat-panel.tsx:596-610` (odeslání po `connected`, jednou per mount, bez prefillu composeru `:612`). Komentář v `routine-create-dialog.tsx:1048` („opens a fresh session“) neodpovídá kódu. Dopad na O4: kdyby O4 použil `?prompt`, kontext Page by odešel bez potvrzení do vlákna, které uživatel vedl o něčem jiném. PRD správně žádá draft, ale musí dodat i **volbu session** (nová vs. poslední) — dnes ji handoff nemá.

**V3 [POTVRZENO] Server zahazuje všechna metadata zprávy kromě `ask_submission` — „stávající message metadata/provenance“ pro O4 nestačí.**
`internal/chatbridge/bridge.go:548-551`: `userMsgMetadata` se přestaví jen z `askforms.EnvelopeFromMetadata`; cokoli jiného se nepersistuje (klient je pošle — `hooks/use-chat.ts:1399-1403`, `internal/ws/client.go:560` je předá — ale záznam konverzace to nenese). Dopad: akceptace „obnova chatu se zachovaným původem zprávy“ vyžaduje nový whitelistovaný klíč (`page_context`) v bridge + přenos do `conversation.Message.Metadata` + čtení v `turn-renderer` (vzor `askProvenanceForTurn`, `components/features/chat/turn-renderer.tsx:195`). Je to malá, ale serverová změna; PRD ji nepojmenovává.

### Střední

**S1 [POTVRZENO] PRD ukazuje na špatný soubor pro „hostitelskou hlavičku“.**
Akce Page (Application/Panels, Share, Edit, Import, New) jsou v `components/features/pages/pages-layout.tsx:276-345` (`SubBar actions`), `page-view.tsx:325-384` má jen breadcrumb, „waiting on“, owner link a `LiveIndicator`. Kapacity (`usePageCapabilities`) rozhoduje layout, ne view. Implementace O4 patří do `pages-layout.tsx`; `page-view.tsx` se mění jen pokud má akce být i v public/embed view (nemá — public view nemá chat).

**S2 [POTVRZENO] Pages list není stránkovaný, cap 100 je konstanta; „přiznat neúplnost“ pro O1 není potřeba, ale je třeba to zamknout testem.**
`pages_handler.go:514-533`: SQL bez `LIMIT`, žádný `?limit` param (grep `limit|offset|cursor` v souboru: jen push-limity a cap). `internal/pages/pages.go:45` `MaxPagesPerWorkspace = 100` — komentář „admin-raisable“ neodpovídá, hodnota je Go konstanta (vynucena `pages_handler.go:767`, `pages_internal_save.go:209`, `pages_transfer.go:532`). Dopad: list je z kontraktu úplný (≤100 řádků, řádově ≤100 kB), paleta nesmí posílat `limit` (memory „list APIs cap at 100 rows without ?limit“ na tento endpoint neplatí). Doporučení: guard test „palette calls `/api/v1/pages` without `limit`“ + věta v PRD, že kompletnost plyne z cap, ne z hledání.

**S3 [POTVRZENO] Agent nemá žádný čtecí nástroj na Page a serverová autorizace agenta pro čtení není složená; import api→chatbridge brání levnému „server re-load“.**
MCP nástroje sidecaru: `save_page`, `page_project`, `workspace_overview`, `discover_capabilities` (`internal/sidecar/routine_mcp.go:150-300`); IPC jen `PUT /pages/{page}/{panel}` (push, `internal/sidecar/pages.go`) a `POST /api/v1/internal/pages/{save,project}` (`pages_internal_save.go:50-51`). `agentViewer` (`pages_grants_authz.go:427`) nemá mimo testy žádného volajícího; `canSeePage` pracuje s lidským `pageViewer`. `internal/api` importuje `chatbridge` (`chat_steer.go:12`, `assignments.go:13`), takže bridge nemůže volat pages authz přímo — potřeboval by injektované rozhraní jako u `askforms`. Dopad: PRD věta „Pokud server musí kontext znovu načíst…“ popisuje novou schopnost (interní read endpoint + agentní reach + rozhraní do bridge), odhadem +2–3 dny navíc; do úzké O4 nepatří a PRD by to mělo říct explicitně.

**S4 [POTVRZENO] `workspace_overview` vrací názvy a slugy všech Pages workspace bez reach filtru.**
`internal/api/internal_status.go:150-158`: `SELECT name, slug FROM pages WHERE workspace_id = ?`. Lidský list filtruje reach (`loadPageIndex`), agentní přehled ne. Dopad na O4: PRD správně říká „lidská metadata nejsou automaticky oprávněním agenta“; opačný směr (agent ví o stránkách, na které člověk nedosáhne) je existující stav a O4 ho nesmí rozšířit o obsah. Pro klastr P je to kontext, ne blokátor; patří do auditu A1.

**S5 [POTVRZENO kód / NEOVĚŘENO za běhu] Prefill composeru přepíše uložený neodeslaný draft session.**
`chat-composer.tsx:123` `restoreInput = () => initialInput ?? drafts[draftKey] ?? ""` a `:136-138` `useEffect(() => { if (initialInput) setInput(initialInput) })`. Draft store je persistovaný (`stores/composer-store.ts:139`, zustand `persist`). Dopad na O4: handoff do vlákna s rozepsaným textem ho nahradí bez varování. Řešení pro 9a: pokud existuje neprázdný draft, kontext připojit (chip) a text nepřepisovat; nebo cílit vždy novou session.

**S6 [POTVRZENO kód] Při ztrátě `workspaceId` za otevřené palety zůstanou staré řádky.**
`command-palette.tsx:420` `if (!open || !workspaceId) return` — reset polí (`:425-433`) běží jen s pravdivým `workspaceId`; cleanup jen abortuje. Pozdní odpověď je správně zahozena (`:449,454`; test §7 „abort guard“ PASS). Dopad malý (paleta se při odhlášení zavře s route změnou), ale pro akceptaci „odhlášení nezanechá cizí názvy“ stojí za jednořádkový reset i ve větvi `!workspaceId`.

### Nízké

**N1 [POTVRZENO] Index Pages nenese `owner_crew_name`, jen `owner_crew_slug`** (`pageListWire:312-352`; `ownerLabelOf` v `use-pages.ts:590-601` padá na slug). Paleta má již načtené `crews` (`command-palette.tsx:440`), takže název crew lze doplnit čistou funkcí bez změny API. Pro rozlišení stejnojmenných stránek je vhodnější složka (`folder.name`) + crew.

**N2 [POTVRZENO] `has_application`/`has_project` jsou v listu** (`pages_handler.go:526-527` EXISTS/LEFT JOIN) — Pages Apps i stránky s pouze draftem projektu jsou stejné řádky jako v overview (`pages-layout.tsx:114` používá týž `usePages`). Viditelnost palety = viditelnost overview bez další práce. Volitelný badge „app“.

**N3 [POTVRZENO] Playwright `e2e/command-palette.spec.ts` má per-entity případy** (`:98-222`) — O1 doplní „a page opens that page“ a musí projít stávající kontrolou `[cmdk-item][data-href]` (`:86-90`).

**N4 [NÁVRHOVÉ RIZIKO] `usePages()` v paletě by porušilo „načítat při otevřené paletě“.** Hook je `enabled: Boolean(workspaceId)` a přihlašuje realtime invalidaci (`use-pages.ts:1069-1094`), paleta je trvale mountovaná v `components/layout/app-toolbar.tsx:443` → fetch při každém `page.panel.updated` i se zavřenou paletou. Použít `apiFetch` ve fan-outu + exportované normalizéry, ne hook.

## 4. Navržené přesné změny PRD

- **§5 O1, věta „Recent musí respektovat identitu, scope a dostupnost stránky.“** → nahradit: „Recent palety je dnes jeden klíč `localStorage` bez uživatele a workspace pro všechny skupiny (`components/command-palette.tsx:252`). O1 ho klíčuje na `[userId, workspaceId]` pro všechny skupiny naráz (jedna změna klíče, žádný redesign); ověření dostupnosti stránky před zobrazením Recent řádku se nedělá — nedosažitelná stránka končí na existujícím 404 stavu Page.“
- **§5 O1, věta „Při neúplném listu přiznat omezení hledání.“** → nahradit: „`GET /api/v1/pages` je nestránkovaný a úplný (workspace cap 100 stránek, `internal/pages/pages.go:45`); paleta nesmí posílat `limit` a nepotřebuje hlášku o neúplnosti. Guard test to zamkne; pokud by list někdy dostal stránkování, hláška se doplní tehdy.“
- **§5 O1, „Ověřit podporu nových Pages Apps v používaném listu.“** → „Ověřeno: list nese `has_application`/`has_project`; Pages Apps i drafty jsou běžné řádky, viditelnost je shodná s overview (`pages-layout.tsx:114`).“
- **§5 O1, doplnit:** „Načítat přes `apiFetch` ve stávajícím `Promise.allSettled`, ne přes `usePages()` (hook je realtime-invalidovaný a paleta je trvale mountovaná).“
- **§10 O4, „Akce v hostitelské hlavičce Page“ + „Využít `components/features/pages/page-view.tsx`“** → „Akce patří do `SubBar` v `components/features/pages/pages-layout.tsx` (vedle Share/Edit, `:276-345`); `page-view.tsx` se nemění.“
- **§10 O4, „Současné `?prompt=` auto-odesílá: pro tento scénář vyžadovat režim draft.“** → doplnit: „`?prompt=` navíc cílí nejnovější existující vlákno agenta (`chat-client.tsx:540-561`). Draft handoff musí (a) nastavit `initialInput` bez `autoSendInitial` (`ChatPanel` to už podporuje, `chat-panel.tsx:612`), (b) explicitně zvolit novou session, nebo respektovat rozepsaný draft cílové session (`chat-composer.tsx:123`).“
- **§10 O4, „stávající message metadata/provenance“** → „Server persistuje z metadat jen `ask_submission` (`internal/chatbridge/bridge.go:548`). O4 přidá whitelistovaný klíč `page_context` {workspace_id, page_id, slug, name, snapshot_at} vedle něj a zobrazí ho jako provenance chip (vzor `askProvenanceForTurn`). Bez toho ‚obnova chatu se zachovaným původem‘ neplatí.“
- **§10 O4, „Pokud server musí kontext znovu načíst…“** → „Serverové znovunačtení kontextu pro agenta je samostatná schopnost (interní read cesta s agentní autorizací; `agentViewer` existuje bez volajícího, `chatbridge` nemůže importovat `internal/api`). Není součástí odhadu 2–4 dny; úzká verze skládá kontext na klientu z DTO autorizovaného pro člověka a vkládá ho jako nedůvěryhodný text zprávy.“
- **§4 tabulka, O4 odhad 2–4 dny** → ponechat, s poznámkou „bez serverového re-loadu; +2–3 dny za agentní čtecí cestu“.
- **§3 inventura, řádek Aplikace** → doplnit „agent dnes nemá čtecí nástroj Page; `workspace_overview` vrací názvy všech stránek workspace bez reach (`internal_status.go:150`) — audit A1“.
- **§15** → doplnit: „Změna Recent klíče je jediná změna O1 mimo skupinu Pages; nesmí měnit tvar záznamu.“

## 5. Návrh iterací pro tento klastr

### Iterace 1 — O1: skupina Pages v paletě (sedí s výchozím plánem, žádné závislosti)
- **Vstupy:** HEAD 5a0ad11f; `pageListWire` kontrakt; `normalizePageList`/`toPageView`; `PageGlyph`; `paletteFilter`.
- **Rozsah:** (1) `apiFetch("/api/v1/pages?workspace_id=<ws>")` jako desátý prvek fan-outu, `if (pagesData) setPages(normalizePageList(pagesData).map(toPageView))`; (2) skupina „Pages“ za Projects: řádek = `PageGlyph` (icon/color), název, sekundárně `folder.name` nebo crew název (lookup ze `crews` podle `owner_crew_slug`), badge „app“ při `has_application`; value `${name} ${slug} page`, keywords `[folder, ownerLabel]`; `data-href` a `go()` s `/pages/${encodeURIComponent(slug)}`, group „Pages“; (3) `RECENT_KEY` → `crewship.palette.recent:<userId>:<workspaceId>` (bez userId Recent nečíst/nepsat), reset polí i ve větvi `!workspaceId`; (4) `docs/guides/pages.mdx` odstavec „Finding a Page from ⌘K“, CHANGELOG „Added“.
- **Co do ní NEPATŘÍ:** `usePages()` v paletě, nový endpoint, `limit`, hláška o neúplnosti, ověřování dostupnosti Recent položek proti serveru, redesign Recent, fulltext obsahu.
- **Testy:** Vitest `components/__tests__/command-palette.test.tsx`: skupina Pages z fixture (název, slug se znaky vyžadujícími encode → href), stejné názvy různých složek/crew jsou dva řádky, `has_application` badge, 403/500 na `/api/v1/pages` → ostatní skupiny stojí, malformed body (objekt bez pole) → žádná skupina, žádný `limit` v URL, Recent klíč obsahuje user+workspace, položka z jiného workspace se nenabídne, pozdní odpověď po změně workspace se zahodí (převzít z §7). Playwright `e2e/command-palette.spec.ts`: „a page opens that page“. Zelené musí zůstat: oba palette test soubory (38 testů, běh 29 s), `TestPagesList_*` (25 s), `e2e/command-palette.spec.ts`.
- **Podmínky dokončení:** klik i Enter otevřou `/pages/<slug>`; 403 listu nevyřadí jiné skupiny; přepnutí workspace s pending requestem nezanechá cizí řádky; Recent jiného uživatele/workspace se nezobrazí; `pnpm lint`, cílené Vitest, `go vet` netřeba (žádná Go změna).
- **Handoff:** PR s tabulkou testů; poznámka, že Recent klíč se změnil pro všechny skupiny a starý klíč se neuklízí (jednorázový `removeItem` starého klíče lze přidat za 1 řádek — rozhodnout v review).

### Iterace 9a — draft handoff v chatu (předpoklad O4 i O3; ~0,5–1 den)
- **Rozsah:** `chat-client.tsx`: vedle `?prompt` číst jednou `?page=<slug>` (referenční, necitlivé) a/nebo `?draft=1`; při draftu `initialInput` bez `autoSendInitial`; explicitní `?new=1` → `startConversation`; nepřepisovat neprázdný draft cílové session (chip místo textu). `writeUrl` už parametry maže (`:377-385`).
- **Testy:** Vitest pro chat-client handoff (draft se neodesílá, po změně session se nepřenáší, refresh nezdvojí), existující `chat-panel-*` testy zelené.
- **NEPATŘÍ:** změna `?prompt` chování pro routine-create-dialog (jen opravit jeho komentář).

### Iterace 9b — O4 tlačítko a kontext (~1,5–2,5 dne; po 9a a po produktovém rozhodnutí §6)
- **Rozsah:** `pages-layout.tsx` SubBar „Ask about this Page“ (jen `selectedSlug && !editing && detail.page`), navigace na `/chat/<agent>?page=<slug>&new=1`; v chatu `usePage(workspaceId, slug)` → chip s názvem, časem snapshotu a křížkem; při odeslání `content = text + "\n\n---\nPage context (untrusted, snapshot <ISO>): …"` (název, slug, id, owner, stavy panelů, last_produced_at; žádný payload panelů, žádná data aplikace, žádný DOM), `metadata.page_context`; Go: `bridge.go` whitelist `page_context` (validace tvaru, délky), persist; `turn-renderer` provenance chip; docs + CHANGELOG.
- **Testy:** Go tabulkový test bridge (validní `page_context` přežije, cizí klíče ne, `ask_submission` beze změny); Vitest: chip odstranitelný před odesláním, odebrání = žádný blok v content ani metadata, obnova chatu s persistovaným `page_context` vykreslí chip; revokace: `usePage` 404 před odesláním → chip se změní na „no longer accessible“ a blok se nevloží. Zelené: `pages-layout.test.tsx`, `page-view.test.tsx`, `chat-panel-history-metadata.test.tsx`, `ask-provenance.test.tsx`, `go test ./internal/chatbridge -run Metadata`.
- **NEPATŘÍ:** serverové znovunačtení pro agenta, výběr řádků uvnitř aplikace, nový agentní read tool, změna `workspace_overview`.
- **Handoff:** seznam otevřených otázek §6 s rozhodnutím; limity: kontext je lidský snapshot v textu, agent ho nemůže ověřit.

Pořadí: O1 v iteraci 1 sedí (nezávislé). O4 v iteraci 9 sedí, pokud 9a předchází a produktová otázka cílového agenta je zodpovězena; 9a lze předsunout kamkoli po iteraci 1, protože ji využije i O3.

## 6. Rozhodnutí vyžadující produktový vstup

1. **Cílový agent pro „Ask about this Page“:** lead crew vlastníka stránky (u `user/<id>` vlastníka není žádný), poslední agent, se kterým uživatel mluvil, nebo výběr před navigací? Technicky nejlevnější: lead owner crew, fallback výběr přes existující agent picker.
2. **Nová session vs. poslední vlákno agenta:** doporučuji vždy novou session (kontext je téma), ale mění to dnešní `?prompt` sémantiku pro routine-create-dialog, pokud by se sdílela.
3. **Rozsah kontextu pro Pages Apps:** pro stránku s aplikací DTO neobsahuje data aplikace; má chip nést jen metadata + stavy panelů (doporučeno), nebo i shrnutí posledních payloadů panelů (větší, citlivější)?
4. **Recent klíč pro všechny skupiny:** změna se dotkne Issues/Agents/… Recent (historie se „ztratí“ jednou při přechodu na nový klíč). Přijatelné?

Technicky rozhodnuto (bez produktového vstupu): `apiFetch` místo `usePages` (N4); žádný `limit`; Recent per user+workspace bez server ověření; O4 kontext v textu + whitelistovaná provenance, bez server re-loadu.

## 7. Příloha: dočasné testy a spuštěné příkazy

Spuštěno (vše v popředí):
- `pnpm exec vitest run components/__tests__/command-palette.test.tsx components/__tests__/command-palette-conversations.test.tsx` → `Test Files 2 passed, Tests 38 passed, 29.01s`.
- `pnpm exec vitest run components/__tests__/zz-opp-P.test.tsx` → první běh 1 failed (localStorage je v `vitest.setup.ts` stub; seeding přes mock), po úpravě `Test Files 1 passed, Tests 3 passed (7.6 s)`.
- `go test ./internal/api -run 'TestPagesList_' -count=1 -timeout 8m` → `ok github.com/crewship-ai/crewship/internal/api 25.542s`.
- `gh pr list --state open`, `gh pr view {2562,2558,2556} --json files`.
- Dočasný soubor `components/__tests__/zz-opp-P.test.tsx` smazán; `git status --short | grep -v docs/prd` prázdný.

Zdroj dočasného testu (kopie též ve scratchpadu `zz-opp-P.test.tsx.txt`):

```tsx
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, waitFor } from "@testing-library/react"
import { apiFetch } from "@/lib/api-fetch"

const h = vi.hoisted(() => ({ workspaceId: "ws-A" as string }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: h.workspaceId, role: "OWNER" }),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
import { CommandPalette } from "../command-palette"

beforeEach(() => {
  h.workspaceId = "ws-A"
  vi.mocked(localStorage.getItem).mockReturnValue(null)
  vi.mocked(apiFetch).mockReset()
})
afterEach(cleanup)

describe("opp-P: palette on HEAD 5a0ad11f", () => {
  it("never asks /api/v1/pages — there is no Pages group", async () => {
    vi.mocked(apiFetch).mockImplementation(async () => ({ ok: true, json: async () => [] }) as unknown as Response)
    render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.length).toBeGreaterThanOrEqual(9))
    const urls = vi.mocked(apiFetch).mock.calls.map((c) => String(c[0]))
    expect(urls.some((u) => u.startsWith("/api/v1/pages"))).toBe(false)
  })

  it("Recent is not scoped: an entry written under ws-A is offered under ws-B", async () => {
    vi.mocked(apiFetch).mockImplementation(async () => ({ ok: true, json: async () => [] }) as unknown as Response)
    vi.mocked(localStorage.getItem).mockReturnValue(
      JSON.stringify([{ href: "/pages/secret-page", label: "Secret Page from ws-A", group: "Pages" }]),
    )
    h.workspaceId = "ws-B"
    render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    expect(await screen.findByText("Secret Page from ws-A")).toBeInTheDocument()
    expect(vi.mocked(localStorage.getItem)).toHaveBeenCalledWith("crewship.palette.recent")
  })

  it("a late response for ws-A does not land after the switch to ws-B (abort guard)", async () => {
    let resolveA: ((r: Response) => void) | null = null
    vi.mocked(apiFetch).mockImplementation(async (url: string, init?: RequestInit) => {
      const u = String(url)
      if (u.includes("ws-A") && u.startsWith("/api/v1/agents")) {
        return new Promise<Response>((resolve, reject) => {
          resolveA = resolve
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))
        })
      }
      if (u.includes("ws-A")) {
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))
        })
      }
      return { ok: true, json: async () => [] } as unknown as Response
    })
    const view = render(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    await waitFor(() => expect(resolveA).not.toBeNull())
    h.workspaceId = "ws-B"
    view.rerender(<CommandPalette open={true} onOpenChange={vi.fn()} />)
    resolveA!({ ok: true, json: async () => [{ id: "a1", name: "Ghost Agent A", slug: "ghost", role_title: null, status: "idle", avatar_seed: null, avatar_style: null, crew: null }] } as unknown as Response)
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByText("Ghost Agent A")).not.toBeInTheDocument()
  })
})
```

Výstup: `Tests 3 passed (3)` — případ 1 potvrzuje absenci skupiny Pages, případ 2 potvrzuje nescopovaný Recent (nález V1), případ 3 potvrzuje, že abort guard palety drží (S6 se týká jen resetu polí, ne pozdní odpovědi). Případy 1 a 3 jsou vhodné jako trvalé regresní testy iterace 1 (případ 1 v obrácené podobě: `/api/v1/pages` se volá bez `limit`).
