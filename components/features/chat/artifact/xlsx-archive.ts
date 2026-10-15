import { strFromU8, Unzip, UnzipInflate } from "fflate"

export const XLSX_LIMITS = {
  compressedBytes: 5 * 1024 * 1024,
  expandedBytes: 20 * 1024 * 1024,
  entries: 128,
  sheets: 10,
  rows: 20_000,
  columns: 100,
  cells: 200_000,
  cellCharacters: 32_768,
  projectedTextBytes: 10 * 1024 * 1024,
  xmlNodes: 1_000_000,
  xmlDepth: 64,
} as const

const CRC_TABLE = Uint32Array.from({ length: 256 }, (_, index) => {
  let value = index
  for (let bit = 0; bit < 8; bit++) value = value & 1 ? 0xedb88320 ^ value >>> 1 : value >>> 1
  return value >>> 0
})
function updateCrc(crc: number, bytes: Uint8Array): number {
  for (const value of bytes) crc = crc >>> 8 ^ CRC_TABLE[(crc ^ value) & 255]
  return crc
}

export function workbookRefusal(reason: string): Error {
  return new Error(`${reason} Download the original workbook.`)
}

/** No ZIP size declaration is trusted as an allocation/expansion budget. */
export function unpackBoundedWorkbook(bytes: Uint8Array, expandedLimit = XLSX_LIMITS.expandedBytes): Map<string, Uint8Array> {
  if (bytes.byteLength > XLSX_LIMITS.compressedBytes) throw workbookRefusal("Workbook exceeds the 5 MiB input limit.")
  if (bytes[0] === 0xd0 && bytes[1] === 0xcf) throw workbookRefusal("Encrypted or legacy workbooks cannot be previewed.")
  if (bytes.length < 22 || bytes[0] !== 0x50 || bytes[1] !== 0x4b || bytes[2] !== 3 || bytes[3] !== 4) throw workbookRefusal("Workbook is not a supported XLSX ZIP archive.")
  // Require a complete single-disk archive. ZIP64 is outside this bounded view.
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  let end = -1
  for (let at = bytes.length - 22; at >= Math.max(0, bytes.length - 65_557); at--) {
    if (view.getUint32(at, true) === 0x06054b50 && at + 22 + view.getUint16(at + 20, true) === bytes.length) { end = at; break }
  }
  if (end < 0 || view.getUint16(end + 4, true) || view.getUint16(end + 6, true)) throw workbookRefusal("Workbook ZIP is incomplete or unsupported.")
  const declaredEntries = view.getUint16(end + 10, true)
  if (!declaredEntries || declaredEntries > XLSX_LIMITS.entries || view.getUint16(end + 8, true) !== declaredEntries || view.getUint32(end + 16, true) + view.getUint32(end + 12, true) !== end) throw workbookRefusal("Workbook ZIP exceeds the entry limit or is unsupported.")
  const checksums = new Map<string, number>()
  let directory = view.getUint32(end + 16, true)
  for (let index = 0; index < declaredEntries; index++) {
    if (directory + 46 > end || view.getUint32(directory, true) !== 0x02014b50) throw workbookRefusal("Workbook ZIP directory is corrupt.")
    const nameLength = view.getUint16(directory + 28, true)
    const next = directory + 46 + nameLength + view.getUint16(directory + 30, true) + view.getUint16(directory + 32, true)
    const local = view.getUint32(directory + 42, true)
    if (next > end || local + 30 > directory || view.getUint32(local, true) !== 0x04034b50 || view.getUint16(local + 26, true) !== nameLength) throw workbookRefusal("Workbook ZIP directory is corrupt or unsupported.")
    if (view.getUint16(directory + 8, true) & 0x41 || view.getUint16(local + 6, true) & 0x41) throw workbookRefusal("Encrypted workbooks cannot be previewed.")
    if (local + 30 + nameLength > directory || !bytes.subarray(directory + 46, directory + 46 + nameLength).every((value, offset) => bytes[local + 30 + offset] === value)) throw workbookRefusal("Workbook ZIP entry names disagree.")
    const name = strFromU8(bytes.subarray(local + 30, local + 30 + nameLength), !(view.getUint16(local + 6, true) & 2048))
    checksums.set(name, view.getUint32(directory + 16, true))
    directory = next
  }
  if (directory !== end) throw workbookRefusal("Workbook ZIP directory is corrupt.")
  let entries = 0, completed = 0, expanded = 0
  let failure: Error | null = null
  const seen = new Set<string>(), files = new Map<string, Uint8Array>()
  const limit = Math.min(expandedLimit, XLSX_LIMITS.expandedBytes)
  const unzip = new Unzip(file => {
    if (++entries > XLSX_LIMITS.entries || seen.has(file.name)) throw workbookRefusal("Workbook ZIP contains too many or duplicate entries.")
    seen.add(file.name)
    if (file.name.length > 512 || file.name.startsWith("/") || /[\\\0]/.test(file.name) || file.name.replace(/\/$/, "").split("/").some(part => !part || part === "." || part === "..")) throw workbookRefusal("Workbook ZIP contains an unsafe path.")
    if (/vbaProject|macrosheets|externalLinks/i.test(file.name)) throw workbookRefusal("Macros and external workbook links are not supported.")
    if (file.compression !== 0 && file.compression !== 8) throw workbookRefusal("Workbook uses unsupported ZIP compression.")
    const keep = file.name.endsWith(".xml") || file.name.endsWith(".rels")
    const chunks: Uint8Array[] = []
    let size = 0, crc = -1
    file.ondata = (error, chunk, final) => {
      if (failure) return
      if (error) { failure = workbookRefusal("Workbook ZIP is encrypted, corrupt or unsupported."); return }
      expanded += chunk.byteLength; size += chunk.byteLength
      if (expanded > limit) { failure = workbookRefusal("Workbook exceeds the 20 MiB expanded ZIP limit."); return }
      crc = updateCrc(crc, chunk)
      if (keep) chunks.push(chunk)
      if (final) {
        completed++
        if (((crc ^ -1) >>> 0) !== checksums.get(file.name)) { failure = workbookRefusal("Workbook ZIP checksum is corrupt."); return }
        if (file.originalSize !== undefined && file.originalSize !== size) { failure = workbookRefusal("Workbook ZIP entry size is corrupt."); return }
        if (keep) {
          const content = new Uint8Array(size)
          let offset = 0
          for (const part of chunks) { content.set(part, offset); offset += part.byteLength }
          files.set(file.name, content)
        }
      }
    }
    file.start()
  })
  // Synchronous streaming inflate, not whole-entry unzip or nested workers.
  // Small compressed pushes bound the temporary output of one decoder call;
  // stop feeding immediately when actual emitted bytes exceed the budget.
  unzip.register(UnzipInflate)
  for (let at = 0; at < bytes.length; at += 256) {
    unzip.push(bytes.subarray(at, at + 256), at + 256 >= bytes.length)
    if (failure) throw failure
  }
  if (entries !== declaredEntries || completed !== entries) throw workbookRefusal("Workbook ZIP is incomplete or corrupt.")
  return files
}
