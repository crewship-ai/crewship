"use client"

import type { CSSProperties } from "react"
import { Toaster as Sonner, type ToasterProps } from "sonner"

function Toaster({ ...props }: ToasterProps) {
  return (
    <Sonner
      className="toaster group"
      position="bottom-right"
      richColors
      closeButton
      {...props}
      // Sonner's light-theme error red is 4.34:1 against its pale red
      // background. The app foreground token clears WCAG AA in both themes.
      style={{ ...props.style, "--error-text": "var(--foreground)" } as CSSProperties}
    />
  )
}

export { Toaster }
