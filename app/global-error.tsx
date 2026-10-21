"use client"

import "./globals.css"

import { useEffect } from "react"
import * as Sentry from "@sentry/nextjs"

import { BareState } from "@/components/layout/app-frame"
import { PageError } from "@/components/layout/page-state"
import { CopyErrorDetails } from "@/components/layout/copy-error-details"
import { globalErrorScheme } from "@/lib/theme/global-error-scheme"

export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string }
  reset: () => void
}) {
  // global-error replaces the root layout when render itself throws, so
  // App-level boundaries have already been blown past. This is the last
  // place we can ship the failure to Sentry — beyond here the user sees
  // a static fallback with no way to file the bug for us. Consent gate
  // still applies via sentry.client.config (no DSN / no opt-in = no-op).
  useEffect(() => {
    Sentry.captureException(error, {
      tags: { boundary: "global", digest: error.digest ?? "" },
    })
  }, [error])

  // Harbor 2 G07: the same error card as every other failure, from the app's
  // own tokens (globals.css is imported above, not inherited from the layout
  // that crashed), on the ground with the lockup.
  return (
    <html lang="en" className={globalErrorScheme() === "dark" ? "dark" : undefined}>
      <body className="font-sans antialiased">
        <BareState>
          <PageError
            title="Crewship couldn’t start this page"
            description="Reload to try again. If it keeps happening, copy the details for support."
            onRetry={reset}
            retryLabel="Reload"
            errorId={error.digest}
            actions={<CopyErrorDetails digest={error.digest} />}
          />
        </BareState>
      </body>
    </html>
  )
}
