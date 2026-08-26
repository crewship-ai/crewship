// Is a character still being composed?
//
// For a reader typing Japanese, Chinese or Korean, Enter is how you CONFIRM
// the candidate the input method is offering — not how you submit the form.
// The browser delivers that keystroke to the page like any other, so a
// handler that branches on `e.key === "Enter"` alone commits whatever
// half-typed romaji or pinyin happens to be in the box. On `TitleEditor`
// that half-composed string is PATCHed straight onto the issue.
//
// One helper rather than a copied condition, because the condition is not
// one line and the browsers do not agree:
//
//   - `isComposing` on the KeyboardEvent is the standard, and Chrome and
//     Firefox set it. React does not surface it on the synthetic event, so
//     it has to be read off `nativeEvent`.
//   - Safari and older WebKit do not set it on keydown at all. They signal
//     an in-flight composition with `keyCode === 229` (and `key ===
//     "Process"` on some builds), which is why a guard that only reads
//     `isComposing` is correct on Chrome and a no-op where it is needed
//     most.
//
// Deliberately NOT tracking compositionstart/compositionend in state: that
// is a second source of truth to keep in sync, and every site that needs
// this guard is a single keydown handler with nothing else to remember.

/**
 * The fields this reads. Structural, so it accepts both a React
 * `KeyboardEvent<T>` (which carries the DOM event on `nativeEvent`) and a
 * raw DOM `KeyboardEvent` (which carries the fields itself).
 */
interface ComposingSignals {
  key?: string
  keyCode?: number
  isComposing?: boolean
}

export type ImeKeyEvent = ComposingSignals & {
  nativeEvent?: ComposingSignals
}

/**
 * True when the keystroke belongs to an input method's composition and must
 * not be treated as a command.
 *
 * Guard the whole handler, not just the Enter branch — Escape mid-composition
 * means "cancel this candidate", and a handler that reads it as "discard the
 * draft" loses the same text by a different route.
 */
export function isImeComposing(e: ImeKeyEvent): boolean {
  const native = e?.nativeEvent ?? e
  if (!native) return false
  return (
    native.isComposing === true ||
    native.keyCode === 229 ||
    native.key === "Process" ||
    e?.key === "Process"
  )
}
