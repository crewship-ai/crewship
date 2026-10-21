/**
 * Copy for non-secure contexts (HTTP dev hosts) and refused clipboard
 * permission: a hidden textarea and the deprecated but still supported
 * document.execCommand("copy"). True only when the browser says the copy
 * happened — a "Copied" over a clipboard that still holds the old value is
 * the one outcome this must not produce.
 */
export function legacyCopy(text: string): boolean {
  if (typeof document === "undefined") return false
  const ta = document.createElement("textarea")
  ta.value = text
  ta.setAttribute("readonly", "")
  ta.setAttribute("aria-hidden", "true")
  ta.className = "fixed -top-[1000px] left-0 opacity-0"
  document.body.appendChild(ta)
  ta.select()
  ta.setSelectionRange(0, text.length)
  try {
    return document.execCommand("copy") === true
  } catch {
    return false
  } finally {
    document.body.removeChild(ta)
  }
}

/** Copies `text`, preferring the async clipboard API in a secure context. Resolves true only once the copy is confirmed. */
export async function copyToClipboard(text: string): Promise<boolean> {
  const modern = typeof navigator !== "undefined" && typeof navigator.clipboard?.writeText === "function" &&
    typeof window !== "undefined" && window.isSecureContext
  if (modern) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      return legacyCopy(text)
    }
  }
  return legacyCopy(text)
}
