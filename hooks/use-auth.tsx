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
import { AUTH_EVENT, AUTH_CHANNEL, broadcastSignOut } from "@/lib/api-fetch"

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

  // Hard-redirect to /login when apiFetch detects a terminal auth state
  // (refresh failed, session_revoked, etc). The BroadcastChannel echo
  // covers other tabs in the same browser. The redirect carries the
  // current path as ?redirect= so post-login can return the user to
  // where they were instead of dumping them on /.
  useEffect(() => {
    if (typeof window === "undefined") return

    let redirected = false
    const goLoginExpired = () => {
      if (redirected) return
      redirected = true
      const currentPath = window.location.pathname + window.location.search
      const params = new URLSearchParams({ reason: "expired" })
      // Don't append ?redirect for /login itself or unsafe absolute URLs.
      if (currentPath !== "/login" && currentPath.startsWith("/") && !currentPath.startsWith("//")) {
        params.set("redirect", currentPath)
      }
      window.location.replace(`/login?${params.toString()}`)
    }
    const goLoginSignedOutElsewhere = () => {
      if (redirected) return
      redirected = true
      window.location.replace("/login")
    }

    const expiredHandler = () => goLoginExpired()
    window.addEventListener(AUTH_EVENT, expiredHandler)

    let channel: BroadcastChannel | null = null
    if (typeof BroadcastChannel !== "undefined") {
      try {
        channel = new BroadcastChannel(AUTH_CHANNEL)
        channel.onmessage = (ev) => {
          if (ev.data?.type === "session-expired") goLoginExpired()
          else if (ev.data?.type === "signout") goLoginSignedOutElsewhere()
        }
      } catch {
        channel = null
      }
    }

    return () => {
      window.removeEventListener(AUTH_EVENT, expiredHandler)
      channel?.close()
    }
  }, [])

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
        credentials: "include",
      })
    } catch {
      // ignore
    }
    setSession(null)
    setStatus("unauthenticated")
    // Tell other tabs in this browser to drop their session UI too.
    broadcastSignOut()
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
