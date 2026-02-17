"use client"

import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react"

interface AuthUser {
  id: string
  name: string
  email: string
}

interface AuthSession {
  user: AuthUser
  expires: string
}

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
    if (!data?.user?.id) return null
    return data as AuthSession
  } catch {
    return null
  }
}

async function fetchCsrfToken(): Promise<string | null> {
  try {
    const res = await fetch("/api/auth/csrf")
    if (!res.ok) return null
    const data = await res.json()
    return data.csrfToken ?? null
  } catch {
    return null
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
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

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error("useAuth must be used within an AuthProvider")
  }
  return ctx
}

export function useSession() {
  const { session, status } = useAuth()
  return { data: session, status }
}
