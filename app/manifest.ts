import type { MetadataRoute } from "next"

/**
 * `output: export` refuses to build a route handler that has not declared
 * itself static, and the manifest is a route handler. Without this the whole
 * frontend build fails — which neither typecheck nor the unit suite catches,
 * only `pnpm build`.
 */
export const dynamic = "force-static"

/**
 * Makes "Add to Home Screen" behave like an app rather than a bookmark: its
 * own icon, its own name, and no browser chrome eating the top of a screen
 * that is already short.
 *
 * Deliberately no service worker. Crewship is a client for a backend it cannot
 * work without — a websocket for realtime, an API for everything else — so
 * caching the shell buys a faster blank screen and costs an invalidation story
 * that would have to be reconciled with the in-app UpdateBanner.
 */
export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "Crewship",
    short_name: "Crewship",
    description: "Self-hosted runtime for AI coding agents.",
    start_url: "/",
    display: "standalone",
    // Matches the navy backdrop baked into app/icon.svg, so the splash and the
    // icon are not two different blues.
    background_color: "#0b0f17",
    theme_color: "#0b0f17",
    icons: [
      { src: "/icon.svg", sizes: "any", type: "image/svg+xml", purpose: "any" },
    ],
  }
}
