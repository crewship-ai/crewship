<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## Why `@sentry/nextjs` is aliased in `vitest.config.ts`

Every entry under `pnpm.overrides` in `package.json` is a security *floor*
(`"ws": ">=8.21.0"`) — raise a transitive dependency past a known CVE. There
is no ceiling among them, and there should not be one: a ceiling freezes a
package at whatever version last worked and hides the reason it stopped.

`@sentry/nextjs` carried one (`"<10.72.0"`) from 2026-08-31 to 2026-09-07,
and it did not even hold: it named one package while the throwing code lived
in `@sentry/server-utils`, which floats on its own. The fault (#2235, upstream
getsentry/sentry-javascript#23789) was in a vendored bundler plugin that picked
its Node-vs-browser branch on `typeof document === 'undefined'`. Under
`happy-dom` a `document` exists, so it took the browser branch, built an
`http:` URL from `document.baseURI`, and handed it to `fileURLToPath` — which
threw `TypeError: The URL must be of scheme file` at module scope. Anything
that transitively imported `@sentry/nextjs` then failed to load at all: on
#2444 that was twelve suites, every assertion inside them still passing, which
reads like anything but a dependency problem. Upstream fixed it in 10.74.0 by
deriving the loader path from `__filename` instead of from the environment;
the Node entry now loads under `happy-dom` too.

Production was never affected — `next build` and the server runtime have no
`document`, so they took the Node branch — and that is the tell. The suite
runs under `happy-dom`, so a component importing the SDK should get the same
client build the browser bundle gets, not the Node one. That holds whether or
not the Node entry happens to load, which is why the alias outlived the bug.
`vitest.config.ts` says so directly, with the entry the package's own
`browser` export condition names:

```ts
'@sentry/nextjs': path.resolve(__dirname, 'node_modules/@sentry/nextjs/build/esm/index.client.js'),
```

That client build is browser code and imports `next/router` extensionless,
which Node's ESM resolver rejects, so it is also listed in
`test.server.deps.inline` to route it through Vite's resolver — the one the
Next bundler stands in for at runtime.

If a future bump breaks the import again, check that entry path against the
package's `exports` map before reaching for a version pin. The alias fails
loudly (every Sentry-importing suite stops loading); a ceiling fails quietly,
by never moving.
