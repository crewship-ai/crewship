import empty from "./wordcraft-native-empty.json"

/** Canonical blank serialized by the qualified original WordCraft engine.
 * Keep this separate from the older checkpoint model: promoted f32 numbers in
 * that representation are intentionally rejected by whole-native validation.
 */
export function createEmptyNativeWord(): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(empty))
}
