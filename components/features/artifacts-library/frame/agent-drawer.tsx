"use client"

import * as React from "react"
import { MessageSquare, Send, ShieldCheck } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Segmented } from "@/components/ui/segmented"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"
import type { AgentMode, AgentSupport } from "./types"

const MODE_TEXT: Record<AgentMode, { label: string; hint: string }> = {
  ask: { label: "Ask", hint: "Answers in chat. Nothing changes." },
  propose: { label: "Propose", hint: "Changes wait for your review. The file stays as it is." },
  edit: { label: "Edit", hint: "The agent works directly. Every change is a new version you can undo." },
}

/** Which modes this instance offers: a mode the backend lacks is left out; one that needs edit access stays, disabled. */
export function agentModes(support: AgentSupport, registered: { propose: boolean; edit: boolean }): AgentMode[] {
  const modes: AgentMode[] = []
  if (support.ask) modes.push("ask")
  if (support.propose || registered.propose) modes.push("propose")
  if (support.edit || registered.edit) modes.push("edit")
  return modes
}

/**
 * One agent model for every kind (Harbor 2 v87 `agentDrawer`): Ask changes
 * nothing, Propose waits for review, Edit changes directly. Each mode sends
 * through an existing backend path only: Ask opens the existing chat with the
 * artifact as context; Propose and Edit are the kind's own path (a send, or a
 * form it mounts with <ArtifactAgentMode>).
 */
export function AgentDrawer({ support, canEdit, kindNoun, registered, modeTarget }: {
  support: AgentSupport
  canEdit: boolean
  /** The kind in a sentence, lower case (kinds.ts `noun`): "document", "sheet". */
  kindNoun: string
  registered: { propose: boolean; edit: boolean }
  /** Where a registered mode's form renders. */
  modeTarget: (mode: "propose" | "edit") => (node: HTMLDivElement | null) => void
}) {
  const modes = agentModes(support, registered)
  const permitted = (mode: AgentMode) => mode === "ask" || canEdit
  const first = modes.find(permitted) ?? modes[0]
  const [chosen, setMode] = React.useState<AgentMode | undefined>(first)
  // A mode whose backend withdrew while open falls back to the first one left.
  const mode = chosen && modes.includes(chosen) && permitted(chosen) ? chosen : first
  const scopes = support.scopes ?? [{ id: "all", label: `Whole ${kindNoun}` }]
  const [scope, setScope] = React.useState(scopes[0].id)
  const [prompt, setPrompt] = React.useState("")
  const [sending, setSending] = React.useState(false)
  const [result, setResult] = React.useState<string>()
  if (!mode) return null
  const send = mode === "propose" ? support.propose : mode === "edit" ? support.edit : undefined
  const form = mode !== "ask" && !send
  const submit = async () => {
    if (!send || !prompt.trim()) return
    setSending(true); setResult(undefined)
    try {
      const ok = await send.send(prompt.trim(), scope)
      setResult(ok ? "Sent to the agent." : "Not sent. Nothing changed; try again.")
      if (ok) setPrompt("")
    } catch {
      setResult("Not sent. Nothing changed; try again.")
    } finally { setSending(false) }
  }
  return <div className="grid content-start gap-3 p-3.5" data-agent-mode={mode}>
    {modes.length > 1 && <Segmented<AgentMode> ariaLabel="Agent mode" fill value={mode} onChange={setMode}
      options={modes.map(value => ({ value, label: MODE_TEXT[value].label, disabled: !permitted(value), title: permitted(value) ? MODE_TEXT[value].hint : "Needs edit access" }))} />}
    <p className="text-xs text-muted-foreground">{MODE_TEXT[mode].hint}</p>
    {!form && <>
      {scopes.length > 1 && <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Scope">
        <span className="mr-1 text-xs font-medium text-muted-foreground">Scope</span>
        {scopes.map(entry => <button key={entry.id} type="button" aria-pressed={scope === entry.id} onClick={() => setScope(entry.id)}
          className={cn("rounded-full border px-2.5 py-0.5 text-xs transition-colors coarse:min-h-[44px]", scope === entry.id ? "border-primary/40 bg-primary/12 text-primary-hover" : "hover:bg-accent")}>{entry.label}</button>)}
      </div>}
      <Textarea aria-label="What should the agent do?" placeholder={mode === "ask" ? `Ask about this ${kindNoun}…` : "What should the agent change?"}
        value={prompt} onChange={event => setPrompt(event.target.value)} className="min-h-20 text-sm" />
      {support.suggestions && support.suggestions.length > 0 && <div className="flex flex-wrap gap-1.5" aria-label="Suggestions">
        {support.suggestions.map(text => <button key={text} type="button" onClick={() => setPrompt(text)}
          className="rounded-full border px-2.5 py-0.5 text-xs text-muted-foreground hover:bg-accent hover:text-foreground coarse:min-h-[44px]">{text}</button>)}
      </div>}
      <div className="flex justify-end">
        {mode === "ask" && support.ask
          ? <Button asChild size="sm"><a href={support.ask.chatHref(prompt.trim())}><MessageSquare aria-hidden />Ask in chat</a></Button>
          : <Button type="button" size="sm" disabled={!prompt.trim() || sending} onClick={() => void submit()}>
            <Send aria-hidden />{mode === "propose" ? "Ask for a proposal" : "Start editing"}
          </Button>}
      </div>
      {result && <p role="status" className="text-xs text-muted-foreground">{result}</p>}
    </>}
    {/* A kind's own form for this mode (its existing backend path). */}
    <div ref={modeTarget("propose")} hidden={mode !== "propose" || !registered.propose} data-agent-form="propose" />
    <div ref={modeTarget("edit")} hidden={mode !== "edit" || !registered.edit} data-agent-form="edit" />
    {support.permission && <p className="flex items-start gap-2 rounded-lg bg-muted/60 px-2.5 py-2 text-xs text-muted-foreground">
      <ShieldCheck aria-hidden className="mt-px size-3.5 shrink-0" /><span>{support.permission}</span>
    </p>}
  </div>
}
