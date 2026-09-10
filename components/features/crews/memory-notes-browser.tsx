"use client"

import { useState, type ReactNode } from "react"
import { BookOpen, CalendarDays, FileText, Pin, Search } from "lucide-react"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

export interface MemoryDocument {
  id: string; name: string; scope: string; content?: string; state: string
  bytes: number | null; revision?: string; updated_at?: string; history_path?: string
}

export function memoryNoteLabel(note: MemoryDocument) {
  const date = /^daily\/(\d{4}-\d{2}-\d{2})\.md$/.exec(note.name)?.[1]
  if (date) return new Date(`${date}T12:00:00Z`).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" })
  const labels: Record<string, string> = { "AGENT.md": "Agent knowledge", "CREW.md": "Team knowledge", "pins.md": "Pinned notes", "BRIEF.md": "Brief", "LEAD.md": "Team coordination", "lessons.md": "Lessons learned" }
  return labels[note.name] ?? note.content?.match(/^#\s+(.+)$/m)?.[1] ?? note.name.replace(/\.md$/, "").replaceAll("-", " ")
}

function group(note: MemoryDocument) { return note.name === "pins.md" ? "Pinned" : note.name.startsWith("daily/") ? "Daily journal" : "Knowledge" }

export function MemoryNotesBrowser({ documents, children }: { documents: MemoryDocument[]; children: (note: MemoryDocument | undefined) => ReactNode }) {
  const [query, setQuery] = useState("")
  const [selected, setSelected] = useState<string | null>(null)
  if (!documents.length) return children(undefined)
  const needle = query.trim().toLocaleLowerCase()
  const filtered = documents.filter(note => `${memoryNoteLabel(note)} ${note.name} ${note.content ?? ""}`.toLocaleLowerCase().includes(needle))
  const ordered = ["Pinned", "Knowledge", "Daily journal"].flatMap(label => filtered.filter(note => group(note) === label).sort((a, b) => label === "Daily journal" ? b.name.localeCompare(a.name) : a.name.localeCompare(b.name)))
  const current = ordered.find(note => note.id === selected) ?? ordered.find(note => note.name === "AGENT.md" || note.name === "CREW.md") ?? ordered[0]
  return <div className="grid items-start gap-4 md:grid-cols-[16rem_minmax(0,1fr)]">
    <nav aria-label="Saved notes" className="min-w-0 overflow-hidden rounded-2xl border border-border bg-card md:sticky md:top-4">
      <div className="border-b border-border p-3"><div className="mb-3 flex items-center justify-between px-1"><h3 className="text-sm font-medium">Notes</h3><span className="text-xs text-muted-foreground">{query ? `${filtered.length} / ` : ""}{documents.length}</span></div><div className="relative"><Search aria-hidden="true" className="absolute left-3 top-2.5 size-4 text-muted-foreground" /><Input aria-label="Search notes" placeholder="Search notes…" value={query} onChange={e => setQuery(e.target.value)} className="pl-9" /></div></div>
      <div className="max-h-64 overflow-y-auto p-2 md:max-h-[65dvh]">
        {["Pinned", "Knowledge", "Daily journal"].map(label => {
          const notes = ordered.filter(note => group(note) === label)
          const Icon = label === "Pinned" ? Pin : label === "Daily journal" ? CalendarDays : BookOpen
          return notes.length ? <div key={label} className="mb-3 last:mb-0"><h4 className="flex items-center gap-2 px-2 py-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground"><Icon className="size-3.5" aria-hidden="true" />{label}<span className="ml-auto">{notes.length}</span></h4>{notes.map(note => <button key={note.id} aria-current={current?.id === note.id ? "true" : undefined} onClick={() => setSelected(note.id)} className={cn("flex w-full items-start gap-2 rounded-lg px-3 py-2.5 text-left text-sm transition-colors hover:bg-muted/50 focus-visible:outline-2 focus-visible:outline-primary", current?.id === note.id && "bg-primary/10 text-primary")}><FileText aria-hidden="true" className="mt-0.5 size-4 shrink-0 opacity-60" /><span className="min-w-0"><span className="block break-words font-medium">{memoryNoteLabel(note)}</span><span className="mt-0.5 block truncate text-xs text-muted-foreground" title={note.name}>{note.name}</span></span></button>)}</div> : null
        })}
        {!ordered.length && <p className="p-3 text-sm text-muted-foreground">No matching notes.</p>}
      </div>
    </nav>
    <div className="min-w-0" key={current?.id ?? "no-results"}>{current ? children(current) : <div className="rounded-2xl border border-border bg-card p-8 text-center"><Search className="mx-auto mb-3 size-6 text-muted-foreground" /><h3 className="font-medium">No matching notes</h3><p className="mt-2 text-sm text-muted-foreground">Search by title, date or a phrase from the note.</p><button className="mt-4 text-sm text-primary" onClick={() => setQuery("")}>Clear search</button></div>}</div>
  </div>
}
