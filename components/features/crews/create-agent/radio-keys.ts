import type { KeyboardEvent } from "react"

/**
 * Arrow/Home/End move the selection inside a role="radiogroup" of buttons,
 * the way a native radio group does. One radio carries tabIndex 0 (the
 * checked one, else the first), so the group is a single tab stop.
 */
export function moveRadio(event: KeyboardEvent<HTMLElement>) {
  if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return
  const radios = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]'))
  const index = radios.indexOf(event.target as HTMLButtonElement)
  if (index < 0) return
  event.preventDefault()
  const next = event.key === "Home" ? 0 : event.key === "End" ? radios.length - 1 : (index + (["ArrowLeft", "ArrowUp"].includes(event.key) ? -1 : 1) + radios.length) % radios.length
  radios[next]?.focus()
  radios[next]?.click()
}

/** tabIndex for the radio at `index`: 0 for the checked one, or the first when none is checked. */
export function radioTabIndex(checked: boolean, index: number, anyChecked: boolean): 0 | -1 {
  return checked || (!anyChecked && index === 0) ? 0 : -1
}
