"use client"

import type { DocumentDurability } from "@/hooks/use-page-document"

/** Text for the one save status line. Only "Confirmed" means the server holds the work. */
export function durabilityLabel(durability: DocumentDurability, status: string): string | undefined {
  switch (durability) {
    case "writing": return "Saving to this browser… · not yet confirmed by server"
    case "local": return "Saved locally in this browser · not yet confirmed by server"
    case "unsaved": return "Not stored in this browser or on the server · keep this tab open"
    case "held": return "An earlier unsent draft is held in this browser and was not restored"
    default: return status === "Saved" ? "Confirmed by server" : undefined
  }
}

/** Extends the header status; it stays the single live region for save state. */
export function DocumentSaveState({ status, durability, storageUnavailable, storageBusy }: { status: string; durability: DocumentDurability; storageUnavailable?: boolean; storageBusy?: boolean }) {
  const label = durabilityLabel(durability, status)
  return <span role="status" aria-live="polite" className="text-sm" data-durability={durability}>
    {status}{label ? <span className="text-muted-foreground"> · {label}</span> : null}
    {storageUnavailable ? <span className="text-muted-foreground"> · {storageBusy ? "Another tab of this browser is editing this document; edits here are not stored locally" : "This browser's storage is not available; an earlier unsent draft may exist"}</span> : null}
  </span>
}
