/**
 * The answer of the server's native initialize (and of the initialize from a
 * finished import) as the browser trusts it: nothing is assumed, every field is
 * checked, and an answer that fails any check is "not confirmed", never a
 * success. The shape is the one internal/api/testdata/craft_native_initialize_response.json
 * holds, which a Go test ties to the server's struct.
 */
export interface NativeInitialized {
  sessionToken: string
  generation: string
}

export function confirmNativeInitialized(value: unknown, application: string, pageId: string): NativeInitialized | null {
  const v = value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null
  if (!v || v.application_id !== application || v.page_id !== pageId || v.version !== "1") return null
  if (typeof v.session_token !== "string" || !/^[a-f0-9]{64}$/.test(v.session_token)) return null
  if (typeof v.generation !== "string" || !/^[1-9][0-9]*$/.test(v.generation)) return null
  return { sessionToken: v.session_token, generation: v.generation }
}
