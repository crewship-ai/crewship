"use client"
import { useEffect, useRef, useState } from "react"
import { useSlidesSelected } from "@/hooks/use-slides-selected"
import { captureSlideTextRange, type SlideTextRange, type SelectedTextRange, type SelectedSummary } from "@/lib/slides/selected"
import type { SlidesState } from "@/lib/slides/mutations"

const control = "min-h-9 coarse:min-h-12 rounded border px-3 disabled:opacity-50"
export function SlideSelectedWork({ workspaceId, slug, pageId, saved, range, ready, canViewResult, onDirtyChange, onViewResult }: { workspaceId: string; slug: string; pageId: string; saved: SlidesState; range?: SlideTextRange; ready: boolean; canViewResult?: boolean; onDirtyChange?: (dirty: boolean) => void; onViewResult: (request: SelectedSummary) => Promise<void> }) {
 const work = useSlidesSelected(workspaceId, slug, pageId)
 const [chat, setChat] = useState(""), [instruction, setInstruction] = useState("")
 const [captured, setCaptured] = useState<{ base_rev: number; page_id: string; selection: SelectedTextRange }>(), [captureBusy, setCaptureBusy] = useState(false), [error, setError] = useState<string>()
 const [history, setHistory] = useState<SelectedSummary>()
 useEffect(() => { if (!work.page) setHistory(undefined) }, [work.page])
 const dirty = !!instruction || !!captured || !!work.pending
 useEffect(() => { onDirtyChange?.(dirty); return () => onDirtyChange?.(false) }, [dirty, onDirtyChange])
 const refresh = useRef(work.refresh); refresh.current = work.refresh
 useEffect(() => { const timer = setInterval(() => void refresh.current(), 5000); return () => clearInterval(timer) }, [])
 const captureCurrent = useRef({ saved, ready, range }); captureCurrent.current = { saved, ready, range }
 const stale = !!captured && (captured.page_id !== pageId || captured.base_rev !== saved.revision)
 const locked = work.busy || captureBusy || !!work.pending
 async function capture() {
  if (!range || !ready || locked) { setError("Select text in the saved text field first."); return }
  setCaptureBusy(true); setError(undefined)
  try { const selected = await captureSlideTextRange(saved, range, ready); const current = captureCurrent.current; if (!current.ready || current.saved !== saved || current.range !== range) throw new Error("The saved selection changed while being captured. Select the saved text again."); setCaptured(selected) }
  catch (cause) { setError(cause instanceof Error ? cause.message : "Could not capture saved text.") }
  finally { setCaptureBusy(false) }
 }
 async function submit() {
  if (!captured || !ready || locked || stale || !chat || !instruction.trim() || new TextEncoder().encode(instruction).length > 4096) return
  const sent = await work.send({ kind: "admit", path: "", body: { ...captured, type_id: "slides", schema_version: 1, submission_id: crypto.randomUUID(), chat_id: chat, instruction } })
  if (sent) { setCaptured(undefined); setInstruction("") }
 }
 return <section aria-label="Slides selected agent work" className="space-y-3 rounded border p-3">
  <h3 className="font-medium">Agent work on selected saved text</h3>
  <p role="status">{work.status}</p>
  {(error || work.error) && <p role="alert">{error || work.error}</p>}
  {!ready && <p>Save or discard pending Slides, image and comment changes before starting agent work or viewing its result.</p>}
  <button className={control} disabled={work.busy} onClick={() => void work.refresh()}>Refresh agent requests</button>
  {work.page?.can_dispatch && <form className="space-y-2" onSubmit={event => { event.preventDefault(); void submit() }}>
   <p>Select a nonempty range in the Text field. Only that range in confirmed revision {saved.revision} will be authorized.</p>
   <button type="button" className={control} disabled={!ready || locked || !range || !!captured} onClick={() => void capture()}>Use selected saved text</button>
   {captured && <blockquote aria-label="Selected saved text" className="whitespace-pre-wrap break-words border-l-2 pl-2">Revision {captured.base_rev}: {captured.selection.quote}</blockquote>}
   <label className="block">Private agent chat<select aria-label="Slides private agent chat" className={control} value={chat} disabled={locked} onChange={event => setChat(event.target.value)}><option value="">Choose a private chat</option>{work.page.eligible_chats.map(target => <option key={target.chat_id} value={target.chat_id}>{target.agent_name} · {target.title || "Private chat"}</option>)}</select></label>
   <label className="block">Agent instruction<textarea aria-label="Slides agent instruction" className="min-h-24 w-full rounded border p-2" value={instruction} disabled={locked} onChange={event => setInstruction(event.target.value)} /></label>
   {stale && <p role="alert">The confirmed revision changed. The original selection is retained; discard it explicitly before selecting again.</p>}
   <button type="submit" className={control} disabled={!ready || locked || stale || !captured || !chat || !instruction.trim() || new TextEncoder().encode(instruction).length > 4096}>Send selected text to agent</button>
   <button type="button" className={control} disabled={work.busy || !!work.pending} onClick={() => { if (!dirty || window.confirm("Discard the unsent selected-text draft?")) { setCaptured(undefined); setInstruction("") } }}>Discard selected-text draft</button>
  </form>}
  {work.pending && <div className="space-y-2"><p>The original {work.pending.kind === "admit" ? "submission and selected revision" : "cancellation receipt"} are retained.</p><button className={control} disabled={work.busy || work.blocked || !ready} onClick={() => void work.send().then(ok => { if (ok && work.pending?.kind === "admit") { setCaptured(undefined); setInstruction("") } })}>Retry original agent request</button><button className={control} disabled={work.busy} onClick={() => { if (window.confirm("Discard the original attempt and review current requests? An uncertain request may already exist. The text draft stays retained.")) void work.discard() }}>Discard original agent attempt and review</button></div>}
  {work.items.map(request => <article key={request.id} aria-label={`Agent request ${request.id}`} className="space-y-2 rounded border p-3"><p>{request.status} · original revision {request.base_revision} · version {request.version}</p><p className="whitespace-pre-wrap break-words">{request.instruction}</p>{request.result_revision && <button className={control} disabled={!(canViewResult ?? ready) || dirty || work.busy} onClick={() => void onViewResult(request).catch(cause => setError(cause instanceof Error ? cause.message : "Could not load result."))}>View confirmed result revision {request.result_revision}</button>}{request.error && <p role="alert">{request.error}</p>}{request.restored && <p>Restored history is inert. A new request requires a new saved selection.</p>}<button className={control} disabled={work.busy} onClick={() => setHistory(request)}>View original selection</button>{(request.status === "queued" || request.status === "running") && <button className={control} disabled={work.busy || !!work.pending || !work.page?.can_cancel} onClick={() => void work.send({ kind: "cancel", path: `/${encodeURIComponent(request.id)}/cancel`, requestId: request.id, body: { page_id: pageId, schema_version: 1, mutation_id: crypto.randomUUID(), if_version: request.version } })}>Cancel request version {request.version}</button>}</article>)}
  {work.page?.has_more && <button className={control} disabled={work.busy} onClick={() => void work.refresh(work.page?.next_cursor)}>Load more agent requests</button>}
  {history && work.page && <aside aria-label="Original agent selection" className="space-y-2"><p>Original revision {history.base_revision} · {history.selection.slide_id}/{history.selection.element_id} · UTF16 {history.selection.from}–{history.selection.to}</p><blockquote className="whitespace-pre-wrap break-words">{history.selection.quote}</blockquote><button className={control} onClick={() => setHistory(undefined)}>Close original selection</button></aside>}
 </section>
}
