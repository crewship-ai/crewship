import type { Node } from "@tiptap/pm/model"

/** A Document is empty while it holds only empty paragraphs. An inserted
 *  heading, list or table is the start of content, even before any text. */
export function isEmptyDocument(doc: Node): boolean {
  return doc.children.every(child => child.type.name === "paragraph" && child.content.size === 0)
}
