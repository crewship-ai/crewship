import type { CSSProperties } from "react"
import { KIND_ACCENT } from "@/components/features/chat/artifact-context/new-artifact/artifact-kinds"
import { cn } from "@/lib/utils"
import type { LibraryKind } from "./library-items"

/*
 * A drawn thumbnail per kind, tinted with the kind's colour: Word pages, a
 * Sheet grid, a slide, dashboard bars, project screens, page panels. It says
 * what the item is before its name is read, and needs no render of the
 * content: nothing private is fetched or drawn. Real previews can replace it
 * per kind once the API serves them.
 */

const line = (width = "100%", strong = false) =>
  <i className={strong ? "block h-1.5 rounded-full bg-[var(--kind)]/45" : "block h-1 rounded-full bg-foreground/12"} style={{ width }} />

function Paper({ className, children }: { className: string; children: React.ReactNode }) {
  return <div className={`absolute rounded-t-md border border-border bg-card shadow-xs ${className}`}>{children}</div>
}

const BARS = [[40, 70, 55, 90], [30, 45, 80, 60], [50, 65, 40, 75, 85, 60]]

function Drawing({ kind }: { kind: LibraryKind }) {
  switch (kind) {
    case "word":
      return <Paper className="inset-x-[22%] bottom-0 top-3.5 grid content-start gap-1.5 p-3">
        {line("60%", true)}{line()}{line("92%")}{line("80%")}{line()}{line("70%")}
      </Paper>
    case "sheet":
      return <Paper className="inset-x-3.5 bottom-0 top-3.5 grid grid-cols-5 auto-rows-[14px] overflow-hidden">
        {Array.from({ length: 30 }, (_, i) => <i key={i} className={`border-b border-r border-border ${i < 5 || i % 5 === 0 ? "bg-[var(--kind)]/18" : ""}`} />)}
      </Paper>
    case "slides":
      return <Paper className="inset-x-[16%] bottom-3 top-3 grid content-center gap-1.5 rounded-b-md px-4">
        {line("55%", true)}{line("70%")}{line("50%")}
      </Paper>
    case "dashboard":
      return <div className="absolute inset-x-3.5 bottom-0 top-3.5 grid grid-cols-3 gap-2">
        {BARS.map((bars, i) => <div key={i} className="flex items-end gap-0.5 rounded-t-md border border-border bg-card px-1.5 pt-2">
          {bars.map((h, j) => <i key={j} className="block flex-1 rounded-t-sm bg-[var(--kind)]/55" style={{ height: `${h}%` }} />)}
        </div>)}
      </div>
    case "project":
      return <div className="absolute inset-x-3.5 bottom-0 top-3.5 grid grid-cols-3 gap-2">
        {[0, 1, 2].map(i => <div key={i} className="grid content-start gap-1.5 rounded-t-md border border-border bg-card p-2">
          {line("70%", true)}{line()}{line("85%")}
        </div>)}
      </div>
    default:
      return <div className="absolute inset-x-3.5 bottom-0 top-3.5 grid grid-cols-2 gap-2">
        {[0, 1, 2, 3].map(i => <div key={i} className="grid content-start gap-1.5 rounded-t-md border border-border bg-card p-2">
          {i < 2 ? <><i className="block h-1 w-1/2 rounded-full bg-foreground/12" /><b className="block h-2 w-1/4 rounded-sm bg-[var(--kind)]/45" /></> : line("80%")}
        </div>)}
      </div>
  }
}

export function KindThumb({ kind, empty, className }: { kind: LibraryKind; empty?: boolean; className?: string }) {
  const style = { "--kind": KIND_ACCENT[kind].tint } as CSSProperties
  return <div aria-hidden="true" data-thumb={kind} style={style}
    className={cn("relative h-[104px] overflow-hidden border-b border-border bg-[color-mix(in_srgb,var(--kind)_8%,var(--muted))]", className)}>
    {empty ? <span className="absolute inset-0 grid place-items-center text-xs text-muted-foreground">Nothing here yet</span> : <Drawing kind={kind} />}
  </div>
}
