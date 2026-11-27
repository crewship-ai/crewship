import type { NativeCraftHandBack } from "@/components/features/pages/native-craft-editor-host"

/** The message never waits longer than this for the Sheet. */
const HAND_BACK_MS = 12_000

const KEPT: Record<Extract<NativeCraftHandBack, { state: "kept" }>["reason"], string> = {
  unsaved: "Your last change is not saved yet, so you still hold the Sheet. The message was not sent.",
  busy: "The Sheet is still working, so you still hold it. The message was not sent.",
  unconfirmed: "Passing the Sheet to the agent was not confirmed. The message was not sent.",
}

export interface HandBackOutcome {
  /** The hand-over was not confirmed: the message must not go with leave to change the Sheet. */
  hold: boolean
  /** What the person should know, in words; null when there is nothing to say. */
  say: string | null
}

/** Asks for the edit access back. Only a confirmed hand-over, or holding nothing, lets the message go. Never throws. */
export async function handBackSheet(ask: () => Promise<NativeCraftHandBack>): Promise<HandBackOutcome> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    const result = await Promise.race([ask(), new Promise<NativeCraftHandBack>(resolve => { timer = setTimeout(() => resolve({ state: "kept", reason: "unconfirmed" }), HAND_BACK_MS) })])
    if (result.state === "returned") return { hold: false, say: "Editing passed to the agent. Take it over again whenever you want." }
    if (result.state === "not_held") return { hold: false, say: null }
    return { hold: true, say: KEPT[result.reason] ?? KEPT.unconfirmed }
  } catch { return { hold: true, say: KEPT.unconfirmed } } finally { clearTimeout(timer) }
}
