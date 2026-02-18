"use client"

import { AuthProvider } from "@/hooks/use-auth"

interface ProvidersProps {
  children: React.ReactNode
}

/** App-wide providers wrapper (auth context). */
export function Providers({ children }: ProvidersProps) {
  return <AuthProvider>{children}</AuthProvider>
}
