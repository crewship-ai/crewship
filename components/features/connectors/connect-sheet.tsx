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
import type { ConnectorManifest } from "./types"

export interface ConnectorConnectSheetProps {
  manifest: ConnectorManifest | null
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  /** Called after successful install (or after OAuth redirect started). */
  onInstalled: (integrationId: string) => void
}

export function ConnectorConnectSheet(_: ConnectorConnectSheetProps): ReactElement {
  throw new Error("TDD STUB — implement ConnectorConnectSheet")
}
