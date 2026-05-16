"use client"

import { useEffect } from "react"
import * as Sentry from "@sentry/nextjs"

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

  return (
    <html lang="en">
      <body style={{ fontFamily: "system-ui, sans-serif", margin: 0, padding: 0 }}>
        <div
          style={{
            display: "flex",
            minHeight: "100vh",
            alignItems: "center",
            justifyContent: "center",
            padding: "24px",
            backgroundColor: "#fafafa",
          }}
        >
          <div style={{ textAlign: "center", maxWidth: "400px" }}>
            <div
              style={{
                width: "48px",
                height: "48px",
                borderRadius: "12px",
                backgroundColor: "#fee2e2",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                margin: "0 auto 16px",
                fontSize: "24px",
              }}
            >
              !
            </div>
            <h2 style={{ fontSize: "18px", fontWeight: 600, margin: "0 0 8px" }}>
              Critical Error
            </h2>
            <p style={{ fontSize: "14px", color: "#6b7280", margin: "0 0 24px" }}>
              The application encountered a critical error. Please reload the page.
            </p>
            {error.digest && (
              <p style={{ fontSize: "12px", color: "#9ca3af", fontFamily: "monospace", margin: "0 0 16px" }}>
                Error ID: {error.digest}
              </p>
            )}
            <button
              onClick={reset}
              style={{
                padding: "8px 20px",
                backgroundColor: "#18181b",
                color: "#fff",
                border: "none",
                borderRadius: "6px",
                fontSize: "14px",
                cursor: "pointer",
              }}
            >
              Reload
            </button>
          </div>
        </div>
      </body>
    </html>
  )
}
