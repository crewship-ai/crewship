// Edge-runtime Sentry init. The Edge runtime is a different bundle from
// the Node server runtime (no Node APIs, V8 isolates). Crewship's static
// export does not run on Edge in production — middleware and route
// handlers don't ship — so this file is effectively a courtesy for
// `next build`, which still evaluates it to type-check the edge runtime
// configuration.
//
// Keep it minimal. Mirror the scrubber posture so if a future routing
// change does end up running middleware in Edge, events arrive
// pre-scrubbed.

import * as Sentry from "@sentry/nextjs"

// Narrow shape for the legacy `modules` field that some Sentry
// integrations still attach to events but is not part of the public
// ErrorEvent type. Mirrors sentry.client.config.ts + server.config.ts.
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
    beforeSend(event) {
      if (event.contexts) {
        delete event.contexts.device
        delete event.contexts.runtime
        delete event.contexts.culture
      }
      event.user = undefined
      // Modules scrub mirrors client + server configs. The pinning
      // test in lib/__tests__/sentry-scrub.test.ts targets the
      // client scrubEvent export; if it changes, sync here too.
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
