"use client"

import { useState } from "react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { backfillAgentAvatars, type AvatarBackfillResult } from "@/lib/agent-avatar-persist"

/**
 * The explicit backfill for agents that have no stored avatar render (#2876).
 *
 * Renders used to be stored as a side effect of viewing an agent. They are
 * now stored when an agent is created or edited, and existing agents — and
 * agents created outside the browser (CLI, templates, Captain), which cannot
 * draw a render because the generator is JavaScript-only — get one here, when
 * someone with edit rights asks for it.
 */
export function AvatarBackfillButton({
  workspaceId,
  crewId,
  className,
}: {
  workspaceId: string
  /** Limit to one crew; omit for the whole workspace. */
  crewId?: string
  className?: string
}) {
  const [busy, setBusy] = useState(false)

  async function run() {
    setBusy(true)
    try {
      const result = await backfillAgentAvatars(workspaceId, { crewId })
      reportBackfill(result)
    } catch (err) {
      toast.error(`Could not store avatars: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Button type="button" variant="outline" size="sm" className={className} disabled={busy} onClick={() => void run()}>
      {busy ? "Storing avatars…" : "Store missing avatars"}
    </Button>
  )
}

/** One toast that says what the backfill did, including what it could not do. */
export function reportBackfill(result: AvatarBackfillResult) {
  const skipped = result.refused + result.failed
  if (result.stored === 0 && skipped === 0) {
    toast.success("Every agent already has a stored avatar")
    return
  }
  const stored = `Stored ${result.stored} avatar${result.stored === 1 ? "" : "s"}`
  if (skipped === 0) {
    toast.success(stored)
    return
  }
  toast.warning(stored, {
    description: `${skipped} agent${skipped === 1 ? "" : "s"} kept a generated avatar` +
      (result.refused ? ` (${result.refused} you cannot edit)` : "") + ".",
  })
}
