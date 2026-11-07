/** A saved native Craft instruction stays readable in its chat, but only an
 *  explicit task runs it. Anything that would resend it, or copy it into another
 *  prompt, has to see this classification first. */
export const NATIVE_CRAFT_INSTRUCTION_REFUSAL =
  "This is a saved Craft instruction. Start it from the document, or send a new message."

export function isNativeCraftInstruction(turn: { metadata?: Record<string, unknown> } | undefined): boolean {
  return turn?.metadata?.native_craft_instruction === true
}

/** The history API returns the classification beside `metadata`; a turn carries
 *  it inside. */
export function historyMetadata(message: { metadata?: Record<string, unknown>; native_craft_instruction?: unknown }): Record<string, unknown> | undefined {
  return message.native_craft_instruction === true
    ? { ...message.metadata, native_craft_instruction: true }
    : message.metadata
}
