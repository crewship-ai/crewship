// The stored Sheet is the workbook file itself: reading it, its sheets and its cells' saved values.
import { expect, type APIRequestContext } from "@playwright/test"
import { inflateRawSync } from "node:zlib"

/** The parts of an .xlsx (a zip), read from its central directory. */
export function unzip(bytes: Buffer): Map<string, string> {
  const out = new Map<string, string>()
  const end = bytes.lastIndexOf(Buffer.from([0x50, 0x4b, 0x05, 0x06]))
  if (end < 0) throw new Error("not a zip: no end of central directory")
  let at = bytes.readUInt32LE(end + 16)
  for (let i = 0, n = bytes.readUInt16LE(end + 10); i < n; i++) {
    const method = bytes.readUInt16LE(at + 10), size = bytes.readUInt32LE(at + 20), nameLength = bytes.readUInt16LE(at + 28)
    const extra = bytes.readUInt16LE(at + 30), comment = bytes.readUInt16LE(at + 32), local = bytes.readUInt32LE(at + 42)
    const name = bytes.toString("utf8", at + 46, at + 46 + nameLength)
    const data = local + 30 + bytes.readUInt16LE(local + 26) + bytes.readUInt16LE(local + 28)
    const raw = bytes.subarray(data, data + size)
    out.set(name, (method === 8 ? inflateRawSync(raw) : raw).toString("utf8"))
    at += 46 + nameLength + extra + comment
  }
  return out
}
/** The value an .xlsx sheet part holds for a cell, inline string or number. */
export function xlsxCell(xml: string, address: string): string | undefined {
  const m = new RegExp(`<c r="${address}"[^>]*?(?:/>|>([\\s\\S]*?)</c>)`).exec(xml)
  return m?.[1] ? (/<v>([^<]*)<\/v>/.exec(m[1])?.[1] ?? /<t[^>]*>([^<]*)<\/t>/.exec(m[1])?.[1]) : undefined
}

/** The stored Sheet is the workbook file itself: its parts, and a sheet's XML by sheet name. */
export interface FileHead { version: string; bytes: Buffer; sheet(name: string): string }
export function workbookOf(version: string, bytes: Buffer): FileHead {
  const parts = unzip(bytes), workbookXml = parts.get("xl/workbook.xml") ?? ""
  return {
    version, bytes,
    sheet(name) {
      const rel = new RegExp(`<sheet[^>]*name="${name}"[^>]*r:id="([^"]+)"`).exec(workbookXml)?.[1]
      const target = new RegExp(`Id="${rel}"[^>]*Target="([^"]+)"`).exec(parts.get("xl/_rels/workbook.xml.rels") ?? "")?.[1] ?? ""
      return parts.get("xl/" + target.replace(/^\/?xl\//, "")) ?? ""
    },
  }
}
/** A cell's stored value as a number (the cached value of a formula). */
export const cell = (head: FileHead, sheet: string, address: string) => { const v = xlsxCell(head.sheet(sheet), address); return v === undefined ? undefined : Number(v) }
export const values = (head: FileHead, sheet: string, addresses: string[]) => addresses.map(a => cell(head, sheet, a))

/** The stored file of the head (or of one version), read under the account's own read admission. */
export async function storedWorkbook(api: APIRequestContext, workspace: string, pageId: string, version?: string): Promise<FileHead> {
  const q = `?workspace_id=${encodeURIComponent(workspace)}`, base = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/`
  const s = await api.post(`${base}sessions${q}`, { data: {} })
  expect(s.status(), "read admission").toBe(201)
  const token = ((await s.json()) as { session_token: string }).session_token
  const r = await api.get(`${base}document${version ? `/versions/${version}` : ""}${q}`, { headers: { "X-Craft-Session": token, "Cache-Control": "no-cache" }, timeout: 120_000 })
  expect(r.status(), "read the stored workbook").toBe(200)
  expect(r.headers()["content-type"] ?? "", "the stored Sheet is the workbook file").toMatch(/spreadsheetml\.sheet|macroEnabled/)
  return workbookOf(r.headers()["x-craft-version"] ?? "", await r.body())
}
