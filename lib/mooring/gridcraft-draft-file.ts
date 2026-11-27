/** A workbook Sheet's file, compressed as the editor wrote it: the server's limit. */
export const GRIDCRAFT_MAX_FILE_BYTES = 64 * 1024 * 1024

/** The Sheet's adapter for a draft kept as a file: xlsx or xlsm, up to the server's limit, a zip ("PK"). */
export const GRIDCRAFT_DRAFT_FILE = {
  formats: ["xlsx", "xlsm"] as const,
  maxBytes: GRIDCRAFT_MAX_FILE_BYTES,
  signature: (bytes: Uint8Array) => bytes[0] === 0x50 && bytes[1] === 0x4b,
}
