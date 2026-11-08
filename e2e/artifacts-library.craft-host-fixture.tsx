// Stand-in for NativeCraftEditorHost in the Artifacts library browser test. The real host runs the
// Craft engine in an isolated frame behind an admission, read-verification and lease protocol that the
// Craft acceptance suites cover against a live server. Here only its outward contract is kept: it
// reports its status to the frame (onStatus) and renders the REAL lock controls (NativeCraftLeaseBar)
// through placeLease, which the frame mounts in the status chip popover. The editor surface is a
// labelled placeholder, never an editor. Every lock action is recorded in window.__craftActions.
import * as React from "react"
import { NativeCraftLeaseBar, type NativeCraftLeaseAction, type NativeCraftLeaseView } from "../components/features/pages/native-craft-lease-bar"
import type { NativeCraftHostStatus } from "../components/features/pages/native-craft-editor-host"

export type { NativeCraftHostStatus }

declare global { interface Window { __craftActions: { pageId: string; action: NativeCraftLeaseAction }[] } }
window.__craftActions = []

/** Partner brief is held by Jana; Price model is free. This person may edit both. */
const INITIAL: Record<string, NativeCraftLeaseView> = {
  "page-brief": { holder: { actor_id: "user:jana", display_name: "Jana Nováková", kind: "human" }, can_edit: true, mine: false, generation: "7", can_write: false, acquired_at: null, handoff_requested_by: [] },
  "page-model": { holder: null, can_edit: true, mine: false, generation: null, can_write: false, acquired_at: null, handoff_requested_by: [] },
}
const VERSION: Record<string, string> = { "page-brief": "4", "page-model": "3" }

export function NativeCraftEditorHost({ pageId, humanId, onStatus, placeLease, editorHidden = false }: {
  workspaceId: string; pageId: string; humanId: string; enabled?: boolean
  placeHistory?: (node: React.ReactNode) => React.ReactNode
  placeAgentStart?: (form: React.ReactNode) => React.ReactNode
  onStatus?: (status: NativeCraftHostStatus) => void
  placeLease?: (controls: React.ReactNode) => React.ReactNode
  editorHidden?: boolean
}) {
  const [view, setView] = React.useState<NativeCraftLeaseView>(INITIAL[pageId] ?? INITIAL["page-model"])
  const [requested, setRequested] = React.useState(false)
  const editing = view.mine && view.can_write
  React.useEffect(() => {
    onStatus?.({ ready: true, dirty: false, canEdit: view.can_edit === true, editing, holderIsMe: view.mine, openProposals: 0, recoveredDrafts: 0, version: VERSION[pageId],
      holder: view.holder ? { kind: view.holder.kind, name: view.holder.display_name } : null })
  }, [onStatus, view, editing, pageId])
  const act = (action: NativeCraftLeaseAction) => {
    window.__craftActions.push({ pageId, action })
    if (action === "request-handoff") setRequested(true)
    if (action === "acquire") setView(current => ({ ...current, holder: { actor_id: `user:${humanId}`, display_name: "Fixture Human", kind: "human" }, mine: true, can_write: true, generation: "1", acquired_at: new Date().toISOString() }))
    if (action === "release") setView(current => ({ ...current, holder: null, mine: false, can_write: false, generation: null, acquired_at: null }))
  }
  const controls = <NativeCraftLeaseBar variant={placeLease ? "popover" : "bar"} view={view} self={humanId} mayEdit={view.can_edit === true} requested={requested} onAction={act} />
  return <>
    {placeLease ? placeLease(controls) : controls}
    {!editorHidden && <div data-native-editor-standin className="m-4 grid min-h-[360px] flex-1 place-items-center rounded-card border border-dashed text-sm text-muted-foreground">
      Craft editor frame (not run in this component test)
    </div>}
  </>
}
