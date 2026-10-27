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
 * Gates the chat artifact-context surfaces (work result cards, linked
 * viewer switching, composer artifact context, conversation library).
 *
 * Default OFF: the server run-state, library and output-link endpoints they
 * read are a proposed contract and not yet served.
 */
export function chatArtifactContext(): boolean {
  return process.env.NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT === "true"
}

/**
 * Gates Duplicate and the atomic move into a working folder. Both need
 * endpoints that are only proposed (coordination entries FE-REQ-F4-1/2), so
 * the controls stay hidden until the server ships them.
 *
 * Set `NEXT_PUBLIC_ARTIFACT_COPY_MOVE=true` at build time to show them.
 */
export function artifactCopyMove(): boolean {
  return process.env.NEXT_PUBLIC_ARTIFACT_COPY_MOVE === "true"
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

/**
 * Gates Document revert and proposal review (client for endpoints that
 * the backend has not shipped yet; see the F3 coordination requests).
 * Default OFF.
 */
export function documentReviewActions(): boolean {
  return process.env.NEXT_PUBLIC_DOCUMENT_REVIEW_ACTIONS === "true"
}

/**
 * Chat search "Files" tab (CH-25). There is no file-search endpoint yet; with
 * the flag on, the tab runs against a fixture client so the states can be
 * reviewed. Default OFF: the tab then says plainly that file search is not
 * available.
 */
export function chatSearchFilesFixtures(): boolean {
  return process.env.NEXT_PUBLIC_CHAT_SEARCH_FILES_FIXTURES === "true"
}

/**
 * Chat sidebar smart sections without a data source yet (CH-26: Mentions,
 * Threads, Saved, Drafts, Waiting for you, Review). Default OFF; with the flag
 * on they render from fixtures.
 */
export function chatSidebarSmartSections(): boolean {
  return process.env.NEXT_PUBLIC_CHAT_SIDEBAR_SMART_SECTIONS === "true"
}

/**
 * Chat sidebar view chips (Unreads, Agents running) and the "hold rows still
 * under the pointer" behaviour (CH-26). Both change live sidebar behaviour, so
 * default OFF.
 */
export function chatSidebarViews(): boolean {
  return process.env.NEXT_PUBLIC_CHAT_SIDEBAR_VIEWS === "true"
}

/**
 * Mounts the native Slides editor on `/pages/<slug>` for a Page the server
 * reports as `has_slides`. Default OFF: the page then behaves exactly as before.
 *
 * Mounting alone never writes. The server has Slides Read and Initialize only
 * (behind CREWSHIP_SLIDES_ENABLED); there is no save route, so editing stays a
 * local draft (coord FE-REQ-F5-1).
 */
export function slidesEditorMount(): boolean {
  return process.env.NEXT_PUBLIC_SLIDES_EDITOR === "true"
}

/**
 * Gates artifact project cards and the side viewer in chat (multi-screen
 * HTML projects with versions and anchored comments).
 *
 * Default OFF: the `/pages/{slug}/artifact` routes are still in review. Even
 * with the flag on, nothing renders until the server reports the feature
 * available for the workspace.
 */
export function chatArtifactProjects(): boolean {
  return process.env.NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS === "true"
}

/**
 * Gates the Craft edit-lease bar, agent proposals and local draft recovery
 * in chat. Default OFF: the native lease routes (#4374) are opt-in per Page.
 */
export function chatCraftLease(): boolean {
  return process.env.NEXT_PUBLIC_CHAT_CRAFT_LEASE === "true"
}

/**
 * Gates the Artifacts library at /artifacts: a gallery of versioned projects,
 * Word, Sheet, Slides and dashboards with their shared version and a project
 * history detail. Default OFF; /artifacts sends saved links to /pages.
 */
export function artifactsLibrary(): boolean {
  return process.env.NEXT_PUBLIC_ARTIFACTS_LIBRARY === "true"
}
