"use client"
import { X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { targetLabel } from "../linked-viewer-targets"
import { BLOCK_TEXT, audienceText, sendBlock, type ComposerArtifactContext, type EditMode } from "./composer-context-model"

interface Props {
  context: ComposerArtifactContext
  onModeChange: (mode: EditMode) => void
  onUpdateTargetChange: (index: number) => void
  onDetach: (index: number) => void
}
const MODES: { id: EditMode; label: string }[] = [
  { id: "create", label: "Create new" },
  { id: "update", label: "Update attached" },
  { id: "reply_only", label: "Reply only" },
]

/** Context strip shown above the composer input. Pure view; the composer owns draft and send. */
export function ComposerArtifactContextStrip({ context, onModeChange, onUpdateTargetChange, onDetach }: Props) {
  const block = sendBlock(context)
  if (context.attachments.length === 0 && context.mode === null) return null
  return <section aria-label="Artifact context" className="space-y-2 border-b p-2 text-sm">
    <ul className="flex flex-wrap gap-2">
      {context.attachments.map((item, index) => <li key={`${item.target.pageId}-${index}`} className="flex items-center gap-1 rounded border px-2 py-1">
        <span className="truncate">{item.title}</span><span className="text-xs text-muted-foreground">{targetLabel(item.target)}</span>
        <Button type="button" size="icon" variant="ghost" className="size-6 coarse:min-h-12 coarse:min-w-12" aria-label={`Remove ${item.title}`} onClick={() => onDetach(index)}><X className="size-3" aria-hidden /></Button>
      </li>)}
    </ul>
    <div role="radiogroup" aria-label="What should the agent do" className="flex flex-wrap gap-2">
      {MODES.map(mode => <Button key={mode.id} type="button" size="sm" role="radio" aria-checked={context.mode === mode.id} variant={context.mode === mode.id ? "default" : "outline"} className="coarse:min-h-12" onClick={() => onModeChange(mode.id)}>{mode.label}</Button>)}
    </div>
    {context.mode === "update" && context.attachments.length > 1 && <label className="flex items-center gap-2 text-xs">Item to update
      <select className="rounded border bg-background p-1 coarse:min-h-12" value={context.updateTarget ?? ""} onChange={event => onUpdateTargetChange(Number(event.target.value))}>
        {context.updateTarget === null && <option value="" disabled>Select an item</option>}
        {context.attachments.map((item, index) => <option key={index} value={index}>{item.title} · {targetLabel(item.target)}</option>)}
      </select></label>}
    {context.mode && <dl className="grid grid-cols-[auto_1fr] gap-x-3 text-xs text-muted-foreground">
      <dt>Executor</dt><dd>{context.mode === "reply_only" ? "Reply in this chat" : context.executor ?? "Not chosen"}</dd>
      <dt>Result</dt><dd>{audienceText(context.audience, context.mode)}</dd>
    </dl>}
    {block && <p role="alert" className="text-xs text-destructive">{BLOCK_TEXT[block]}</p>}
  </section>
}
