import { Extension, Node, Mark, getSchema } from "@tiptap/core"
import { Table, TableRow, TableCell, TableHeader } from "@tiptap/extension-table"
import { Plugin } from "@tiptap/pm/state"
import * as Y from "yjs"
import { prosemirrorToYDoc, yDocToProsemirrorJSON } from "@tiptap/y-tiptap"
import { createDocumentSchema } from "../../tools/docs-service/schema-core.mjs"

/** Shared closed model with this browser's own pinned Yjs classes. */
export const documentSchema = createDocumentSchema({ Extension, Node, Mark, getSchema, Table, TableRow, TableCell, TableHeader, Plugin, Y, prosemirrorToYDoc, yDocToProsemirrorJSON })
export function normalizeDocumentLink(value: string): string | undefined {
  try { const href = new URL(value.trim()).href; return documentSchema.validLinkHref(href) ? href : undefined } catch { return undefined }
}
