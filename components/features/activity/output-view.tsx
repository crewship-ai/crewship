"use client"

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
}: OutputViewProps) {
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
}
