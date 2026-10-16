"use client"

import { useTheme } from "next-themes"
import { Toaster as Sonner, type ToasterProps } from "sonner"

/** Harbor 2 feedback rules: a toast leaves after 4 s; at most three at once. */
export const TOAST_DURATION_MS = 4000

function Toaster({ ...props }: ToasterProps) {
  const { resolvedTheme } = useTheme()
  return (
    <Sonner
      className="toaster group"
      position="bottom-right"
      // Sonner's light palette only under the light theme, where it sits on
      // a light shell.
      theme={resolvedTheme === "light" ? "light" : "dark"}
      richColors
      closeButton
      // Harbor 2 feedback rules: at most three at once, 4 s each.
      visibleToasts={3}
      duration={TOAST_DURATION_MS}
      {...props}
    />
  )
}

export { Toaster }
