"use client"
import { agentVersionHref } from "@/lib/mooring/version-href"
import { useEffect, useState } from "react"
import { requestSheetUndo } from "@/lib/mooring/native-undo"
import { Lock } from "lucide-react"
import { SheetIcon } from "./sheet-icon"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { usePage } from "@/hooks/use-pages"
import { artifactsItemHref } from "../artifacts-href"
import { useArtifactViewer } from "../projects/artifact-viewer-context"
import { useSheetCardsWorkspace, useSheetPanel, useSheetPick } from "./sheet-panel-context"
import { publishAgentVersion } from "@/lib/mooring/native-agent-version"
import { SHEET_REFUSAL_TEXT, type SheetApplyActivity } from "./sheet-tool-result"
import type { NativeCraftHandBack } from "@/components/features/pages/native-craft-editor-host"

const CARD = "flex w-full min-w-0 max-w-[520px] flex-wrap items-center gap-2.5 rounded-xl border bg-card py-2 pl-2 pr-2.5"

/**
 * Chat card for a Sheet the agent changed. Name, kind and access come from the
 * server for this reader; the tool text only names the Sheet. Without access,
 * or once the Sheet is gone, it says only that it is not available.
 */
export function SheetChatCard({ activity, turnStreaming }: { activity: SheetApplyActivity; turnStreaming: boolean }) {
  const viewer = useArtifactViewer()
  const cards = useSheetCardsWorkspace()
  const workspaceId = cards ?? viewer?.workspaceId
  const reference = activity.slug ?? activity.pageId
  const { page, loading, notFound } = usePage(workspaceId, reference)
  const panel = useSheetPanel()
  const pick = useSheetPick()
  // Opens beside the chat the first time the agent changes this Sheet in the
  // live turn. A turn read back from history never opens anything.
  const shown = Boolean(page && (page.artifactKind === "sheet" || page.nativeCraftApplication === "gridcraft"))
  const live = turnStreaming && activity.state === "done" && shown
  // The open editor of this Sheet shows the agent's version (once per version; the host ignores any it already has).
  const savedVersion = activity.saved?.version
  useEffect(() => { if (page && shown && activity.state === "done" && activity.saved) publishAgentVersion({ pageId: page.id, ...activity.saved }) }, [page?.id, savedVersion]) // eslint-disable-line react-hooks/exhaustive-deps -- once per saved version
  useEffect(() => { if (live && page && panel) panel.noteChanged(page.id) }, [live, page?.id, panel?.noteChanged]) // eslint-disable-line react-hooks/exhaustive-deps -- once per Sheet becoming live
  if (!workspaceId) return null
  if (activity.state === "failed") {
    // The message did not let the agent change the Sheet: a plain line on how to allow it, never a second Sheet chip.
    if (activity.reason === "not_attached") return <p role="status" data-testid="sheet-refusal" className="max-w-[520px] px-1 text-xs text-muted-foreground">The agent wanted to change a Sheet, but this message did not let it. To allow it, turn on “Let the agent change it” for the Sheet on your next message.</p>
    // The person themselves holds the Sheet here: say so, and let them hand it back.
    if (activity.reason === "holder_human" && page && panel?.editingPageId === page.id && panel.handBack) return <HeldByYou handBack={panel.handBack} />
    // Say why only when the server gave a reason from its list; otherwise the step shows its own error.
    return activity.reason
      ? <p role="status" data-testid="sheet-refusal" className="max-w-[520px] rounded-xl border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">The agent could not change the Sheet. {SHEET_REFUSAL_TEXT[activity.reason]}</p>
      : null
  }
  if (activity.state === "running") {
    if (!turnStreaming) return null
    return <div className={CARD} aria-busy="true" aria-label="Changing the Sheet" data-testid="sheet-chat-card">
      <Skeleton className="size-8 rounded-[28%]" />
      <div className="flex min-w-0 flex-1 flex-col gap-1.5"><Skeleton className="h-4 w-40" /><Skeleton className="h-3 w-28" /></div>
    </div>
  }
  if (loading) {
    return <div className={CARD} aria-busy="true" aria-label="Loading Sheet" data-testid="sheet-chat-card">
      <Skeleton className="size-8 rounded-[28%]" />
      <div className="flex min-w-0 flex-1 flex-col gap-1.5"><Skeleton className="h-4 w-40" /><Skeleton className="h-3 w-28" /></div>
    </div>
  }
  if (!page || notFound || (page.artifactKind !== "sheet" && page.nativeCraftApplication !== "gridcraft")) {
    // No name: the reader may not learn what the item is called.
    return <div className={CARD} data-testid="sheet-chat-card-restricted">
      <span aria-hidden="true" className="flex size-8 shrink-0 items-center justify-center rounded-[28%] border bg-muted text-muted-foreground"><Lock className="size-4" /></span>
      <p className="min-w-0 flex-1 truncate text-sm text-muted-foreground">Not available</p>
    </div>
  }
  const title = page.name || page.slug || "Sheet"
  const version = activity.saved?.version
  if (activity.summary && version) return <SheetChangeCard workspaceId={workspaceId} pageId={page.id} title={title} version={version} activity={activity} />
  return <article aria-label={title} data-testid="sheet-chat-card" className={cn(CARD)}>
    <SheetIcon />
    <div className="grid min-w-0 flex-[1_1_140px] gap-0.5">
      <p className="truncate text-sm font-semibold">{title}</p>
      <p className="text-xs text-muted-foreground">Sheet · changed by the agent</p>
    </div>
    {panel
      ? <Button type="button" size="xs" variant="secondary" aria-label={`Open ${title}`} aria-pressed={panel.activePageId === page.id} onClick={() => panel.open(page.id)}>Open</Button>
      : <Button asChild size="xs" variant="secondary">
        <a href={artifactsItemHref(workspaceId, page.id)} aria-label={`Open ${title}`}>Open</a>
      </Button>}
    {!panel && pick && <Button type="button" size="xs" variant="outline" className="coarse:min-h-[44px]" aria-label={`Use ${title} with the next message`} aria-pressed={pick.chosenPageId === page.id} onClick={() => pick.choose(page.id)}>Use</Button>}
  </article>
}

