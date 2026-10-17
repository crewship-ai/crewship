"use client"

import { useEffect } from "react"
import * as Sentry from "@sentry/nextjs"

import { BareState } from "@/components/layout/app-frame"
import { PageError } from "@/components/layout/page-state"
import { CopyErrorDetails } from "@/components/layout/copy-error-details"

/**
 * The app itself crashed above the dashboard's own boundary (Harbor 2 G07).
 * No app frame — the frame may be what failed — just the error card on the
 * ground with the lockup, and Reload.
 */
export default function Error({
  error,
  reset,
}: {
  error: Error & { digest?: string }
  reset: () => void
}) {
  useEffect(() => {
    // Forward to Sentry first — captureException is gated on consent
    // by sentry.client.config (no DSN / no opt-in = no-op). Without
    // this the App-level React error boundary would log to the
    // browser console only, and the maintainer would never see the
    // bug. Console.error stays as a developer-time aid.
    Sentry.captureException(error, {
      tags: { boundary: "app", digest: error.digest ?? "" },
    })
    console.error("App error boundary caught:", error)
  }, [error])

  return (
    <BareState>
      <PageError
        title="Crewship hit an error"
        description="Reload to try again. If it keeps happening, copy the details for support."
        onRetry={reset}
        retryLabel="Reload"
        errorId={error.digest}
        actions={<CopyErrorDetails digest={error.digest} />}
      />
    </BareState>
  )
}
