import { parseXlsxWorkbook } from "./xlsx-data"

self.onmessage = async ({ data }: MessageEvent<{ bytes: ArrayBuffer }>) => {
  try { self.postMessage({ workbook: await parseXlsxWorkbook(new Uint8Array(data.bytes)) }) }
  catch (error) { self.postMessage({ error: error instanceof Error ? error.message : "Could not parse workbook. Download the original workbook." }) }
}
