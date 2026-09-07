// The suite runs under happy-dom. That makes the SDK's Node entry the wrong
// one to load — and not merely wrong in principle: `index.server.js` pulls in
// a vendored bundler plugin that decides Node-vs-browser on
// `typeof document === 'undefined'`, sees happy-dom's document, resolves its
// loader against `document.baseURI` and hands the resulting `http:` URL to
// `fileURLToPath`, which throws at module scope. On #2444 that killed twelve
// unrelated suites at import time with every assertion inside them still
// passing, and the version pin that was supposed to prevent it (a
// `pnpm.overrides` ceiling on `@sentry/nextjs`) did not, because the throwing
// package is `@sentry/server-utils` and it floats on its own.
//
// So the thing worth asserting is not "Sentry works" but "tests resolve the
// browser build", which is what `vitest.config.ts` aliases. This file fails
// the moment that alias stops applying — loudly, and with the reason attached
// — instead of the failure resurfacing as a dozen suites that no longer load.
import { describe, expect, it } from "vitest"
import * as Sentry from "@sentry/nextjs"

describe("@sentry/nextjs resolves to the client build under happy-dom", () => {
  it("imports at all", () => {
    // The #2444 failure was a module-scope throw: the import above never
    // completed. Reaching this line is the assertion.
    expect(typeof Sentry.captureException).toBe("function")
  })

  it("is the browser entry, not the Node one", () => {
    // `browserTracingIntegration` ships only in `index.client.js`;
    // `httpIntegration` and `fsIntegration` only in `index.server.js`. If Node
    // resolution wins again this flips, and it flips before anything downstream
    // has a chance to fail obscurely.
    expect(Sentry).toHaveProperty("browserTracingIntegration")
    expect(Sentry).not.toHaveProperty("httpIntegration")
    expect(Sentry).not.toHaveProperty("fsIntegration")
  })
})
