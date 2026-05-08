"use client"

import { useMemo, useState } from "react"
import { Copy, Check } from "lucide-react"
import type { RoutineDetail } from "./routines-detail-panel"
import { Button } from "@/components/ui/button"

// RoutineEditorTab — read-only DSL view. Future enhancement: Monaco
// editor with JSON Schema validation for in-UI authoring. For now we
// render the canonicalized JSON in a scrollable <pre> block with a
// copy-to-clipboard button so users can edit externally and re-import
// or paste into the agent prompt.

export function RoutineEditorTab({ routine }: { routine: RoutineDetail }) {
  const [copied, setCopied] = useState(false)

  const formatted = useMemo(() => {
    try {
      return JSON.stringify(routine.definition, null, 2)
    } catch {
      return "// failed to render definition"
    }
  }, [routine.definition])

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(formatted)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      /* ignore */
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between border-b border-white/[0.06] bg-card/30 px-3 py-1.5 shrink-0">
        <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
          <span>JSON DSL</span>
          <span>·</span>
          <span>{formatted.length.toLocaleString()} chars</span>
          <span>·</span>
          <span className="font-mono">v{routine.dsl_version}</span>
        </div>
        <Button size="sm" variant="ghost" onClick={copy} className="h-6 gap-1.5 text-[10px]">
          {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
      <pre className="flex-1 overflow-auto bg-background p-3 font-mono text-[11px] leading-relaxed">
        <code>{formatted}</code>
      </pre>
      <div className="border-t border-white/[0.06] bg-card/20 px-3 py-1.5 text-[10px] text-muted-foreground shrink-0">
        Read-only. To edit, export the bundle, modify the JSON, then re-import. In-UI editing
        with JSON Schema validation is a follow-up.
      </div>
    </div>
  )
}
