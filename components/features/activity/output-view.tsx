"use client"

import { useState } from "react"
import { Code2, Eye } from "lucide-react"
import type { BundledLanguage } from "shiki"
import { cn } from "@/lib/utils"
import { analyzeOutput } from "@/lib/output-language"
import { MessageResponse } from "@/components/ai-elements/message"
import {
  CodeBlock,
  CodeBlockCopyButton,
  CodeBlockHeader,
  CodeBlockTitle,
} from "@/components/ai-elements/code-block"
import { JSONViewer } from "./json-viewer"

// OutputView — the shared renderer for any agent / routine-step output
// surface. It makes a step's output read the same as the agent's chat
// reply instead of a wall of plain monospace.
//
// Decision logic (see lib/output-language.ts for the pure detection):
//   - already-parsed object/array → JSONViewer (JSON/Table toggle)
//   - markdown w/ fenced blocks   → MessageResponse (the chat renderer,
//     so each ```lang block is shiki-highlighted, prose stays prose)
//   - whole value is JSON text    → JSONViewer
//   - raw yaml / bash / …         → CodeBlock(detectedLanguage) (shiki)
//   - plain text / logs           → monospace <pre>, whitespace preserved
//
// Reused as-is by the trace side panel (Output + Logs tabs) and the
// sub-span detail; export it cleanly so other output surfaces can adopt
// the same treatment.

export interface OutputViewProps {
  /** Raw output: a string (likely the run record's text/JSON output) or
   *  an already-parsed object/array for inputs we know are structured. */
  value: unknown
  className?: string
  /** Override the empty-state copy (e.g. "No logs yet."). */
  emptyLabel?: string
  /** Force the plain-text `<pre>` rendering regardless of detected kind —
   *  the "raw" side of the OutputWithRawToggle copy-paste-fidelity switch. */
  raw?: boolean
}

// rawText renders any value as a monospace <pre> for copy-paste fidelity:
// strings verbatim, structured values pretty-printed JSON. Shared by the
// `raw` branch below and the toggle wrapper.
function rawText(value: unknown): string {
  if (typeof value === "string") return value
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

function RawPre({ text, className }: { text: string; className?: string }) {
  return (
    <pre
      className={cn(
        "overflow-auto whitespace-pre-wrap break-words rounded bg-background/60 p-2 font-mono text-[11px] leading-relaxed text-foreground/80",
        className,
      )}
    >
      {text}
    </pre>
  )
}

function EmptyState({ label }: { label: string }) {
  return (
    <div className="flex h-32 items-center justify-center text-xs text-muted-foreground/50">
      {label}
    </div>
  )
}

export function OutputView({
  value,
  className,
  emptyLabel = "No output yet.",
  raw = false,
}: OutputViewProps) {
  // Raw mode — copy-paste fidelity: skip detection entirely and show the
  // verbatim text (or pretty JSON for structured values) in a <pre>.
  if (raw) {
    if (value === undefined || value === null || value === "") {
      return <EmptyState label={emptyLabel} />
    }
    const text = rawText(value)
    if (text.trim() === "") return <EmptyState label={emptyLabel} />
    return <RawPre text={text} className={className} />
  }
  // Already-structured value (object/array) — hand straight to the JSON
  // inspector, which owns its own JSON/Table toggle + copy.
  if (value !== null && typeof value === "object") {
    return (
      <div className={className}>
        <JSONViewer value={value} />
      </div>
    )
  }

  if (value === undefined || value === null || value === "") {
    return <EmptyState label={emptyLabel} />
  }

  const text = String(value)
  if (text.trim() === "") return <EmptyState label={emptyLabel} />

  const analysis = analyzeOutput(text)

  switch (analysis.kind) {
    case "markdown":
      // The chat's markdown renderer (ai-elements Response / Streamdown):
      // fenced ```lang blocks → shiki-highlighted CodeBlocks, prose → prose.
      return (
        <div
          className={cn(
            "max-w-full overflow-x-auto text-sm leading-relaxed",
            className,
          )}
        >
          <MessageResponse>{text}</MessageResponse>
        </div>
      )

    case "json":
      return (
        <div className={className}>
          <JSONViewer value={text} />
        </div>
      )

    case "code":
      return (
        <CodeBlock
          code={text}
          language={(analysis.language ?? "text") as BundledLanguage}
          className={className}
        >
          <CodeBlockHeader>
            <CodeBlockTitle>{analysis.language}</CodeBlockTitle>
            <CodeBlockCopyButton />
          </CodeBlockHeader>
        </CodeBlock>
      )

    default:
      // Plain text / logs — monospace, whitespace preserved.
      return <RawPre text={text} className={className} />
  }
}

// OutputWithRawToggle — OutputView plus a small "raw ⇄ rendered" switch for
// copy-paste fidelity (#851). The toggle only appears when there's something
// to toggle: a string value whose detected rendering differs from raw
// (markdown / code / json). Plain text and already-structured objects (which
// the JSONViewer handles with its own JSON/Table toggle) render without it.
export function OutputWithRawToggle({
  value,
  className,
  emptyLabel,
}: OutputViewProps) {
  const [raw, setRaw] = useState(false)
  const toggleable =
    typeof value === "string" &&
    value.trim() !== "" &&
    analyzeOutput(value).kind !== "text"

  if (!toggleable) {
    return <OutputView value={value} className={className} emptyLabel={emptyLabel} />
  }

  return (
    <div className={cn("space-y-1", className)}>
      <div className="flex justify-end">
        <button
          type="button"
          onClick={() => setRaw((r) => !r)}
          aria-pressed={raw}
          className="inline-flex items-center gap-1 rounded border border-white/[0.08] px-1.5 py-0.5 text-[10px] text-muted-foreground/70 transition-colors hover:border-white/20 hover:text-foreground"
        >
          {raw ? (
            <>
              <Eye className="h-3 w-3" /> rendered
            </>
          ) : (
            <>
              <Code2 className="h-3 w-3" /> raw
            </>
          )}
        </button>
      </div>
      <OutputView value={value} emptyLabel={emptyLabel} raw={raw} />
    </div>
  )
}