const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`
type Undo = { at: "idle" } | { at: "working" } | { at: "done"; version: string; skipped: { sheet: string; cell: string }[]; left: number } | { at: "refused"; text: string }
const UNDO_REFUSED: Record<string, string> = {
  busy: "Someone is editing this Sheet now. Try Undo again when they are done.",
  forbidden: "You may not change this Sheet.",
  not_found: "This change can no longer be undone here.",
  not_undoable: "This change can no longer be undone here.",
  moved: "The Sheet changed meanwhile. Try Undo again.",
  fidelity_loss: "Undo would drop parts of this workbook (macros or signatures), so nothing was changed.",
}

const KEPT: Record<string, string> = {
  unsaved: "Your last change is not saved yet, so you still edit the Sheet.",
  busy: "The Sheet is busy saving, so you still edit it. Try again in a moment.",
  unconfirmed: "Your last save is not confirmed yet, so you still edit the Sheet.",
}
function HeldByYou({ handBack }: { handBack: () => Promise<NativeCraftHandBack | undefined> }) {
  const [state, setState] = useState<{ at: "idle" | "working" } | { at: "said"; text: string }>({ at: "idle" })
  const run = async () => {
    setState({ at: "working" })
    try {
      const result = await handBack()
      setState({ at: "said", text: !result || result.state === "returned" || result.state === "not_held" ? "Handed back. Ask the agent again." : KEPT[result.reason] ?? "You still edit the Sheet." })
    } catch { setState({ at: "said", text: "Editing could not be handed back. Try again." }) }
  }
  return <div role="status" data-testid="sheet-refusal" className="flex max-w-[520px] flex-wrap items-center gap-2 rounded-xl border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
    <span>{state.at === "said" ? state.text : "You are editing this Sheet, so the agent could not change it."}</span>
    {state.at !== "said" && <Button type="button" size="xs" variant="outline" disabled={state.at === "working"} onClick={() => void run()}>Hand back</Button>}
  </div>
}

/**
 * The agent's change under its reply: what changed, as the server counted it,
 * with a way to see it beside the chat and to undo it. Undo reverts the
 * agent's cells as a new version (nothing is deleted), only where nobody
 * changed them since; the cells it left are named, with the history.
 */
function SheetChangeCard({ workspaceId, pageId, title, version, activity }: { workspaceId: string; pageId: string; title: string; version: string; activity: SheetApplyActivity }) {
  const panel = useSheetPanel()
  const [undo, setUndo] = useState<Undo>({ at: "idle" })
  const summary = activity.summary!
  // Where, as the server counted it: up to three ranges, else the sheets.
  const where = summary.ranges.length > 0
    ? ` in ${summary.ranges.slice(0, 3).join(", ")}${summary.ranges.length > 3 ? ` and ${summary.ranges.length - 3} more` : ""}`
    : summary.sheets.length === 0 ? "" : ` on ${summary.sheets.length === 1 ? "sheet" : "sheets"} ${summary.sheets.join(", ")}`
  const history = artifactsItemHref(workspaceId, pageId)
  const run = async () => {
    if (!activity.operationId || undo.at === "working") return
    setUndo({ at: "working" })
    try {
      // The server's undo needs the open editor's session and lease: the editor host of this Sheet sends it.
      const answer = await requestSheetUndo(pageId, activity.operationId)
      if (answer === "no_editor" || answer === "not_editing") {
        if (answer === "no_editor") panel?.open(pageId)
        setUndo({ at: "refused", text: "Undo works while you edit this Sheet beside the chat. Take over editing there, then press Undo again." })
        return
      }
      const response = answer
      const body: unknown = await response.json().catch(() => null)
      const fields = body && typeof body === "object" ? body as Record<string, unknown> : {}
      const made = typeof fields.version === "number" && Number.isSafeInteger(fields.version) && fields.version > 0 ? String(fields.version) : typeof fields.version === "string" && /^[1-9][0-9]*$/.test(fields.version) ? fields.version : undefined
      // The server left the cells changed since the agent's version; it names them (sheet and address only).
      const skipped = Array.isArray(fields.skipped) ? fields.skipped.filter((c): c is { sheet: string; cell: string } => Boolean(c) && typeof c === "object" && typeof (c as { sheet?: unknown }).sheet === "string" && typeof (c as { cell?: unknown }).cell === "string" && /^\$?[A-Z]{1,3}\$?[1-9][0-9]{0,6}$/.test((c as { cell: string }).cell)) : []
      // The server gives the number of cells it left; a list names them.
      const left = typeof fields.skipped === "number" && Number.isSafeInteger(fields.skipped) && fields.skipped >= 0 ? fields.skipped : skipped.length
      if (response.ok && made) { setUndo({ at: "done", version: made, skipped, left }); publishAgentVersion({ pageId, version: made }); return }
      const code = typeof fields.code === "string" ? fields.code : ""
      setUndo({ at: "refused", text: UNDO_REFUSED[response.status === 403 ? "forbidden" : response.status === 404 ? "not_found" : code] ?? "Undo did not go through. Try again." })
    } catch { setUndo({ at: "refused", text: "Undo did not go through. Try again." }) }
  }
  const show = panel
    ? <Button type="button" size="xs" variant="outline" onClick={() => panel.open(pageId)}>Show in Sheet</Button>
    : <Button asChild size="xs" variant="outline"><a href={history}>Show in Sheet</a></Button>
  return <article aria-label={title} data-testid="sheet-chat-card" className={cn(CARD)}>
    <SheetIcon />
    <p className="min-w-0 flex-[1_1_180px] text-sm">Changed <b className="font-semibold">{title}</b> · version {version} · {plural(summary.cellsChanged, "cell", "cells")}{where}</p>
    {show}
    {activity.operationId && activity.undoable && (undo.at === "idle" || undo.at === "working" || undo.at === "refused") && <Button type="button" size="xs" variant="outline" disabled={undo.at === "working"} onClick={() => void run()}>Undo</Button>}
    {/* A change the server cannot undo says so, and where the version before it is. */}
    {activity.operationId && activity.undoable === false && <p className="basis-full text-xs text-muted-foreground">
      This change is too large to undo here. You can restore {activity.baseVersion ? `version ${activity.baseVersion}` : "the version before it"} from{" "}
      <a className="underline underline-offset-2" href={activity.baseVersion ? agentVersionHref(history, activity.baseVersion) : history}>version history</a>.
    </p>}
    <p role="status" className="basis-full text-xs text-muted-foreground empty:hidden">
      {undo.at === "done" ? (undo.left === 0 ? `Undone · version ${undo.version}`
          : <>{`Undone · version ${undo.version}. ${plural(undo.left, "cell", "cells")} ${undo.left === 1 ? "was left as it is because someone changed it" : "were left as they are because someone changed them"} since${undo.skipped.length ? `: ${undo.skipped.slice(0, 20).map(c => `'${c.sheet.replace(/'/g, "''")}'!${c.cell}`).join(", ")}${undo.skipped.length > 20 ? ", …" : ""}` : ""}.`} <a className="underline underline-offset-2" href={history} target="_blank" rel="noopener noreferrer">Open history</a></>)
        : undo.at === "refused" ? undo.text : ""}
    </p>
  </article>
}
