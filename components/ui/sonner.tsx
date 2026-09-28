"use client"

import { useTheme } from "next-themes"
import { Toaster as Sonner, type ToasterProps } from "sonner"

function Toaster({ ...props }: ToasterProps) {
  const { resolvedTheme } = useTheme()
  return (
    <Sonner
      className="toaster group"
      position="bottom-right"
      // Night and Dusk are both dark grounds. Sonner's light palette is only
      // used under Day, where it sits on a light shell.
      theme={resolvedTheme === "light" ? "light" : "dark"}
      richColors
      closeButton
      {...props}
    />
  )
}

export { Toaster }
