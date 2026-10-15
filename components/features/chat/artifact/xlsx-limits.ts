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
