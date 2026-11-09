"use client"

import { DOMSerializer } from "@tiptap/pm/model"
import { isEmptyDocument } from "@/lib/documents/empty"
import { documentSchema } from "@/lib/documents/schema"

export interface DocumentReadonlyProps {
  schema: 2 | 3 | 4 | 5 | 6
  content: unknown
  /** A label from the authorized caller; never infer a private title here. */
  accessibleLabel: string
  /** Shown in place of a body that holds only empty paragraphs. */
  emptyText?: string
}

const unavailable = "Document unavailable. This content cannot be displayed by this reader."

function confirmedContent(content: unknown, version: number) {
  if (version !== 2 && version !== 3 && version !== 4 && version !== 5 && version !== 6) return null
  try {
    documentSchema.validateJSON(content, version)
    const schema = documentSchema.schemaFor(version)
    const node = schema.nodeFromJSON(content)
    node.check()
    if (node.type !== schema.topNodeType) return null
    return { schema, node }
  } catch { return null }
}

/** A separate saved-content view. It has no editor or authority of its own. */
export function DocumentReadonly({ schema, content, accessibleLabel, emptyText = "This document is empty." }: DocumentReadonlyProps) {
  const confirmed = confirmedContent(content, schema)
  if (!confirmed) return <p role="status">{unavailable}</p>
  if (isEmptyDocument(confirmed.node)) return <div role="region" aria-label={accessibleLabel} className="rounded-md border p-4 text-sm text-muted-foreground"><p>{emptyText}</p></div>
  return <div
    role="region"
    aria-label={accessibleLabel}
    className="min-w-0 break-words rounded-md border p-4 [&_p]:my-2 [&_h1]:text-2xl [&_h2]:text-xl [&_h3]:text-lg [&_ul]:list-disc [&_ol]:list-decimal [&_li]:ml-5 [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto [&_td]:border [&_td]:p-2 [&_th]:border [&_th]:p-2 [&_pre]:whitespace-pre-wrap [&_strong]:font-semibold [&_em]:italic [&_s]:line-through [&_blockquote]:border-l-2 [&_blockquote]:pl-4 [&_blockquote]:text-muted-foreground [&_ul[data-type=taskList]]:list-none [&_li[data-type=taskItem]]:ml-0 [&_li[data-type=taskItem]]:flex [&_li[data-type=taskItem]]:gap-2 [&_li[data-checked=true]>div]:line-through"
    ref={element => {
      if (!element) return
      // Clear before serialization as well: a failed new read cannot retain old DOM.
      element.replaceChildren()
      try {
        element.replaceChildren(DOMSerializer.fromSchema(confirmed.schema).serializeFragment(
          confirmed.node.content, { document: element.ownerDocument },
        ))
      } catch {
        const status = element.ownerDocument.createElement("p")
        status.setAttribute("role", "status")
        status.textContent = unavailable
        element.replaceChildren(status)
      }
    }}
  />
}
