"use client"

// Development-only smoke trigger for the Sentry pipeline. Mount this
// anywhere in the layout tree and visit `?sentry_test=1` to throw a
// canary error from a click handler — useful during the initial DSN
// rollout to verify:
//
//   1. The /api/v1/system/telemetry consent gate is reachable.
//   2. Sentry.init actually ran (window-level Sentry global present).
//   3. The BeforeSend scrubber stripped the contexts we expect.
//
// Two layered guards so it can never fire in production:
//
//   - process.env.NODE_ENV !== "development"  → component renders null.
//     Next strips the branch at build time when NODE_ENV is "production".
//   - The query param check is a secondary belt-and-braces gate. Even
//     with a dev server running, a stray Cmd-click on a UI link without
//     the param does nothing.
//
// Not wired into layout.tsx by default — intentionally opt-in so the
// component is a debugging knob, not a shipping feature. Mount it
// locally during Sentry verification, remove the mount when done.

import { useEffect } from "react"
import * as Sentry from "@sentry/nextjs"

export function SentrySmoke() {
  useEffect(() => {
    if (process.env.NODE_ENV !== "development") return
    if (typeof window === "undefined") return
    const params = new URLSearchParams(window.location.search)
    if (params.get("sentry_test") !== "1") return

    // Capture a synthetic exception so we can verify the scrub pipeline
    // end-to-end. Using a distinctive error string + a fake email-shaped
    // substring + a fake bearer-token substring lets us check (in the
    // Sentry UI) that:
    //   - the event arrived (DSN + init + consent all worked)
    //   - server-side data-scrubbing rules redacted the email/token
    //     substrings ("[Filtered]" replacement)
    //   - the device/runtime/culture contexts are absent (client-side
    //     scrubber did its job)
    Sentry.captureException(
      new Error(
        "crewship-sentry-smoke: triggered via ?sentry_test=1 — " +
          "harmless canary, ignore in dashboards. " +
          "Scrub canaries: test@example.com / Bearer abcdef1234567890",
      ),
      {
        tags: { feature: "sentry-smoke", channel: "frontend" },
      },
    )
  }, [])

  return null
}
