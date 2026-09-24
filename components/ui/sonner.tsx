"use client"

import { Toaster as Sonner, type ToasterProps } from "sonner"

function Toaster({ className, ...props }: ToasterProps) {
  return (
    <Sonner
      position="bottom-right"
      richColors
      closeButton
      {...props}
      // Sonner's light-theme error red is 4.34:1 against its pale red
      // background. The app foreground token clears WCAG AA in both themes.
      className={`${className ?? "toaster group"} ![--error-text:var(--foreground)]`}
    />
  )
}

export { Toaster }
