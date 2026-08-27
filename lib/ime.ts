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
// one line and `isComposing` alone is false at exactly the moment that
// matters:
//
//   - `isComposing` on the KeyboardEvent is the standard signal, and every
//     current engine sets it. React does not surface it on the synthetic
//     event, so it has to be read off `nativeEvent`.
//   - It is nevertheless FALSE on the keystroke that ENDS a composition,
//     which for a submit handler is the only keystroke there is. MDN states
//     it plainly on the `keydown` page: "`compositionend` may fire BEFORE
//     `keydown` when typing the last character that closes the IME. In these
//     cases, `isComposing` is false even when the event is part of
//     composition. However, `KeyboardEvent.keyCode` is still 229." MDN's own
//     recommended guard is therefore `event.isComposing || event.keyCode === 229`,
//     which is what this is.
//   - Safari made that boundary the rule rather than the edge. Its
//     `isComposing` is a PARTIAL implementation from 10.1 through 26: the
//     composition-completing keydown is dispatched after `compositionend`,
//     so it always reports false (webkit.org/b/165004, fixed in Safari 27).
//     On those versions an `isComposing`-only guard is not a weak fix, it is
//     no fix at all — and they are the browsers a CJK reader is most likely
//     to be on.
//   - `key === "Process"` is the third spelling, used by Gecko and by the
//     legacy IE/Edge path.
//
// The 229 arm is not free, and the cost belongs here so nobody deletes it as
// dead weight later: `keyCode` 229 is not IME-exclusive on Android, where
// Chrome + Gboard report it for ordinary keystrokes and for Enter
// (crbug.com/809107). Nothing distinguishes Gboard's Enter from Safari's
// composition-completing Enter — both are `key: "Enter", keyCode: 229` — so
// this guard trades a possible swallowed Enter on an Android soft keyboard
// for a working one on Safari. That is the trade MDN recommends and the one
// this dashboard wants; reverse it only with a reason.
//
// Deliberately NOT tracking compositionstart/compositionend in state: that
// is a second source of truth to keep in sync, AND on Safari <= 26 it is the
// same broken answer, because `compositionend` has already fired by the time
// the keydown arrives.

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
