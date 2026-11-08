"use client"

import dynamic from "next/dynamic"
import * as React from "react"
import { UserCog } from "lucide-react"
import type { DesignEditorFrameState } from "@/components/features/artifacts-design/design-editor"
import { readablePeerFill } from "@/components/features/artifacts-design/peer-colors"
import type { FrameAction, FrameStatus } from "../../frame"
import type { ResolvedKind } from "../../kinds"
import { designFrameStatus } from "./design-status"

const DesignEditor = dynamic(() => import("@/components/features/artifacts-design/design-editor").then(module => module.DesignEditor), { ssr: false })
const DesignAccessDialog = dynamic(() => import("@/components/features/artifacts-design/design-access-dialog").then(module => module.DesignAccessDialog), { ssr: false })

/** What a kind body tells the frame around it; the detail merges it into its capabilities. */
export interface KindFrameReport {
  canEdit: boolean
  status: FrameStatus | null
  presence?: React.ReactNode
  actions?: FrameAction[]
  /** What the body found the artifact to be, once it read it (a project that is a design system is a Design library). */
  kind?: ResolvedKind
}

/** Real people on the canvas, from the socket's awareness; nothing when nobody else is here. */
export function DesignPresence({ peers }: { peers: DesignEditorFrameState["peers"] }) {
  if (!peers.length) return null
  return <div className="flex -space-x-1" aria-label="People on this canvas">
    {peers.slice(0, 4).map(peer => <span key={peer.clientId} title={peer.name}
      className="flex size-6 items-center justify-center rounded-full border-2 border-background text-xs font-medium text-white"
      style={{ backgroundColor: readablePeerFill(peer.color) }}>{peer.name.slice(0, 2).toUpperCase()}</span>)}
    {peers.length > 4 && <span className="flex size-6 items-center justify-center rounded-full border-2 border-background bg-muted text-xs">+{peers.length - 4}</span>}
  </div>
}

/**
 * A Design inside the ArtifactFrame (Harbor 2 v87 `design`): the live Yjs
 * canvas with its controls in the frame's slots and drawer. The chip, presence
 * and the "Design access" action go up to the frame through `onFrame`.
 */
export function DesignBody({ workspaceId, slug, onFrame }: { workspaceId: string; slug: string; onFrame: (report: KindFrameReport) => void }) {
  const [state, setState] = React.useState<DesignEditorFrameState | null>(null)
  const [accessOpen, setAccessOpen] = React.useState(false)
  const status = state ? designFrameStatus(state) : null
  const canEdit = state?.canEdit === true
  const key = JSON.stringify([status, canEdit, state?.peers ?? []])
  const report = React.useRef(onFrame); report.current = onFrame
  React.useEffect(() => {
    report.current({
      canEdit, status,
      presence: state?.peers.length ? <DesignPresence peers={state.peers} /> : undefined,
      // Only an owner or administrator changes grants; the dialog itself asks the server and says so otherwise.
      actions: state?.ready ? [{ id: "design-access", label: "Design access…", icon: UserCog, onSelect: () => setAccessOpen(true) }] : [],
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the key holds everything reported
  }, [key])
  return <div className="flex min-h-0 flex-1 flex-col">
    <DesignEditor workspaceId={workspaceId} slug={slug} onFrameState={setState} />
    {accessOpen && <DesignAccessDialog workspaceId={workspaceId} slug={slug} onClose={() => setAccessOpen(false)} />}
  </div>
}
