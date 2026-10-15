"use client"

import { useState } from "react"
import { toast } from "sonner"
import { useIsMobile } from "@/hooks/use-mobile"
import { feedbackPreferenceChoices, useTurnPreference } from "@/hooks/use-feedback-preferences"
import { useDrawerStore } from "@/stores/drawer-store"
import { useArtifactStore } from "@/stores/artifact-store"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { PreferencesTab, preferenceStatusLabel } from "./right-panel-tabs/preferences-tab"

interface Props {
  workspaceId: string; userId: string; agentId: string; messageId: string
  signal: "helpful" | "not_helpful"; chatId?: string; traceId?: string
}

export function TurnPreferenceFeedback(props: Props) {
  const [opened, setOpened] = useState(false)
  return <div className="basis-full pt-1">
    {!opened ? <button type="button" className="min-h-8 text-xs text-muted-foreground underline-offset-4 hover:underline coarse:min-h-12" onClick={() => setOpened(true)}>What should change next time? (optional)</button> : <PreferenceReasonForm {...props} />}
  </div>
}

function PreferenceReasonForm(props: Props) {
  const { state, remember } = useTurnPreference(props)
  const [manageOpen, setManageOpen] = useState(false)
  const mobile = useIsMobile()
  const status = state.data?.memory?.status ?? "feedback_only"
  const failed = state.isError || remember.isError
  return <div className="max-w-sm space-y-2 rounded-lg border bg-card p-3">
    <p className="text-xs text-muted-foreground">Optional: choose a preference for future private chats with this agent. A thumb alone only rates this response.</p>
    <div className="flex flex-col gap-1">{feedbackPreferenceChoices.map(reason => <Button key={reason} type="button" variant="outline" size="sm" className="h-auto min-h-8 justify-start whitespace-normal py-2 text-left coarse:min-h-12" disabled={remember.isPending} onClick={() => {
      void remember.mutateAsync(reason).catch(error => toast.error(error instanceof Error ? error.message : "Unable to save your preference"))
    }}>{reason}</Button>)}</div>
    <p role="status" className="text-xs">{remember.isPending ? "Saving your preference…" : failed ? "Unable to verify memory status." : preferenceStatusLabel(status)}
      {!remember.isPending && !failed && status === "saved" && " for your next private chat with this agent."}
      {!remember.isPending && !failed && status === "not_eligible" && ". This chat or preference is not eligible for personal memory."}
    </p>
    <div className="flex flex-wrap items-center gap-2">
      <Button type="button" size="sm" variant="ghost" onClick={() => { remember.reset(); void state.refetch() }}>Refresh status</Button>
      <Button type="button" size="sm" variant="ghost" onClick={() => {
        if (mobile || useArtifactStore.getState().open) setManageOpen(true)
        else useDrawerStore.getState().setActiveTab("memory")
      }}>Manage my preferences</Button>
    </div>
    <Dialog open={manageOpen} onOpenChange={setManageOpen}><DialogContent className="max-h-[85dvh] overflow-y-auto"><DialogHeader><DialogTitle>Your preferences</DialogTitle><DialogDescription>Personal memory for this agent</DialogDescription></DialogHeader><PreferencesTab agentId={props.agentId} workspaceId={props.workspaceId} /></DialogContent></Dialog>
  </div>
}
