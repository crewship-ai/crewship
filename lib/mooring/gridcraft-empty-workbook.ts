import { strToU8, zipSync } from "fflate"
import parts from "./gridcraft-empty-workbook-parts.json" with { type: "json" }

/** A blank workbook with a single editable worksheet. */
export function emptyGridCraftWorkbook(): Uint8Array {
  return zipSync(Object.fromEntries(Object.entries(parts).map(([name, lines]) => [name, strToU8(lines.join("\n"))])))
}
