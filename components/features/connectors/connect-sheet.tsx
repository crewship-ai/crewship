"use client"

// ConnectorConnectSheet — the per-manifest connect form, opened when
// the user clicks a tile in ConnectorCatalog. Renders one of three
// shapes depending on manifest.auth_mode:
//
//   mcp_oauth   → single "Connect" button (no fields). Submitting
//                 calls install which returns next_step=mcp_oauth and
//                 the OAuth flow begins.
//   pat / conn_string → SchemaForm with manifest.fields, then submit
//                 calls verify (best-effort) and install.
//   byo_oauth   → SchemaForm with client_id+client_secret (rendered
//                 by SchemaForm), plus the manifest.docs.setup_md
//                 markdown rendered above the form so the user has
//                 step-by-step provider setup instructions.
//
// The "Add custom MCP server" path bypasses this entirely and opens
// the legacy AddMCPWizard.
//
// TDD STUB — body throws until implemented.

import type { ReactElement } from "react"
import type { ConnectorManifest, InstallResult } from "./types"

export interface ConnectorConnectSheetProps {
  manifest: ConnectorManifest | null
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  /**
   * Called once the install endpoint returns. `result.status` is
   * `installed` for synchronous paths (pat / conn_string / none) and
   * `oauth-redirect` for asynchronous paths (mcp_oauth / byo_oauth)
   * — in the latter case the parent typically opens `oauthUrl` in a
   * popup and the integration is fully active after consent returns.
   */
  onInstalled: (result: InstallResult) => void
}

// Rendered as a non-crashing placeholder until ConnectorConnectSheet
// is implemented. A throw here would crash the whole page if the
// sheet ever gets mounted accidentally; render-empty-on-closed plus
// a small notice on open is the safer scaffold.
export function ConnectorConnectSheet(props: ConnectorConnectSheetProps): ReactElement | null {
  if (!props.open || !props.manifest) return null
  return (
    <div role="dialog" aria-live="polite" className="text-sm text-muted-foreground p-4">
      Connect sheet for <strong>{props.manifest.name}</strong> is not implemented yet.
    </div>
  )
}
