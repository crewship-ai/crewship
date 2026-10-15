import { Parser } from "saxen"
import { workbookRefusal, XLSX_LIMITS } from "./xlsx-archive"

export const localName = (name: string) => name.slice(name.lastIndexOf(":") + 1)
export const xmlEscape = (value: string) => value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&apos;")
export const xmlBytes = (value: string) => new TextEncoder().encode(value)

export interface XmlBudget { nodes: number }
interface XmlHooks {
  open?: (name: string, attributes: Record<string, string>, stack: string[]) => void
  close?: (name: string, stack: string[]) => void
  text?: (value: string, stack: string[]) => void
}

/** Strict bounded preflight; no DTD, entity declarations, recovery or network. */
export function inspectWorkbookXml(bytes: Uint8Array, budget: XmlBudget, hooks: XmlHooks = {}): void {
  let xml: string
  try { xml = new TextDecoder("utf-8", { fatal: true }).decode(bytes) }
  catch { throw workbookRefusal("Workbook XML is not valid UTF-8.") }
  if (/<!DOCTYPE|<!ENTITY/i.test(xml)) throw workbookRefusal("Workbook XML declarations are not supported.")
  const stack: string[] = []
  const parser = new Parser()
  parser.on("error", () => { throw workbookRefusal("Workbook XML is corrupt.") })
  parser.on("warn", () => { throw workbookRefusal("Workbook XML is malformed.") })
  parser.on("attention", () => { throw workbookRefusal("Workbook XML declarations are not supported.") })
  parser.on("openTag", (rawName, getAttributes, decode) => {
    const name = localName(rawName)
    if (++budget.nodes > XLSX_LIMITS.xmlNodes || stack.length >= XLSX_LIMITS.xmlDepth) throw workbookRefusal("Workbook XML exceeds the complexity limit.")
    const source = getAttributes(), attributes: Record<string, string> = Object.create(null)
    if (Object.keys(source).length > 64) throw workbookRefusal("Workbook XML exceeds the attribute limit.")
    for (const [key, value] of Object.entries(source)) {
      if (key === "xmlns" || key.startsWith("xmlns:")) continue
      const decoded = decode(value)
      if (decoded.length > XLSX_LIMITS.cellCharacters || /[\x00-\x08\x0b\x0c\x0e-\x1f]/.test(decoded)) throw workbookRefusal("Workbook XML attribute is unsupported.")
      attributes[localName(key)] = decoded
    }
    stack.push(name)
    hooks.open?.(name, attributes, stack)
  })
  parser.on("closeTag", rawName => {
    const name = localName(rawName)
    if (stack.at(-1) !== name) throw workbookRefusal("Workbook XML tags are corrupt.")
    hooks.close?.(name, stack)
    stack.pop()
  })
  const text = (value: string) => {
    if (/[\x00-\x08\x0b\x0c\x0e-\x1f]/.test(value)) throw workbookRefusal("Workbook XML text is unsupported.")
    hooks.text?.(value, stack)
  }
  parser.on("text", (value, decode) => text(decode(value)))
  parser.on("cdata", text)
  parser.parse(xml)
  if (stack.length) throw workbookRefusal("Workbook XML is incomplete.")
}
