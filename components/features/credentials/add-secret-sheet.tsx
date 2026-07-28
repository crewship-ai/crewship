"use client"

// AddSecretSheet — the "+ Add" entry point on /credentials.
//
// It used to host the flat one-page CredentialForm ("paste a secret, name it
// after the env var"). P6 replaced the body with AddCredentialWizard, which
// separates the two questions that form fused: WHAT SHAPE is this secret, and
// WHICH VARIABLE should the container see it under. CredentialForm is still
// the edit surface (EditCredentialDialog) and is untouched.
//
// The Sheet stays here so the page's mounting contract — and the props it
// passes — do not change.

import * as React from "react"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet"
import { AddCredentialWizard } from "./add-credential-wizard"

interface AddSecretSheetProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
  /** Tags already in use across the workspace, for the autocomplete in the form. */
  knownTags?: string[]
}

export function AddSecretSheet({ workspaceId, open, onOpenChange, onSuccess, knownTags }: AddSecretSheetProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="sm:max-w-[520px] p-0 flex flex-col">
        <SheetHeader className="px-5 pt-4 pb-3 border-b border-white/10">
          <SheetTitle className="text-base">Add a credential</SheetTitle>
          <SheetDescription className="text-xs">
            Pick the shape, fill what it asks for, then say who gets it and under which variable
            name. Values are encrypted with AES-256-GCM and never shown again.
          </SheetDescription>
        </SheetHeader>

        <div className="flex-1 overflow-y-auto px-5 py-4">
          {/* Remount per open so a half-finished draft never survives a close —
              a wizard that reopens on step 3 with somebody else's pasted token
              still in state is a leak waiting for a screenshot. */}
          {open && (
            <AddCredentialWizard
              key={open ? "open" : "closed"}
              workspaceId={workspaceId}
              knownTags={knownTags}
              onCancel={() => onOpenChange(false)}
              onSuccess={() => {
                onSuccess()
                onOpenChange(false)
              }}
            />
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
