"use client"

// Connect-via-OAuth entry point for the /credentials page (#1034).
// Thin shell around the existing gateway OAuth flow
// (initiate/loopback/callback/exchange + popup) that until now was
// reachable only from the MCP server config's credential picker. The
// flow creates an OAUTH2 credential row, opens the provider consent
// popup, and polls until the tokens land — all inside OAuthForm; this
// component only supplies the page-level chrome and refresh wiring.
//
// The chrome is now CreateSurface at size `sm` (480px). It used to be a
// bare `sm:max-w-md` DialogContent carrying the shared dialog DEFAULTS —
// 448px, `p-6`, an 18px DialogTitle — which is why the audit on /design
// counted it as reading like a different design system rather than merely a
// different width.
//
// Two parts of the shell are deliberately NOT taken up here, and both have
// the same cause: OAuthForm owns the primary action, and OAuthForm lives in
// components/features/mcp/components/oauth-form.tsx, shared verbatim with the
// MCP credential picker.
//
//   · No CreateSurfaceFooter. "Authorize"/"Cancel" stay inside the body where
//     the form draws them, so the primary is inside the scrollport. Hoisting
//     it means giving OAuthForm a way to expose its handler, which changes a
//     component the MCP surface also renders.
//   · No CreateSurfaceRefusal, and so no ⌘↵. Every failure in the flow is
//     reported by a `toast.error` inside OAuthForm; there is no error state
//     this component can read, and ⌘↵ has no handler to call.
//
// `dirty` IS wired, because it can be observed from the outside: React's
// synthetic change event bubbles through the React tree, so the wrapper below
// sees any keystroke into the form's fields without reaching into its state.
// Picking a provider does not mark the surface dirty — that only prefills the
// URL/scope fields programmatically, and prompting about a discarded radio
// choice is the kind of false alarm that teaches people to click through.

import * as React from "react"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { OAuthForm } from "@/components/features/mcp/components/oauth-form"

interface ConnectOAuthDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Fired when the flow mutates the credential list — both on the
   *  intermediate PENDING row creation and on the final authorized
   *  credential — so the page can refresh its rows. */
  onSuccess: () => void
}

export function ConnectOAuthDialog({ workspaceId, open, onOpenChange, onSuccess }: ConnectOAuthDialogProps) {
  const [dirty, setDirty] = React.useState(false)

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="sm"
      dirty={dirty}
      discardLabel="this connection"
    >
      <CreateSurfaceHeader
        concept="credentials"
        context="Credentials"
        title="Connect via OAuth"
        description="Authorize a provider in a popup and store the resulting tokens as an encrypted OAUTH2 credential."
        onClose={() => onOpenChange(false)}
      />

      {/* The body supplies no padding of its own: OAuthForm carries a `p-3`
          because it is also rendered inline inside the MCP credential picker,
          and stacking the shell's padding on top of it would double-pad a
          480px surface. */}
      <CreateSurfaceBody className="px-0 py-0 sm:px-0 sm:py-0">
        <div onChange={() => setDirty(true)}>
          <OAuthForm
            envKey=""
            workspaceId={workspaceId}
            onAddCredential={onSuccess}
            onSelectCredential={() => {
              onSuccess()
              // Straight through the guard: a finished authorization has
              // nothing left to discard.
              setDirty(false)
              onOpenChange(false)
            }}
            onCancel={() => onOpenChange(false)}
          />
        </div>
      </CreateSurfaceBody>
    </CreateSurface>
  )
}
