# E2E tests

Playwright suite covering the dashboard, agent surfaces, and the six
Crew Journal pages shipped in PR #204 + PR #205.

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

## Running on the dev VM (`crewship-dev`)

Chromium + system fonts aren't in the base image — bootstrap once:

```bash
ssh crewship-dev
cd /opt/crewship
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
authenticated page; the fixture handles CSRF + NextAuth credentials
login once per worker.

Keep smoke specs shallow — one layout landmark per route. Deep
interaction specs belong in a per-feature file (e.g. `approvals.spec.ts`
for the decide flow, `journal.spec.ts` for SSE stream assertions).
