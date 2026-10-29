"use client"
import { useEffect, useState } from "react"
import { Layers, RefreshCw, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { discardLocalDraft, readLocalDraft, type LocalDraft } from "./local-draft"

interface LocalDraftBannerProps {
  userId: string
  workspaceId: string
  slug: string
  /** Display name of whoever holds the lease now, when it is not me. */
  holderName?: string | null
  onCompare: (draft: LocalDraft) => void
}

const BTN = "coarse:min-h-[44px]"

function clock(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? "earlier" : date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
}

/**
 * Offers a recovered local draft as compare or copy. Deliberately has no
 * restore or save action: the server version may have moved on, and the
 * human decides what to carry over. "Keep as copy" becomes "Copy text"
 * until a create-copy endpoint exists.
 */
export function LocalDraftBanner({ userId, workspaceId, slug, holderName, onCompare }: LocalDraftBannerProps) {
  const identity = JSON.stringify([userId, workspaceId, slug])
  // What was read is remembered with whose it is; after a change of person or
  // document it is not shown, not even for the render before the re-read.
  const [read, setRead] = useState<{ identity: string; draft: LocalDraft | null }>({ identity: "", draft: null })
  const [copy, setCopy] = useState<{ identity: string; state: "idle" | "copied" | "failed" }>({ identity: "", state: "idle" })
  // Read after mount: storage does not exist during prerender.
  useEffect(() => { setRead({ identity, draft: readLocalDraft(userId, workspaceId, slug) }) }, [identity, userId, workspaceId, slug])
  const draft = read.identity === identity ? read.draft : null
  const copied = copy.identity === identity ? copy.state : "idle"
  const setCopied = (state: "copied" | "failed") => setCopy({ identity, state })
  if (!draft) return null

  const copyDraft = async () => {
    try {
      await navigator.clipboard.writeText(draft.content)
      setCopied("copied")
    } catch {
      setCopied("failed")
    }
  }
  const discard = () => { discardLocalDraft(userId, workspaceId, slug); setRead({ identity, draft: null }) }
  const tail = holderName ? `${holderName} edits now, so it is not saved over their work.` : "Not saved yet."

  return <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-warn/10 px-3 py-2 text-sm">
    <RefreshCw aria-hidden className="size-4 shrink-0 text-warn" />
    <div className="grid min-w-0 flex-[1_1_200px]">
      <b className="font-semibold">Recovered local draft</b>
      <span className="text-xs text-muted-foreground">From {clock(draft.savedAt)}, before your browser closed. {tail}</span>
    </div>
    <span role="status" className="text-xs text-muted-foreground">{copied === "copied" ? "Copied" : copied === "failed" ? "Could not copy" : ""}</span>
    <Button type="button" size="sm" variant="ghost" className={BTN} onClick={() => onCompare(draft)}><Layers aria-hidden />Compare</Button>
    <Button type="button" size="sm" variant="secondary" className={BTN} onClick={() => void copyDraft()}>Copy text</Button>
    <Button type="button" size="icon" variant="ghost" aria-label="Discard draft" className="size-8 coarse:size-[44px]" onClick={discard}><X aria-hidden /></Button>
  </div>
}
