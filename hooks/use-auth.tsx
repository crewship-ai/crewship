"use client"

import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react"
import { z } from "zod"

const sessionSchema = z.object({
  user: z.object({
    id: z.string(),
    name: z.string().optional().default(""),
    email: z.string().optional().default(""),
  }),
  expires: z.string(),
})

const csrfSchema = z.object({
  csrfToken: z.string(),
})

type AuthSession = z.infer<typeof sessionSchema>

type AuthStatus = "loading" | "authenticated" | "unauthenticated"

interface AuthContextValue {
  session: AuthSession | null
  status: AuthStatus
  signIn: (email: string, password: string) => Promise<{ ok: boolean; error?: string }>
  signOut: () => Promise<void>
  refresh: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

async function fetchSession(): Promise<AuthSession | null> {
  try {
    const res = await fetch("/api/auth/session")
    if (!res.ok) return null
    const data = await res.json()
    const parsed = sessionSchema.safeParse(data)
    return parsed.success ? parsed.data : null
  } catch {
    return null
  }
}

async function fetchCsrfToken(): Promise<string | null> {
  try {
    const res = await fetch("/api/auth/csrf")
    if (!res.ok) return null
    const data = await res.json()
    const parsed = csrfSchema.safeParse(data)
    return parsed.success ? parsed.data.csrfToken : null
  } catch {
    return null
  }
}

interface AuthProviderProps {
  children: ReactNode
}

/** Provides auth context (session, signIn, signOut) to the component tree. */
export function AuthProvider({ children }: AuthProviderProps) {
  const [session, setSession] = useState<AuthSession | null>(null)
  const [status, setStatus] = useState<AuthStatus>("loading")

  const refresh = useCallback(async () => {
    const s = await fetchSession()
    setSession(s)
    setStatus(s ? "authenticated" : "unauthenticated")
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  const signIn = useCallback(async (email: string, password: string) => {
    const csrfToken = await fetchCsrfToken()
    if (!csrfToken) {
      return { ok: false, error: "Failed to get CSRF token" }
    }

    try {
      const res = await fetch("/api/auth/callback/credentials", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password, csrfToken, redirect: "false" }),
      })

      if (!res.ok) {
        const data = await res.json().catch(() => null)
        return { ok: false, error: data?.error ?? "Login failed" }
      }

      const data = await res.json()
      if (data.error) {
        return { ok: false, error: data.error === "CredentialsSignin" ? "Invalid email or password" : data.error }
      }

      await refresh()
      return { ok: true }
    } catch {
      return { ok: false, error: "Network error" }
    }
  }, [refresh])

  const signOut = useCallback(async () => {
    try {
      await fetch("/api/auth/signout", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
      })
    } catch {
      // ignore
    }
    setSession(null)
    setStatus("unauthenticated")
  }, [])

  return (
    <AuthContext value={{ session, status, signIn, signOut, refresh }}>
      {children}
    </AuthContext>
  )
}

/** Returns the full auth context (session, status, signIn, signOut, refresh). */
export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error("useAuth must be used within an AuthProvider")
  }
  return ctx
}

/** Returns session data and auth status (drop-in replacement for next-auth useSession). */
export function useSession() {
  const { session, status } = useAuth()
  return { data: session, status }
}
