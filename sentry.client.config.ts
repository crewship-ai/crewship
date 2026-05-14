// Client-side Sentry init. Loaded by @sentry/nextjs in the browser bundle
// at app boot via Next 16's instrumentation hook. We deliberately keep this
// file small and side-effect-aware:
//
//   1. Empty DSN  → bail without calling Sentry.init. No network, no global
//      handlers patched, no quota spent. Matches the Go-side ResolveDSN()
//      behaviour for builds where the secret wasn't piped in (PR builds
//      from forks, local `pnpm build`, dev preview).
//
//   2. Telemetry consent off → bail, even with DSN present. The backend's
//      /api/v1/system/telemetry endpoint is the source of truth for the
//      operator's consent; the frontend must mirror it or we ship crashes
//      from an opted-out install. Privacy bias: any fetch failure (offline,
//      backend down, CORS, anything) also bails — we'd rather lose a crash
//      report than send one without explicit consent.
//
//   3. Scrubbing posture mirrors internal/crashreport/sentry_adapter.go
//      scrubEvent: drop device/runtime/culture contexts, clear User,
//      drop Modules, blank Breadcrumb.data. Pinning these here means a
//      future @sentry/nextjs upgrade that adds new auto-collected
//      identifiers under those keys is still scrubbed; the upgrade can
//      only widen the leak surface if it adds a NEW context key, which
//      the test in sentry-scrub.test.ts will catch.
//
// We do NOT enable replay or APM tracing (sample rates pinned to 0) — the
// beta budget is a crash-only signal, not a session-replay quota burn.

import * as Sentry from "@sentry/nextjs"

// Narrow shape for the legacy `modules` field that some Sentry
// integrations still attach to events but is not part of the
// public ErrorEvent type.
type ErrorEventWithModules = Sentry.ErrorEvent & { modules?: Record<string, string> }

// Build-time-injected DSN. The release.yml + nightly.yml workflows set
// NEXT_PUBLIC_SENTRY_DSN from the SENTRY_DSN repo secret; local builds
// leave it empty so dev never accidentally ships to production Sentry.
const DSN = process.env.NEXT_PUBLIC_SENTRY_DSN ?? ""

// Mirrors classifyEnv() in internal/crashreport/sentry_adapter.go so the
// Sentry UI can filter beta vs production vs dev consistently across the
// Go and JS event streams. "nightly-<sha>" is unique to the JS side
// (the Go binary's version comes from goreleaser's semver shape) and
// maps to "beta" — the nightly channel is a pre-release stream, not
// production.
function classifyEnv(version: string): string {
  if (version.startsWith("nightly-")) return "beta"
  if (version.includes("-beta") || version.includes("-rc")) return "beta"
  if (!version || version === "dev") return "development"
  return "production"
}

// scrubEvent is the BeforeSend hook. Returning null drops the event; we
// never drop here (caller-side gate already decided we should report),
// only redact. Exported so the pinning test can call it directly without
// standing up a Sentry client.
export function scrubEvent(event: Sentry.ErrorEvent): Sentry.ErrorEvent {
  if (event.contexts) {
    // Match the Go scrubber list exactly. "os" stays — it's just the OS
    // name and helps triage platform-specific crashes.
    delete event.contexts.device
    delete event.contexts.runtime
    delete event.contexts.culture
  }

  // Never identify the user. Even an IP address gleaned from request
  // metadata is more than we want to ship for a v0.1 beta.
  event.user = undefined

  // Modules is the bundled-deps list — not strictly PII but reveals the
  // customer's exact JS toolchain inventory. Release tag covers the same
  // triage need. The field is present on the runtime event shape but not
  // in the ErrorEvent type, so cast through a narrow shape rather than
  // using `any`.
  const withModules = event as ErrorEventWithModules
  if (withModules.modules) withModules.modules = undefined

  // Breadcrumb.data is free-form and can contain absolutely anything the
  // SDK auto-instrumented (fetch URLs with query secrets, click targets
  // with element text, etc). Keep .message for triage, drop .data.
  if (Array.isArray(event.breadcrumbs)) {
    for (const bc of event.breadcrumbs) {
      if (bc) bc.data = undefined
    }
  }

  return event
}

// Best-effort consent fetch. Returns false on ANY failure — privacy bias:
// when in doubt, do not ship events. The /api/v1/system/telemetry handler
// is added in the same PR as this file (router_system.go).
async function consentAllows(): Promise<boolean> {
  try {
    const res = await fetch("/api/v1/system/telemetry", {
      // No auth header on purpose — the handler is unauthenticated
      // because the frontend init runs before login and we still want
      // crash coverage on the login page itself.
      credentials: "omit",
      cache: "no-store",
    })
    if (!res.ok) return false
    const body = (await res.json()) as { enabled?: boolean }
    return body.enabled === true
  } catch {
    return false
  }
}

// Top-level init lives in an async IIFE so we can await the consent
// fetch before calling Sentry.init. @sentry/nextjs allows this — it
// reads sentry.client.config at instrumentation time and runs whatever
// side effects the module's top level executes.
//
// Skip during vitest runs (`import.meta.vitest`-equivalent — Vitest sets
// process.env.VITEST). Importing the module in a unit test (to access
// scrubEvent) must not trigger a real fetch or Sentry.init. Same logic
// for any non-browser context where `window` is missing.
void (async () => {
  if (typeof window === "undefined") return
  if (process.env.VITEST) return
  if (!DSN) return
  if (!(await consentAllows())) return

  Sentry.init({
    dsn: DSN,
    release: process.env.NEXT_PUBLIC_CREWSHIP_VERSION || undefined,
    environment: classifyEnv(process.env.NEXT_PUBLIC_CREWSHIP_VERSION ?? ""),

    // Crash-only signal. APM (tracesSampleRate) and Replay (the two
    // *replays* sample rates) cost quota and add data-collection surface
    // we explicitly do not want for v0.1.
    tracesSampleRate: 0,
    replaysSessionSampleRate: 0,
    replaysOnErrorSampleRate: 0,

    // Match the Go side: send 100% of errors. Volume is low enough that
    // sampling would hurt diagnostic value more than it'd save quota.
    sampleRate: 1.0,

    // Stack traces for stringified errors too — keeps captureException
    // useful when callers throw plain strings.
    attachStacktrace: true,

    // Default integrations attach a lot of auto-collected context that
    // the scrubber then strips. That's the right "defense in depth"
    // posture, but we also disable a couple integrations whose entire
    // purpose is to ship data we'll always remove anyway.
    integrations: (defaults) =>
      defaults.filter((integration) => {
        // BrowserApiErrors wraps setTimeout/setInterval/etc to capture
        // errors that would otherwise be swallowed — keep.
        // We strip integrations that ship environment metadata the
        // scrubber would just delete:
        return ![
          "Modules", // dependency list shipping → scrubbed anyway
          "ContextLines", // local source context (paths can carry usernames)
        ].includes(integration.name)
      }),

    beforeSend: scrubEvent,
  })
})()
