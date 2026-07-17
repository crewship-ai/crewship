"use client"

// Connect-via-OAuth entry point for the /credentials page (#1034).
// Thin dialog shell around the existing gateway OAuth flow
// (initiate/loopback/callback/exchange + popup) that until now was
// reachable only from the MCP server config's credential picker. The
// flow creates an OAUTH2 credential row, opens the provider consent
// popup, and polls until the tokens land — all inside OAuthForm; this
// component only supplies the page-level chrome and refresh wiring.

import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog"
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
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Connect via OAuth</DialogTitle>
          <DialogDescription>
            Authorize a provider in a popup and store the resulting tokens as an encrypted OAUTH2 credential.
          </DialogDescription>
        </DialogHeader>
        <OAuthForm
          envKey=""
          workspaceId={workspaceId}
          onAddCredential={onSuccess}
          onSelectCredential={() => {
            onSuccess()
            onOpenChange(false)
          }}
          onCancel={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  )
}
