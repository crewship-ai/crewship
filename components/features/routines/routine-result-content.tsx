"use client"

import { Streamdown } from "streamdown"

/** Keep Streamdown's default GFM and sanitization plugins, including tables. */
export function RoutineResultContent({ output }: { output: string }) {
  // Structured results stay structured instead of being interpreted as Markdown.
  let structured: unknown
  try { structured = JSON.parse(output) } catch { /* A text result is Markdown. */ }
  if (structured !== null && typeof structured === "object") return <pre className="overflow-auto whitespace-pre-wrap break-words text-sm">{JSON.stringify(structured, null, 2)}</pre>
  return <Streamdown mode="static" controls={false} className="min-w-0 space-y-3 text-sm leading-relaxed [&_h1]:text-lg [&_h1]:font-semibold [&_h2]:text-base [&_h2]:font-semibold [&_a]:text-primary [&_table]:w-full [&_table]:text-sm [&_th]:text-left [&_th]:font-medium [&_th]:p-2 [&_td]:p-2 [&_td]:border-b [&_td]:border-border/60 [&_pre]:overflow-auto">{output}</Streamdown>
}
