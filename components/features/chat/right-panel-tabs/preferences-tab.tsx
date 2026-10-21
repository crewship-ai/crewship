"use client"

import { useState } from "react"
import Link from "next/link"
import { entityHref } from "@/lib/entity-links"
import { useChatAgent } from "../chat-agent-context"
import { Brain, RefreshCw, Trash2 } from "lucide-react"
import { toast } from "@/lib/toast"
import { useSession } from "@/hooks/use-auth"
import { feedbackPreferenceChoices, useAgentPreferences, type FeedbackPreference } from "@/hooks/use-feedback-preferences"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { StatusPill } from "@/components/ui/status-pill"

const preferenceStatusLabels: Record<string, string> = {
  feedback_only: "Feedback only", processing: "Processing", saved: "Remembered",
  not_eligible: "Not eligible for memory", disabled: "Memory off", failed: "Could not save memory", withdrawn: "Forgotten",
}
export const preferenceStatusLabel = (status?: string) => preferenceStatusLabels[status ?? ""] ?? "Status unavailable"

export function PreferencesTab({ agentId, workspaceId }: { agentId: string; workspaceId: string | null }) {
  const session = useSession()
  const agent = useChatAgent()
  const userId = session.data?.user.id ?? ""
  const { entries, consent, change, setConsent, refresh } = useAgentPreferences(workspaceId ?? "", userId, agentId)
  const [editing, setEditing] = useState<string | null>(null)
  const [confirmation, setConfirmation] = useState<FeedbackPreference | "opt-out" | null>(null)
  const perform = async (action: () => Promise<unknown>) => {
    try { await action() } catch (error) { toast.error(error instanceof Error ? error.message : "Unable to update preferences"); throw error }
  }
  if (!workspaceId || !userId) return <p className="p-3 text-sm text-muted-foreground">Sign in and select a workspace to see your preferences.</p>
  return <section className="space-y-4 p-3" aria-label="Your preferences for this agent">
    <div className="flex items-start justify-between gap-2">
      <p className="text-xs leading-relaxed text-muted-foreground">Only your explicitly stated preferences for this agent appear here. They apply to your private chats with this agent.</p>
      <Button variant="ghost" size="icon" aria-label="Refresh preferences" onClick={() => { void refresh() }}><RefreshCw className="size-4" /></Button>
    </div>
    {consent.isError ? <p role="alert" className="text-xs">Unable to load your memory setting.</p> : consent.data && <div className="space-y-2 rounded-lg border p-3">
      <p className="text-sm font-medium">Personal memory {consent.data.opted_out ? "off" : "on"}</p>
      <p className="text-xs text-muted-foreground">This setting applies throughout this workspace. Turning it off also forgets existing personal preferences and profiles.</p>
      <Button size="sm" variant="outline" disabled={setConsent.isPending} onClick={() => {
        if (consent.data?.opted_out) void perform(() => setConsent.mutateAsync(false)).catch(() => undefined)
        else setConfirmation("opt-out")
      }}>{consent.data.opted_out ? "Enable personal memory" : "Turn off and forget"}</Button>
    </div>}
    {entries.isPending ? <p role="status" className="text-sm">Loading preferences…</p> : entries.isError ? <div role="alert" className="text-sm">Unable to load your preferences. <Button variant="link" onClick={() => { void entries.refetch() }}>Retry</Button></div> : !entries.data?.length ? <p className="flex items-center gap-2 text-sm text-muted-foreground"><Brain className="size-4" />No preferences saved for this agent yet.</p> : <ul className="space-y-3">
      {entries.data.map(entry => <li key={entry.id} className="space-y-2 rounded-lg border bg-card p-3">
        <div className="flex items-start justify-between gap-2"><p className="text-sm font-medium">{entry.reason}</p><StatusPill status={entry.status} label={preferenceStatusLabel(entry.status)} /></div>
        <details className="text-xs text-muted-foreground"><summary className="cursor-pointer coarse:min-h-[44px]">Source</summary>{agent?.slug && <Link className="block py-2 underline underline-offset-4 coarse:min-h-[44px]" href={entityHref({ kind: "chat", agentSlug: agent.slug, sessionId: entry.chat_id })}>Open source conversation</Link>}<p className="mt-1 break-all">Your feedback on message {entry.message_id}</p><p className="break-all">Feedback {entry.feedback_id}, source revision {entry.source_revision}</p></details>
        <div className="flex flex-wrap gap-1">
          <Button size="sm" variant="ghost" disabled={change.isPending} onClick={() => setEditing(editing === entry.id ? null : entry.id)}>Change preference</Button>
          <Button size="sm" variant="ghost" disabled={change.isPending} onClick={() => setConfirmation(entry)}><Trash2 className="size-3.5" />Forget</Button>
        </div>
        {editing === entry.id && <div className="space-y-1" aria-label="Choose a replacement preference">{feedbackPreferenceChoices.map(reason => <Button key={reason} size="sm" variant="outline" className="h-auto min-h-8 w-full whitespace-normal py-2 text-left coarse:min-h-[44px]" disabled={change.isPending} onClick={() => {
          void perform(() => change.mutateAsync({ entry, reason })).then(() => setEditing(null)).catch(() => undefined)
        }}>{reason}</Button>)}</div>}
      </li>)}
    </ul>}
    <ConfirmDialog open={confirmation !== null} onOpenChange={open => { if (!open) setConfirmation(null) }}
      title={confirmation === "opt-out" ? "Turn off personal memory?" : "Forget this preference?"}
      description={confirmation === "opt-out" ? "This forgets your existing personal preferences and profiles throughout this workspace. Your feedback remains." : "This removes the preference from future private chats with this agent. Your feedback and independent preferences remain."}
      confirmLabel={confirmation === "opt-out" ? "Turn off and forget" : "Forget preference"} destructive
      onConfirm={() => perform(() => confirmation === "opt-out" ? setConsent.mutateAsync(true) : confirmation ? change.mutateAsync({ entry: confirmation }) : Promise.resolve())} />
  </section>
}
