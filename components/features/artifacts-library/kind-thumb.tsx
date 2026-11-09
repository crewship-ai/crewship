import type { CSSProperties } from "react"
import { cn } from "@/lib/utils"
import { kindAccent, KINDS, type ArtifactKind } from "./kinds"

/*
 * A drawn thumbnail per kind, tinted with the kind's registry colour: a Docs
 * page, dashboard bars, a slide, design artboards, an app window, a Word page,
 * a Sheet grid. It says what the item is before its name is read, and needs no
 * render of the content: nothing private is fetched or drawn, and no author
 * code runs for it. Because the drawing could pass for a picture of the
 * content (dashboard bars, a slide), it carries a visible "<Kind> · no preview"
 * label (#5154). A safe thumbnail of one exact version, rendered outside the
 * host origin and checked against the reader's access, can replace it per
 * kind once the API serves one.
 */

const line = (width = "100%", strong = false) =>
  <i className={strong ? "block h-1.5 rounded-full bg-[var(--kind)]/45" : "block h-1 rounded-full bg-foreground/12"} style={{ width }} />

function Paper({ className, children }: { className: string; children: React.ReactNode }) {
  return <div className={`absolute rounded-t-md border border-border bg-card shadow-xs ${className}`}>{children}</div>
}

const BARS = [38, 52, 72, 46]

function Drawing({ kind, mode }: { kind: ArtifactKind; mode?: string }) {
  switch (kind) {
    case "word":
      return <Paper className="inset-x-[22%] bottom-0 top-3.5 grid content-start gap-1.5 p-3">
        {line("60%", true)}{line()}{line("92%")}{line("80%")}{line()}{line("70%")}
      </Paper>
    case "sheet":
      return <Paper className="inset-x-3.5 bottom-0 top-3.5 grid auto-rows-[14px] grid-cols-5 overflow-hidden">
        {Array.from({ length: 30 }, (_, i) => <i key={i} className={`border-b border-r border-border ${i < 5 || i % 5 === 0 ? "bg-[var(--kind)]/18" : ""}`} />)}
      </Paper>
    case "slides":
      return <div className="absolute inset-x-[18%] bottom-3 top-3 grid content-center gap-2 rounded-md bg-[var(--kind)]/14 px-5">
        <b className="block h-2.5 w-2/3 rounded-sm bg-[var(--kind)]/50" /><b className="block h-2 w-1/3 rounded-sm bg-[var(--kind)]/35" />
        <b className="ml-auto block h-6 w-1/4 rounded-sm bg-[var(--kind)]/45" />
      </div>
    case "dashboard":
      return <div className="absolute inset-x-[22%] bottom-0 top-5 flex items-end gap-2.5">
        {BARS.map((height, i) => <i key={i} className={`block flex-1 rounded-t-sm ${i === 2 ? "bg-[var(--kind)]/70" : "bg-[var(--kind)]/35"}`} style={{ height: `${height + 20}%` }} />)}
      </div>
    case "design":
      return mode === "library"
        ? <div className="absolute inset-x-[18%] bottom-3 top-4 grid grid-cols-4 gap-1.5">
          {Array.from({ length: 8 }, (_, i) => <i key={i} className="block rounded-sm bg-[var(--kind)]" style={{ opacity: 0.18 + (i % 4) * 0.16 }} />)}
        </div>
        : <div className="absolute inset-0">
          <i className="absolute left-[15%] top-[24%] block h-[26%] w-[25%] rounded-md bg-[var(--kind)]/40" />
          <i className="absolute right-[18%] top-[18%] block h-[20%] w-[20%] rounded-full bg-[var(--kind)]/40" />
          <i className="absolute left-[22%] right-[22%] top-[64%] block h-0.5 bg-[var(--kind)]/45" />
          <i className="absolute bottom-[14%] right-[13%] block h-[22%] w-[15%] rotate-6 rounded-md bg-[var(--kind)]/55" />
        </div>
    case "app":
      return <div className="absolute inset-x-[34%] bottom-3 top-3 grid content-start gap-1.5 rounded-lg border-2 border-[var(--kind)]/45 bg-card/60 p-2">
        {line("70%", true)}{line("50%", true)}<b className="mt-auto block h-1.5 w-full rounded-full bg-[var(--kind)]/40" />
      </div>
    case "docs":
      return mode === "canvas"
        ? <div className="absolute inset-0">
          {[["12%", "20%"], ["42%", "30%"], ["70%", "18%"], ["26%", "58%"], ["58%", "60%"]].map(([left, top], i) =>
            <i key={i} className="absolute block h-[20%] w-[18%] rounded-sm bg-[var(--kind)]/30" style={{ left, top }} />)}
        </div>
        : <Paper className="inset-x-[16%] bottom-0 top-3.5 grid content-start gap-1.5 p-3">
          {line("45%", true)}{line()}{line("88%")}
          <span className="mt-1 grid grid-cols-3 gap-1">{[0, 1, 2].map(i => <b key={i} className="block h-3 rounded-sm bg-[var(--kind)]/20" />)}</span>
          {line("75%")}
        </Paper>
  }
}

export function KindThumb({ kind, mode, empty, className }: { kind: ArtifactKind; mode?: string; empty?: boolean; className?: string }) {
  const style = { "--kind": kindAccent(kind).tint } as CSSProperties
  return <div aria-hidden="true" data-thumb={kind} data-thumb-fallback="kind" style={style}
    className={cn("relative h-[104px] overflow-hidden rounded-md bg-[color-mix(in_srgb,var(--kind)_12%,var(--muted))]", className)}>
    {empty ? <span className="absolute inset-0 grid place-items-center text-xs text-muted-foreground">Nothing here yet</span> : <>
      <Drawing kind={kind} mode={mode} />
      <span className="absolute bottom-1.5 left-1.5 rounded-[var(--radius-row)] bg-card/90 px-1.5 py-0.5 text-micro font-medium leading-none text-muted-foreground shadow-xs">{KINDS[kind].label} · no preview</span>
    </>}
  </div>
}
