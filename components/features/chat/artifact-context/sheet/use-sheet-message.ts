import { useCallback, useState } from "react"

/** Whether a message lets the agent change the open Sheet before the person says so. The only place this is decided. */
export const SHEET_EDIT_DEFAULT = false

/** Said when the Sheet changed under a choice already made, so the person has to choose again. */
export const SHEET_CONSENT_STALE = "The Sheet changed after you allowed the agent to change it. Allow it again."

type Choice = { key: string; value: boolean; version?: string; stale: boolean; /** Chosen by the person (with the server default on). */ touched?: boolean }

/**
 * The person's choice to let the agent change one Sheet, for one message. It is
 * bound to the conversation, the Sheet and the version shown when it was made,
 * and is never remembered: another Sheet, another conversation or a sent
 * message starts over. A version the person saved themselves moves the choice
 * along; any other change turns it off and asks again. The server decides again
 * on every message; this only says what was chosen.
 */
export function useSheetMessage({ scope, pageId, blocked, version, ownSave, defaultAllowed = false }: {
  /** Identity of the person, workspace and conversation. */
  scope: string
  pageId: string | null
  /** Why the choice cannot apply to the next message, in words; null when it can. */
  blocked: string | null
  /** The version the editor shows now, once known. */
  version?: string
  /** The version this editor itself saved last, if any. */
  ownSave?: string
  /** The server's default: in the person's own private chat, for a Sheet uploaded there, the choice
   * starts on and stays on for every message, following each version, until the person turns it off. Never a permission. */
  defaultAllowed?: boolean
}) {
  const key = JSON.stringify([scope, pageId])
  const [choice, setChoice] = useState<Choice>({ key, value: SHEET_EDIT_DEFAULT, stale: false })
  // Reset during render: an earlier choice must not come back with its Sheet.
  if (choice.key !== key) setChoice({ key, value: SHEET_EDIT_DEFAULT, stale: false })
  else if (!defaultAllowed && choice.value && version && choice.version !== version) {
    // The person's own save is a version they saw; anything else is a change they did not agree to.
    setChoice(version === ownSave ? { ...choice, version } : { key, value: false, stale: true })
  }
  const current = choice.key === key ? choice : null
  // With the server's default the person's own choice for this Sheet in this chat is kept in this tab, so a reload keeps it.
  const kept = defaultAllowed && pageId ? keptChoice(key) : null
  const standing = defaultAllowed && pageId !== null && Boolean(version) && (current?.touched ? current.value : kept ?? true)
  const allowed = standing || (!defaultAllowed && Boolean(current?.value) && pageId !== null && Boolean(current?.version) && current?.version === version)
  const setAllowed = useCallback((value: boolean) => {
    if (defaultAllowed) { keepChoice(key, value); setChoice({ key, value, version, stale: false, touched: true }); return }
    setChoice({ key, value: value && Boolean(version), version: value ? version : undefined, stale: false })
  }, [key, version, defaultAllowed])
  const sent = useCallback(() => { if (!defaultAllowed) setChoice({ key, value: SHEET_EDIT_DEFAULT, stale: false }) }, [key, defaultAllowed])
  const metadata = useCallback((): { artifact_refs: { page_id: string; version: string }[] } | undefined =>
    allowed && !blocked && pageId && version ? { artifact_refs: [{ page_id: pageId, version }] } : undefined, [allowed, blocked, pageId, version])
  // The standing choice is on and only the version is missing (the editor is opening): Send waits for it.
  const awaitsVersion = defaultAllowed && pageId !== null && !version && (current?.touched ? current.value : kept ?? true)
  return { allowed, setAllowed, sent, metadata, stale: Boolean(current?.stale), awaitsVersion }
}

const keptKey = (key: string) => `crewship.sheet-consent:v1:${key}`
function keptChoice(key: string): boolean | null {
  try { const v = sessionStorage.getItem(keptKey(key)); return v === "on" ? true : v === "off" ? false : null } catch { return null }
}
function keepChoice(key: string, value: boolean) {
  try { sessionStorage.setItem(keptKey(key), value ? "on" : "off") } catch { /* this visit only */ }
}
