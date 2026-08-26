"use client"

// AddSecretSheet — the "+ Add" entry point on /credentials.
//
// It used to host the flat one-page CredentialForm ("paste a secret, name it
// after the env var"). P6 replaced the body with AddCredentialWizard, which
// separates the two questions that form fused: WHAT SHAPE is this secret, and
// WHICH VARIABLE should the container see it under. CredentialForm is still
// the edit surface (EditCredentialDialog) and is untouched.
//
// The container is now CreateSurface — the shell every create/import door in
// the product mounts (components/layout/create-surface.tsx). What that keeps
// and what it changes, since this file used to argue both:
//
//  · Still a CENTRED DIALOG on a pointer device, for the reason this comment
//    gave before the shell existed: a sheet reads as an inspector, something
//    slid out beside the thing you were already looking at, which is right for
//    the detail view and wrong for a creation flow that owns the screen until
//    it is finished or abandoned. The width moves 680 → 640 (`md`), which is
//    the entire point of four fixed widths instead of eleven.
//
//  · Below `sm` the shell is a BOTTOM SHEET capped at 92dvh, where this file
//    hand-rolled a full-screen takeover. Same problem, different answer: the
//    takeover kept the footer clear of the browser chrome by owning the
//    viewport; the sheet anchors to the bottom edge, pads past the home
//    indicator and leaves the page you came from visible above it. Both are
//    dvh-based — neither centres a 358px card — so the failure this file was
//    written to avoid stays avoided, and one dismissal gesture now behaves the
//    same here as on the other eleven doors.
//
//  · Esc, the overlay click and the header × go through the shell's DISCARD
//    GUARD. Before, any of the three threw a half-typed secret away without
//    asking; the wizard reports `dirty` up and the shell asks. Cancel in the
//    footer stays unguarded, which is the shell's rule and this flow's
//    previous behaviour.
//
//  · ⌘↵ fires whatever the footer's primary is on the step you are on. The
//    wizard hands that action up through `primaryRef` because only it knows
//    whether the primary is "Continue" or "Save secret".
//
// The wizard still owns the three bands below the header — step bar,
// scrolling body, docked footer — because only it knows which control belongs
// in which band. It draws them with the shell's parts now rather than its own.
//
// The file and export names are unchanged so the page's mounting contract and
// the props it passes stay put; renaming them is a mechanical change that
// would only bury this one in the diff.

import * as React from "react"
import { CreateSurface, CreateSurfaceHeader } from "@/components/layout/create-surface"
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
  // The guard belongs to the shell, the input belongs to the wizard, so the
  // one bit that connects them travels up.
  const [dirty, setDirty] = React.useState(false)
  React.useEffect(() => {
    if (!open) setDirty(false)
  }, [open])

  // A ref, not state: the primary action closes over every field in the
  // wizard, so it is a new function on every keystroke and re-rendering the
  // surface for it would be absurd.
  const primaryRef = React.useRef<(() => void) | null>(null)

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="md"
      dirty={dirty}
      discardLabel="this credential"
      onSubmit={() => primaryRef.current?.()}
    >
      <CreateSurfaceHeader
        concept="credentials"
        context="Credentials"
        title="Add a credential"
        description={
          <>
            Encrypted with AES-256-GCM and never shown again.
            {/* The steps say what the steps are; on a phone that sentence is
                three lines of chrome above the first control. */}
            <span className="hidden sm:inline">
              {" "}Pick the shape, fill what it asks for, then say who gets it and under which
              variable name.
            </span>
          </>
        }
        onClose={() => onOpenChange(false)}
      />

      {/* Remount per open so a half-finished draft never survives a close —
          a wizard that reopens on step 3 with somebody else's pasted token
          still in state is a leak waiting for a screenshot. */}
      {open && (
        <AddCredentialWizard
          key="open"
          workspaceId={workspaceId}
          knownTags={knownTags}
          onDirtyChange={setDirty}
          primaryRef={primaryRef}
          onCancel={() => onOpenChange(false)}
          onSuccess={() => {
            onSuccess()
            onOpenChange(false)
          }}
        />
      )}
    </CreateSurface>
  )
}
