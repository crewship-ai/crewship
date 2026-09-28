"use client"

import { useTheme } from "next-themes"
import { Toaster as Sonner, type ToasterProps } from "sonner"

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
      {...props}
    />
  )
}

export { Toaster }
