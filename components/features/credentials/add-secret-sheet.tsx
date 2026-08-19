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
// …but only from `sm` up. The shared DialogContent insets a phone-width card
// by 1rem, which leaves a 358px column for a step bar, six shape tiles and a
// two-button footer, and centres it vertically so the actions sit under the
// browser chrome. Below `sm` the dialog is the screen: full bleed, square
// corners, 100dvh so the on-screen keyboard cannot push the footer out of
// reach. One component, two anchorings — not a separate mobile dialog.
//
// The wizard owns its own scrolling from here on. This file supplies a flex
// column and the title; the wizard splits it into step bar / scrolling body /
// docked footer, because the footer has to sit OUTSIDE the scrollport and only
// the wizard knows what belongs in it.
//
// The file and export names are unchanged so the page's mounting contract and
// the props it passes stay put; renaming them is a mechanical change that
// would only bury this one in the diff.

import * as React from "react"
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription,
} from "@/components/ui/dialog"
import { cn } from "@/lib/utils"
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
      <DialogContent
        className={cn(
          "flex flex-col gap-0 overflow-hidden p-0",
          "sm:max-h-[85vh] sm:max-w-[680px]",
          // Not w-screen: 100vw includes a classic scrollbar's width, so on a
          // narrow desktop window it overflows by exactly the scrollbar. inset-0
          // plus the base w-full already resolves to the viewport.
          "max-sm:inset-0 max-sm:h-[100dvh] max-sm:max-w-none",
          "max-sm:translate-x-0 max-sm:translate-y-0 max-sm:rounded-none max-sm:border-0",
        )}
      >
        <DialogHeader className="shrink-0 gap-1 border-b border-hairline px-4 py-3 pr-12 text-left sm:px-5 sm:py-4">
          <DialogTitle className="type-row font-semibold sm:text-base">Add a credential</DialogTitle>
          <DialogDescription className="type-meta text-muted-foreground">
            Encrypted with AES-256-GCM and never shown again.
            {/* The steps say what the steps are; on a phone that sentence is
                three lines of chrome above the first control. */}
            <span className="hidden sm:inline">
              {" "}Pick the shape, fill what it asks for, then say who gets it and under which
              variable name.
            </span>
          </DialogDescription>
        </DialogHeader>

        {/* Remount per open so a half-finished draft never survives a close —
            a wizard that reopens on step 3 with somebody else's pasted token
            still in state is a leak waiting for a screenshot. */}
        {open && (
          <AddCredentialWizard
            key="open"
            workspaceId={workspaceId}
            knownTags={knownTags}
            onCancel={() => onOpenChange(false)}
            onSuccess={() => {
              onSuccess()
              onOpenChange(false)
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
