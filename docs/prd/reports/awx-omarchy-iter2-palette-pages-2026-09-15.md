# Iterace 2 — Pages v command palette: handoff

Datum 2026-09-15. Stav: implementováno, PR otevřen, nemergnuto, nenasazeno.
Navazuje na [plán iterací](../awx-omarchy-implementation-iterations-2026-09-15.md),
[PRD](../awx-omarchy-product-improvements-2026-09-14.md) §5 (O1) a
[klastr P](awx-omarchy-prd-opponent-2026-09-15/klastr-P-pages-paleta-chat.md).
Nezávislé na iteraci 1 (#2567, `fix/run-invoking-identity`) — nic z ní nepřevzato.

## Kde to je

| Co | Hodnota |
|---|---|
| Issue | [#2570](https://github.com/crewship-ai/crewship/issues/2570), claim clone crewship_3 |
| Větev | `feat/palette-pages`, base `main` @ `775e799c` |
| Commity | `72aba307` feat(palette): find a page by name, and keep Recent per user and workspace; `7b204783` fix(palette): a Recent deep link onto a page tab is still a page row (HEAD větve) |
| PR | [#2571](https://github.com/crewship-ai/crewship/pull/2571) |
| Souběžné PR | #2567, #2562, #2556 sdílejí s touto větví jen `CHANGELOG.md` (ověřeno `gh pr view --json files`); po každém merge čekat CHANGELOG konflikt |

Změněné soubory (8): `components/command-palette.tsx`,
`components/__tests__/command-palette-pages.test.tsx` (nový),
`components/__tests__/command-palette.test.tsx`,
`components/__tests__/command-palette-conversations.test.tsx`,
`components/__tests__/telemetry-palette-search.test.tsx`,
`e2e/command-palette.spec.ts`, `docs/guides/pages.mdx`, `CHANGELOG.md`.
Žádná Go změna, žádný nový endpoint, žádná CLI/OpenAPI změna.

## Ověřené předpoklady (proti `main` @ 775e799c, ne proti reportu)

- Paleta `/api/v1/pages` nevolala; skupina Pages nikde mezitím nevznikla.
- `GET /api/v1/pages` (`pages_handler.go` List/loadPageIndex) je bez `LIMIT`,
  bez `?limit`, reach-filtrovaný, nese `has_application`/`has_project`,
  `owner_crew_slug` (ne název crew), `folder`, `icon`, `color`.
  Cap 100/workspace je konstanta `internal/pages/pages.go`.
- `useSessionSafe` (`hooks/use-auth.tsx`) vrací `session.user.id`;
  `AuthProvider` obaluje celou aplikaci (`components/providers.tsx`), paleta je
  uvnitř (`components/layout/app-toolbar.tsx`).
- Původní abort guard (`ac.signal.aborted` po `allSettled` i po `Promise.all`)
  zachován; nález S6 (reset polí se ve větvi `!workspaceId` přeskakoval) potvrzen
  a opraven.

## Co je implementováno

1. **Skupina Pages** (za Projects): desátý `apiFetch("/api/v1/pages?workspace_id=…")`
   ve stejném `Promise.allSettled`; parsování `normalizePageList` + `toPageView`
   z `hooks/use-pages.ts` (ne `usePages()` — realtime-invalidovaný hook, paleta
   trvale mountovaná). Řádek: `PageGlyph` (icon/color; bez barvy muted, jinak
   barva palety), název, badge `app` při `has_application`, vpravo název složky,
   jinak název crew dohledaný z už načtených `crews` podle `owner_crew_slug`
   (fallback slug). Hodnota `"<name> <slug> page"`, keywords `[context, "app application"]`.
   Klik/Enter → `go("/pages/" + encodeURIComponent(slug), name, "Pages")`.
   Navigační řádek Pages zůstává.
2. **Recent per user + workspace** pro všechny skupiny:
   klíč `crewship.palette.recent:<userId>:<workspaceId>`. Bez userId nebo
   workspaceId se Recent nečte ani nepíše. Při otevření palety se jednou
   `removeItem("crewship.palette.recent")` — **stará nescopeovaná historie se
   nepřenáší, po nasazení bude Recent všech uživatelů prázdný** (v CHANGELOG
   i v `docs/guides/pages.mdx`). Tvar záznamu `{href,label,group}` nezměněn.
3. **Verifikace Page řádků v Recent**: řádek s `href` tvaru `/pages/<segment>`
   se zobrazí jen když `pagesLoaded && slug ∈ právě načtený list`; zobrazí se
   pod aktuálním názvem ze seznamu. Během pending, po 403/500, po
   nevalidním JSON nebo nerozpoznané obálce (`toPageResults` → `null`) se Page
   řádky nezobrazí; ostatní Recent řádky (Issues, Navigation `/pages`…) beze
   změny. Jeden list request pro všechny řádky.
4. **Okamžitý reset** všech skupin na začátku fan-out efektu (i při
   `!workspaceId`, i při `authStatus === "unauthenticated"`); závislosti efektu
   `[open, workspaceId, userId, authStatus]`. `safeJson` chytá výjimku z
   `.json()`.

## Výsledky testů (skutečné běhy, vše v popředí)

| Běh | Výsledek |
|---|---|
| `pnpm exec vitest run components/__tests__/command-palette-pages.test.tsx` | 17 passed |
| `… command-palette.test.tsx` (30 původních + 1 nový) | 31 passed |
| `… command-palette-conversations.test.tsx` | 14 passed |
| `… telemetry-palette-search.test.tsx` | 6 passed |
| celkem 4 soubory | 62 passed, 0 skipped, 5.3 s |
| `go test ./internal/api -run 'TestPagesList_' -count=1` | ok 15.9 s |
| `pnpm lint` | 0 errors, 30 warnings (identické s `main` — ověřeno přes stash) |
| `pnpm exec tsc --noEmit`, `pnpm build` | čisté |
| `go run ./scripts/docs-surface-check`, `go run ./scripts/docs-inventory -strict` | čisté (pages.mdx upraven) |
| `pnpm exec playwright test e2e/command-palette.spec.ts` proti izolované instanci | 13 passed, 0 skipped, 18.7 s (včetně nového „a page opens that page“) |

Vitest pokrývá: autorizované řádky + deep link; encode slugu (`q&a/2026 sít`);
stejné názvy ve složce / crew / user-owned = 3 řádky s 3 href; glyph a `app`
badge, `has_project`-only řádek běžný; URL bez `limit`; klik i Enter →
`router.push`, zápis pod scoped klíč; 403/500/malformed/nerozpoznaná obálka →
žádná skupina Pages, Crews stojí, Page řádek v Recent skryt, Issues řádek
zůstává; Recent: smazaná/revokovaná stránka mizí, přežívající pod aktuálním
názvem, jediný list request; pending list → Page řádek skryt, po odpovědi se
objeví; přepnutí workspace s pending requestem (pozdní odpověď ws-A nedopadne,
volání jen `ws-A` + `ws-B`); ztráta workspace → řádky pryč, žádný další
request; odhlášení → řádky pryč, žádný request; jiný uživatel → jeho Recent;
bez uživatele se nečte ani nepíše; zavřená paleta: 0 requestů za 60 s, po
otevření 1× `/api/v1/pages`, po zavření za 120 s nic. Escape testuje jen
Playwright („closes on Escape“, prošel).

## Skutečná navigace v prohlížeči

Izolovaná instance: `pnpm build` → `scripts/embed-web-out.sh sync` →
`go build -ldflags "$(scripts/build-stamp.sh ldflags)"` do scratchpadu; start
`crewship-iter2 start --no-docker` s `CREWSHIP_DATA_DIR`, `DATABASE_URL`,
`CREWSHIP_HOST=127.0.0.1`, `CREWSHIP_PORT=8111`, `CREWSHIP_SKIP_SIDECAR=1`,
náhodné `NEXTAUTH_SECRET`/`ENCRYPTION_KEY`; `crewship seed --skip-issues`
(recept ci.yml, pouze na této instanci — dev1–3 ani stage nedotčeny, dev3
nerestartován). Proces po skončení ukončen podle PID, port 8111 uvolněn.

Ručně v Chromiu (Playwright skript ve scratchpadu): dvě stránky „Fleet status“
(Ops / Quality) = dva řádky s crew vpravo a rocket glyphem ve violet; Enter →
`/pages/fleet-status`, název na stránce; „síť“ → klik → `/pages/sit-2026`;
`localStorage` klíč `crewship.palette.recent:<userId>:<wsId>`; Recent nese oba;
po `DELETE /pages/sit-2026` (204) další otevření ukazuje v Recent jen
`fleet-status`; všechna `/api/v1/pages?` volání nesla jen `workspace_id`.
Screenshoty: `scratchpad/iter2/palette-pages-fleet.png`, `page-opened.png`,
`palette-recent.png` (nepřidány do repa).

## Limity

- Recent se ověřuje proti listu jen při otevření palety; revokace během otevřené
  palety se projeví až dalším otevřením (list se nepolluje — záměr PRD).
- Stránky vlastněné uživatelem (`user/<id>`) bez složky nemají sekundární popisek
  (index nenese jméno uživatele; zobrazit id by nebyl „oprávněně dostupný popis“).
- Pages list ≤100 řádků bez hlášky o neúplnosti — plyne z cap konstanty, ne z
  hledání; pokud list někdy dostane stránkování, guard test „no limit“ selže a
  hláška se doplní tehdy.
- Vitest s happy-dom netestuje Escape (Radix) — kryje Playwright.
- Lint: 30 pre-existujících warningů mimo dotčené soubory, 0 errors.
- Recent historie starého klíče se ztratí jednou pro všechny uživatele (rozhodnutí
  PRD, ne oponenta).
- Handoff je v `docs/prd/reports` mimo commit (stejně jako handoff iterace 1 a
  ostatní reporty v tomto klonu — untracked cizí práce v adresáři nedotčena).

## Stav review

- `scripts/review-status.sh 2571 --checks` po otevření: **THROTTLED** —
  CodeRabbit rate-limited („next review in 22m“), check zelený bez review,
  15 checků skipped. Podle repo pravidla nečekáno: self-review celého diffu
  zapsán do PR jako komentář (co bylo/nebylo strojově reviewováno) a
  `scripts/review-status.sh --retrigger 2571` odeslán.
- CI na `7b204783`: 25 pass, 16 skipping, 0 fail (`gh pr checks 2571`). Před merge znovu
  `scripts/review-status.sh 2571 --checks` — zelený CodeRabbit check není
  důkaz review (memory: rate limit posílá „Review finished“ bez čtení).
- Merge, nasazení na dev3 ani úprava zákaznických ACL nebyly provedeny.

## Vstup pro další relaci (iterace 3 — evidence kontrakt a korelace)

- Iterace 3 na paletě nezávisí. Před startem: `git fetch`, stav #2571 a #2567
  (merge/rebase CHANGELOG), `scripts/claim-issue.sh --list`.
- Po merge #2571 nasazení na dev3 = `sudo systemctl restart crewship-ws@3`
  (dev3 buildí clone 3; clone je přepnutý zpět na `main`).
- Otevřené produktové otázky klastru P §6 (cílový agent pro O4, session, rozsah
  kontextu) zůstávají pro iteraci 10; tato iterace je nerozhoduje.
