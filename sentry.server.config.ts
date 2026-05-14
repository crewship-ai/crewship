// Server-side Sentry init for the Next.js node runtime (route handlers,
// metadata/generateMetadata, RSC). In Crewship the Next app is exported
// statically and served by the Go binary, so this file's effective scope
// is narrow — Vercel-style server bundles do not run in production.
//
// We still wire it up for two reasons:
//   1. `next build` evaluates this file; missing it produces noisy
//      warnings from @sentry/nextjs about an unconfigured runtime.
//   2. The dev server (`next dev`) DOES run a Node process and benefits
//      from server-side error capture during local Sentry verification.
//
// DSN handling, environment classification, and scrubbing mirror the
// client config exactly so a UI/SSR error and a browser error look like
// peers in the Sentry project, not two unrelated event streams.

import * as Sentry from "@sentry/nextjs"

// Narrow shape for the legacy `modules` field that some Sentry
// integrations still attach to events but is not part of the public
// ErrorEvent type. Mirrors the same workaround in sentry.client.config.ts.
type ErrorEventWithModules = Sentry.ErrorEvent & { modules?: Record<string, string> }

const DSN = process.env.NEXT_PUBLIC_SENTRY_DSN ?? ""

function classifyEnv(version: string): string {
  if (version.startsWith("nightly-")) return "beta"
  if (version.includes("-beta") || version.includes("-rc")) return "beta"
  if (!version || version === "dev") return "development"
  return "production"
}

if (DSN) {
  Sentry.init({
    dsn: DSN,
    release: process.env.NEXT_PUBLIC_CREWSHIP_VERSION || undefined,
    environment: classifyEnv(process.env.NEXT_PUBLIC_CREWSHIP_VERSION ?? ""),
    tracesSampleRate: 0,
    sampleRate: 1.0,
    attachStacktrace: true,

    // The server bundle does not call the /api/v1/system/telemetry
    // consent endpoint — it CANNOT, because at static-export build time
    // there is no server to query, and at `next dev` time the consent
    // signal is the operator's local DB state (the Go process owns it
    // and the JS side has no DB handle). We accept this asymmetry: in
    // production the Next server bundle is unused; in dev the operator
    // can `crewship telemetry off` and the Go side stops, leaving only
    // the dev-only Node bundle as a potential source, which an
    // engineer running `pnpm dev` against a beta DSN will notice.
    beforeSend(event) {
      if (event.contexts) {
        delete event.contexts.device
        delete event.contexts.runtime
        delete event.contexts.culture
      }
      event.user = undefined
      // Modules: bundled-deps inventory. Not strictly PII but reveals
      // the toolchain. Release tag already covers the triage need.
      // Mirrors client.config + the Go-side internal/crashreport
      // adapter. See lib/__tests__/sentry-scrub.test.ts for the
      // pinning assertions that catch regressions here.
      const withModules = event as ErrorEventWithModules
      if (withModules.modules) withModules.modules = undefined
      if (Array.isArray(event.breadcrumbs)) {
        for (const bc of event.breadcrumbs) {
          if (bc) bc.data = undefined
        }
      }
      return event
    },
  })
}
