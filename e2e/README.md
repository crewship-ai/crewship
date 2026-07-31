# E2E tests

Playwright suite covering the dashboard, agent surfaces, and the six
Crew Journal pages shipped in PR #204 + PR #205.

## What runs in CI

Three configs, three owners — do not cross-wire them:

| Config | Specs | Where it runs |
|---|---|---|
| `playwright.fresh.config.ts` | `onboarding-wizard.spec.ts` only | `e2e-devcontainer.yml`, nightly 02:30 UTC |
| `playwright.nightly.config.ts` | everything except `visual` + `onboarding-wizard`, split into a gate bucket and a drift bucket | `nightly-e2e.yml`, nightly 03:50 UTC |
| `playwright.config.ts` | the main suite minus its `testIgnore` list | local `pnpm test:e2e` |

Before `nightly-e2e.yml` landed, **6 of ~84 tests ran automatically** — the
onboarding wizard, and nothing else. A full sweep of the main config against a
freshly seeded instance then found **24 of 78 passing**: the application was
healthy and the specs had drifted. So the nightly splits them:

- **gate bucket** (`GATE_SPECS` in the workflow) — verified green, hard-fails
  the nightly. Today that is `create-crew-wizard.spec.ts`.
- **drift bucket** (`DRIFT_SPECS`) — run every night, never fatal, summarised
  into one auto-refreshed `e2e-drift` tracking issue with per-spec failure
  counts. Repair a spec, move it to `GATE_SPECS`, and it starts gating.

A `coverage-guard` job fails if any `e2e/*.spec.ts` belongs to neither bucket,
so a new spec cannot silently skip nightly coverage.

`visual.spec.ts` is in neither: every baseline in `visual.spec.ts-snapshots` is
`*-chromium-darwin.png`, and Playwright resolves snapshots per platform — on a
Linux runner it looks for `*-chromium-linux.png`, finds nothing and fails with
"snapshot missing". Three of its five surfaces are also on route trees the
/crews redesign deleted, so committing Linux baselines would pin pictures of
pages that no longer exist. Regenerating them belongs with rewriting the spec.

## Running locally (macOS)

```bash
pnpm install
pnpm exec playwright install chromium
export E2E_EMAIL=demo@crewship.ai
export E2E_PASSWORD=password123
pnpm test:e2e             # headless
pnpm test:e2e:ui          # interactive
```

Playwright's `webServer` block in `playwright.config.ts` brings the
Next.js dev server up automatically and reuses an existing one if
port 3001 is already serving.

## Running on a remote dev server

If you keep a long-running dev server (e.g. a VM) and want to run the suite there, Chromium + system fonts typically aren't in the base image — bootstrap once on that host:

```bash
ssh <your-dev-server>
cd <your-crewship-path>
sudo apt-get update && sudo apt-get install -y chromium-browser fonts-liberation \
  libasound2t64 libatk-bridge2.0-0 libatk1.0-0 libcups2 libdrm2 libgbm1 \
  libnspr4 libnss3 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libxkbcommon0
pnpm install
pnpm exec playwright install chromium
```

Or let Playwright pull its own bundled browser (recommended — avoids
system-chrome version drift):

```bash
pnpm exec playwright install --with-deps chromium
```

Then run against the already-running dev server:

```bash
export E2E_EMAIL=demo@crewship.ai
export E2E_PASSWORD=password123
pnpm test:e2e --project=chromium
```

## Adding a new spec

Place it under `e2e/*.spec.ts`. Use the auth fixture (`./fixtures/auth`)
rather than `@playwright/test` directly so the test lands on an
authenticated page.

Auth flow:

1. `e2e/global-setup.ts` runs once at the start of the whole test
   run. It logs in via NextAuth credentials (CSRF + POST to
   `/api/auth/callback/credentials`) and writes the resulting session
   cookies to `$TMPDIR/crewship-e2e-auth-<instance>.json`.
   `<instance>` comes from `CREWSHIP_INSTANCE_ID` or `NEXT_PORT` so
   concurrent instances on one host don't clobber each other's auth.
2. `playwright.config.ts` points `use.storageState` at the same file,
   so every spec inherits the cookies for free.
3. `e2e/fixtures/auth.ts` just lands the page on `/` — no per-test
   login, which previously tripped the `/api/auth/callback/credentials`
   rate limit (429 after ~5 logins in a minute).

Keep smoke specs shallow — one layout landmark per route. Deep
interaction specs belong in a per-feature file (e.g. `approvals.spec.ts`
for the decide flow, `journal.spec.ts` for SSE stream assertions).
