import type { NextConfig } from "next"
import { withSentryConfig, type SentryBuildOptions } from "@sentry/nextjs/config"

const isDev = process.env.NODE_ENV === "development"
const goPort = process.env.NEXT_PUBLIC_GO_PORT || "8080"

// Additional dev origins for cross-origin dev access (browser → LAN IP /
// SSH-tunnelled hostname). Comma-separated env var; leave empty for the
// default Next.js behaviour (`localhost` only).
const extraDevOrigins = (process.env.CREWSHIP_DEV_ORIGINS ?? "")
  .split(",")
  .map((s) => s.trim())
  .filter(Boolean)

const nextConfig: NextConfig = {
  ...(isDev ? {} : { output: "export" }),
  allowedDevOrigins: extraDevOrigins,
  images: {
    unoptimized: true,
  },
  experimental: {
    // Rewrite barrel imports of these packages to direct per-module
    // imports so tree-shaking sees through them. lucide-react alone is
    // imported in hundreds of files; recharts sits in the dashboard
    // landing route; date-fns shows up across list views.
    optimizePackageImports: ["lucide-react", "recharts", "date-fns"],
  },
  async rewrites() {
    if (!isDev) return []
    return [
      {
        source: "/api/:path*",
        destination: `http://localhost:${goPort}/api/:path*`,
      },
    ]
  },
}

// The Sentry 11 build wrapper has a separate entry point from the runtime SDK.
// Source maps are removed from the export; symbol upload remains optional.
// Debug logging is stripped for webpack builds (Turbopack has no equivalent).
const sentryBuildOptions = {
  // Empty org/project on purpose. The plugin still installs the
  // instrumentation when these are missing; without auth it just skips
  // the upload step. CI sets SENTRY_AUTH_TOKEN/SENTRY_ORG/SENTRY_PROJECT
  // only if/when we enable source-map upload later.
  org: process.env.SENTRY_ORG,
  project: process.env.SENTRY_PROJECT,
  silent: true,
  sourcemaps: { deleteSourcemapsAfterUpload: true },
  webpack: { treeshake: { removeDebugLogging: true } },
  widenClientFileUpload: false,
  // Tunnel routes through /monitoring would let us bypass ad blockers that
  // block sentry.io requests, but it requires server runtime and we're
  // statically exporting. Leave disabled.
  tunnelRoute: undefined as string | undefined,
} satisfies SentryBuildOptions

export default withSentryConfig(nextConfig, sentryBuildOptions)
