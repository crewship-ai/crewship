/**
 * The single seam through which an update can become available: the server's own confirmation that this chat's
 * audience is private (an authorised preview/visibility field, requested in FE-CONTRACT-NOTE-F2-13).
 *
 * That field does not exist yet (BE ruling 2026-10-20 11:04 UTC: no preview audience route; a successful document read
 * does not authorise a targeted iteration), so this is false for every chat. When the field lands, source it here and
 * nowhere else. The client-side estimate in chat-privacy.ts only blocks; it must never feed this value.
 */
export function useServerConfirmedPrivate(_chatId: string): boolean {
  return false
}
