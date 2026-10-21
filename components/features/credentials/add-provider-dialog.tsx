"use client"

/**
 * Add AI provider — its own large dialog (Harbor 2): Provider → Connect →
 * Pays for. The providers are the ones the provider-login API accepts
 * (`LOGIN_PROVIDERS`, mirrored from the backend catalog), never the
 * decorative brand list. Connect is the provider's own sign-in: a setup
 * token, a device code with its countdown, a login file or an API key.
 * Pays for writes the agents' bindings and, optionally, joins an account
 * group.
 *
 * Sign in again opens the same dialog at Connect for one account: its
 * provider and name are locked, and only the token is replaced — who it pays
 * for stays.
 *
 * The flow itself lives in AddCredentialWizard, which already carries every
 * guard a value needs (no copy survives a close, discard prompt, partial-save
 * report); this file is the surface around it.
 */

import * as React from "react"
import { Sparkles } from "lucide-react"

import { CreateSurface, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { AddCredentialWizard, type WizardInitial } from "./add-credential-wizard"

export interface AddProviderDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: (credentialId?: string) => void
  knownTags?: string[]
  /** Sign in again: the account's id, provider, mode and name; the dialog opens at Connect. */
  initial?: WizardInitial
  /** Overrides the device-code poll period; tests pass milliseconds. */
  devicePollMs?: number
}

export function AddProviderDialog({ workspaceId, open, onOpenChange, onSuccess, knownTags, initial, devicePollMs }: AddProviderDialogProps) {
  const [dirty, setDirty] = React.useState(false)
  React.useEffect(() => {
    if (!open) setDirty(false)
  }, [open])
  const primaryRef = React.useRef<(() => void) | null>(null)
  const relogin = Boolean(initial?.credentialId)
  const start: WizardInitial = { ...initial, itemType: "PROVIDER_LOGIN", step: relogin ? "values" : initial?.step }

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      dirty={dirty}
      discardLabel="this AI provider account"
      onSubmit={() => primaryRef.current?.()}
    >
      <CreateSurfaceHeader
        icon={Sparkles}
        context="Vault"
        title={relogin ? "Sign in again" : "Add AI provider"}
        description={
          relogin
            ? `A new token for ${initial?.name ?? "this account"}. It keeps paying for the same agents.`
            : "A model account your agents think with: a subscription sign-in or an API key."
        }
        onClose={() => onOpenChange(false)}
      />
      {/* Remount per open: a half-finished sign-in never survives a close. */}
      {open && (
        <AddCredentialWizard
          key={`${workspaceId}:${initial?.credentialId ?? "new"}:${initial?.provider ?? ""}`}
          workspaceId={workspaceId}
          knownTags={knownTags}
          initial={start}
          devicePollMs={devicePollMs}
          onDirtyChange={setDirty}
          primaryRef={primaryRef}
          onCancel={() => onOpenChange(false)}
          onSuccess={(id) => {
            onSuccess(id)
            onOpenChange(false)
          }}
        />
      )}
    </CreateSurface>
  )
}
