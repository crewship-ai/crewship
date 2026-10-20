"use client"
import { X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useArtifactContextEnabled } from "../use-artifact-context-enabled"
import { targetLabel } from "../linked-viewer-targets"
import { BLOCK_TEXT, audienceText, pinnedUpdateWarning, sendBlock, updateTarget, type ComposerArtifactContext, type EditMode } from "./composer-context-model"

interface Props {
  context: ComposerArtifactContext
  onModeChange: (mode: EditMode) => void
  onUpdateTargetChange: (id: string) => void
  onDetach: (id: string) => void
}
const MODES: { id: EditMode; label: string }[] = [
  { id: "create", label: "Create new" },
  { id: "update", label: "Update attached" },
  { id: "reply_only", label: "Reply only" },
]

/** Context strip shown above the composer input. Pure view; the composer owns draft and send. */
export function ComposerArtifactContextStrip({ context, onModeChange, onUpdateTargetChange, onDetach }: Props) {
  const enabled = useArtifactContextEnabled()
  if (!enabled || (context.attachments.length === 0 && context.mode === null)) return null
  const block = sendBlock(context)
  const target = context.mode === "update" ? updateTarget(context) : null
  const audience = context.mode === "update" ? target?.audience ?? { kind: "unknown" as const } : context.createAudience
  const resultType = context.mode === "update" ? target?.resultType ?? null : context.mode === "create" ? context.createResultType : null
  const warning = pinnedUpdateWarning(context)
  // Direct edits are for the author's personal Document only; when every attached item sits in a shared folder, editing is not offered.
  const readable = context.attachments.filter(item => item.access === "readable")
  const sharedOnly = readable.length > 0 && readable.every(item => item.audience.kind === "folder")
  const editUnavailable = sharedOnly || !context.privateChat
  return <section aria-label="Artifact context" className="space-y-2 border-b p-2 text-sm">
    <ul className="flex flex-wrap gap-2">
      {context.attachments.map(item => <li key={item.id} className="flex items-center gap-1 rounded border px-2 py-1">
        {item.access === "lost" ? <span className="text-muted-foreground">Unavailable item</span>
          : <><span className="truncate">{item.title}</span><span className="text-xs text-muted-foreground">{targetLabel(item.target)}</span></>}
        <Button type="button" size="icon" variant="ghost" className="size-6 coarse:min-h-12 coarse:min-w-12" aria-label={item.access === "lost" ? "Remove unavailable item" : `Remove ${item.title}`} onClick={() => onDetach(item.id)}><X className="size-3" aria-hidden /></Button>
      </li>)}
    </ul>
    <fieldset className="flex flex-wrap gap-3">
      <legend className="sr-only">What should the agent do</legend>
      {MODES.map(mode => <label key={mode.id} className="flex items-center gap-1 coarse:min-h-12">
        <input type="radio" name="artifact-edit-mode" checked={context.mode === mode.id} disabled={mode.id === "update" && editUnavailable} aria-describedby={mode.id === "update" && editUnavailable ? "artifact-edit-unavailable" : undefined} onChange={() => onModeChange(mode.id)} />{mode.label}
      </label>)}
    </fieldset>
    {editUnavailable && <p id="artifact-edit-unavailable" className="text-xs text-muted-foreground">{!context.privateChat ? "Editing an item needs a private chat, and this conversation is not one." : "Editing an item in a shared folder needs a proposal, which is not available yet."}</p>}
    {context.mode === "update" && context.attachments.length > 1 && <label className="flex items-center gap-2 text-xs">Item to update
      <select className="rounded border bg-background p-1 coarse:min-h-12" value={context.updateTargetId ?? ""} onChange={event => { if (event.target.value) onUpdateTargetChange(event.target.value) }}>
        {!target && <option value="" disabled>Select an item</option>}
        {context.attachments.filter(item => item.access === "readable").map(item => <option key={item.id} value={item.id}>{item.title} · {targetLabel(item.target)}</option>)}
      </select></label>}
    {context.mode && <dl className="grid grid-cols-[auto_1fr] gap-x-3 text-xs text-muted-foreground">
      <dt>Executor</dt><dd>{context.mode === "reply_only" ? "Reply in this chat" : context.executor ?? "Not chosen"}</dd>
      {resultType && <><dt>Result type</dt><dd>{resultType}</dd></>}
      <dt>Result</dt><dd>{audienceText(audience, context.mode)}</dd>
    </dl>}
    {warning && <p className="text-xs">{warning}</p>}
    {block && <p role="alert" className="text-xs text-destructive">{BLOCK_TEXT[block]}</p>}
  </section>
}
