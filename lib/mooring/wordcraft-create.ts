import { loadWordCraftReceiptSDK, type WordCraftReceiptSDK } from "./wordcraft-host-session"

interface CreationSDK extends WordCraftReceiptSDK {
  WordCraftModel: new (native: string, ids: string) => {
    is_read_only(): boolean
    checkpoint(): Uint8Array
    free(): void
  }
}

/** An upstream canonical blank document, encoded by the release's existing
 * WordCraft/Yrs model. It is only an initialization proposal; the host validates
 * current Page ownership and commits it through the ordinary native path. */
export async function createEmptyWordCraftCheckpoint(prefix: string): Promise<string> {
  const sdk = await loadWordCraftReceiptSDK(prefix) as CreationSDK
  if (typeof sdk.WordCraftModel !== "function") throw new Error("The WordCraft editor is unavailable.")
  const { WORDCRAFT_EMPTY_NATIVE } = await import("./wordcraft-empty")
  const model = new sdk.WordCraftModel(WORDCRAFT_EMPTY_NATIVE, JSON.stringify([crypto.randomUUID()]))
  try {
    if (model.is_read_only()) throw new Error("The blank WordCraft document cannot be edited.")
    const checkpoint = model.checkpoint()
    if (!checkpoint.length || checkpoint.length > 1572864) throw new Error("The WordCraft document could not be initialized.")
    let binary = ""
    for (const byte of checkpoint) binary += String.fromCharCode(byte)
    return btoa(binary)
  } finally { model.free() }
}
