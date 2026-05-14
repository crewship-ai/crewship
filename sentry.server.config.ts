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
      if (Array.isArray(event.breadcrumbs)) {
        for (const bc of event.breadcrumbs) {
          if (bc) bc.data = undefined
        }
      }
      return event
    },
  })
}
