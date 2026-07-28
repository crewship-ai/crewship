"use client"

// AddSecretSheet — the "+ Add" entry point on /credentials.
//
// It used to host the flat one-page CredentialForm ("paste a secret, name it
// after the env var"). P6 replaced the body with AddCredentialWizard, which
// separates the two questions that form fused: WHAT SHAPE is this secret, and
// WHICH VARIABLE should the container see it under. CredentialForm is still
// the edit surface (EditCredentialDialog) and is untouched.
//
// The container is a centred Dialog, matching New crew. That is a shape
// decision, not a taste one: a sheet reads as an inspector — something slid
// out beside the thing you were already looking at — which is right for the
// detail view and wrong for a creation flow that owns the screen until it is
// finished or abandoned. New crew is the reference because it is the app's
// other multi-step create, and two creates in two containers is the kind of
// inconsistency a user feels without being able to name.
//
// The file and export names are unchanged so the page's mounting contract and
// the props it passes stay put; renaming them is a mechanical change that
// would only bury this one in the diff.

import * as React from "react"
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription,
} from "@/components/ui/dialog"
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
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* Same width as New crew's single-column steps. The wizard's widest
          step is a field list rather than a gallery, so it needs none of that
          dialog's step-dependent growth. max-h + the scrolling body keep a
          long field list from pushing the footer off a laptop screen. */}
      <DialogContent className="sm:max-w-[680px] max-h-[85vh] p-0 overflow-hidden flex flex-col">
        <DialogHeader className="px-5 pt-4 pb-3 border-b border-white/10">
          <DialogTitle className="text-base">Add a credential</DialogTitle>
          <DialogDescription className="text-[12.5px]">
            Pick the shape, fill what it asks for, then say who gets it and under which variable
            name. Values are encrypted with AES-256-GCM and never shown again.
          </DialogDescription>
        </DialogHeader>

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
      </DialogContent>
    </Dialog>
  )
}
