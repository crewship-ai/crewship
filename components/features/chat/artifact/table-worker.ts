import { decodeTableBytes, parseDelimited, TABLE_LIMITS } from "./table-data"

// Worker is deliberately local: file contents never leave the browser.
const workerScope = self as unknown as { onmessage: ((event: MessageEvent<{ bytes: ArrayBuffer; separator: "," | "\t" }>) => void) | null; postMessage: (value: unknown) => void }
workerScope.onmessage = ({ data }) => {
  try {
    if (data.bytes.byteLength > TABLE_LIMITS.bytes) throw new Error("Table exceeds the 5 MiB preview limit. Download the original file.")
    const text = decodeTableBytes(data.bytes)
    workerScope.postMessage({ table: parseDelimited(text, data.separator) })
  } catch (error) {
    workerScope.postMessage({ error: error instanceof Error ? error.message : "Could not parse table. Download the original file." })
  }
}
