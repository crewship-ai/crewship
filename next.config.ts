import type { NextConfig } from "next"
import { withSentryConfig } from "@sentry/nextjs"

const isDev = process.env.NODE_ENV === "development"
const goPort = process.env.NEXT_PUBLIC_GO_PORT || "8080"

const nextConfig: NextConfig = {
  ...(isDev ? {} : { output: "export" }),
  allowedDevOrigins: ["192.168.1.201", "crewship-dev.unifylab.cz"],
  images: {
    unoptimized: true,
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

// withSentryConfig augments next.config with Sentry's webpack plugin. Even
// with an empty DSN at build time the wrapper is cheap — it just no-ops
// the source-map upload phase — so we apply it unconditionally and let
// the runtime init() calls gate the actual data path.
//
// Options pinned for v0.1 beta:
//
//   silent: true            quiet during `pnpm build`; the wrapper otherwise
//                           prints a banner on every build, which is noise
//                           in CI logs.
//   hideSourceMaps: true    upload source maps but do not serve them from
//                           the static export. Even though we're not
//                           uploading for v0.1, the flag is set so we don't
//                           ship a stack-trace-readable bundle to anyone
//                           who happens to fetch the .map file from /out.
//   disableLogger: true     strip Sentry SDK's internal console.log calls
//                           from the production bundle. Saves a few KB and
//                           silences "Sentry Logger [Info]" noise in the
//                           browser devtools. Note: as of @sentry/nextjs
//                           v10 this is flagged deprecated in favor of
//                           webpack.treeshake.removeDebugLogging (which
//                           Turbopack does not yet support); keep until
//                           the SDK removes it or Turbopack adds the
//                           replacement path.
//   widenClientFileUpload:  false. Symbol-upload auth is not configured
//                           yet (SENTRY_AUTH_TOKEN + org/project slugs),
//                           and unsymbolicated minified stack frames are
//                           the trade-off for beta. The frontend bundle's
//                           filenames are stable enough between releases
//                           that the Release tag + line numbers in the
//                           minified file are still useful for triage. We
//                           wire up symbolication in a follow-up once
//                           we've sized the Sentry quota cost.
const sentryBuildOptions = {
  // Empty org/project on purpose. The plugin still installs the
  // instrumentation when these are missing; without auth it just skips
  // the upload step. CI sets SENTRY_AUTH_TOKEN/SENTRY_ORG/SENTRY_PROJECT
  // only if/when we enable source-map upload later.
  org: process.env.SENTRY_ORG,
  project: process.env.SENTRY_PROJECT,
  silent: true,
  hideSourceMaps: true,
  disableLogger: true,
  widenClientFileUpload: false,
  // Tunnel routes through /monitoring would let us bypass ad blockers that
  // block sentry.io requests, but it requires server runtime and we're
  // statically exporting. Leave disabled.
  tunnelRoute: undefined as string | undefined,
}

export default withSentryConfig(nextConfig, sentryBuildOptions)
