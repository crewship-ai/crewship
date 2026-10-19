"use client"

import { useEffect, useId, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { useHotkeys } from "react-hotkeys-hook"
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"
import {
  CHAT_SEARCH_PAGE, hitMessageHref, parseChatSearchQuery, runChatSearch, snippetSegments,
  type ChatSearchResult, type ChatSearchRoster, type ChatSearchScope,
} from "@/lib/chat-search"
import { chatSearchFilesFixtures } from "@/lib/feature-flags"
import { searchFilesFixtures } from "@/lib/chat-search-files"
import { CONVERSATION_SEARCH_DEBOUNCE_MS } from "@/lib/conversation-search"

const SCOPES: { id: ChatSearchScope; label: string }[] = [
  { id: "this", label: "This chat" },
  { id: "all", label: "All chats" },
  { id: "files", label: "Files" },
]

interface Props {
  workspaceId: string | undefined
  roster: ChatSearchRoster
  /** The open agent session, if any. Human rooms have none (not searchable yet). */
  current?: { agentId: string; sessionId: string } | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** Chat search (CH-25): This chat / All chats / Files with from:/in:/has:/after:/before: filters. */
export function ChatSearchDialog({ workspaceId, roster, current, open, onOpenChange }: Props) {
  const router = useRouter()
  const listId = useId()
  const [scope, setScope] = useState<ChatSearchScope>("all")
  const [raw, setRaw] = useState("")
  const [result, setResult] = useState<ChatSearchResult>({ status: "idle", hits: [] })
  const [loading, setLoading] = useState(false)
  const [active, setActive] = useState(0)
  const [retry, setRetry] = useState(0)
  const files = scope === "files" ? searchFilesFixtures(parseChatSearchQuery(raw).text) : null

  useEffect(() => { if (!open) { setRaw(""); setResult({ status: "idle", hits: [] }); setActive(0) } }, [open])

  useEffect(() => {
    if (!open || scope === "files" || !workspaceId) return
    const ctrl = new AbortController()
    setLoading(true)
    const timer = setTimeout(() => {
      void runChatSearch({ raw, scope, workspaceId, roster, current, signal: ctrl.signal }).then((r) => {
        if (ctrl.signal.aborted) return
        setResult(r); setActive(0); setLoading(false)
      })
    }, CONVERSATION_SEARCH_DEBOUNCE_MS)
    return () => { clearTimeout(timer); ctrl.abort() }
    // roster identity changes every render upstream; key on ids
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, scope, raw, workspaceId, current?.sessionId, current?.agentId, retry, roster.map((a) => a.id).join(",")])

  const parsed = parseChatSearchQuery(raw)
  const rows = result.hits.map((hit) => ({ hit, href: hitMessageHref(hit) })).filter((r): r is { hit: typeof r.hit; href: string } => r.href !== null)

  function openRow(index: number) {
    const row = rows[index]
    if (!row) return
    onOpenChange(false)
    router.push(row.href)
  }

  const filesOn = chatSearchFilesFixtures()
  const status = scope === "files" ? (filesOn && files ? files.status : "unavailable") : result.status

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="top-[10%] max-h-[80dvh] w-[calc(100vw-1rem)] translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-xl" aria-describedby={undefined}>
        <DialogTitle className="sr-only">Search chats</DialogTitle>
        <DialogDescription className="sr-only">Filters: from:, in:, has:link|code|tool, after:YYYY-MM-DD, before:YYYY-MM-DD</DialogDescription>
        <div role="tablist" aria-label="Search scope" className="flex gap-1 border-b px-2 pt-2 pr-10">
          {SCOPES.map((s) => (
            <button key={s.id} type="button" role="tab" aria-selected={scope === s.id} onClick={() => setScope(s.id)}
              className={cn("kit-tap min-h-8 rounded-t-md px-3 text-xs", scope === s.id ? "border-b-2 border-primary font-medium" : "text-muted-foreground hover:text-foreground")}>{s.label}</button>
          ))}
        </div>
        <div className="p-2">
          <Input
            autoFocus role="combobox" aria-expanded={rows.length > 0} aria-controls={listId} aria-label="Search query"
            placeholder={scope === "files" ? "Search files…" : "Search messages… from: in: has: after: before:"}
            value={raw} onChange={(e) => setRaw(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") { e.preventDefault(); setActive((i) => Math.min(i + 1, rows.length - 1)) }
              else if (e.key === "ArrowUp") { e.preventDefault(); setActive((i) => Math.max(i - 1, 0)) }
              else if (e.key === "Enter") { e.preventDefault(); openRow(active) }
            }}
          />
          {parsed.invalid.length > 0 && <p role="alert" className="mt-1 text-xs text-destructive">Unsupported filter: {parsed.invalid.join(", ")}</p>}
        </div>
        <div className="max-h-[55dvh] overflow-y-auto pb-2" aria-live="polite">
          {loading && scope !== "files" && <p role="status" className="px-3 py-2 text-xs text-muted-foreground">Searching…</p>}
          {(scope === "files" || !loading) && (status === "partial" || status === "unavailable" || status === "error") && (
            <p role={status === "error" ? "alert" : "status"} className="mx-2 mb-1 rounded-md border border-dashed px-2 py-1.5 text-xs text-muted-foreground">
              {scope === "files" ? (filesOn ? "Partial: binary files are not indexed and are not searched." : "File search is not available yet.") : result.note}
              {status === "error" && scope !== "files" && <button type="button" className="ml-2 underline" onClick={() => setRetry((n) => n + 1)}>Retry</button>}
            </p>
          )}
          {scope !== "files" && !loading && status === "idle" && <p className="px-3 py-2 text-xs text-muted-foreground">Type at least two characters. Use from:, in:, has:link, after:2026-10-01.</p>}
          {scope !== "files" && !loading && (status === "complete" || status === "partial") && rows.length === 0 && (
            <p className="px-3 py-2 text-xs text-muted-foreground">{status === "partial" ? "No matches among the results checked." : "No matches."}</p>
          )}
          {scope !== "files" && rows.length > 0 && (
            <ul id={listId} role="listbox" aria-label="Results">
              {rows.map(({ hit, href }, i) => (
                <li key={`${hit.session_id}:${hit.id}`} role="option" aria-selected={i === active}>
                  <a href={href} onClick={(e) => { e.preventDefault(); openRow(i) }} onMouseMove={() => setActive(i)}
                    className={cn("kit-tap block min-h-11 px-3 py-1.5 text-xs", i === active && "bg-accent")}>
                    <span className="flex items-center gap-2 text-[11px] text-muted-foreground">
                      <span className="truncate">{hit.role === "user" ? "You" : hit.agent_name || hit.agent_slug}</span>
                      <time dateTime={hit.ts}>{new Date(hit.ts).toLocaleDateString()}</time>
                    </span>
                    <span className="line-clamp-2 break-words">
                      {snippetSegments(hit.content || hit.tool_summary || "", parsed.text).map((seg, k) => seg.match ? <mark key={k} className="bg-primary/20 text-foreground">{seg.text}</mark> : <span key={k}>{seg.text}</span>)}
                    </span>
                  </a>
                </li>
              ))}
            </ul>
          )}
          {scope === "files" && filesOn && files && (
            files.items.length === 0 && files.status !== "idle" ? <p className="px-3 py-2 text-xs text-muted-foreground">No matching files among indexed content.</p> :
            <ul aria-label="File results">{files.items.map((f) => (
              <li key={f.id}><a href={f.href} className="kit-tap block min-h-11 px-3 py-1.5 text-xs"><span className="block truncate font-medium">{f.name}</span>{f.snippet && <span className="line-clamp-2 text-muted-foreground">{snippetSegments(f.snippet, parsed.text).map((s, k) => s.match ? <mark key={k}>{s.text}</mark> : <span key={k}>{s.text}</span>)}</span>}</a></li>
            ))}</ul>
          )}
          {rows.length >= CHAT_SEARCH_PAGE && <p className="px-3 py-1 text-[11px] text-muted-foreground">Showing the first {CHAT_SEARCH_PAGE} matches. Narrow the search to see others.</p>}
        </div>
      </DialogContent>
    </Dialog>
  )
}

/** Opens the dialog from ⌘⇧F (⌘K stays owned by the global palette). */
export function useChatSearchHotkey(onOpen: () => void) {
  const ref = useRef(onOpen)
  ref.current = onOpen
  useHotkeys("mod+shift+f", (e) => { e.preventDefault(); ref.current() }, { enableOnFormTags: true, enableOnContentEditable: true })
}
