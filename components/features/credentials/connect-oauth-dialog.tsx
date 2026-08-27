"use client"

// Connect-via-OAuth entry point for the /credentials page (#1034).
// Thin shell around the existing gateway OAuth flow
// (initiate/loopback/callback/exchange + popup) that until now was
// reachable only from the MCP server config's credential picker. The
// flow creates an OAUTH2 credential row, opens the provider consent
// popup, and polls until the tokens land — all inside OAuthForm; this
// component only supplies the page-level chrome and refresh wiring.
//
// The chrome is CreateSurface at size `sm` (480px). It used to be a bare
// `sm:max-w-md` DialogContent carrying the shared dialog DEFAULTS — 448px,
// `p-6`, an 18px DialogTitle — which is why the audit on /design counted it as
// reading like a different design system rather than merely a different width.
//
// The primary is the shell's now. It used to stay inside the body, because
// OAuthForm drew it and OAuthForm is shared verbatim with the MCP credential
// picker; the form publishes its action through `onActionChange` instead, so
// the picker keeps its inline row and this surface puts Authorize in the
// footer — outside the scrollport, next to Cancel, and reachable by ⌘↵.
//
// Still NOT taken up: CreateSurfaceRefusal. Every failure in the flow is
// reported by a `toast.error` inside OAuthForm, so there is no error state
// this component can read. Wiring a band that can never fill is worse than
// none — it says the surface reports its refusals here, and then it does not.
//
// `dirty` IS wired, because it can be observed from the outside: React's
// synthetic change event bubbles through the React tree, so the wrapper below
// sees any keystroke into the form's fields without reaching into its state.
// Picking a provider does not mark the surface dirty — that only prefills the
// URL/scope fields programmatically, and prompting about a discarded radio
// choice is the kind of false alarm that teaches people to click through.

import * as React from "react"
import { ExternalLink } from "lucide-react"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { OAuthForm, type OAuthFormAction } from "@/components/features/mcp/components/oauth-form"

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
  const [action, setAction] = React.useState<OAuthFormAction | null>(null)

  // Stable, so the form's publishing effect depends only on the action's own
  // state rather than re-firing every render.
  const handleActionChange = React.useCallback((next: OAuthFormAction) => setAction(next), [])

  return (
    <CreateSurface
      open={open}
      size="sm"
      dirty={dirty}
      discardLabel="this connection"
      onSubmit={() => {
        if (action && !action.disabled) action.authorize()
      }}
      // OAuthForm unmounts when the surface closes, so its fields are blank
      // on the next open — but `dirty` lives out here and survived, which made
      // the second open ask about work that no longer existed.
      onOpenChange={(next) => {
        if (!next) setDirty(false)
        onOpenChange(next)
      }}
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
            onActionChange={handleActionChange}
            variant="surface"
          />
        </div>
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={action?.label ?? "Authorize"}
        primaryIcon={ExternalLink}
        // Disabled until the form says otherwise: before it has published an
        // action there are no credentials typed, so there is nothing to send.
        primaryDisabled={action?.disabled ?? true}
        busy={action?.busy ?? false}
        onPrimary={() => action?.authorize()}
      />
    </CreateSurface>
  )
}
