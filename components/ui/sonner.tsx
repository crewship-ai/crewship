"use client"

import { Toaster as Sonner, type ToasterProps } from "sonner"

function Toaster({ ...props }: ToasterProps) {
  return (
    <Sonner
      className="toaster group"
      position="bottom-right"
      // The app root is always dark. Sonner's light error palette has
      // insufficient text contrast and looks wrong against the shell.
      theme="dark"
      richColors
      closeButton
      {...props}
    />
  )
}

export { Toaster }
