"use client"

import { useEffect } from "react"
import Link from "next/link"
import * as Sentry from "@sentry/nextjs"

import { PageError } from "@/components/layout/page-state"
import { CopyErrorDetails } from "@/components/layout/copy-error-details"

/**
 * A screen inside the app crashed (Harbor 2 G06). The frame stays — rail, top
 * bar, banners — and only the content is replaced by the error card: what
 * happened, Try again, and the error id with Copy details for support. One
 * primary action; the way to the dashboard is a quiet footer link.
 */
export default function DashboardError({
  error,
  reset,
}: {
  error: Error & { digest?: string }
  reset: () => void
}) {
  useEffect(() => {
    Sentry.captureException(error, {
      tags: { boundary: "dashboard", digest: error.digest ?? "" },
    })
    console.error("Dashboard error boundary caught:", error)
  }, [error])

  return (
    <PageError
      title="This page hit an error"
      description="Try again. If it keeps happening, copy the details for support."
      onRetry={reset}
      retryLabel="Try again"
      errorId={error.digest}
      actions={
        <>
          <CopyErrorDetails digest={error.digest} />
          <Link href="/" className="page-link-btn">Go to Dashboard</Link>
        </>
      }
    />
  )
}
