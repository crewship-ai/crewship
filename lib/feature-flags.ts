/**
 * Frontend feature flags. Backed by `NEXT_PUBLIC_*` env vars so they work
 * under static export (values are inlined at build time).
 *
 * Default is always OFF — call sites must opt in explicitly.
 */

export function crewsUnifiedUI(): boolean {
  return process.env.NEXT_PUBLIC_CREWS_UNIFIED_UI === "true"
}

/**
 * Gates the legacy self-hosted MCP-server Integrations UI.
 *
 * Default OFF: the hand-rolled MCP connector management at `/integrations`
 * is being retired in favour of a managed integration platform (Composio).
 * While that rework lands, the page shows a "managed integrations coming
 * soon" placeholder instead of the legacy connector list.
 *
 * Set `NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS=true` at build time to bring the
 * old UI back (e.g. a developer who still needs to drive the existing MCP
 * endpoints while the managed path is built). Flipping the env var is a full
 * rollback — no legacy code is deleted.
 */
export function legacyMcpIntegrations(): boolean {
  return process.env.NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS === "true"
}

/**
 * Gates the static HTML App preview (`StaticAppPreview`).
 *
 * Default OFF: no Page endpoint returns publication HTML yet, so nothing
 * mounts the preview in app routes. A mount site must also pass a freshly
 * authorized publication; this flag only keeps the component dark until then.
 */
export function staticAppPreview(): boolean {
  return process.env.NEXT_PUBLIC_STATIC_APP_PREVIEW === "true"
}
