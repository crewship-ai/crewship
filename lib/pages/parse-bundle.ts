import { isAlias, isMap, isSeq, parseDocument, type Node } from "yaml"
import type { WirePageBundle } from "@/hooks/use-page-sharing"

export const MAX_PAGE_BUNDLE_BYTES = 4 * 1024 * 1024 + 320 * 1024
export function parsePageBundle(text: string): WirePageBundle {
  if (new TextEncoder().encode(text).length > MAX_PAGE_BUNDLE_BYTES) throw new Error("Page bundle exceeds the import size limit.")
  const doc = parseDocument(text, { uniqueKeys: true })
  if (doc.errors.length) throw new Error(doc.errors[0].message)
  function check(node: Node | null | undefined, depth: number) {
    if (!node) return
    if (depth > 16 || isAlias(node) || node.anchor) throw new Error("YAML aliases, anchors and deeply nested bundles are not supported.")
    if (isMap(node)) for (const item of node.items) { check(item.key as Node, depth + 1); check(item.value as Node, depth + 1) }
    if (isSeq(node)) for (const item of node.items) check(item as Node, depth + 1)
  }
  check(doc.contents, 0)
  return doc.toJS({ maxAliasCount: 0 }) as WirePageBundle
}
